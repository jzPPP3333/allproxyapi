package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// The Cloud Code PA envelope requires a project id. A credential imported from
// an existing client often carries only tokens, so the project is discovered by
// calling loadCodeAssist once and caching the answer on the account.

// loadCodeAssistPath is the onboarding endpoint that reports the account's
// provisioned project and tier.
const loadCodeAssistPath = "/v1internal:loadCodeAssist"

// projectProbeTimeout bounds the discovery call so a slow onboarding service
// cannot stall a chat request.
const projectProbeTimeout = 15 * time.Second

// projectCache memoizes discovered projects per account id so concurrent
// requests do not each perform the onboarding round trip.
var (
	projectCacheMu sync.RWMutex
	projectCache   = map[string]string{}
)

// loadCodeAssistResponse mirrors the onboarding reply. The project id has been
// observed under several names across client versions, so every known spelling
// is probed rather than trusting one.
type loadCodeAssistResponse struct {
	CloudAICompanionProject string `json:"cloudaicompanionProject"`
	CloudCodeProject        string `json:"cloudCodeProject"`
	Project                 string `json:"project"`
	ProjectID               string `json:"projectId"`
	Data                    *struct {
		CloudAICompanionProject string `json:"cloudaicompanionProject"`
		CloudCodeProject        string `json:"cloudCodeProject"`
		Project                 string `json:"project"`
		ProjectID               string `json:"projectId"`
	} `json:"data"`
}

// project returns the first non-empty project id in the reply.
func (r loadCodeAssistResponse) project() string {
	candidates := []string{
		r.CloudAICompanionProject,
		r.CloudCodeProject,
		r.Project,
		r.ProjectID,
	}
	if r.Data != nil {
		candidates = append(candidates,
			r.Data.CloudAICompanionProject,
			r.Data.CloudCodeProject,
			r.Data.Project,
			r.Data.ProjectID,
		)
	}
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			return strings.TrimSpace(c)
		}
	}
	return ""
}

// LoadCodeAssist asks the upstream which project this credential belongs to.
func (c *Client) LoadCodeAssist(ctx context.Context, acc *pool.Account) (string, error) {
	if acc == nil {
		return "", fmt.Errorf("antigravity loadCodeAssist: nil account")
	}

	baseURL := c.cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	// An empty JSON object is the observed request shape; the endpoint derives
	// everything it needs from the bearer token.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+loadCodeAssistPath, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", fmt.Errorf("antigravity loadCodeAssist: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/hub/2.9.1 darwin/arm64")
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.21.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("antigravity loadCodeAssist: request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("antigravity loadCodeAssist: read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		if refreshErr := RefreshToken(ctx, acc, c.cfg.ClientID, c.cfg.ClientSecret, c.cfg.Proxy); refreshErr == nil {
			return c.LoadCodeAssist(ctx, acc)
		}
		return "", fmt.Errorf("antigravity loadCodeAssist: unauthorized (401)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("antigravity loadCodeAssist: status %d: %s", resp.StatusCode, string(raw))
	}

	var parsed loadCodeAssistResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("antigravity loadCodeAssist: decode response: %w", err)
	}

	project := parsed.project()
	if project == "" {
		return "", fmt.Errorf("antigravity loadCodeAssist: response carried no project id")
	}
	return project, nil
}

// ResolveProject returns the project id to use for an account, discovering and
// caching it when the account does not already carry one.
//
// A discovery failure is not fatal: the caller falls back to the configured
// default project rather than failing the request, because an account imported
// with a valid token still works against the default project in practice.
func (c *Client) ResolveProject(ctx context.Context, acc *pool.Account) string {
	if acc == nil {
		return c.defaultProject()
	}

	if strings.TrimSpace(acc.ProjectID) != "" {
		return acc.ProjectID
	}

	projectCacheMu.RLock()
	cached, ok := projectCache[acc.ID]
	projectCacheMu.RUnlock()
	if ok {
		acc.ProjectID = cached
		return cached
	}

	probeCtx, cancel := context.WithTimeout(ctx, projectProbeTimeout)
	defer cancel()

	project, err := c.LoadCodeAssist(probeCtx, acc)
	if err != nil {
		fallback := c.defaultProject()
		log.Printf("[Antigravity] 账号 %s 的 project 探测失败 (%v)，回退到 %s", acc.Name, err, fallback)
		return fallback
	}

	projectCacheMu.Lock()
	projectCache[acc.ID] = project
	projectCacheMu.Unlock()

	acc.ProjectID = project
	log.Printf("[Antigravity] 账号 %s 探测到 project: %s", acc.Name, project)

	if acc.FilePath != "" {
		if saveErr := pool.SaveAccountToFile(acc); saveErr != nil {
			log.Printf("[Antigravity] 持久化账号 %s 的 project 失败: %v", acc.Name, saveErr)
		}
	}

	return project
}

// defaultProject is the configured fallback project id.
func (c *Client) defaultProject() string {
	if c.cfg.DefaultProject != "" {
		return c.cfg.DefaultProject
	}
	return DefaultProjectID
}

// ForgetProject drops a cached project id, used when the upstream rejects it.
func ForgetProject(accountID string) {
	projectCacheMu.Lock()
	delete(projectCache, accountID)
	projectCacheMu.Unlock()
}
