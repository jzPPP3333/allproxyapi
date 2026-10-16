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

// TestCatalogTiersAreRecognised checks the tiers the catalog advertises are
// the ones the client's model picker defines, and that they survive
// normalization unchanged.
func TestCatalogTiersAreRecognised(t *testing.T) {
	c := NewClient(configForTest())

	// The set the official client defines for its model picker.
	want := map[string]bool{
		"auto": true, "ultimate": true, "performance": true,
		"efficient": true, "lite": true,
	}
	got := map[string]bool{}
	for _, m := range c.DefaultModels() {
		got[m.ID] = true
		if NormalizeModel(m.ID) != m.ID {
			t.Errorf("catalog tier %q does not survive normalization", m.ID)
		}
	}

	for id := range want {
		if !got[id] {
			t.Errorf("the client's tier %q is missing from the catalog", id)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("the catalog advertises %q, which is not a client tier", id)
		}
	}
}

// TestUnknownNamesPassThrough checks a model name is not rewritten onto a
// tier, which would serve a different model than the caller asked for.
func TestUnknownNamesPassThrough(t *testing.T) {
	for _, name := range []string{"qwen-max", "deepseek-chat", "kimi-k3", "glm-5.3", "minimax-m3"} {
		if got := NormalizeModel(name); got != name {
			t.Errorf("NormalizeModel(%q) = %q; it must not be mapped onto a tier", name, got)
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
