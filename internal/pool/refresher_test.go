package pool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestHasRenewableCredential covers the case that left imported Qoder personal
// tokens un-renewable: the credential lives in the access field and the refresh
// field is empty, so a refresh-token-only check skipped them.
func TestHasRenewableCredential(t *testing.T) {
	cases := []struct {
		name string
		acc  *Account
		want bool
	}{
		{"qoder personal token", &Account{Type: TypeQoder, AccessToken: "pt-abc"}, true},
		{"qoder device token", &Account{Type: TypeQoder, AccessToken: "dt-abc"}, false},
		{"qoder job token", &Account{Type: TypeQoder, AccessToken: "jt-abc"}, false},
		// Another provider's token with the same prefix must not qualify.
		{"workbuddy with pt- prefix", &Account{Type: TypeWorkBuddy, AccessToken: "pt-abc"}, false},
		{"antigravity with pt- prefix", &Account{Type: TypeAntigravity, AccessToken: "pt-abc"}, false},
		{"empty", &Account{Type: TypeQoder}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasRenewableCredential(tc.acc); got != tc.want {
				t.Fatalf("hasRenewableCredential = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRefresherRenewsExpiringPersonalToken proves the refresher now picks up an
// expiring personal-token account and calls the provider handler for it.
func TestRefresherRenewsExpiringPersonalToken(t *testing.T) {
	p := NewAccountPool()
	p.AddOrUpdate(&Account{
		ID:          "q1",
		Type:        TypeQoder,
		Name:        "q",
		AccessToken: "pt-personal",
		// Expires within the refresh window.
		ExpiresAt: time.Now().Add(2 * time.Minute),
	})

	var calls int64
	r := NewRefresher(p, time.Minute)
	r.RegisterHandler(TypeQoder, func(ctx context.Context, acc *Account) error {
		atomic.AddInt64(&calls, 1)
		return nil
	})

	r.CheckAndRefreshAll()

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("handler calls = %d, want 1 for an expiring personal token", got)
	}
}

// TestRefresherSkipsHealthyAccount checks the window is respected so a healthy
// account is not refreshed on every tick.
func TestRefresherSkipsHealthyAccount(t *testing.T) {
	p := NewAccountPool()
	p.AddOrUpdate(&Account{
		ID:          "q1",
		Type:        TypeQoder,
		AccessToken: "pt-personal",
		ExpiresAt:   time.Now().Add(12 * time.Hour),
	})

	var calls int64
	r := NewRefresher(p, time.Minute)
	r.RegisterHandler(TypeQoder, func(ctx context.Context, acc *Account) error {
		atomic.AddInt64(&calls, 1)
		return nil
	})

	r.CheckAndRefreshAll()

	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("handler calls = %d, want 0 for a healthy account", got)
	}
}

// TestRefresherStillSkipsNonRenewable checks the change did not start refreshing
// every account: a job token with nothing to renew stays untouched.
func TestRefresherStillSkipsNonRenewable(t *testing.T) {
	p := NewAccountPool()
	p.AddOrUpdate(&Account{
		ID:          "q1",
		Type:        TypeQoder,
		AccessToken: "jt-only",
		ExpiresAt:   time.Now().Add(-time.Hour), // already expired
	})

	var calls int64
	r := NewRefresher(p, time.Minute)
	r.RegisterHandler(TypeQoder, func(ctx context.Context, acc *Account) error {
		atomic.AddInt64(&calls, 1)
		return nil
	})

	r.CheckAndRefreshAll()

	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("handler calls = %d, want 0 for a non-renewable credential", got)
	}
}
