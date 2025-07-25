package external

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	config "github.com/Azzurriii/slythr/config"
	"github.com/Azzurriii/slythr/internal/domain/constants"
)

const (
	DefaultModel      = "gemini-2.0-flash"
	DefaultTimeout    = 30 * time.Second
	MaxRetries        = 3
	RetryDelay        = time.Second
	MaxSourceCodeSize = 1024 * 1024
)

type RiskLevel string

const (
	RiskLevelLow      RiskLevel = "LOW"
	RiskLevelMedium   RiskLevel = "MEDIUM"
	RiskLevelHigh     RiskLevel = "HIGH"
	RiskLevelCritical RiskLevel = "CRITICAL"
)

type GeminiClient struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
	maxRetries int
}

type GeminiClientOptions struct {
	Model      string
	Timeout    time.Duration
	MaxRetries int
}

type GeminiRequest struct {
	Contents         []GeminiContent   `json:"contents"`
	GenerationConfig *GenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings   []SafetySetting   `json:"safetySettings,omitempty"`
}

type GenerationConfig struct {
	Temperature     float32 `json:"temperature,omitempty"`
	TopK            int     `json:"topK,omitempty"`
	TopP            float32 `json:"topP,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type SafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiResponse struct {
	Candidates     []GeminiCandidate `json:"candidates"`
	PromptFeedback *PromptFeedback   `json:"promptFeedback,omitempty"`
}

type PromptFeedback struct {
	BlockReason   string         `json:"blockReason,omitempty"`
	SafetyRatings []SafetyRating `json:"safetyRatings,omitempty"`
}

type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

type GeminiCandidate struct {
	Content       GeminiContentResponse `json:"content"`
	FinishReason  string                `json:"finishReason,omitempty"`
	Index         int                   `json:"index,omitempty"`
	SafetyRatings []SafetyRating        `json:"safetyRatings,omitempty"`
}

type GeminiContentResponse struct {
	Parts []GeminiPartResponse `json:"parts"`
	Role  string               `json:"role,omitempty"`
}

type GeminiPartResponse struct {
	Text string `json:"text"`
}

type SecurityAnalysis struct {
	Success  bool               `json:"success"`
	Analysis SecurityAssessment `json:"analysis"`
	Error    string             `json:"error,omitempty"`
}

type SecurityAssessment struct {
	SecurityScore   int             `json:"security_score"`
	RiskLevel       RiskLevel       `json:"risk_level"`
	Summary         string          `json:"summary"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
	GoodPractices   interface{}     `json:"good_practices"`
	Recommendations interface{}     `json:"recommendations"`
}

type Vulnerability struct {
	Title          string      `json:"title"`
	Severity       RiskLevel   `json:"severity"`
	Description    string      `json:"description"`
	Location       interface{} `json:"location"`
	Recommendation interface{} `json:"recommendation"`
}

type GeminiError struct {
	Message    string
	StatusCode int
	RetryAfter time.Duration
}

func (e *GeminiError) Error() string {
	return fmt.Sprintf("gemini api error (status %d): %s", e.StatusCode, e.Message)
}

func NewGeminiClient(config config.GeminiConfig, opts *GeminiClientOptions) *GeminiClient {
	if opts == nil {
		opts = &GeminiClientOptions{}
	}

	// Set defaults
	model := DefaultModel
	if opts.Model != "" {
		model = opts.Model
	}

	timeout := DefaultTimeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	maxRetries := MaxRetries
	if opts.MaxRetries > 0 {
		maxRetries = opts.MaxRetries
	}

	baseURL := strings.Replace(
		"https://generativelanguage.googleapis.com/v1beta/models/$model:generateContent",
		"$model", model, 1,
	)

	return &GeminiClient{
		apiKey:  config.APIKey,
		model:   model,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		maxRetries: maxRetries,
	}
}

func (g *GeminiClient) AnalyzeSmartContract(ctx context.Context, sourceCode string) (*SecurityAnalysis, error) {
	if err := g.validateSourceCode(sourceCode); err != nil {
		return nil, fmt.Errorf("invalid source code: %w", err)
	}

	prompt := g.buildSecurityAnalysisPrompt(sourceCode)

	request := GeminiRequest{
		Contents: []GeminiContent{
			{
				Parts: []GeminiPart{
					{Text: prompt},
				},
				Role: "user",
			},
		},
		GenerationConfig: &GenerationConfig{
			Temperature:     0.1,
			MaxOutputTokens: 4096,
		},
		SafetySettings: g.getDefaultSafetySettings(),
	}

	response, err := g.makeRequestWithRetry(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to get analysis from Gemini: %w", err)
	}

	if len(response.Candidates) == 0 || len(response.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response from Gemini API")
	}

	if response.PromptFeedback != nil && response.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("request blocked: %s", response.PromptFeedback.BlockReason)
	}

	responseText := response.Candidates[0].Content.Parts[0].Text
	analysis, err := g.parseAnalysisResponse(responseText)
	if err != nil {
		return nil, fmt.Errorf("failed to parse analysis response: %w", err)
	}

	return analysis, nil
}

func (g *GeminiClient) validateSourceCode(sourceCode string) error {
	if strings.TrimSpace(sourceCode) == "" {
		return fmt.Errorf("source code cannot be empty")
	}

	if len(sourceCode) > MaxSourceCodeSize {
		return fmt.Errorf("source code too large (max %d bytes)", MaxSourceCodeSize)
	}

	return nil
}

func (g *GeminiClient) getDefaultSafetySettings() []SafetySetting {
	return []SafetySetting{
		{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_MEDIUM_AND_ABOVE"},
		{Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "BLOCK_MEDIUM_AND_ABOVE"},
		{Category: "HARM_CATEGORY_SEXUALLY_EXPLICIT", Threshold: "BLOCK_MEDIUM_AND_ABOVE"},
		{Category: "HARM_CATEGORY_DANGEROUS_CONTENT", Threshold: "BLOCK_MEDIUM_AND_ABOVE"},
	}
}

func (g *GeminiClient) buildSecurityAnalysisPrompt(sourceCode string) string {
	return constants.AnalysisPrompt + sourceCode
}

func (g *GeminiClient) parseAnalysisResponse(responseText string) (*SecurityAnalysis, error) {
	jsonStart := strings.Index(responseText, "{")
	jsonEnd := strings.LastIndex(responseText, "}")

	if jsonStart == -1 || jsonEnd == -1 {
		return nil, fmt.Errorf("no valid JSON found in response")
	}

	jsonStr := responseText[jsonStart : jsonEnd+1]

	var analysis SecurityAnalysis
	if err := json.Unmarshal([]byte(jsonStr), &analysis); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON response: %w", err)
	}

	return &analysis, nil
}

func (g *GeminiClient) makeRequestWithRetry(ctx context.Context, request GeminiRequest) (*GeminiResponse, error) {
	var lastErr error

	for attempt := 0; attempt <= g.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(RetryDelay * time.Duration(attempt)):
			}
		}

		response, err := g.makeRequest(ctx, request)
		if err == nil {
			return response, nil
		}

		lastErr = err

		if geminiErr, ok := err.(*GeminiError); ok {
			if geminiErr.StatusCode >= 400 && geminiErr.StatusCode < 500 && geminiErr.StatusCode != 429 {
				break
			}
		}
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", g.maxRetries+1, lastErr)
}

func (g *GeminiClient) makeRequest(ctx context.Context, request GeminiRequest) (*GeminiResponse, error) {
	jsonData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s?key=%s", g.baseURL, g.apiKey)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "slythr-smart-contract-analyzer/1.0")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp map[string]interface{}
		if json.Unmarshal(body, &errorResp) == nil {
			if errorMsg, ok := errorResp["error"].(map[string]interface{}); ok {
				if message, ok := errorMsg["message"].(string); ok {
					return nil, &GeminiError{
						Message:    message,
						StatusCode: resp.StatusCode,
					}
				}
			}
		}

		return nil, &GeminiError{
			Message:    fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
			StatusCode: resp.StatusCode,
		}
	}

	var response GeminiResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &response, nil
}

func (g *GeminiClient) GetModel() string {
	return g.model
}

func (g *GeminiClient) SetTimeout(timeout time.Duration) {
	g.httpClient.Timeout = timeout
}

func (g *GeminiClient) GenerateTestCases(ctx context.Context, sourceCode, testFramework, testLanguage string, staticAnalysis, securityAnalysis interface{}) (string, error) {
	if err := g.validateSourceCode(sourceCode); err != nil {
		return "", fmt.Errorf("invalid source code: %w", err)
	}

	if strings.TrimSpace(testFramework) == "" {
		return "", fmt.Errorf("test framework cannot be empty")
	}

	if strings.TrimSpace(testLanguage) == "" {
		return "", fmt.Errorf("test language cannot be empty")
	}

	prompt := g.buildTestCaseGenerationPrompt(sourceCode, testFramework, testLanguage, staticAnalysis, securityAnalysis)

	request := GeminiRequest{
		Contents: []GeminiContent{
			{
				Parts: []GeminiPart{
					{Text: prompt},
				},
				Role: "user",
			},
		},
		GenerationConfig: &GenerationConfig{
			Temperature:     0.2, // Slightly higher temperature for more creative test generation
			MaxOutputTokens: 8192,
		},
		SafetySettings: g.getDefaultSafetySettings(),
	}

	response, err := g.makeRequestWithRetry(ctx, request)
	if err != nil {
		return "", fmt.Errorf("failed to generate test cases from Gemini: %w", err)
	}

	if len(response.Candidates) == 0 || len(response.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from Gemini API")
	}

	if response.PromptFeedback != nil && response.PromptFeedback.BlockReason != "" {
		return "", fmt.Errorf("request blocked: %s", response.PromptFeedback.BlockReason)
	}

	return response.Candidates[0].Content.Parts[0].Text, nil
}

func (g *GeminiClient) buildTestCaseGenerationPrompt(sourceCode, testFramework, testLanguage string, staticAnalysis, securityAnalysis interface{}) string {
	prompt := constants.TestcaseGeneratePrompt

	prompt = strings.ReplaceAll(prompt, "{contracts}", sourceCode)
	prompt = strings.ReplaceAll(prompt, "{testFramework}", testFramework)
	prompt = strings.ReplaceAll(prompt, "{testLanguage}", testLanguage)

	staticAnalysisJSON := "No static analysis available"
	if staticAnalysis != nil {
		if jsonBytes, err := json.MarshalIndent(staticAnalysis, "", "  "); err == nil {
			staticAnalysisJSON = string(jsonBytes)
		}
	}
	prompt = strings.ReplaceAll(prompt, "{slitherAnalysis}", staticAnalysisJSON)

	securityAnalysisJSON := "No security analysis available"
	if securityAnalysis != nil {
		if jsonBytes, err := json.MarshalIndent(securityAnalysis, "", "  "); err == nil {
			securityAnalysisJSON = string(jsonBytes)
		}
	}
	prompt = strings.ReplaceAll(prompt, "{securityAnalysis}", securityAnalysisJSON)

	return prompt
}
