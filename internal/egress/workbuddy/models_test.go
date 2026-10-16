package workbuddy

import "testing"

// TestCatalogIDsPassThroughNormalizeModel keeps the advertised list honest: an
// advertised name must already be an upstream model name, so a client that
// picks it gets exactly that model.
func TestCatalogIDsPassThroughNormalizeModel(t *testing.T) {
	if len(Catalog()) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, m := range Catalog() {
		if got := NormalizeModel(m.ID); got != m.ID {
			t.Errorf("NormalizeModel(%q) = %q; an advertised model must not be rewritten", m.ID, got)
		}
	}
}

func TestAliasesResolve(t *testing.T) {
	cases := map[string]string{
		"deepseek-chat":     "deepseek-v4.1-flash",
		"deepseek":          "deepseek-v4.1-flash",
		"deepseek-reasoner": "deepseek-reasoner",
		"deepseek-r1":       "deepseek-reasoner",
		"kimi":              "kimi-k3-1",
		"KIMI":              "kimi-k3-1",
		"  kimi  ":          "kimi-k3-1",
		// An unknown name passes through so callers can address upstream models.
		"some-new-model": "some-new-model",
	}
	for in, want := range cases {
		if got := NormalizeModel(in); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGetDefaultModelsMatchesCatalog guards the two accessors from drifting.
func TestGetDefaultModelsMatchesCatalog(t *testing.T) {
	c := NewClient(nil)
	ids := c.GetDefaultModels()
	catalog := Catalog()

	if len(ids) != len(catalog) {
		t.Fatalf("GetDefaultModels returned %d ids, Catalog has %d", len(ids), len(catalog))
	}
	for i := range ids {
		if ids[i] != catalog[i].ID {
			t.Errorf("GetDefaultModels[%d] = %q but Catalog[%d] = %q", i, ids[i], i, catalog[i].ID)
		}
	}
}

// TestCatalogReturnsIndependentCopy protects the package catalog from mutation
// through a caller's slice.
func TestCatalogReturnsIndependentCopy(t *testing.T) {
	a := Catalog()
	a[0].ID = "mutated"
	if Catalog()[0].ID == "mutated" {
		t.Fatal("Catalog returned a slice aliasing the package catalog")
	}
}

func TestCatalogEntriesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Catalog() {
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
