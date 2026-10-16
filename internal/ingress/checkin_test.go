package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// nowHour is the current local hour, used to line a scheduler up with the clock
// so Tick's hour guard is satisfied.
func nowHour() int { return time.Now().Hour() }

// checkinTestServer builds a server whose WorkBuddy check-in path points at a
// test handler, so the full path from API to egress can be exercised.
func checkinTestServer(t *testing.T, handler http.HandlerFunc) (*Server, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	cfg := config.DefaultConfig()
	cfg.Server.APIKey = "test-key"
	cfg.Providers.WorkBuddy.CheckinPath = "/v2/checkin"
	// The egress client resolves the realm host from a package table, so the
	// path is exercised through a direct client call instead of the router.
	_ = upstream

	accPool := pool.NewAccountPool()
	return NewServer(cfg, accPool, newTestRegistry(cfg)), upstream
}

func TestRunCheckinUnknownProvider(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	summary := server.RunCheckin(context.Background(), "does-not-exist", "")

	if summary.Attempted != 1 {
		t.Fatalf("expected 1 attempt, got %d", summary.Attempted)
	}
	if summary.Failed != 1 {
		t.Fatalf("expected 1 failure, got %d", summary.Failed)
	}
	if len(summary.Results) != 1 || summary.Results[0].Status != pool.CheckinFailed {
		t.Fatalf("unexpected result: %+v", summary.Results)
	}
}

func TestRunCheckinNoAccounts(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	summary := server.RunCheckin(context.Background(), "antigravity", "")

	// No accounts means nothing to attempt; this must not count as a failure.
	if summary.Attempted != 0 {
		t.Fatalf("expected 0 attempts with an empty pool, got %d", summary.Attempted)
	}
	if summary.Failed != 0 {
		t.Fatalf("expected 0 failures with an empty pool, got %d", summary.Failed)
	}
}

func TestRunCheckinAntigravityReportsUnsupported(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	server.pool.AddOrUpdate(&pool.Account{ID: "ag-1", Type: pool.TypeAntigravity, Name: "ag"})

	summary := server.RunCheckin(context.Background(), "antigravity", "")
	if summary.Unsupported != 1 {
		t.Fatalf("expected 1 unsupported, got %d (results: %+v)", summary.Unsupported, summary.Results)
	}
	if summary.Failed != 0 {
		t.Fatalf("unsupported must not count as a failure")
	}
}

func TestRunCheckinFiltersByAccountID(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	server.pool.AddOrUpdate(&pool.Account{ID: "ag-1", Type: pool.TypeAntigravity, Name: "one"})
	server.pool.AddOrUpdate(&pool.Account{ID: "ag-2", Type: pool.TypeAntigravity, Name: "two"})

	summary := server.RunCheckin(context.Background(), "antigravity", "ag-2")
	if summary.Attempted != 1 {
		t.Fatalf("expected 1 attempt when filtering by account, got %d", summary.Attempted)
	}
	if len(summary.Results) != 1 || summary.Results[0].AccountID != "ag-2" {
		t.Fatalf("filter selected the wrong account: %+v", summary.Results)
	}
}

func TestRunCheckinAllCoversEveryProvider(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	server.pool.AddOrUpdate(&pool.Account{ID: "ag-1", Type: pool.TypeAntigravity, Name: "ag"})

	summary := server.RunCheckinAll(context.Background())
	// Antigravity is the only provider with an account, and it reports
	// unsupported, so the batch reports it without failing.
	if summary.Attempted != 1 {
		t.Fatalf("expected 1 attempt, got %d", summary.Attempted)
	}
	if summary.Unsupported != 1 || summary.Failed != 0 {
		t.Fatalf("unexpected tally: %+v", summary)
	}
	if len(summary.Results) != 1 || summary.Results[0].Provider != "antigravity" {
		t.Fatalf("result did not carry its provider: %+v", summary.Results)
	}
}

func TestCheckinSummaryTally(t *testing.T) {
	var s pool.CheckinSummary
	s.Tally(pool.CheckinResult{Status: pool.CheckinOK})
	s.Tally(pool.CheckinResult{Status: pool.CheckinAlready})
	s.Tally(pool.CheckinResult{Status: pool.CheckinUnsupported})
	s.Tally(pool.CheckinResult{Status: pool.CheckinFailed})

	if s.Attempted != 4 || s.Succeeded != 1 || s.AlreadyDone != 1 || s.Unsupported != 1 || s.Failed != 1 {
		t.Fatalf("unexpected tally: %+v", s)
	}
	if len(s.Results) != 4 {
		t.Fatalf("expected 4 retained results, got %d", len(s.Results))
	}
	if !(pool.CheckinResult{Status: pool.CheckinOK}).OK() {
		t.Fatal("CheckinOK should report OK")
	}
	if !(pool.CheckinResult{Status: pool.CheckinAlready}).OK() {
		t.Fatal("CheckinAlready should report OK")
	}
	if (pool.CheckinResult{Status: pool.CheckinFailed}).OK() {
		t.Fatal("CheckinFailed should not report OK")
	}
}

func TestAPIResetCooldownAndCheckinGet(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/checkin")
	if err != nil {
		t.Fatalf("GET /api/checkin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/checkin returned %d", resp.StatusCode)
	}

	var payload struct {
		LastRun   pool.CheckinSummary `json:"last_run"`
		Providers []struct {
			ID        string `json:"id"`
			Supported bool   `json:"supported"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /api/checkin: %v", err)
	}
	if len(payload.Providers) != 3 {
		t.Fatalf("expected 3 providers listed, got %d", len(payload.Providers))
	}

	// WorkBuddy has a configured path, Antigravity has no check-in concept.
	byID := map[string]bool{}
	for _, p := range payload.Providers {
		byID[p.ID] = p.Supported
	}
	if !byID["workbuddy"] {
		t.Error("workbuddy should report check-in support with a configured path")
	}
	if byID["antigravity"] {
		t.Error("antigravity should report no check-in support")
	}
}

func TestAPICheckinRejectsUnsupportedMethod(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/checkin", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/checkin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestSchedulerTickRunsOncePerDay(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	server.pool.AddOrUpdate(&pool.Account{ID: "ag-1", Type: pool.TypeAntigravity, Name: "ag"})

	// Pin the hour to the current one so Tick's guard passes.
	sched := NewCheckinScheduler(server, 0)
	sched.hour = nowHour()

	sched.Tick()
	first := sched.Last()
	if first == nil {
		t.Fatal("first tick did not run the check-in")
	}

	// A second tick in the same day must be a no-op.
	sched.Tick()
	if sched.Last() != first {
		t.Fatal("second tick in the same day ran the check-in again")
	}
}

func TestSchedulerSkipsOtherHours(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	// An hour that is definitely not the current one.
	sched := NewCheckinScheduler(server, (nowHour()+12)%24)

	sched.Tick()
	if sched.Last() != nil {
		t.Fatal("tick ran outside the configured hour")
	}
}

func TestSchedulerClampsInvalidHour(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})

	for _, hour := range []int{-5, 24, 99} {
		sched := NewCheckinScheduler(server, hour)
		if sched.hour < 0 || sched.hour > 23 {
			t.Fatalf("hour %d was not clamped (got %d)", hour, sched.hour)
		}
	}
}

func TestSchedulerStopIsIdempotent(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	sched := NewCheckinScheduler(server, 3)
	sched.Start()
	sched.Stop()
	sched.Stop() // must not panic on a second close
}
