package ingress

import (
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// aliasServer builds a server with the given alias table.
func aliasServer(t *testing.T, mapping map[string]string) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.APIKey = "k"
	cfg.Routing.ModelMapping = mapping
	// Fold keys the way LoadConfig does, so the test exercises the real path.
	if len(mapping) > 0 {
		folded := make(map[string]string, len(mapping))
		for alias, target := range mapping {
			folded[strings.ToLower(strings.TrimSpace(alias))] = target
		}
		cfg.Routing.ModelMapping = folded
	}
	return NewServer(cfg, pool.NewAccountPool(), newTestRegistry(cfg))
}

// TestAliasPinsProviderAndModel covers the "<provider>/<model>" form, which the
// regex rules cannot express.
func TestAliasPinsProviderAndModel(t *testing.T) {
	s := aliasServer(t, map[string]string{
		"claude-3-5-sonnet": "antigravity/claude-sonnet-4-6",
		"qwen-max":          "qoder/ultimate",
	})

	for _, tc := range []struct{ in, wantProvider, wantModel string }{
		{"claude-3-5-sonnet", "antigravity", "claude-sonnet-4-6"},
		{"qwen-max", "qoder", "ultimate"},
	} {
		provider, model := s.ResolveProvider(tc.in)
		if provider != tc.wantProvider || model != tc.wantModel {
			t.Errorf("ResolveProvider(%q) = %q/%q, want %q/%q",
				tc.in, provider, model, tc.wantProvider, tc.wantModel)
		}
	}
}

// TestAliasOverridesRules checks the alias table is consulted before the regex
// rules, which is the whole point of putting it first.
func TestAliasOverridesRules(t *testing.T) {
	// Without an alias, ^claude-.* sends this to antigravity unchanged.
	s := aliasServer(t, nil)
	provider, model := s.ResolveProvider("claude-3-5-sonnet")
	if provider != "antigravity" {
		t.Fatalf("baseline: resolved to %q, want antigravity", provider)
	}
	if model != "claude-3-5-sonnet" {
		t.Fatalf("baseline: model = %q", model)
	}

	// With an alias, the model is rewritten.
	s = aliasServer(t, map[string]string{
		"claude-3-5-sonnet": "antigravity/claude-sonnet-4-6",
	})
	provider, model = s.ResolveProvider("claude-3-5-sonnet")
	if provider != "antigravity" || model != "claude-sonnet-4-6" {
		t.Fatalf("alias did not take effect: %q/%q", provider, model)
	}
}

// TestAliasBareModelKeepsProviderFromRules checks the short form: overriding
// only the model name still routes through the rules.
func TestAliasBareModelKeepsProviderFromRules(t *testing.T) {
	s := aliasServer(t, map[string]string{
		"deepseek-r1": "deepseek-v4-pro",
	})

	provider, model := s.ResolveProvider("deepseek-r1")
	if model != "deepseek-v4-pro" {
		t.Errorf("model = %q, want the alias target", model)
	}
	// ^deepseek-.* sends this to workbuddy.
	if provider != "workbuddy" {
		t.Errorf("provider = %q, want workbuddy from the rules", provider)
	}
}

// TestExplicitPrefixBeatsAlias checks the caller's own prefix is not overridden
// by the operator's table; being explicit on the request wins.
func TestExplicitPrefixBeatsAlias(t *testing.T) {
	s := aliasServer(t, map[string]string{
		"claude-3-5-sonnet": "antigravity/claude-sonnet-4-6",
	})

	provider, model := s.ResolveProvider("qoder/claude-3-5-sonnet")
	if provider != "qoder" {
		t.Errorf("provider = %q, want qoder from the explicit prefix", provider)
	}
	if model != "claude-3-5-sonnet" {
		t.Errorf("model = %q; the prefix form must not consult the alias table", model)
	}
}

// TestAliasKeysAreCaseInsensitive checks an operator does not have to match the
// caller's capitalization.
func TestAliasKeysAreCaseInsensitive(t *testing.T) {
	s := aliasServer(t, map[string]string{
		"Claude-3-5-Sonnet": "antigravity/claude-sonnet-4-6",
	})

	for _, in := range []string{"claude-3-5-sonnet", "CLAUDE-3-5-SONNET", "Claude-3-5-Sonnet"} {
		provider, model := s.ResolveProvider(in)
		if provider != "antigravity" || model != "claude-sonnet-4-6" {
			t.Errorf("ResolveProvider(%q) = %q/%q, want the alias to apply", in, provider, model)
		}
	}
}

// TestAliasToUnknownProviderFallsBackToBareModel checks a typo in the provider
// half does not silently drop the model rewrite.
func TestAliasToUnknownProviderFallsBackToBareModel(t *testing.T) {
	s := aliasServer(t, map[string]string{
		"some-model": "nosuchprovider/real-model",
	})

	provider, model := s.ResolveProvider("some-model")
	if provider == "nosuchprovider" {
		t.Fatal("routed to a provider that is not registered")
	}
	// The whole value is treated as a model name when the prefix is not a
	// provider, which keeps the rewrite visible rather than lost.
	if model != "nosuchprovider/real-model" {
		t.Errorf("model = %q", model)
	}
}

// TestEmptyAliasIsIgnored checks a key with no target does not break routing.
func TestEmptyAliasIsIgnored(t *testing.T) {
	s := aliasServer(t, map[string]string{"claude-3-5-sonnet": "   "})

	provider, model := s.ResolveProvider("claude-3-5-sonnet")
	if provider != "antigravity" || model != "claude-3-5-sonnet" {
		t.Errorf("an empty alias changed routing: %q/%q", provider, model)
	}
}

// TestAliasTableSurvivesReload checks the mapping is part of the reloadable
// config, since it lives on the same struct the request path reads.
func TestAliasTableSurvivesReload(t *testing.T) {
	s := aliasServer(t, map[string]string{"a": "workbuddy/b"})

	if _, model := s.ResolveProvider("a"); model != "b" {
		t.Fatalf("baseline failed: %q", model)
	}

	// A reload swapping the config must be visible to the resolver.
	s.cfgMu.Lock()
	s.cfg.Routing.ModelMapping = map[string]string{"a": "qoder/c"}
	s.cfgMu.Unlock()

	provider, model := s.ResolveProvider("a")
	if provider != "qoder" || model != "c" {
		t.Errorf("after swap: %q/%q, want qoder/c", provider, model)
	}
}
