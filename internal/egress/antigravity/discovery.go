package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// The Cloud Code PA exposes the account's real model list at
// v1internal:fetchAvailableModels. Using it instead of a hand-written catalog
// is what keeps the advertised set matching what the account can actually
// serve: the upstream adds and retires models without notice, and a static
// list goes stale silently.

// fetchModelsPath is the discovery route, appended to the configured base URL.
const fetchModelsPath = "/v1internal:fetchAvailableModels"

// discoveryTimeout bounds the call so a slow discovery cannot stall a request.
const discoveryTimeout = 20 * time.Second

// discoveryCacheTTL is how long a discovered list is reused. Discovery is not
// free and the set changes slowly.
const discoveryCacheTTL = 30 * time.Minute

// internalModelIDs are non-selectable entries the upstream reports alongside
// real models: internal routing targets and tab-completion models. They cannot
// be requested as chat models, so advertising them would be misleading.
var internalModelIDs = map[string]bool{
	"chat_20706":                  true,
	"chat_23310":                  true,
	"tab_flash_lite_preview":      true,
	"tab_jump_flash_lite_preview": true,
}

// availableModelsResponse is the discovery reply. The model set is a map keyed
// by model id rather than a list.
type availableModelsResponse struct {
	Models map[string]availableModelEntry `json:"models"`
}

// availableModelEntry carries the per-model metadata the upstream reports.
type availableModelEntry struct {
	DisplayName     string `json:"displayName"`
	MaxTokens       int    `json:"maxTokens"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	// The upstream also reports credit and tier data here; those fields are not
	// consumed yet and are deliberately not modelled rather than guessed at.
}

type discoveryEntry struct {
	models  []pool.ModelMetadata
	fetched time.Time
}

// FetchAvailableModels discovers the account's real model list.
//
// It returns the account's project id along with the models, because discovery
// needs the project when the credential carries one and the response is scoped
// to it.
func (c *Client) FetchAvailableModels(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	if acc == nil {
		return nil, fmt.Errorf("antigravity discovery: nil account")
	}

	c.discMu.RLock()
	cached, ok := c.discCache[acc.ID]
	c.discMu.RUnlock()
	if ok && time.Since(cached.fetched) < discoveryCacheTTL {
		return append([]pool.ModelMetadata(nil), cached.models...), nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	models, err := c.fetchModelsOnce(probeCtx, acc)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("antigravity discovery: upstream returned no usable models")
	}

	c.discMu.Lock()
	if c.discCache == nil {
		c.discCache = make(map[string]discoveryEntry)
	}
	c.discCache[acc.ID] = discoveryEntry{models: models, fetched: time.Now()}
	c.discMu.Unlock()

	return append([]pool.ModelMetadata(nil), models...), nil
}

// fetchModelsOnce performs a single discovery call.
func (c *Client) fetchModelsOnce(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	baseURL := c.cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	// The request is scoped to the project when one is known; an empty object
	// is accepted otherwise and the upstream infers it from the credential.
	var payload []byte
	if project := acc.ProjectID; strings.TrimSpace(project) != "" {
		payload = []byte(fmt.Sprintf(`{"project":%q}`, strings.TrimSpace(project)))
	} else {
		payload = []byte(`{}`)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+fetchModelsPath, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("antigravity discovery: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	req.Header.Set("User-Agent", c.userAgent())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity discovery: request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("antigravity discovery: read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		if refreshErr := RefreshToken(ctx, acc, c.cfg.ClientID, c.cfg.ClientSecret, c.cfg.Proxy); refreshErr != nil {
			return nil, fmt.Errorf("antigravity discovery: unauthorized and refresh failed: %w", refreshErr)
		}
		return c.fetchModelsOnce(ctx, acc)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("antigravity discovery: status %d: %s", resp.StatusCode, truncateForError(raw))
	}

	var parsed availableModelsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("antigravity discovery: decode response: %w", err)
	}

	models := buildCatalog(parsed.Models)
	if len(models) == 0 {
		return nil, fmt.Errorf("antigravity discovery: response carried no selectable models")
	}

	log.Printf("[Antigravity] 账号 %s 发现 %d 个可用模型", acc.Name, len(models))
	return models, nil
}

// buildCatalog converts the upstream map into a sorted catalog, dropping the
// internal entries and any id that could not be requested by name.
func buildCatalog(entries map[string]availableModelEntry) []pool.ModelMetadata {
	out := make([]pool.ModelMetadata, 0, len(entries))
	for id, entry := range entries {
		id = strings.TrimSpace(id)
		if id == "" || internalModelIDs[id] {
			continue
		}

		name := strings.TrimSpace(entry.DisplayName)
		if name == "" {
			name = id
		}

		out = append(out, pool.ModelMetadata{
			ID:              id,
			Name:            name,
			MaxInputTokens:  entry.MaxTokens,
			MaxOutputTokens: entry.MaxOutputTokens,
			// Capabilities are not reported by this endpoint. They are left
			// false rather than guessed; the id is what routing needs.
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// userAgent is the identifier sent upstream, which the upstream gates on. The
// configured override exists for matching a specific client build; empty keeps
// the build this gateway emulates.
func (c *Client) userAgent() string {
	if ua := strings.TrimSpace(c.cfg.UserAgent); ua != "" {
		return ua
	}
	return "antigravity/hub/2.9.1 darwin/arm64"
}

func truncateForError(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) <= 300 {
		return s
	}
	return s[:300] + "..."
}
