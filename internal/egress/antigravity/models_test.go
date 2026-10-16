package antigravity

import (
	"context"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// TestCatalogIDsPassThroughNormalizeModel is the invariant that keeps the
// advertised list honest: every id the gateway publishes must be a real
// upstream model name, not a friendly alias that gets rewritten on the way out.
func TestCatalogIDsPassThroughNormalizeModel(t *testing.T) {
	if len(Catalog) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, m := range Catalog {
		if got := NormalizeModel(m.ID); got != m.ID {
			t.Errorf("NormalizeModel(%q) = %q; an advertised model must not be rewritten", m.ID, got)
		}
	}
}

// TestAliasesStillResolve checks the friendly names keep working on input even
// though they are no longer advertised as distinct model ids.
func TestAliasesStillResolve(t *testing.T) {
	cases := map[string]string{
		"claude-3-7-sonnet": "claude-sonnet-4-6",
		"claude-3-5-sonnet": "claude-sonnet-4-6",
		"claude-sonnet":     "claude-sonnet-4-6",
		"claude-opus":       "claude-opus-4-6-thinking",
		"sonnet":            "claude-sonnet-4-6",
		"opus":              "claude-opus-4-6-thinking",
		"haiku":             "gemini-2.5-flash",
		"gemini":            "gemini-2.5-flash",
		"gemini-flash":      "gemini-2.5-flash",
		"gemini-pro":        "gemini-2.5-pro",
	}
	for in, want := range cases {
		if got := NormalizeModel(in); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCatalogIsOneSourceOfTruth guards the two accessors the rest of the gateway
// reads from, which previously returned different sets.
func TestCatalogIsOneSourceOfTruth(t *testing.T) {
	client := NewClient(&config.AntigravityConfig{})

	ids := client.GetDefaultModels()
	if len(ids) != len(Catalog) {
		t.Fatalf("GetDefaultModels returned %d ids but Catalog has %d entries", len(ids), len(Catalog))
	}
	for i := range ids {
		if ids[i] != Catalog[i].ID {
			t.Errorf("GetDefaultModels[%d] = %q but Catalog[%d] = %q", i, ids[i], i, Catalog[i].ID)
		}
	}

	fetched, err := client.FetchModels(context.Background(), &pool.Account{ID: "a"})
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(fetched) != len(Catalog) {
		t.Fatalf("FetchModels returned %d models, Catalog has %d", len(fetched), len(Catalog))
	}
	for i := range fetched {
		if fetched[i].ID != Catalog[i].ID {
			t.Errorf("FetchModels[%d] = %q but Catalog[%d] = %q", i, fetched[i].ID, i, Catalog[i].ID)
		}
	}
}

// TestCatalogCopyIsIndependent makes sure a caller mutating the returned slice
// cannot corrupt the package-level catalog.
func TestCatalogCopyIsIndependent(t *testing.T) {
	a := CatalogCopy()
	a[0].ID = "mutated"
	if Catalog[0].ID == "mutated" {
		t.Fatal("CatalogCopy returned a slice aliasing the package catalog")
	}
}

// TestFetchModelsIsIdempotent checks a second call returns the cached set
// rather than a different one.
func TestFetchModelsIsIdempotent(t *testing.T) {
	client := NewClient(&config.AntigravityConfig{})
	acc := &pool.Account{ID: "a"}

	first, err := client.FetchModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("first FetchModels: %v", err)
	}
	second, err := client.FetchModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("second FetchModels: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("index %d: %q then %q", i, first[i].ID, second[i].ID)
		}
	}
}
