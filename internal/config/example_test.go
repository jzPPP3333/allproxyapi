package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExampleConfigIsValid guards the shipped example.
//
// It is the file a new user copies, so a YAML error in it is a startup failure
// on their first run — which happened: a duplicated mapping key in the routing
// rules made the example unloadable.
func TestExampleConfigIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "data", "config.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example config not present at %s", path)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}

	// The defaults a new user relies on must actually be populated.
	if cfg.Server.APIKey == "" {
		t.Error("example config has no api_key")
	}
	if cfg.Server.Port == 0 {
		t.Error("example config has no port")
	}
	if cfg.Routing.DefaultProvider == "" {
		t.Error("example config has no default_provider")
	}
	if len(cfg.Routing.ModelRules) == 0 {
		t.Error("example config has no model rules")
	}
	if len(cfg.Storage.OAuthDirs) == 0 {
		t.Error("example config has no credential directories")
	}

	// Every rule must name a provider, or it would route nowhere.
	for i, rule := range cfg.Routing.ModelRules {
		if rule.Provider == "" {
			t.Errorf("rule %d (%s) has no provider", i, rule.Pattern)
		}
	}
}

// TestExampleConfigMatchesDefaults checks the example does not contradict the
// code defaults, which would mean a copied file behaves differently from no
// file at all.
func TestExampleConfigMatchesDefaults(t *testing.T) {
	path := filepath.Join("..", "..", "data", "config.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skip("example config not present")
	}

	example, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defaults := DefaultConfig()

	if example.Server.Port != defaults.Server.Port {
		t.Errorf("example port %d differs from the default %d",
			example.Server.Port, defaults.Server.Port)
	}
	if example.Routing.DefaultProvider != defaults.Routing.DefaultProvider {
		t.Errorf("example default_provider %q differs from %q",
			example.Routing.DefaultProvider, defaults.Routing.DefaultProvider)
	}
	if len(example.Routing.ModelRules) != len(defaults.Routing.ModelRules) {
		t.Errorf("example has %d model rules, the default has %d",
			len(example.Routing.ModelRules), len(defaults.Routing.ModelRules))
	}
}
