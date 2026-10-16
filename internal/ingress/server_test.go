package ingress

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/egress/antigravity"
	"github.com/jzPPP3333/allproxyapi/internal/egress/qoder"
	"github.com/jzPPP3333/allproxyapi/internal/egress/workbuddy"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/openai"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

func newTestRegistry(cfg *config.Config) *Registry {
	reg := NewRegistry()
	reg.Register(NewWorkBuddyAdapter(
		workbuddy.NewClient(cfg.Providers.WorkBuddy.ExcludeModelPatterns, cfg.Providers.WorkBuddy.UserAgent),
		cfg.Providers.WorkBuddy.CheckinPath,
	))
	reg.Register(NewAntigravityAdapter(antigravity.NewClient(&cfg.Providers.Antigravity)))
	reg.Register(NewQoderAdapter(qoder.NewClient(cfg.Providers.Qoder), cfg.Providers.Qoder.CheckinPath))
	return reg
}

func TestServerEndpoints(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.APIKey = "test-key"

	accPool := pool.NewAccountPool()
	server := NewServer(cfg, accPool, newTestRegistry(cfg))
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// 1. Test /health
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("expected status ok in body: %s", string(body))
	}

	// 2. Test /v1/models (requires auth)
	req, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	respModels, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models failed: %v", err)
	}
	defer respModels.Body.Close()
	if respModels.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", respModels.StatusCode)
	}
	var modelList openai.ModelListResponse
	if err := json.NewDecoder(respModels.Body).Decode(&modelList); err != nil {
		t.Fatalf("failed to decode models: %v", err)
	}
	if len(modelList.Data) == 0 {
		t.Errorf("expected non-empty model list")
	}

	// 3. Test Unauthorized request
	reqBadAuth, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
	reqBadAuth.Header.Set("Authorization", "Bearer wrong-key")
	respBad, err := http.DefaultClient.Do(reqBadAuth)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	respBad.Body.Close()
	if respBad.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", respBad.StatusCode)
	}

	// 4. Test Embedded WebUI (GET /)
	respUI, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer respUI.Body.Close()
	if respUI.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for WebUI, got %d", respUI.StatusCode)
	}
	uiBody, _ := io.ReadAll(respUI.Body)
	if !strings.Contains(string(uiBody), "AllProxyAPI") {
		t.Errorf("expected dashboard title in WebUI body")
	}
}
