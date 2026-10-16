package ingress

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// TestRunCheckinRecordsTimestamps guards a regression where the deferred
// FinishedAt assignment wrote to a local copy and the zero value was returned.
func TestRunCheckinRecordsTimestamps(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	server.pool.AddOrUpdate(&pool.Account{ID: "ag-1", Type: pool.TypeAntigravity, Name: "ag"})

	summary := server.RunCheckin(context.Background(), "antigravity", "")

	if summary.StartedAt.IsZero() {
		t.Fatal("StartedAt was not recorded")
	}
	if summary.FinishedAt.IsZero() {
		t.Fatal("FinishedAt was not recorded")
	}
	if summary.FinishedAt.Before(summary.StartedAt) {
		t.Fatalf("FinishedAt %v precedes StartedAt %v", summary.FinishedAt, summary.StartedAt)
	}
	if time.Since(summary.FinishedAt) > time.Minute {
		t.Fatalf("FinishedAt %v is not recent", summary.FinishedAt)
	}
}

func TestRunCheckinAllRecordsTimestamps(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	server.pool.AddOrUpdate(&pool.Account{ID: "ag-1", Type: pool.TypeAntigravity, Name: "ag"})

	summary := server.RunCheckinAll(context.Background())

	if summary.StartedAt.IsZero() || summary.FinishedAt.IsZero() {
		t.Fatalf("timestamps were not recorded: start=%v finish=%v", summary.StartedAt, summary.FinishedAt)
	}
	if summary.FinishedAt.Before(summary.StartedAt) {
		t.Fatalf("FinishedAt %v precedes StartedAt %v", summary.FinishedAt, summary.StartedAt)
	}
}

// TestRunCheckinUnknownProviderRecordsTimestamps covers the early-return path,
// which is where a missing defer is easiest to miss.
func TestRunCheckinUnknownProviderRecordsTimestamps(t *testing.T) {
	server, _ := checkinTestServer(t, func(w http.ResponseWriter, r *http.Request) {})

	summary := server.RunCheckin(context.Background(), "does-not-exist", "")
	if summary.FinishedAt.IsZero() {
		t.Fatal("FinishedAt was not recorded on the unknown-provider path")
	}
}
