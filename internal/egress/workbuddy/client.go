package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

type Client struct {
	httpClient   *http.Client
	excludeModel []*regexp.Regexp
}

// NewClient builds a client. excludePatterns are regexps matched against an
// upstream model id; a match drops that entry from the discovered list.
func NewClient(excludePatterns []string) *Client {
	var compiled []*regexp.Regexp
	for _, p := range excludePatterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			log.Printf("[WorkBuddy] 忽略无效的 exclude_model_patterns 项 %q: %v", p, err)
			continue
		}
		compiled = append(compiled, re)
	}
	return &Client{
		excludeModel: compiled,
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

// excluded reports whether an upstream model id matches an exclusion pattern.
func (c *Client) excluded(id string) bool {
	for _, re := range c.excludeModel {
		if re.MatchString(id) {
			return true
		}
	}
	return false
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

// catalog is the fallback model set this provider advertises when no account
// has synced yet.
//
// Every id here was observed in a real /v2/enterprises/personal/models
// response. The live list replaces it per account, so this exists only so the
// channel is discoverable before its first sync — an invented id would be
// advertised to clients and then fail when selected.
var catalog = []pool.ModelMetadata{
	{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", DescriptionZh: "DeepSeek 极速版", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", DescriptionZh: "DeepSeek V4 极速版", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", DescriptionZh: "DeepSeek V4 旗舰", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "kimi-k3-1", Name: "Kimi K3", DescriptionZh: "月之暗面 Kimi K3", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "kimi-k2-thinking", Name: "Kimi K2 Thinking", DescriptionZh: "Kimi 深度思考模型", SupportsToolCall: true, SupportsReasoning: true},
	{ID: "glm-5.3", Name: "GLM-5.3", DescriptionZh: "智谱旗舰模型，擅长复杂推理与长程 Agent 任务", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash", DescriptionZh: "智谱 GLM-5.3 极速版", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "minimax-m3", Name: "MiniMax-M3", DescriptionZh: "MiniMax M3", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "hy4-preview", Name: "Hy4 preview", DescriptionZh: "腾讯混元 4 预览版", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
	{ID: "hy3", Name: "Hy3", DescriptionZh: "混元思考模型，推理能力增强", SupportsToolCall: true, SupportsImages: true, SupportsReasoning: true},
}

// Catalog returns a copy of the static model catalog.
func Catalog() []pool.ModelMetadata {
	out := make([]pool.ModelMetadata, len(catalog))
	copy(out, catalog)
	return out
}

// modelOf reads the model name out of an OpenAI-shaped request body, used to
// scope a per-model cooldown.
func modelOf(reqBody []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(reqBody, &probe); err != nil {
		return ""
	}
	return probe.Model
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
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// A concurrency cap is per model on this upstream, so only the model
		// that was capped is parked.
		acc.SetModelCooldown(modelOf(reqBody), 0, "429 Too Many Requests")
		return nil, fmt.Errorf("upstream rate limit (429): %s", truncateForMessage(raw))
	}

	// Exhausted quota is not transient: park the account until the allowance
	// refills rather than retrying into the same limit.
	if isQuotaExhausted(resp.StatusCode) {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		reason := quotaReason(resp.StatusCode, raw)
		acc.SetHardCooldown(reason)
		return nil, fmt.Errorf("workbuddy %s", reason)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		// Try token refresh
		if refreshErr := RefreshToken(ctx, acc); refreshErr == nil {
			// Retry once with new token
			return c.DoChat(ctx, acc, reqBody)
		}
		acc.Disable("401 Unauthorized，且刷新失败")
		return nil, fmt.Errorf("upstream unauthorized (401)")
	}

	// A 403 carrying a quota code is the same signal under a different status.
	if resp.StatusCode == http.StatusForbidden {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if reason, ok := quotaTextReason(raw); ok {
			acc.SetHardCooldown(reason)
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
	skipped := 0

	// 1. Primary models with rich metadata
	for _, m := range parsed.Data.Models {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		// Tier presets such as default-model are server-side routing aliases,
		// not selectable models, so they are dropped here rather than shown as
		// if they were models.
		if c.excluded(m.ID) {
			skipped++
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
			if c.excluded(mid) {
				skipped++
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

	if skipped > 0 {
		log.Printf("[WorkBuddy] 已过滤 %d 个档位条目 (exclude_model_patterns)", skipped)
	}

	// Update account's cached models
	acc.SetModels(result)
	return result, nil
}
