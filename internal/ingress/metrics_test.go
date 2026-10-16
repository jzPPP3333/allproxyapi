package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

func TestMetricsCountsAndPercentiles(t *testing.T) {
	m := NewMetrics()

	for i := 1; i <= 100; i++ {
		m.Observe(RequestRecord{
			Status:         200,
			Model:          "m1",
			Provider:       "qoder",
			Account:        "acct-a",
			TTFTMillis:     int64(i),
			DurationMillis: int64(i * 2),
		})
	}
	m.Observe(RequestRecord{Status: 502, Model: "m1", Provider: "qoder", Err: "boom"})

	snap := m.Snapshot()
	if snap.Total != 101 {
		t.Errorf("total = %d, want 101", snap.Total)
	}
	if snap.Succeeded != 100 {
		t.Errorf("succeeded = %d, want 100", snap.Succeeded)
	}
	if snap.Failed != 1 {
		t.Errorf("failed = %d, want 1", snap.Failed)
	}
	if snap.ByModel["m1"] != 101 {
		t.Errorf("by_model[m1] = %d, want 101", snap.ByModel["m1"])
	}
	if snap.ByProvider["qoder"] != 101 {
		t.Errorf("by_provider[qoder] = %d", snap.ByProvider["qoder"])
	}
	if snap.ByAccount["acct-a"] != 100 {
		t.Errorf("by_account[acct-a] = %d, want 100", snap.ByAccount["acct-a"])
	}
	if snap.ByStatus[200] != 100 || snap.ByStatus[502] != 1 {
		t.Errorf("by_status = %v", snap.ByStatus)
	}

	// The window holds the most recent 256 samples; here it holds all 100.
	if snap.TTFTSamples != 100 {
		t.Errorf("ttft samples = %d, want 100", snap.TTFTSamples)
	}
	if snap.TTFTMillisP50 == 0 || snap.TTFTMillisP95 == 0 {
		t.Errorf("percentiles not computed: p50=%d p95=%d", snap.TTFTMillisP50, snap.TTFTMillisP95)
	}
	if snap.TTFTMillisP95 < snap.TTFTMillisP50 {
		t.Errorf("p95 (%d) below p50 (%d)", snap.TTFTMillisP95, snap.TTFTMillisP50)
	}
}

// TestMetricsErrorCountsAsFailure checks a 200 with an error is not counted as
// a success, which would hide upstream failures.
func TestMetricsErrorCountsAsFailure(t *testing.T) {
	m := NewMetrics()
	m.Observe(RequestRecord{Status: 200, Err: "upstream returned no content"})

	snap := m.Snapshot()
	if snap.Failed != 1 || snap.Succeeded != 0 {
		t.Fatalf("succeeded=%d failed=%d, want 0/1", snap.Succeeded, snap.Failed)
	}
}

// TestMetricsWindowIsBounded checks the latency window does not grow without
// limit, and that it holds the most recent samples: fed a slow burst after a
// long fast run, the reported p95 must reflect the burst.
func TestMetricsWindowIsBounded(t *testing.T) {
	m := NewMetrics()

	// Fill the window entirely with fast samples.
	for i := 0; i < metricsWindow; i++ {
		m.Observe(RequestRecord{Status: 200, TTFTMillis: 1})
	}
	// Then a burst of slow ones large enough to occupy the top of the window.
	for i := 0; i < metricsWindow/4; i++ {
		m.Observe(RequestRecord{Status: 200, TTFTMillis: 9000})
	}

	snap := m.Snapshot()
	if snap.TTFTSamples != metricsWindow {
		t.Fatalf("samples = %d, want the window size %d", snap.TTFTSamples, metricsWindow)
	}
	if snap.TTFTMillisP95 < 5000 {
		t.Errorf("p95 = %d; the window is not holding the recent burst", snap.TTFTMillisP95)
	}
	// p50 sits in the fast majority, so it must not have been dragged up.
	if snap.TTFTMillisP50 != 1 {
		t.Errorf("p50 = %d, want the fast majority's value", snap.TTFTMillisP50)
	}
}

// TestMetricsLogIsNewestFirst checks the log order the panel relies on.
func TestMetricsLogIsNewestFirst(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < 5; i++ {
		m.Observe(RequestRecord{Status: 200, Model: string(rune('a' + i))})
	}

	rows := m.RecentRequests(10)
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	if rows[0].Model != "e" {
		t.Errorf("first row = %q, want the newest", rows[0].Model)
	}
	if rows[4].Model != "a" {
		t.Errorf("last row = %q, want the oldest", rows[4].Model)
	}
}

// TestMetricsLogIsBounded checks the ring does not grow without limit.
func TestMetricsLogIsBounded(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < requestLogLimit*2; i++ {
		m.Observe(RequestRecord{Status: 200})
	}
	if rows := m.RecentRequests(100000); len(rows) != requestLogLimit {
		t.Fatalf("log holds %d rows, want the cap %d", len(rows), requestLogLimit)
	}
}

func TestMetricsReset(t *testing.T) {
	m := NewMetrics()
	m.Observe(RequestRecord{Status: 200, Model: "m"})
	m.Reset()

	snap := m.Snapshot()
	if snap.Total != 0 || len(snap.ByModel) != 0 {
		t.Fatalf("reset left data behind: %+v", snap)
	}
	if rows := m.RecentRequests(10); len(rows) != 0 {
		t.Fatalf("reset left %d log rows", len(rows))
	}
}

// TestMetricsEndpoint covers the API surface.
func TestMetricsEndpoint(t *testing.T) {
	_, ts := newModelServer(t)
	// Generate one recorded request through the real handler path.
	req, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	resp, err := http.Get(ts.URL + "/api/metrics")
	if err != nil {
		t.Fatalf("GET /api/metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var payload struct {
		Summary  MetricsSnapshot `json:"summary"`
		Requests []RequestRecord `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Summary.Total == 0 {
		t.Error("the /v1 request was not recorded")
	}
	if len(payload.Requests) == 0 {
		t.Error("no request log entries returned")
	}
}

func TestMetricsEndpointRejectsPost(t *testing.T) {
	_, ts := newModelServer(t)
	resp, err := http.Post(ts.URL+"/api/metrics", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

// TestTTFTCapturedOnStreamedResponse checks the first write is timed.
func TestTTFTCapturedOnStreamedResponse(t *testing.T) {
	start := time.Now()
	rw := &statusResponseWriter{ResponseWriter: httptest.NewRecorder(), statusCode: 200, at: start}

	time.Sleep(5 * time.Millisecond)
	if _, err := rw.Write([]byte("data: hello\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := rw.firstWriteMillis(start); got < 3 {
		t.Fatalf("ttft = %dms, want the elapsed time", got)
	}
}

// TestStatusResponseWriterForwardsFlush checks streaming is not broken by the
// wrapper: without a Flush passthrough the SSE path would buffer.
func TestStatusResponseWriterForwardsFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &statusResponseWriter{ResponseWriter: rec, at: time.Now()}
	rw.Flush() // must not panic and must mark the first write
	if rw.firstWrite.IsZero() {
		t.Error("flush did not record the first write time")
	}
}

// TestRequestAttributionReachesMetrics checks the model, provider and account a
// handler learned show up in the metrics record, so the panel's per-model and
// per-account breakdowns are populated rather than blank.
func TestRequestAttributionReachesMetrics(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.APIKey = "k"
	p := pool.NewAccountPool()
	p.AddOrUpdate(&pool.Account{ID: "wb1", Type: pool.TypeWorkBuddy, Name: "wb-account"})

	server := NewServer(cfg, p, newTestRegistry(cfg))
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	body := `{"model":"kimi-k3-1","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	snap := server.metrics.Snapshot()
	if snap.ByModel["kimi-k3-1"] == 0 {
		t.Errorf("the requested model was not attributed: %v", snap.ByModel)
	}
	if snap.ByProvider["workbuddy"] == 0 {
		t.Errorf("the provider was not attributed: %v", snap.ByProvider)
	}
	// The upstream call fails with the test registry, but the attribution set
	// before dispatch must still be recorded.
	rows := server.metrics.RecentRequests(10)
	if len(rows) == 0 {
		t.Fatal("no request recorded")
	}
	if rows[0].Model != "kimi-k3-1" {
		t.Errorf("recorded model = %q, want the requested name", rows[0].Model)
	}
}

// TestAccountAttributedOnFailure checks the account is recorded even when the
// upstream call fails, since that is when an operator needs to know which
// account was tried.
func TestAccountAttributedOnFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.APIKey = "k"
	p := pool.NewAccountPool()
	// A credential that cannot succeed, so the request fails after dispatch.
	p.AddOrUpdate(&pool.Account{
		ID: "wb1", Type: pool.TypeWorkBuddy, Name: "failing-account",
		AccessToken: "bad", Realm: "cn",
	})

	server := NewServer(cfg, p, newTestRegistry(cfg))
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	body := `{"model":"kimi-k3-1","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	rows := server.metrics.RecentRequests(5)
	if len(rows) == 0 {
		t.Fatal("no request recorded")
	}
	if rows[0].Account == "" {
		t.Errorf("the failing account was not attributed: %+v", rows[0])
	}
	if rows[0].Err == "" {
		t.Error("the failure reason was not recorded")
	}
}
