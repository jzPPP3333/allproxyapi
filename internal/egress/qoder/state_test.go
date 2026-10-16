package qoder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// TestFailedExchangeDisablesAccount is a regression guard: the exchange runs
// before the request is sent, so a failure there used to return without
// touching the account's state. The account was then selected again on the very
// next request and failed identically, forever.
func TestFailedExchangeDisablesAccount(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errorMessage":"bad token"}`))
	}))
	defer authSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the chat host must not be reached when the exchange fails")
	}))
	defer chatSrv.Close()

	client := NewClient(config.QoderConfig{Region: "cn", BaseURL: chatSrv.URL, AuthURL: authSrv.URL})
	acc := &pool.Account{ID: "q1", AccountName: "u@example.com", AccessToken: "pt-bad"}

	if _, err := client.Chat(context.Background(), acc, []byte(`{"model":"ultimate","messages":[]}`)); err == nil {
		t.Fatal("expected the exchange failure to surface")
	}

	if acc.State() != pool.StateDisabled {
		t.Fatalf("state = %q, want disabled so the account stops being retried", acc.State())
	}
	if acc.AvailableFor("ultimate") {
		t.Error("a disabled account still reported available")
	}
	if acc.DisabledReason() == "" {
		t.Error("no reason recorded for the operator")
	}
}

// TestSuccessfulExchangeLeavesAccountActive checks the healthy path is not
// affected: an account whose exchange works stays in rotation.
func TestSuccessfulExchangeLeavesAccountActive(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "jt-good", "expires_in": 3600})
	}))
	defer authSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer chatSrv.Close()

	client := NewClient(config.QoderConfig{Region: "cn", BaseURL: chatSrv.URL, AuthURL: authSrv.URL})
	acc := &pool.Account{ID: "q1", AccessToken: "pt-good"}

	resp, err := client.Chat(context.Background(), acc, []byte(`{"model":"ultimate","messages":[]}`))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	resp.Body.Close()

	if acc.State() != pool.StateActive {
		t.Fatalf("state = %q, want active", acc.State())
	}
	if !acc.AvailableFor("ultimate") {
		t.Error("an exchanged account reported unavailable")
	}
}
