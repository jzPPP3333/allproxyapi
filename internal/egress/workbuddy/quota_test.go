package workbuddy

import "testing"

func TestIsQuotaExhaustedStatus(t *testing.T) {
	if !isQuotaExhausted(402) {
		t.Fatal("402 Payment Required must count as exhausted quota")
	}
	for _, status := range []int{200, 400, 401, 403, 404, 429, 500, 502} {
		if isQuotaExhausted(status) {
			t.Errorf("status %d incorrectly classified as exhausted quota", status)
		}
	}
}

func TestQuotaCodeOfWorkBuddy(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"insufficient credits", `{"code":6004,"msg":"insufficient credits"}`, 6004},
		{"daily quota", `{"code":6005}`, 6005},
		{"plan limit", `{"code":10006}`, 10006},
		{"account quota", `{"code":40103}`, 40103},
		{"nested code", `{"code":0,"data":{"code":6004}}`, 6004},
		{"unrelated code", `{"code":0,"msg":"ok"}`, 0},
		{"not json", `<<html>>`, 0},
		{"empty", ``, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaCodeOf([]byte(tc.body)); got != tc.want {
				t.Fatalf("quotaCodeOf(%q) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestQuotaTextReasonPhrases(t *testing.T) {
	// English and Chinese phrasings observed from the CodeBuddy backend.
	bodies := []string{
		`{"code":6004,"msg":"insufficient credits"}`,
		`{"msg":"quota exceeded for this account"}`,
		`{"msg":"余额不足，请充值"}`,
		`{"msg":"额度不足"}`,
		`{"msg":"no remaining quota"}`,
	}
	for _, body := range bodies {
		if _, ok := quotaTextReason([]byte(body)); !ok {
			t.Errorf("quotaTextReason(%q) did not detect exhaustion", body)
		}
	}

	for _, body := range []string{
		`{"code":0,"msg":"ok"}`,
		`{"msg":"invalid request parameter"}`,
		``,
	} {
		if reason, ok := quotaTextReason([]byte(body)); ok {
			t.Errorf("quotaTextReason(%q) falsely reported %q", body, reason)
		}
	}
}

func TestQuotaReasonFallsBackToStatus(t *testing.T) {
	// An unparseable body still yields a usable reason.
	reason := quotaReason(402, []byte("garbage"))
	if reason == "" {
		t.Fatal("quotaReason returned an empty reason")
	}
}

func TestQuotaCooldownOutlastsRateLimit(t *testing.T) {
	if QuotaCooldown.Seconds() < 60 {
		t.Fatalf("QuotaCooldown %v is not longer than the 60s rate-limit window", QuotaCooldown)
	}
}
