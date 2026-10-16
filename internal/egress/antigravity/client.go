package antigravity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
	"github.com/jzPPP3333/allproxyapi/internal/util"
)

type Client struct {
	cfg        *config.AntigravityConfig
	httpClient *http.Client
}

func NewClient(cfg *config.AntigravityConfig) *Client {
	effectiveProxy := util.DetectProxyURL(cfg.Proxy)
	client := util.NewHTTPClient(effectiveProxy, true, 180*time.Second)

	return &Client{
		cfg:        cfg,
		httpClient: client,
	}
}

// NormalizeModel maps standard public model names to Antigravity internal model names
func NormalizeModel(m string) string {
	lower := strings.ToLower(strings.TrimSpace(m))
	if strings.Contains(lower, "opus") {
		return "claude-opus-4-6-thinking"
	}
	if strings.Contains(lower, "sonnet") || strings.HasPrefix(lower, "claude") {
		return "claude-sonnet-4-6"
	}
	if strings.Contains(lower, "haiku") {
		return "gemini-2.5-flash"
	}
	if lower == "gemini-flash" || lower == "gemini" {
		return "gemini-2.5-flash"
	}
	if lower == "gemini-pro" {
		return "gemini-2.5-pro"
	}
	// Fallback for non-Gemini names if dispatched or fallen back to Antigravity
	if strings.HasPrefix(lower, "deepseek") || strings.HasPrefix(lower, "gpt") || strings.HasPrefix(lower, "kimi") || strings.HasPrefix(lower, "glm") {
		return "gemini-2.5-flash"
	}
	return m
}

// RequestEnvelope is the outer envelope required by Google Cloud Code PA
type RequestEnvelope struct {
	Project     string                 `json:"project"`
	Model       string                 `json:"model"`
	RequestType string                 `json:"requestType"`
	UserAgent   string                 `json:"userAgent"`
	RequestID   string                 `json:"requestId"`
	Request     map[string]interface{} `json:"request"`
}

// DoGenerate sends a content generation request to Google Cloud Code PA
func (c *Client) DoGenerate(ctx context.Context, acc *pool.Account, model string, geminiReq map[string]interface{}, isStream bool) (*http.Response, error) {
	targetModel := NormalizeModel(model)

	baseURL := c.cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	endpoint := baseURL + "/v1internal:generateContent"
	if isStream {
		endpoint = baseURL + "/v1internal:streamGenerateContent?alt=sse"
	}

	projectID := acc.ProjectID
	if projectID == "" {
		projectID = c.cfg.DefaultProject
	}
	if projectID == "" {
		projectID = DefaultProjectID
	}

	// Generate negative random sessionId as STRING if absent
	if _, ok := geminiReq["sessionId"]; !ok {
		n, _ := rand.Int(rand.Reader, big.NewInt(9000000000000000000))
		geminiReq["sessionId"] = "-" + strconv.FormatInt(n.Int64()+1, 10)
	} else {
		// Ensure sessionId is a string
		switch v := geminiReq["sessionId"].(type) {
		case int:
			geminiReq["sessionId"] = strconv.Itoa(v)
		case int64:
			geminiReq["sessionId"] = strconv.FormatInt(v, 10)
		case float64:
			geminiReq["sessionId"] = strconv.FormatInt(int64(v), 10)
		}
	}

	// Clean safetySettings which cause issues on some endpoints
	delete(geminiReq, "safetySettings")

	// For Claude models, enforce VALIDATED mode on toolConfig if tools exist
	if strings.Contains(strings.ToLower(targetModel), "claude") {
		if _, ok := geminiReq["tools"]; ok {
			geminiReq["toolConfig"] = map[string]interface{}{
				"functionCallingConfig": map[string]interface{}{
					"mode": "VALIDATED",
				},
			}
		}
	}

	n, _ := rand.Int(rand.Reader, big.NewInt(100000000))
	reqID := fmt.Sprintf("agent-%08x", n.Int64())

	envelope := RequestEnvelope{
		Project:     projectID,
		Model:       targetModel,
		RequestType: "agent",
		UserAgent:   "antigravity",
		RequestID:   reqID,
		Request:     geminiReq,
	}

	bodyBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope error: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	httpReq.Header.Set("User-Agent", "antigravity/hub/2.9.1 darwin/arm64")
	httpReq.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")
	httpReq.Header.Set("Content-Type", "application/json")
	if isStream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("antigravity request failed: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		acc.SetCooldown(60*time.Second, "429 Too Many Requests")
		return nil, fmt.Errorf("antigravity rate limit (429)")
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		// Try token refresh
		if refreshErr := RefreshToken(ctx, acc, c.cfg.ClientID, c.cfg.ClientSecret, c.cfg.Proxy); refreshErr == nil {
			// Retry once with new token
			return c.DoGenerate(ctx, acc, model, geminiReq, isStream)
		}
		acc.SetCooldown(300*time.Second, "401 Unauthorized")
		return nil, fmt.Errorf("antigravity unauthorized (401)")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("antigravity upstream error (%d): %s", resp.StatusCode, string(errBytes))
	}

	return resp, nil
}

func (c *Client) GetDefaultModels() []string {
	return []string{
		"gemini-2.5-flash",
		"gemini-2.5-pro",
		"gemini-2.5-flash-thinking",
		"gemini-3-flash",
		"claude-3-7-sonnet",
		"claude-3-5-sonnet",
	}
}

// FetchModels returns available models for the Antigravity account.
func (c *Client) FetchModels(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	if existing := acc.GetModels(); len(existing) > 0 {
		return existing, nil
	}

	defaultList := []pool.ModelMetadata{
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", DescriptionZh: "Google 最新极速轻量模型，低延迟高吞吐", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", DescriptionZh: "Google 旗舰深度推理模型，复杂编码首选", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
		{ID: "gemini-2.5-flash-thinking", Name: "Gemini 2.5 Flash Thinking", DescriptionZh: "具备显式思维链的 Gemini 增强模型", SupportsToolCall: true, SupportsReasoning: true},
		{ID: "gemini-3-flash", Name: "Gemini 3 Flash", DescriptionZh: "新一代 Gemini 3 早期体验版本", SupportsToolCall: true, SupportsImages: true},
		{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", DescriptionZh: "Anthropic 旗舰混合推理模型 (映射至 claude-sonnet-4-6)", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
		{ID: "claude-3-5-sonnet", Name: "Claude 3.5 Sonnet", DescriptionZh: "Anthropic 前沿编码智能体模型", SupportsToolCall: true, SupportsImages: true},
		{ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 Thinking", DescriptionZh: "Anthropic 顶级能力思考增强模型", SupportsToolCall: true, SupportsReasoning: true},
	}

	acc.SetModels(defaultList)
	return defaultList, nil
}
