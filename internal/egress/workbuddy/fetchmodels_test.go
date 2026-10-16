package workbuddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// upstreamModelsBody is the real response shape, taken from the ids the
// dashboard showed: four tier presets mixed in with genuine models, each tier
// carrying a display name and a credit multiplier.
const upstreamModelsBody = `{
  "code": 0,
  "msg": "ok",
  "data": {
    "models": [
      {"id":"default-model","name":"Auto","descriptionZh":"优秀的编码模型，适合日常使用","credits":"x0.79","supportsToolCall":true,"supportsReasoning":false,"supportsImages":true},
      {"id":"fast-model","name":"Fast","descriptionZh":"响应快，适合简单任务","credits":"","supportsToolCall":true,"supportsReasoning":false,"supportsImages":false},
      {"id":"balanced-model","name":"Balanced","descriptionZh":"速度与质量兼顾，日常工作首选","credits":"x0.59","supportsToolCall":true,"supportsReasoning":true,"supportsImages":true},
      {"id":"primary-model","name":"Primary","descriptionZh":"高质量输出，胜任复杂任务","credits":"x3.31","supportsToolCall":true,"supportsReasoning":true,"supportsImages":false},
      {"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","credits":"x0.15","supportsToolCall":true},
      {"id":"kimi-k3-1","name":"Kimi K3.1","supportsToolCall":true,"supportsReasoning":true},
      {"id":"gpt-4o","name":"GPT-4o","supportsToolCall":true,"supportsImages":true}
    ],
    "agents": [
      {"name":"coder","models":["default-model","deepseek-v4.1-flash"]}
    ]
  }
}`

// withFakeUpstream points the realm host at a test server for the duration of
// the test, so FetchModels runs its real code path end to end.
func withFakeUpstream(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	original := Realms
	Realms = map[string]RealmConfig{
		"cn": {
			ChatUpstream: srv.URL,
			Origin:       srv.URL,
			Domain:       "test.local",
			UserAgent:    "test",
			Version:      "test",
		},
	}
	t.Cleanup(func() { Realms = original })
}

// TestFetchModelsDropsTierPresets is the end-to-end guard for the reported
// symptom, running the same path the gateway uses against the real payload.
func TestFetchModelsDropsTierPresets(t *testing.T) {
	withFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v2/enterprises/personal/models") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamModelsBody))
	})

	c := NewClient(config.DefaultExcludedModelPatterns)
	acc := &pool.Account{ID: "wb-1", Realm: "cn", UID: "u1", AccessToken: "tok"}

	models, err := c.FetchModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}

	got := map[string]bool{}
	for _, m := range models {
		got[m.ID] = true
	}
	t.Logf("returned %d models: %v", len(models), keysOf(got))

	for _, tier := range []string{"default-model", "fast-model", "balanced-model", "primary-model"} {
		if got[tier] {
			t.Errorf("tier preset %q is still listed", tier)
		}
	}
	for _, real := range []string{"deepseek-v4.1-flash", "kimi-k3-1", "gpt-4o"} {
		if !got[real] {
			t.Errorf("real model %q was dropped", real)
		}
	}

	// The tier ids also appear in the agents list; the filter must apply there.
	if len(models) != 3 {
		t.Errorf("expected exactly the 3 real models, got %d: %v", len(models), keysOf(got))
	}
}

// TestFetchModelsKeepsMetadata checks filtering does not strip the metadata of
// the models that survive.
func TestFetchModelsKeepsMetadata(t *testing.T) {
	withFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamModelsBody))
	})

	c := NewClient(config.DefaultExcludedModelPatterns)
	acc := &pool.Account{ID: "wb-1", Realm: "cn", UID: "u1", AccessToken: "tok"}

	models, err := c.FetchModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}

	for _, m := range models {
		if m.ID == "deepseek-v4.1-flash" {
			if m.Name != "DeepSeek V4.1 Flash" {
				t.Errorf("name lost: %q", m.Name)
			}
			if m.Credits != "x0.15" {
				t.Errorf("credits lost: %q", m.Credits)
			}
			if !m.SupportsToolCall {
				t.Error("capability flags lost")
			}
			return
		}
	}
	t.Fatal("deepseek-v4.1-flash not found in the result")
}

// TestFetchModelsCachesFilteredList checks the account's cached list is the
// filtered one, so a tier id cannot reappear through the pool.
func TestFetchModelsCachesFilteredList(t *testing.T) {
	withFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamModelsBody))
	})

	c := NewClient(config.DefaultExcludedModelPatterns)
	acc := &pool.Account{ID: "wb-1", Realm: "cn", UID: "u1", AccessToken: "tok"}

	if _, err := c.FetchModels(context.Background(), acc); err != nil {
		t.Fatalf("FetchModels: %v", err)
	}

	for _, m := range acc.GetModels() {
		if strings.HasSuffix(m.ID, "-model") {
			t.Errorf("tier preset %q was cached on the account", m.ID)
		}
	}
}

// TestFetchModelsWithoutFilterKeepsEverything documents that clearing the
// patterns shows the raw upstream list, so the behaviour is opt-out.
func TestFetchModelsWithoutFilterKeepsEverything(t *testing.T) {
	withFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamModelsBody))
	})

	c := NewClient(nil)
	acc := &pool.Account{ID: "wb-1", Realm: "cn", UID: "u1", AccessToken: "tok"}

	models, err := c.FetchModels(context.Background(), acc)
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 7 {
		t.Fatalf("expected all 7 entries with no filter, got %d: %v", len(models), modelIDs(models))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func modelIDs(models []pool.ModelMetadata) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}
