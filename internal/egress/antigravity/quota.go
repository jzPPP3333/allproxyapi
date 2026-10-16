package antigravity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// QuotaCooldown is how long an account is parked after its allowance runs out.
// Refills are slow, so retrying inside the rate-limit window only re-sends the
// same request into the same wall.
const QuotaCooldown = 30 * time.Minute

// quotaPhrases are the error strings the Cloud Code PA returns when a plan
// allowance is exhausted. The endpoint reports these under 403 as often as 429,
// so the body is inspected rather than trusting the status alone.
var quotaPhrases = []string{
	"resource_exhausted",
	"quota exceeded",
	"quota_exceeded",
	"out of quota",
	"insufficient quota",
	"daily limit",
	"no remaining",
	"exceeded your current quota",
}

// isQuotaExhausted reports whether a status code itself identifies exhausted
// quota. 402 is Payment Required and always counts.
func isQuotaExhausted(status int) bool { return status == http.StatusPaymentRequired }

// quotaTextReason inspects a 403 body for a quota phrase and returns a cooldown
// reason when one is present.
func quotaTextReason(raw []byte) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}

	// A structured status field is the most precise signal.
	var probe struct {
		Error *struct {
			Status  string `json:"status"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Error != nil {
		if strings.EqualFold(probe.Error.Status, "RESOURCE_EXHAUSTED") ||
			strings.EqualFold(probe.Error.Status, "QUOTA_EXCEEDED") {
			return fmt.Sprintf("quota exhausted (%s)", probe.Error.Status), true
		}
		if containsQuotaPhrase(probe.Error.Message) {
			return fmt.Sprintf("quota exhausted: %s", truncate(probe.Error.Message, 160)), true
		}
	}

	if containsQuotaPhrase(string(raw)) {
		return "quota exhausted", true
	}
	return "", false
}

// quotaReason builds a cooldown reason for a status code plus body.
func quotaReason(status int, raw []byte) string {
	if reason, ok := quotaTextReason(raw); ok {
		return reason
	}
	return fmt.Sprintf("%d quota exhausted", status)
}

func containsQuotaPhrase(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, phrase := range quotaPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
