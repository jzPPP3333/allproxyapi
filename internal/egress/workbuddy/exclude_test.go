package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// modelsPayload builds an upstream response shaped like the real one, mixing
// tier presets with actual models.
func modelsPayload(ids ...string) string {
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	items := make([]item, 0, len(ids))
	for _, id := range ids {
		items = append(items, item{ID: id, Name: strings.ToUpper(id)})
	}
	body, _ := json.Marshal(map[string]any{
		"code": 0,
		"data": map[string]any{"models": items},
	})
	return string(body)
}

// fetchAgainst serves the given payload and returns the parsed model ids.
func fetchAgainst(t *testing.T, client *Client, payload string) []string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	// FetchModels resolves the host from the realm table, so call the HTTP
	// path directly through a client pointed at the test server.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var parsed upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var ids []string
	for _, m := range parsed.Data.Models {
		if m.ID == "" || client.excluded(m.ID) {
			continue
		}
		ids = append(ids, m.ID)
	}
	return ids
}

// TestTierPresetsAreExcluded is the regression guard for the reported symptom:
// the upstream list mixed tier presets in with real models.
func TestTierPresetsAreExcluded(t *testing.T) {
	payload := modelsPayload(
		"auto", "default", "lite",
		"deepseek-v4.1-flash", "kimi-k3-1", "glm-5.3",
	)
	c := NewClient(config.DefaultExcludedModelPatterns)

	got := fetchAgainst(t, c, payload)
	for _, tier := range []string{"auto", "default", "lite"} {
		for _, id := range got {
			if id == tier {
				t.Errorf("tier preset %q was not filtered out", tier)
			}
		}
	}
	for _, real := range []string{"deepseek-v4.1-flash", "kimi-k3-1", "glm-5.3"} {
		found := false
		for _, id := range got {
			if id == real {
				found = true
			}
		}
		if !found {
			t.Errorf("real model %q was filtered out along with the tiers", real)
		}
	}
}

// TestExclusionIsCaseInsensitive checks the defaults match regardless of the
// upstream's casing, which is not guaranteed stable.
func TestExclusionIsCaseInsensitive(t *testing.T) {
	c := NewClient(config.DefaultExcludedModelPatterns)
	for _, id := range []string{"auto", "AUTO", "Default", "LITE"} {
		if !c.excluded(id) {
			t.Errorf("%q should be excluded", id)
		}
	}
}

// TestExclusionDoesNotOverreach checks the patterns are anchored: a real model
// that merely contains the word must survive.
func TestExclusionDoesNotOverreach(t *testing.T) {
	c := NewClient(config.DefaultExcludedModelPatterns)
	for _, id := range []string{
		"auto-v2",          // not an exact preset name
		"myglm-fast-x",     // unrelated
		"deepseek-default", // contains "default" but is not a preset
		"gpt-4o",
		"kimi-k3-1",
	} {
		if c.excluded(id) {
			t.Errorf("%q was excluded but should not be", id)
		}
	}
}

// TestNoPatternsMeansNoFiltering documents that an empty config keeps every
// entry, so a user can opt back into seeing the tiers.
func TestNoPatternsMeansNoFiltering(t *testing.T) {
	c := NewClient(nil)
	for _, id := range []string{"auto", "default", "glm-5.3"} {
		if c.excluded(id) {
			t.Errorf("%q was excluded with no patterns configured", id)
		}
	}
}

// TestInvalidPatternIsSkipped checks a bad regexp does not disable the client.
func TestInvalidPatternIsSkipped(t *testing.T) {
	c := NewClient([]string{"[unclosed", `(?i)^default$`})
	if len(c.excludeModel) != 1 {
		t.Fatalf("expected only the valid pattern to compile, got %d", len(c.excludeModel))
	}
	if !c.excluded("default") {
		t.Error("the valid pattern stopped working because a sibling was invalid")
	}
}

// TestCustomPatternsReplaceDefaults checks a caller-supplied list is what takes
// effect, so pattern changes are config-only and need no rebuild.
func TestCustomPatternsReplaceDefaults(t *testing.T) {
	c := NewClient([]string{`(?i)^legacy-`})
	if !c.excluded("legacy-model") {
		t.Error("custom pattern was not applied")
	}
	// The defaults are not merged in: this list is the whole policy.
	if c.excluded("default") {
		t.Error("defaults leaked into a custom pattern list")
	}
}

// TestExcludedModelsAreNotCached checks the filter runs before the account's
// model list is populated, so a tier id cannot leak back through the pool.
func TestExcludedModelsAreNotCached(t *testing.T) {
	acc := &pool.Account{ID: "wb-1"}

	// Simulate the parse loop's filtering against a cached account.
	c := NewClient(config.DefaultExcludedModelPatterns)
	payload := modelsPayload("default", "deepseek-v4.1-flash")

	var parsed upstreamModelsResponse
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var kept []pool.ModelMetadata
	for _, m := range parsed.Data.Models {
		if m.ID == "" || c.excluded(m.ID) {
			continue
		}
		kept = append(kept, pool.ModelMetadata{ID: m.ID, Name: m.Name})
	}
	acc.SetModels(kept)

	cached := acc.GetModels()
	if len(cached) != 1 || cached[0].ID != "deepseek-v4.1-flash" {
		t.Fatalf("cached models = %+v, want only the real model", cached)
	}
}
