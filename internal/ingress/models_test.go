package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/openai"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// newModelServer builds a server with an empty pool.
func newModelServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.APIKey = "test-key"
	server := NewServer(cfg, pool.NewAccountPool(), newTestRegistry(cfg))
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return server, ts
}

// fetchModels calls the real /v1/models endpoint.
func fetchModels(t *testing.T, ts *httptest.Server) openai.ModelListResponse {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models returned %d", resp.StatusCode)
	}
	var list openai.ModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}
	return list
}

// TestEveryProviderAppearsWithNoAccounts is the regression guard for the bug
// where one synced account hid every other provider from /v1/models.
func TestEveryProviderAppearsWithNoAccounts(t *testing.T) {
	server, ts := newModelServer(t)

	// A WorkBuddy account that has already synced its upstream catalog. This is
	// what used to suppress the other two providers.
	wb := &pool.Account{ID: "wb1", Type: pool.TypeWorkBuddy, Name: "wb"}
	server.pool.AddOrUpdate(wb)
	server.pool.SetAccountModels("wb1", []pool.ModelMetadata{{ID: "deepseek-v4.1-flash"}})

	list := fetchModels(t, ts)

	owners := map[string]int{}
	for _, m := range list.Data {
		owners[m.OwnedBy]++
	}
	t.Logf("models per provider: %v", owners)

	for _, want := range []string{"workbuddy", "antigravity", "qoder"} {
		if owners[want] == 0 {
			t.Errorf("provider %q is missing from /v1/models once another provider synced", want)
		}
	}
}

// genuinelySharedIDs are names served by more than one provider, so an
// unprefixed request is ambiguous by nature and routing must pick one. They are
// addressed unambiguously as "<provider>/<id>".
//
// WorkBuddy and Qoder both expose an "auto" and a "lite" tier preset. That is
// a real upstream overlap, not a routing defect, so it is recorded here rather
// than papered over.
var genuinelySharedIDs = map[string]bool{
	"auto": true,
	"lite": true,
}

// TestAdvertisedModelsResolveToTheirOwnProvider is the invariant that makes
// this class of mismatch impossible: every id the gateway advertises must route
// back to the provider that advertised it, rather than to another provider
// whose rule happened to match first.
//
// Genuinely shared names are exempt, and are required to be reachable with an
// explicit prefix instead, so an ambiguous name still has a working path.
func TestAdvertisedModelsResolveToTheirOwnProvider(t *testing.T) {
	server, ts := newModelServer(t)
	list := fetchModels(t, ts)

	if len(list.Data) == 0 {
		t.Fatal("/v1/models returned nothing")
	}

	for _, m := range list.Data {
		provider, upstream := server.ResolveProvider(m.ID)
		if provider == m.OwnedBy {
			continue
		}
		if !genuinelySharedIDs[m.ID] {
			t.Errorf("model %q is advertised by %q but routes to %q", m.ID, m.OwnedBy, provider)
			continue
		}
		// A shared name must still be reachable by its explicit prefix.
		prefixed, prefixedModel := server.ResolveProvider(m.OwnedBy + "/" + m.ID)
		if prefixed != m.OwnedBy || prefixedModel != m.ID {
			t.Errorf("shared model %q is not reachable as %q/%q (got %q/%q)",
				m.ID, m.OwnedBy, m.ID, prefixed, prefixedModel)
		}
		_ = upstream
	}
}

// TestAdvertisedNamesNeedNoPrefix documents the consequence of the invariant
// above: a client can pass an advertised name through verbatim, with no
// "<provider>/" prefix, and reach the right upstream.
func TestAdvertisedNamesNeedNoPrefix(t *testing.T) {
	server, ts := newModelServer(t)
	list := fetchModels(t, ts)

	for _, m := range list.Data {
		if _, upstreamModel := server.ResolveProvider(m.ID); upstreamModel != m.ID {
			t.Errorf("advertised model %q is rewritten to %q before reaching the upstream",
				m.ID, upstreamModel)
		}
	}
}

// TestProviderCatalogsDoNotOverlap checks that a name is claimed by exactly one
// provider. Two providers offering the same id would make routing depend on
// rule order, which is exactly the ambiguity the aggregate list should not have.
func TestProviderCatalogsDoNotOverlap(t *testing.T) {
	_, ts := newModelServer(t)
	list := fetchModels(t, ts)

	claims := map[string]map[string]bool{}
	for _, m := range list.Data {
		if claims[m.ID] == nil {
			claims[m.ID] = map[string]bool{}
		}
		claims[m.ID][m.OwnedBy] = true
	}

	for id, owners := range claims {
		if len(owners) > 1 {
			names := make([]string, 0, len(owners))
			for o := range owners {
				names = append(names, o)
			}
			t.Errorf("model id %q is advertised by several providers %v, making it ambiguous", id, names)
		}
	}
}

// TestModelCatalogMatchesDefaultModels guards against the two catalogs drifting
// apart, which is how the dashboard and /v1/models came to disagree.
func TestModelCatalogMatchesDefaultModels(t *testing.T) {
	cfg := config.DefaultConfig()
	server := NewServer(cfg, pool.NewAccountPool(), newTestRegistry(cfg))

	for _, up := range server.upstreams.All() {
		ids := up.DefaultModels()
		meta := up.ModelCatalog()

		if len(ids) != len(meta) {
			t.Errorf("%s: DefaultModels has %d entries but ModelCatalog has %d",
				up.ID(), len(ids), len(meta))
			continue
		}
		for i := range ids {
			if ids[i] != meta[i].ID {
				t.Errorf("%s: DefaultModels[%d]=%q but ModelCatalog[%d]=%q",
					up.ID(), i, ids[i], i, meta[i].ID)
			}
		}
	}
}

// TestModelCatalogEntriesAreWellFormed checks the catalogs are usable: no empty
// ids, no duplicates within a provider, and metadata worth showing.
func TestModelCatalogEntriesAreWellFormed(t *testing.T) {
	cfg := config.DefaultConfig()
	server := NewServer(cfg, pool.NewAccountPool(), newTestRegistry(cfg))

	for _, up := range server.upstreams.All() {
		seen := map[string]bool{}
		for _, m := range up.ModelCatalog() {
			if m.ID == "" {
				t.Errorf("%s: catalog contains an empty model id", up.ID())
				continue
			}
			if seen[m.ID] {
				t.Errorf("%s: catalog lists %q twice", up.ID(), m.ID)
			}
			seen[m.ID] = true

			if m.Name == "" {
				t.Errorf("%s: model %q has no display name", up.ID(), m.ID)
			}
		}
		if len(up.ModelCatalog()) == 0 {
			t.Errorf("%s: catalog is empty", up.ID())
		}
	}
}

// TestDashboardModelsCoverEveryProvider checks /api/models uses the same union
// rule, so the panel cannot disagree with /v1/models.
func TestDashboardModelsCoverEveryProvider(t *testing.T) {
	server, ts := newModelServer(t)

	wb := &pool.Account{ID: "wb1", Type: pool.TypeWorkBuddy, Name: "wb"}
	server.pool.AddOrUpdate(wb)
	server.pool.SetAccountModels("wb1", []pool.ModelMetadata{{ID: "deepseek-v4.1-flash"}})

	resp, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatalf("GET /api/models: %v", err)
	}
	defer resp.Body.Close()

	var items []UIModelItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode /api/models: %v", err)
	}

	providers := map[string]int{}
	for _, it := range items {
		providers[it.Provider]++
	}
	t.Logf("dashboard models per provider: %v", providers)

	for _, want := range []string{"workbuddy", "antigravity", "qoder"} {
		if providers[want] == 0 {
			t.Errorf("dashboard is missing provider %q", want)
		}
	}

	// Capability flags must come from the catalog, not a name heuristic: a
	// vision-capable model must be flagged as such.
	for _, it := range items {
		if it.ID == "gpt-4o" && !it.SupportsImages {
			t.Error("gpt-4o is not flagged as image-capable")
		}
	}
}

// TestDashboardAndV1ModelsAgree checks the two model endpoints expose the same
// set of names, since a client and the dashboard should never disagree.
func TestDashboardAndV1ModelsAgree(t *testing.T) {
	server, ts := newModelServer(t)

	wb := &pool.Account{ID: "wb1", Type: pool.TypeWorkBuddy, Name: "wb"}
	server.pool.AddOrUpdate(wb)
	server.pool.SetAccountModels("wb1", []pool.ModelMetadata{{ID: "deepseek-v4.1-flash"}})

	apiList := fetchModels(t, ts)
	apiSet := map[string]bool{}
	for _, m := range apiList.Data {
		apiSet[m.ID] = true
	}

	resp, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatalf("GET /api/models: %v", err)
	}
	defer resp.Body.Close()
	var items []UIModelItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode /api/models: %v", err)
	}

	dashSet := map[string]bool{}
	for _, it := range items {
		dashSet[it.ID] = true
	}

	for id := range apiSet {
		if !dashSet[id] {
			t.Errorf("%q is in /v1/models but missing from the dashboard", id)
		}
	}
	for id := range dashSet {
		if !apiSet[id] {
			t.Errorf("%q is in the dashboard but missing from /v1/models", id)
		}
	}
}
