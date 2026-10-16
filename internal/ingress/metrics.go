package ingress

import (
	"sort"
	"sync"
	"time"
)

// Operational visibility: an operator needs to know whether the gateway is
// serving requests, how fast the first token arrives, and what recently failed.
// All of it is kept in memory — this is a local single-binary gateway, and
// shipping a metrics pipeline with it would be out of proportion.

// metricsWindow is how many recent requests feed the latency figures. A ring
// rather than a running mean so a burst cannot skew the reported latency
// indefinitely.
const metricsWindow = 256

// requestLogLimit bounds the in-memory request log.
const requestLogLimit = 200

// RequestRecord is one completed request, as shown in the panel.
type RequestRecord struct {
	At       time.Time `json:"at"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Model    string    `json:"model,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Account  string    `json:"account,omitempty"`
	Status   int       `json:"status"`
	Stream   bool      `json:"stream"`
	// TTFTMillis is the time to first token. Zero for a non-streaming request,
	// where the first token and the last arrive together.
	TTFTMillis int64 `json:"ttft_ms,omitempty"`
	// DurationMillis is the total time spent on the request.
	DurationMillis int64 `json:"duration_ms"`
	// Tokens is the completion token count when the upstream reported one.
	Tokens int    `json:"tokens,omitempty"`
	Err    string `json:"error,omitempty"`
}

// Metrics aggregates request statistics.
type Metrics struct {
	mu sync.Mutex

	total     int64
	succeeded int64
	failed    int64

	// startedAt anchors the average rates so they do not decay to nothing.
	startedAt time.Time

	// ttfts holds recent first-token latencies so a percentile can be computed.
	ttfts   []int64
	ttftPos int

	// byModel and byProvider count requests, for the consumption breakdown.
	byModel    map[string]int64
	byProvider map[string]int64
	byAccount  map[string]int64
	byStatus   map[int]int64

	// log is a bounded ring of recent requests.
	log    []RequestRecord
	logPos int
}

// NewMetrics builds an empty metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		startedAt:  time.Now(),
		ttfts:      make([]int64, 0, metricsWindow),
		byModel:    make(map[string]int64),
		byProvider: make(map[string]int64),
		byAccount:  make(map[string]int64),
		byStatus:   make(map[int]int64),
		log:        make([]RequestRecord, 0, requestLogLimit),
	}
}

// Observe records a completed request.
func (m *Metrics) Observe(r RequestRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.total++
	if r.Status >= 200 && r.Status < 400 && r.Err == "" {
		m.succeeded++
	} else {
		m.failed++
	}
	m.byStatus[r.Status]++

	if r.Model != "" {
		m.byModel[r.Model]++
	}
	if r.Provider != "" {
		m.byProvider[r.Provider]++
	}
	if r.Account != "" {
		m.byAccount[r.Account]++
	}

	if r.TTFTMillis > 0 {
		if len(m.ttfts) < metricsWindow {
			m.ttfts = append(m.ttfts, r.TTFTMillis)
		} else {
			m.ttfts[m.ttftPos%metricsWindow] = r.TTFTMillis
		}
		// ttftPos counts every sample taken, so the slot is its value modulo
		// the window rather than the number of samples currently retained.
		m.ttftPos = (m.ttftPos + 1) % metricsWindow
	}

	if len(m.log) < requestLogLimit {
		m.log = append(m.log, r)
	} else {
		m.log[m.logPos%requestLogLimit] = r
	}
	m.logPos++
}

// MetricsSnapshot is a point-in-time view for the panel.
type MetricsSnapshot struct {
	UptimeSeconds int64            `json:"uptime_seconds"`
	Total         int64            `json:"total"`
	Succeeded     int64            `json:"succeeded"`
	Failed        int64            `json:"failed"`
	QPS           float64          `json:"qps"`
	TTFTMillisP50 int64            `json:"ttft_ms_p50"`
	TTFTMillisP95 int64            `json:"ttft_ms_p95"`
	TTFTSamples   int              `json:"ttft_samples"`
	ByModel       map[string]int64 `json:"by_model"`
	ByProvider    map[string]int64 `json:"by_provider"`
	ByAccount     map[string]int64 `json:"by_account"`
	ByStatus      map[int]int64    `json:"by_status"`
}

// Snapshot returns the aggregate figures.
func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	elapsed := time.Since(m.startedAt).Seconds()
	snap := MetricsSnapshot{
		UptimeSeconds: int64(elapsed),
		Total:         m.total,
		Succeeded:     m.succeeded,
		Failed:        m.failed,
		TTFTSamples:   len(m.ttfts),
		ByModel:       copyCounts(m.byModel),
		ByProvider:    copyCounts(m.byProvider),
		ByAccount:     copyCounts(m.byAccount),
	}
	if elapsed > 0 {
		snap.QPS = float64(m.total) / elapsed
	}

	// Percentiles over a copy so the lock is not held during the sort.
	if len(m.ttfts) > 0 {
		sorted := append([]int64(nil), m.ttfts...)
		sortInt64(sorted)
		snap.TTFTMillisP50 = percentile(sorted, 0.50)
		snap.TTFTMillisP95 = percentile(sorted, 0.95)
	}

	snap.ByStatus = make(map[int]int64, len(m.byStatus))
	for k, v := range m.byStatus {
		snap.ByStatus[k] = v
	}
	return snap
}

// RecentRequests returns the log newest first, up to limit entries.
func (m *Metrics) RecentRequests(limit int) []RequestRecord {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored := len(m.log)
	if stored == 0 {
		return nil
	}
	if limit <= 0 || limit > stored {
		limit = stored
	}
	out := make([]RequestRecord, 0, limit)

	// Walk backwards from the most recently written slot. logPos counts every
	// write ever made, so it must be reduced modulo the capacity before it can
	// index the buffer; otherwise it walks off the end of the slice once the
	// ring has wrapped.
	for i := 0; i < limit; i++ {
		idx := (m.logPos - 1 - i) % stored
		if idx < 0 {
			idx += stored
		}
		out = append(out, m.log[idx])
	}
	return out
}

// Reset clears all collected metrics, for a manual operator reset.
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.total, m.succeeded, m.failed = 0, 0, 0
	m.startedAt = time.Now()
	m.ttfts = m.ttfts[:0]
	m.ttftPos = 0
	m.byModel = make(map[string]int64)
	m.byProvider = make(map[string]int64)
	m.byAccount = make(map[string]int64)
	m.byStatus = make(map[int]int64)
	m.log = m.log[:0]
	m.logPos = 0
}

func copyCounts(src map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// percentile returns the value at p (0..1) of a sorted slice, using nearest-rank.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func sortInt64(v []int64) { sort.Slice(v, func(i, j int) bool { return v[i] < v[j] }) }
