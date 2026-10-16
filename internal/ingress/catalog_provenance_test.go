package ingress

import (
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// The static catalogs are the fallback a channel advertises before its first
// sync. An id in there that the upstream does not recognise gets shown to a
// client, which then selects it and fails. So each catalog may only contain ids
// with a verifiable source.
//
// This test pins the ids that were previously invented and removed. It is a
// regression guard, not a proof of correctness: it cannot confirm an id is
// live, only that a known-bad one has not returned.

// fabricatedWorkBuddyIDs were asserted in an earlier revision with no source.
// The real /v2/enterprises/personal/models response does not contain them, and
// "gpt-4o" in particular cannot belong to a Tencent upstream.
var fabricatedWorkBuddyIDs = []string{
	"deepseek-v3.1",
	"deepseek-reasoner",
	"glm-4-flash",
	"gpt-4o",
	"hy4-preview-f",
}

// fabricatedQoderIDs were invented key names. The official client defines its
// model set as new Set(["auto","ultimate","performance","efficient","lite"]).
var fabricatedQoderIDs = []string{
	"qmodel_38max", "qmodel_latest", "qmodel", "qfmodel",
	"kmodel_latest", "kmodel", "gmodel", "gfmodel",
	"dmodel", "dfmodel", "mmodel",
}

func catalogIDsOf(t *testing.T, provider pool.AccountType) []string {
	t.Helper()
	cfg := config.DefaultConfig()
	upstreams := newTestRegistry(cfg)
	up, ok := upstreams.Get(provider)
	if !ok {
		t.Fatalf("no adapter registered for %q", provider)
	}
	ids := make([]string, 0, 8)
	for _, m := range up.ModelCatalog() {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestWorkBuddyCatalogHasNoInventedIDs(t *testing.T) {
	ids := catalogIDsOf(t, pool.TypeWorkBuddy)
	if len(ids) == 0 {
		t.Fatal("the WorkBuddy catalog is empty")
	}
	for _, bad := range fabricatedWorkBuddyIDs {
		for _, id := range ids {
			if id == bad {
				t.Errorf("WorkBuddy catalog advertises %q, which no upstream response contained", bad)
			}
		}
	}
}

func TestQoderCatalogHasNoInventedIDs(t *testing.T) {
	ids := catalogIDsOf(t, pool.TypeQoder)
	if len(ids) == 0 {
		t.Fatal("the Qoder catalog is empty")
	}
	for _, bad := range fabricatedQoderIDs {
		for _, id := range ids {
			if id == bad {
				t.Errorf("Qoder catalog advertises %q, which the client does not define", bad)
			}
		}
	}

	// The five tiers the client defines are what should be there.
	want := map[string]bool{
		"auto": true, "ultimate": true, "performance": true,
		"efficient": true, "lite": true,
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("Qoder catalog advertises %q; the client defines only %v", id, want)
		}
	}
}

func TestAntigravityCatalogHasNoInventedIDs(t *testing.T) {
	ids := catalogIDsOf(t, pool.TypeAntigravity)
	if len(ids) == 0 {
		t.Fatal("the Antigravity catalog is empty")
	}
	// The binary advertises gemini-3.* and claude-4-6/5-5 era models; the
	// gemini-2.5 entries were carried over from an older generation.
	for _, bad := range []string{"gemini-2.5-pro", "gemini-2.5-flash-thinking"} {
		for _, id := range ids {
			if id == bad {
				t.Errorf("Antigravity catalog advertises %q, which the client binary does not list", bad)
			}
		}
	}
}

// TestStaticCatalogIsNotMergedIntoLiveModels is the guard for how invented ids
// reached the panel: the aggregate used to union the static catalog with the
// live one, so an unverified id appeared alongside real models from the same
// provider with no way to tell them apart.
func TestStaticCatalogIsNotMergedIntoLiveModels(t *testing.T) {
	server, ts := newModelServer(t)

	// A provider with a synced account must report only what it synced.
	live := []pool.ModelMetadata{{ID: "synced-model-a"}, {ID: "synced-model-b"}}
	acc := &pool.Account{ID: "wb1", Type: pool.TypeWorkBuddy, Name: "wb"}
	server.pool.AddOrUpdate(acc)
	server.pool.SetAccountModels("wb1", live)

	list := fetchModels(t, ts)

	var workbuddy []string
	for _, m := range list.Data {
		if m.OwnedBy == "workbuddy" {
			workbuddy = append(workbuddy, m.ID)
		}
	}

	if len(workbuddy) != len(live) {
		t.Fatalf("workbuddy advertises %d ids (%v), want only the %d synced ones",
			len(workbuddy), workbuddy, len(live))
	}
	for _, m := range workbuddy {
		if m != "synced-model-a" && m != "synced-model-b" {
			t.Errorf("static catalog id %q was merged into a live provider's list", m)
		}
	}

	// A provider with no account still advertises its fallback, or its channel
	// would be undiscoverable.
	var antigravity int
	for _, m := range list.Data {
		if m.OwnedBy == "antigravity" {
			antigravity++
		}
	}
	if antigravity == 0 {
		t.Error("a provider with no accounts advertises nothing")
	}
}
