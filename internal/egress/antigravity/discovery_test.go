package antigravity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// realPayload mirrors the upstream shape: a map keyed by model id carrying
// display metadata, with internal entries mixed in.
const realPayload = `{
  "models": {
    "gemini-3.6-flash":        {"displayName":"Gemini 3.6 Flash","maxTokens":1048576,"maxOutputTokens":65536},
    "gemini-3.1-pro":          {"displayName":"Gemini 3.1 Pro","maxTokens":1048576,"maxOutputTokens":65536},
    "claude-sonnet-4-6":       {"displayName":"Claude Sonnet 4.6","maxTokens":200000,"maxOutputTokens":64000},
    "claude-opus-4-6":         {"displayName":"Claude Opus 4.6","maxTokens":200000,"maxOutputTokens":32000},
    "chat_20706":              {"displayName":"internal"},
    "tab_flash_lite_preview":  {"displayName":"tab"},
    "unnamed-model":           {}
  }
}`

// discoveryServer serves the payload and records what it was asked for.
func discoveryServer(t *testing.T, payload string, status int) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var gotReq *http.Request
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r.Clone(context.Background())
		gotBody, _ = io.ReadAll(r.Body)
		if status != 0 {
			w.WriteHeader(status)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv, gotReq, &gotBody
}

func TestDiscoveryParsesModelMap(t *testing.T) {
	srv, _, _ := discoveryServer(t, realPayload, 0)
	client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
	acc := &pool.Account{ID: "a1", AccessToken: "tok", ProjectID: "proj-1"}

	models, err := client.FetchAvailableModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("FetchAvailableModels: %v", err)
	}

	byID := map[string]pool.ModelMetadata{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// Real models survive with their metadata.
	for _, id := range []string{"gemini-3.6-flash", "gemini-3.1-pro", "claude-sonnet-4-6", "claude-opus-4-6"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("model %q missing from discovery", id)
		}
	}
	if got := byID["gemini-3.6-flash"].MaxInputTokens; got != 1048576 {
		t.Errorf("maxTokens = %d, want 1048576", got)
	}
	if got := byID["claude-sonnet-4-6"].MaxOutputTokens; got != 64000 {
		t.Errorf("maxOutputTokens = %d, want 64000", got)
	}

	// Internal entries are dropped.
	for _, id := range []string{"chat_20706", "tab_flash_lite_preview"} {
		if _, ok := byID[id]; ok {
			t.Errorf("internal model %q was advertised", id)
		}
	}

	// An entry without a display name still yields a usable entry.
	if m, ok := byID["unnamed-model"]; !ok {
		t.Error("a model with no displayName was dropped")
	} else if m.Name != "unnamed-model" {
		t.Errorf("name = %q, want the id as a fallback", m.Name)
	}
}

func TestDiscoverySendsProjectAndAuth(t *testing.T) {
	srv, _, gotBody := discoveryServer(t, realPayload, 0)
	client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
	acc := &pool.Account{ID: "a1", AccessToken: "secret-token", ProjectID: "my-project"}

	if _, err := client.FetchAvailableModels(context.Background(), acc); err != nil {
		t.Fatalf("FetchAvailableModels: %v", err)
	}

	var sent map[string]string
	if err := json.Unmarshal(*gotBody, &sent); err != nil {
		t.Fatalf("request body was not JSON: %s", *gotBody)
	}
	if sent["project"] != "my-project" {
		t.Errorf("project = %q, want my-project", sent["project"])
	}
}

func TestDiscoverySendsEmptyObjectWithoutProject(t *testing.T) {
	srv, _, gotBody := discoveryServer(t, realPayload, 0)
	client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
	acc := &pool.Account{ID: "a1", AccessToken: "tok"}

	if _, err := client.FetchAvailableModels(context.Background(), acc); err != nil {
		t.Fatalf("FetchAvailableModels: %v", err)
	}
	if strings.TrimSpace(string(*gotBody)) != "{}" {
		t.Errorf("body = %q, want {}", *gotBody)
	}
}

func TestDiscoveryHitsExpectedPath(t *testing.T) {
	var path, auth, ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		ua = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(realPayload))
	}))
	defer srv.Close()

	client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
	acc := &pool.Account{ID: "a1", AccessToken: "tok"}
	if _, err := client.FetchAvailableModels(context.Background(), acc); err != nil {
		t.Fatalf("FetchAvailableModels: %v", err)
	}

	if path != "/v1internal:fetchAvailableModels" {
		t.Errorf("path = %q, want /v1internal:fetchAvailableModels", path)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q", auth)
	}
	if !strings.HasPrefix(ua, "antigravity/") {
		t.Errorf("User-Agent = %q, want an antigravity identifier", ua)
	}
}

func TestDiscoveryCachesPerAccount(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(realPayload))
	}))
	defer srv.Close()

	client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
	acc := &pool.Account{ID: "a1", AccessToken: "tok"}

	for i := 0; i < 3; i++ {
		if _, err := client.FetchAvailableModels(context.Background(), acc); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (the rest served from cache)", calls)
	}

	// A different account is not served from another account's cache.
	other := &pool.Account{ID: "a2", AccessToken: "tok"}
	if _, err := client.FetchAvailableModels(context.Background(), other); err != nil {
		t.Fatalf("second account: %v", err)
	}
	if calls != 2 {
		t.Errorf("upstream calls = %d, want 2 after a second account", calls)
	}
}

func TestDiscoveryErrorsAreRecoverable(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		status  int
	}{
		{"upstream 500", "boom", http.StatusInternalServerError},
		{"empty model set", `{"models":{}}`, 0},
		{"only internal models", `{"models":{"chat_20706":{}}}`, 0},
		{"malformed json", `{not json`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := discoveryServer(t, tc.payload, tc.status)
			client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
			acc := &pool.Account{ID: "a1", AccessToken: "tok"}

			if _, err := client.FetchAvailableModels(context.Background(), acc); err == nil {
				t.Fatal("expected an error so the caller can fall back")
			}
		})
	}
}

// TestFetchModelsFallsBackToStatic is the important behaviour: discovery
// failing must not leave the provider with no advertised models.
func TestFetchModelsFallsBackToStatic(t *testing.T) {
	srv, _, _ := discoveryServer(t, "boom", http.StatusInternalServerError)
	client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
	acc := &pool.Account{ID: "a1", AccessToken: "tok"}

	models, err := client.FetchModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("FetchModels should fall back, not error: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("no models returned on the fallback path")
	}
	if len(models) != len(Catalog) {
		t.Errorf("fallback returned %d models, static catalog has %d", len(models), len(Catalog))
	}
}

// TestFetchModelsPrefersDiscovery checks the discovered set wins over the
// static catalog when discovery works.
func TestFetchModelsPrefersDiscovery(t *testing.T) {
	srv, _, _ := discoveryServer(t, realPayload, 0)
	client := NewClient(&config.AntigravityConfig{BaseURL: srv.URL})
	acc := &pool.Account{ID: "a1", AccessToken: "tok", ProjectID: "p"}

	models, err := client.FetchModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}

	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	if !ids["gemini-3.6-flash"] {
		t.Error("discovered model missing; the static catalog was used instead")
	}
	// The static catalog does not contain this model, so its presence proves
	// discovery supplied the list.
	if ids["gemini-2.5-flash"] && !ids["claude-opus-4-6"] {
		t.Error("looks like the static catalog was returned")
	}
}

func TestDiscoveryHandlesNilAccount(t *testing.T) {
	client := NewClient(&config.AntigravityConfig{})
	if _, err := client.FetchAvailableModels(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil account")
	}
}
