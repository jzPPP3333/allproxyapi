package qoder

import "testing"

func TestIsQuotaExhausted(t *testing.T) {
	if !isQuotaExhausted(402) {
		t.Fatal("402 Payment Required must count as exhausted quota")
	}
	for _, status := range []int{200, 400, 401, 403, 404, 429, 500, 503} {
		if isQuotaExhausted(status) {
			t.Errorf("status %d incorrectly classified as exhausted quota", status)
		}
	}
}

func TestQuotaCodeOf(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"top-level 6004", `{"code":6004,"msg":"insufficient credits"}`, 6004},
		{"daily quota", `{"code":6005}`, 6005},
		{"plan limit", `{"code":10006}`, 10006},
		{"nested data code", `{"code":0,"data":{"code":6004}}`, 6004},
		{"unknown code", `{"code":1234}`, 0},
		{"no code", `{"msg":"ok"}`, 0},
		{"not json", `not json at all`, 0},
		{"empty", ``, 0},
		{"quota text without a code", `{"error":"insufficient credits"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaCodeOf([]byte(tc.body)); got != tc.want {
				t.Fatalf("quotaCodeOf(%q) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestQuotaCooldownIsLongerThanRateLimit(t *testing.T) {
	// A quota cooldown must outlast the 429 window, otherwise a retry just
	// burns the same exhausted account again.
	if QuotaCooldown <= 60*1e9*1e0 {
		t.Fatalf("QuotaCooldown %v is not longer than the 60s rate-limit window", QuotaCooldown)
	}
}
