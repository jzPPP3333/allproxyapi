package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
)

// doRequest drives a handler directly, bypassing auth so the handler logic is
// what is under test.
func doRequest(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func doReload(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, s, http.MethodPost, "/api/reload")
}

// reloadServer builds a server whose live config was loaded from a real file,
// which is what a reload replaces.
func reloadServer(t *testing.T, initial string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	server := NewServer(cfg, nil, NewRegistry())
	server.SetConfigPath(path)
	return server, path
}

const baseConfig = `
server:
  host: "127.0.0.1"
  port: 8320
  api_key: "first-key"
routing:
  default_provider: "workbuddy"
`

// TestReloadAppliesNewValues is the hot-reload guarantee: editing the file and
// posting to the endpoint changes behaviour without a restart.
func TestReloadAppliesNewValues(t *testing.T) {
	server, path := reloadServer(t, baseConfig)

	if got := server.cfg.Server.APIKey; got != "first-key" {
		t.Fatalf("initial api key = %q", got)
	}

	updated := `
server:
  host: "127.0.0.1"
  port: 8320
  api_key: "second-key"
routing:
  default_provider: "qoder"
`
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	rec := doReload(t, server)
	if rec.Code != http.StatusOK {
		t.Fatalf("reload status = %d", rec.Code)
	}

	if got := server.cfg.Server.APIKey; got != "second-key" {
		t.Errorf("api key after reload = %q, want the new value", got)
	}
	if got := server.cfg.Routing.DefaultProvider; got != "qoder" {
		t.Errorf("default provider after reload = %q", got)
	}
}

// TestReloadReportsWhatItApplied checks the response distinguishes settings
// that took effect from ones that need a restart, so the operator is not left
// assuming everything changed.
func TestReloadReportsWhatItApplied(t *testing.T) {
	server, _ := reloadServer(t, baseConfig)
	rec := doReload(t, server)

	var payload struct {
		Status          string   `json:"status"`
		Applied         []string `json:"applied"`
		RestartRequired []string `json:"restart_required"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if payload.Status != "ok" {
		t.Errorf("status = %q", payload.Status)
	}
	if len(payload.Applied) == 0 {
		t.Error("no applied fields reported")
	}
	if len(payload.RestartRequired) == 0 {
		t.Error("restart-required fields must be named, not silently ignored")
	}

	// The listener cannot change under a running process.
	found := false
	for _, f := range payload.RestartRequired {
		if f == "server.port" {
			found = true
		}
	}
	if !found {
		t.Errorf("server.port not listed as restart-required: %v", payload.RestartRequired)
	}
}

// TestReloadRejectsInvalidConfig checks a broken file leaves the running config
// untouched rather than half-applying.
func TestReloadRejectsInvalidConfig(t *testing.T) {
	server, path := reloadServer(t, baseConfig)

	// Invalid YAML: a tab where a mapping value is expected.
	if err := os.WriteFile(path, []byte("server:\n\tport: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := doReload(t, server)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid config", rec.Code)
	}
	if got := server.cfg.Server.APIKey; got != "first-key" {
		t.Errorf("a failed reload changed the live config to %q", got)
	}
}

// TestReloadMissingFile checks a missing config is reported, not panicked on.
func TestReloadMissingFile(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	server := NewServer(cfg, nil, NewRegistry())
	server.SetConfigPath(filepath.Join(dir, "absent.yaml"))

	rec := doReload(t, server)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestReloadRejectsGet checks the endpoint is POST-only.
func TestReloadRejectsGet(t *testing.T) {
	server, _ := reloadServer(t, baseConfig)
	rec := doRequest(t, server, http.MethodGet, "/api/reload")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
}

// TestReloadWithoutPathReportsClearly checks a server with no known config path
// says so rather than failing obscurely.
func TestReloadWithoutPathReportsClearly(t *testing.T) {
	cfg := config.DefaultConfig()
	server := NewServer(cfg, nil, NewRegistry())

	rec := doReload(t, server)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
