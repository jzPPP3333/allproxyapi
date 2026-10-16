package qoder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// TestNeedsExchangeDetection covers the first-use exchange trigger.
func TestNeedsExchangeDetection(t *testing.T) {
	cases := []struct {
		name string
		acc  *pool.Account
		want bool
	}{
		{"fresh personal token", &pool.Account{AccessToken: "pt-abc"}, true},
		{"already exchanged", &pool.Account{AccessToken: "jt-abc", RefreshToken: "pt-orig"}, false},
		{"device token", &pool.Account{AccessToken: "dt-abc"}, false},
		{"job token", &pool.Account{AccessToken: "jt-abc"}, false},
		{"nil account", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsExchange(tc.acc); got != tc.want {
				t.Fatalf("NeedsExchange = %v, want %v", got, tc.want)
			}
		})
	}
}

// exchangeServer stands in for the auth host and records what it was asked for.
func exchangeServer(t *testing.T, jobToken string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body["personal_token"])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      jobToken,
			"expires_in": 3600,
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// TestExchangeKeepsPersonalToken is the important one: the personal token must
// be retained in the refresh field, otherwise the account can never renew and
// dies when its first job token expires.
func TestExchangeKeepsPersonalToken(t *testing.T) {
	srv, _ := exchangeServer(t, "jt-fresh")
	acc := &pool.Account{ID: "q1", AccessToken: "pt-personal-123"}

	if err := RefreshAccount(context.Background(), acc, "cn", srv.URL, ""); err != nil {
		t.Fatalf("RefreshAccount: %v", err)
	}

	if acc.AccessToken != "jt-fresh" {
		t.Errorf("access token = %q, want the job token", acc.AccessToken)
	}
	if acc.RefreshToken != "pt-personal-123" {
		t.Fatalf("refresh token = %q; the personal token must be kept for renewal", acc.RefreshToken)
	}
	if acc.ExpiresAt.IsZero() || !acc.ExpiresAt.After(time.Now()) {
		t.Errorf("expiry not set from the response: %v", acc.ExpiresAt)
	}
	if NeedsExchange(acc) {
		t.Error("an exchanged account still reports needing an exchange")
	}
}

// TestRepeatedExchangeUsesRetainedPersonalToken checks the second renewal works
// after the first job token expires.
func TestRepeatedExchangeUsesRetainedPersonalToken(t *testing.T) {
	srv, seen := exchangeServer(t, "jt-fresh")
	acc := &pool.Account{ID: "q1", AccessToken: "pt-personal-123"}

	// First exchange.
	if err := RefreshAccount(context.Background(), acc, "cn", srv.URL, ""); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	// Second renewal now that the access token is a job token.
	if err := RefreshAccount(context.Background(), acc, "cn", srv.URL, ""); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	if len(*seen) != 2 {
		t.Fatalf("expected 2 exchange calls, got %d", len(*seen))
	}
	for i, tok := range *seen {
		if tok != "pt-personal-123" {
			t.Errorf("call %d sent %q, want the retained personal token", i, tok)
		}
	}
}

// TestRefreshRejectsNonRenewableCredential checks a bare job token reports
// clearly instead of silently doing nothing.
func TestRefreshRejectsNonRenewableCredential(t *testing.T) {
	acc := &pool.Account{ID: "q1", AccessToken: "jt-only"}
	err := RefreshAccount(context.Background(), acc, "cn", "http://127.0.0.1:1", "")
	if err == nil {
		t.Fatal("expected an error for a non-renewable credential")
	}
	if !strings.Contains(err.Error(), "non-refreshable") {
		t.Errorf("error = %v, want it to name the credential as non-refreshable", err)
	}
}

// TestChatExchangesBeforeFirstRequest proves an un-exchanged personal token is
// converted on the way out rather than sent and rejected.
func TestChatExchangesBeforeFirstRequest(t *testing.T) {
	var authCalls, chatCalls int
	var chatAuthHeader string

	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "jt-from-exchange", "expires_in": 3600})
	}))
	defer authSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls++
		chatAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\n"))
	}))
	defer chatSrv.Close()

	client := NewClient(config.QoderConfig{Region: "cn", BaseURL: chatSrv.URL, AuthURL: authSrv.URL})
	acc := &pool.Account{ID: "q1", AccountName: "u@example.com", AccessToken: "pt-personal-123"}

	resp, err := client.Chat(context.Background(), acc, []byte(`{"model":"ultimate","messages":[]}`))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	resp.Body.Close()

	if authCalls != 1 {
		t.Errorf("exchange calls = %d, want 1 before the first request", authCalls)
	}
	if chatCalls != 1 {
		t.Errorf("chat calls = %d, want 1", chatCalls)
	}
	if chatAuthHeader != "Bearer jt-from-exchange" {
		t.Errorf("chat Authorization = %q, want the exchanged job token", chatAuthHeader)
	}
}

// TestChatSkipsExchangeForUsableToken checks a device token goes straight out
// with no exchange round trip.
func TestChatSkipsExchangeForUsableToken(t *testing.T) {
	var authCalls int
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls++
	}))
	defer authSrv.Close()

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer chatSrv.Close()

	client := NewClient(config.QoderConfig{Region: "cn", BaseURL: chatSrv.URL, AuthURL: authSrv.URL})
	acc := &pool.Account{ID: "q1", AccessToken: "dt-device"}

	resp, err := client.Chat(context.Background(), acc, []byte(`{"model":"ultimate","messages":[]}`))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	resp.Body.Close()

	if authCalls != 0 {
		t.Errorf("a device token triggered %d exchange call(s), want 0", authCalls)
	}
}
