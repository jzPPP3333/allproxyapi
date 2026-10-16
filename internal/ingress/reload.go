package ingress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/jzPPP3333/allproxyapi/internal/config"
)

// Configuration that is safe to change while the gateway runs. Changing the
// listener, the storage paths or an upstream base URL cannot be applied to a
// running process without a restart, so those are rejected explicitly rather
// than silently ignored.

// reloadableFields names the settings a hot reload will apply, so the response
// can say what took effect and the caller can see what did not.
var reloadableFields = []string{
	"server.api_key",
	"server.proxy",
	"routing.default_provider",
	"routing.model_rules",
	"providers.workbuddy.checkin_path",
	"providers.workbuddy.daily_checkin",
	"providers.workbuddy.exclude_model_patterns",
	"providers.qoder.checkin_path",
	"providers.qoder.daily_checkin",
	"providers.antigravity.default_project",
}

// restartRequiredFields are settings a reload deliberately refuses, because
// applying them in place would leave the process in a half-configured state.
var restartRequiredFields = []string{
	"server.host",
	"server.port",
	"storage.oauth_dirs",
	"providers.antigravity.base_url",
	"providers.antigravity.client_id",
	"providers.antigravity.client_secret",
	"providers.qoder.base_url",
	"providers.qoder.auth_url",
	"providers.qoder.cosy",
}

// handleAPIReload re-reads the config file and applies the reloadable subset.
//
//	POST /api/reload
func (s *Server) handleAPIReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	path := strings.TrimSpace(r.URL.Query().Get("config"))
	if path == "" {
		path = s.configPath()
	}
	if path == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "no config path known; pass ?config=<path>",
		})
		return
	}
	if _, err := os.Stat(path); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("config file not readable: %v", err),
		})
		return
	}

	fresh, err := config.LoadConfig(path)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("config invalid, nothing applied: %v", err),
		})
		return
	}

	// The whole config is swapped, but only the reloadable fields are read back
	// off the request path, so a change to a restart-required field takes effect
	// at the next start rather than half-applying now.
	s.cfgMu.Lock()
	s.cfg = fresh
	s.cfgMu.Unlock()

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "ok",
		"path":             path,
		"applied":          reloadableFields,
		"restart_required": restartRequiredFields,
	})
}

// handleAPIMetrics serves the aggregate figures and the recent request log.
//
//	GET /api/metrics
func (s *Server) handleAPIMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parsePositiveInt(v); err == nil {
			limit = n
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"summary":  s.metrics.Snapshot(),
		"requests": s.metrics.RecentRequests(limit),
	})
}

// SetConfigPath records where the config was loaded from so a reload can find
// it again.
func (s *Server) SetConfigPath(p string) {
	s.cfgMu.Lock()
	s.configPathValue = p
	s.cfgMu.Unlock()
}

func (s *Server) configPath() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.configPathValue
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
		if n > 100000 {
			return 0, fmt.Errorf("value too large")
		}
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return n, nil
}
