package qoder

import "testing"

// TestCatalogIDsPassThroughNormalizeModel keeps the advertised list honest: a
// key the gateway publishes must be a real Qoder model key, so a client that
// picks it gets exactly that model.
func TestCatalogIDsPassThroughNormalizeModel(t *testing.T) {
	c := NewClient(configForTest())
	catalog := c.DefaultModels()

	if len(catalog) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, m := range catalog {
		if got := NormalizeModel(m.ID); got != m.ID {
			t.Errorf("NormalizeModel(%q) = %q; an advertised key must not be rewritten", m.ID, got)
		}
	}
}

// TestPublicAliasesResolve checks friendly names still map onto catalog keys.
func TestPublicAliasesResolve(t *testing.T) {
	c := NewClient(configForTest())
	known := map[string]bool{}
	for _, m := range c.DefaultModels() {
		known[m.ID] = true
	}

	cases := map[string]string{
		"":                  "ultimate",
		"Qwen3.8-Max":       "qmodel_38max",
		"qwen-max":          "qmodel_38max",
		"qwen-flash":        "qfmodel",
		"deepseek-chat":     "dmodel",
		"deepseek-v4-flash": "dfmodel",
		"kimi-k2-code":      "kmodel",
		"kimi-k3":           "kmodel_latest",
		"glm-5.3-flash":     "gfmodel",
		"glm-5.3":           "gmodel",
		"minimax-m3":        "mmodel",
	}
	for in, want := range cases {
		got := NormalizeModel(in)
		if got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
		if !known[got] {
			t.Errorf("NormalizeModel(%q) = %q, which is not in the catalog", in, got)
		}
	}
}

// TestDefaultModelIDsMatchesCatalog guards the two accessors from drifting.
func TestDefaultModelIDsMatchesCatalog(t *testing.T) {
	c := NewClient(configForTest())
	ids := c.DefaultModelIDs()
	catalog := c.DefaultModels()

	if len(ids) != len(catalog) {
		t.Fatalf("DefaultModelIDs returned %d ids, catalog has %d", len(ids), len(catalog))
	}
	for i := range ids {
		if ids[i] != catalog[i].ID {
			t.Errorf("DefaultModelIDs[%d] = %q but catalog[%d] = %q", i, ids[i], i, catalog[i].ID)
		}
	}
}

func TestCatalogEntriesAreWellFormed(t *testing.T) {
	c := NewClient(configForTest())
	seen := map[string]bool{}
	for _, m := range c.DefaultModels() {
		if m.ID == "" {
			t.Error("catalog contains an empty model id")
			continue
		}
		if seen[m.ID] {
			t.Errorf("catalog lists %q twice", m.ID)
		}
		seen[m.ID] = true
		if m.Name == "" {
			t.Errorf("model %q has no display name", m.ID)
		}
	}
}
