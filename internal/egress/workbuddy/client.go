package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 180 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// NormalizeModel maps a friendly alias onto a WorkBuddy upstream model name.
// A name that is already an upstream model, or is unknown, passes through
// unchanged so a caller can address the upstream catalog directly.
func NormalizeModel(m string) string {
	lower := strings.ToLower(strings.TrimSpace(m))
	switch lower {
	case "deepseek-chat", "deepseek":
		return "deepseek-v4.1-flash"
	case "deepseek-reasoner", "deepseek-r1":
		return "deepseek-reasoner"
	case "kimi":
		return "kimi-k3-1"
	default:
		return m
	}
}

// catalog is the static set of upstream models this provider serves. The
// /v2/enterprises/personal/models route replaces it per account once synced;
// this is what the gateway advertises before that happens.
var catalog = []pool.ModelMetadata{
	{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", DescriptionZh: "DeepSeek 极速版", SupportsToolCall: true},
	{ID: "deepseek-v3.1", Name: "DeepSeek V3.1", DescriptionZh: "DeepSeek 通用模型", SupportsToolCall: true},
	{ID: "deepseek-reasoner", Name: "DeepSeek Reasoner", DescriptionZh: "DeepSeek 深度推理模型", SupportsToolCall: true, SupportsReasoning: true},
	{ID: "kimi-k3-1", Name: "Kimi K3.1", DescriptionZh: "月之暗面 Kimi K3.1", SupportsToolCall: true, SupportsReasoning: true},
	{ID: "hy4-preview-f", Name: "Hunyuan 4 Preview", DescriptionZh: "腾讯混元 4 预览版", SupportsToolCall: true},
	{ID: "hy3", Name: "Hunyuan 3", DescriptionZh: "腾讯混元 3", SupportsToolCall: true},
	{ID: "glm-4-flash", Name: "GLM-4 Flash", DescriptionZh: "智谱 GLM-4 极速版", SupportsToolCall: true},
	{ID: "gpt-4o", Name: "GPT-4o", DescriptionZh: "OpenAI GPT-4o", SupportsToolCall: true, SupportsImages: true},
}

// Catalog returns a copy of the static model catalog.
func Catalog() []pool.ModelMetadata {
	out := make([]pool.ModelMetadata, len(catalog))
	copy(out, catalog)
	return out
}

// DoChat forwards a chat completion request to WorkBuddy upstream /v2/chat/completions.
func (c *Client) DoChat(ctx context.Context, acc *pool.Account, reqBody []byte) (*http.Response, error) {
	realm := "cn"
	if acc.Realm != "" {
		realm = acc.Realm
	}
	cfg := GetRealmConfig(realm)
	endpoint := cfg.ChatUpstream + "/v2/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	headers := BuildIdentityHeaders(acc)
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("workbuddy request failed: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		acc.SetCooldown(60*time.Second, "429 Too Many Requests")
		return nil, fmt.Errorf("upstream rate limit (429)")
	}

	// Exhausted quota is not transient: park the account for the long window so
	// the pool routes around it rather than retrying into the same limit.
	if isQuotaExhausted(resp.StatusCode) {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		reason := quotaReason(resp.StatusCode, raw)
		acc.SetCooldown(QuotaCooldown, reason)
		return nil, fmt.Errorf("workbuddy %s", reason)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		// Try token refresh
		if refreshErr := RefreshToken(ctx, acc); refreshErr == nil {
			// Retry once with new token
			return c.DoChat(ctx, acc, reqBody)
		}
		acc.SetCooldown(300*time.Second, "401 Unauthorized")
		return nil, fmt.Errorf("upstream unauthorized (401)")
	}

	// A 403 carrying a quota code is the same signal under a different status.
	if resp.StatusCode == http.StatusForbidden {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if reason, ok := quotaTextReason(raw); ok {
			acc.SetCooldown(QuotaCooldown, reason)
			return nil, fmt.Errorf("workbuddy %s", reason)
		}
		return nil, fmt.Errorf("workbuddy upstream error (403): %s", string(raw))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("workbuddy upstream error (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	return resp, nil
}

func (c *Client) GetDefaultModels() []string {
	models := Catalog()
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

type upstreamModelItem struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DescriptionZh     string `json:"descriptionZh"`
	Credits           string `json:"credits"`
	MaxInputTokens    int    `json:"maxInputTokens"`
	MaxOutputTokens   int    `json:"maxOutputTokens"`
	SupportsImages    bool   `json:"supportsImages"`
	SupportsReasoning bool   `json:"supportsReasoning"`
	SupportsToolCall  bool   `json:"supportsToolCall"`
}

type upstreamModelsResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Models []upstreamModelItem `json:"models"`
		Agents []struct {
			Name   string   `json:"name"`
			Models []string `json:"models"`
		} `json:"agents"`
	} `json:"data"`
}

// FetchModels queries the WorkBuddy upstream /v2/enterprises/personal/models to discover all available models.
func (c *Client) FetchModels(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	realm := "cn"
	if acc.Realm != "" {
		realm = acc.Realm
	}
	cfg := GetRealmConfig(realm)
	endpoint := cfg.ChatUpstream + "/v2/enterprises/personal/models"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	headers := BuildIdentityHeaders(acc)
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetch models request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read models body failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch models returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var parsed upstreamModelsResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal models error: %w", err)
	}

	seen := make(map[string]bool)
	var result []pool.ModelMetadata

	// 1. Primary models with rich metadata
	for _, m := range parsed.Data.Models {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		result = append(result, pool.ModelMetadata{
			ID:                m.ID,
			Name:              m.Name,
			DescriptionZh:     m.DescriptionZh,
			Credits:           m.Credits,
			MaxInputTokens:    m.MaxInputTokens,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsImages:    m.SupportsImages,
			SupportsReasoning: m.SupportsReasoning,
			SupportsToolCall:  m.SupportsToolCall,
		})
	}

	// 2. Models from agents list (supplementary)
	for _, ag := range parsed.Data.Agents {
		for _, mid := range ag.Models {
			if mid == "" || seen[mid] {
				continue
			}
			seen[mid] = true
			result = append(result, pool.ModelMetadata{
				ID:                mid,
				Name:              mid,
				DescriptionZh:     fmt.Sprintf("Agent (%s) 模型", ag.Name),
				SupportsToolCall:  true,
				SupportsReasoning: true,
			})
		}
	}

	// Update account's cached models
	acc.SetModels(result)
	return result, nil
}
