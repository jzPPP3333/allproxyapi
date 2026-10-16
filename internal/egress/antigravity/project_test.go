package antigravity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

func TestLoadCodeAssistResponseProjectFieldOrder(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"cloudaicompanionProject", `{"cloudaicompanionProject":"proj-a"}`, "proj-a"},
		{"cloudCodeProject", `{"cloudCodeProject":"proj-b"}`, "proj-b"},
		{"project", `{"project":"proj-c"}`, "proj-c"},
		{"projectId", `{"projectId":"proj-d"}`, "proj-d"},
		{"nested data", `{"data":{"cloudaicompanionProject":"proj-e"}}`, "proj-e"},
		{"nested project", `{"data":{"project":"proj-f"}}`, "proj-f"},
		{"whitespace trimmed", `{"project":"  proj-g  "}`, "proj-g"},
		{"none present", `{"tier":"free"}`, ""},
		{"empty object", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var parsed loadCodeAssistResponse
			if err := json.Unmarshal([]byte(tc.body), &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := parsed.project(); got != tc.want {
				t.Fatalf("project() = %q, want %q", got, tc.want)
			}
		})
	}
}

// newProbeClient points a Client at a test server.
func newProbeClient(t *testing.T, handler http.HandlerFunc) (*Client, *config.AntigravityConfig) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := &config.AntigravityConfig{
		BaseURL:        srv.URL,
		DefaultProject: "fallback-project",
	}
	return NewClient(cfg), cfg
}

func TestResolveProjectUsesAccountValue(t *testing.T) {
	var called bool
	client, _ := newProbeClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	acc := &pool.Account{ID: "acc-1", ProjectID: "from-account"}
	if got := client.ResolveProject(context.Background(), acc); got != "from-account" {
		t.Fatalf("ResolveProject = %q, want the account's own project", got)
	}
	if called {
		t.Fatal("a request was made even though the account already had a project")
	}
}

func TestResolveProjectDiscoversAndCaches(t *testing.T) {
	ForgetProject("acc-2")

	var calls int
	client, _ := newProbeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":"discovered-project"}`))
	})

	acc := &pool.Account{ID: "acc-2", AccessToken: "tok"}
	if got := client.ResolveProject(context.Background(), acc); got != "discovered-project" {
		t.Fatalf("first resolve = %q", got)
	}
	if acc.ProjectID != "discovered-project" {
		t.Fatalf("discovered project was not cached on the account: %q", acc.ProjectID)
	}

	// The second account has no project, but the cache is keyed by account id,
	// so a different account must still probe.
	other := &pool.Account{ID: "acc-3", AccessToken: "tok"}
	_ = client.ResolveProject(context.Background(), other)
	before := calls

	// Re-resolving the first account must come from the cache.
	again := &pool.Account{ID: "acc-2", AccessToken: "tok"}
	if got := client.ResolveProject(context.Background(), again); got != "discovered-project" {
		t.Fatalf("cached resolve = %q", got)
	}
	if calls != before {
		t.Fatalf("cached account triggered %d extra request(s)", calls-before)
	}
}

func TestResolveProjectFallsBackOnFailure(t *testing.T) {
	ForgetProject("acc-4")

	client, _ := newProbeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})

	acc := &pool.Account{ID: "acc-4", AccessToken: "tok"}
	got := client.ResolveProject(context.Background(), acc)
	if got != "fallback-project" {
		t.Fatalf("ResolveProject = %q, want the configured fallback", got)
	}
}

func TestResolveProjectFallsBackWhenBodyHasNoProject(t *testing.T) {
	ForgetProject("acc-5")

	client, _ := newProbeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tier":"free"}`))
	})

	acc := &pool.Account{ID: "acc-5", AccessToken: "tok"}
	if got := client.ResolveProject(context.Background(), acc); got != "fallback-project" {
		t.Fatalf("ResolveProject = %q, want the fallback when no project is reported", got)
	}
}

func TestLoadCodeAssistHandlesNilAccount(t *testing.T) {
	client, _ := newProbeClient(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := client.LoadCodeAssist(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil account")
	}
}

func TestResolveProjectDefaultConstants(t *testing.T) {
	// With no configured default the package constant applies.
	cfg := &config.AntigravityConfig{}
	client := NewClient(cfg)
	if got := client.defaultProject(); got != DefaultProjectID {
		t.Fatalf("defaultProject() = %q, want %q", got, DefaultProjectID)
	}
}

func TestForgetProjectClearsCache(t *testing.T) {
	ForgetProject("acc-6")

	var calls int
	client, _ := newProbeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"project":"p"}`))
	})

	acc := &pool.Account{ID: "acc-6", AccessToken: "tok"}
	_ = client.ResolveProject(context.Background(), acc)
	if calls != 1 {
		t.Fatalf("expected 1 discovery call, got %d", calls)
	}

	// After a rejection the caller drops the cache, so the next resolve probes.
	ForgetProject("acc-6")
	fresh := &pool.Account{ID: "acc-6", AccessToken: "tok"}
	_ = client.ResolveProject(context.Background(), fresh)
	if calls != 2 {
		t.Fatalf("expected a second discovery call after ForgetProject, got %d", calls)
	}
}
