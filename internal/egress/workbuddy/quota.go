package workbuddy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// QuotaCooldown is how long an account is parked once its allowance is spent.
// The refill cadence is slow, so a short cooldown would just retry the same
// exhausted account.
const QuotaCooldown = 30 * time.Minute

// quotaCodes are the application-level codes CodeBuddy returns when the account
// has no remaining allowance. The upstream answers with HTTP 200 in some cases
// and 4xx in others, so the body is inspected separately from the status.
var quotaCodes = map[int]bool{
	6004:  true, // insufficient credits
	6005:  true, // daily quota reached
	10006: true, // plan limit reached
	40103: true, // account quota exhausted
}

// quotaPhrases catch deployments that report exhaustion in prose only.
var quotaPhrases = []string{
	"insufficient credit",
	"insufficient quota",
	"quota exceeded",
	"quota_exhausted",
	"out of quota",
	"daily limit",
	"weekly limit",
	"no remaining",
	"not enough credit",
	"余额不足",
	"额度不足",
	"配额不足",
	"已达上限",
}

// isQuotaExhausted reports whether the status code alone identifies exhausted
// quota. 402 is Payment Required and always counts.
func isQuotaExhausted(status int) bool { return status == http.StatusPaymentRequired }

// quotaCodeOf extracts a known exhaustion code from a response body.
func quotaCodeOf(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var probe struct {
		Code int `json:"code"`
		Data *struct {
			Code int `json:"code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		// A non-JSON 200 body can still name the condition in prose.
		return 0
	}
	candidates := []int{probe.Code}
	if probe.Data != nil {
		candidates = append(candidates, probe.Data.Code)
	}
	for _, code := range candidates {
		if quotaCodes[code] {
			return code
		}
	}
	return 0
}

// quotaTextReason inspects a body for a quota signal and builds a reason.
func quotaTextReason(raw []byte) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	if code := quotaCodeOf(raw); code != 0 {
		return fmt.Sprintf("quota exhausted (code %d)", code), true
	}
	lower := strings.ToLower(string(raw))
	for _, phrase := range quotaPhrases {
		if strings.Contains(lower, phrase) {
			return "quota exhausted", true
		}
	}
	return "", false
}

// quotaReason builds a cooldown reason from a status code and body.
func quotaReason(status int, raw []byte) string {
	if reason, ok := quotaTextReason(raw); ok {
		return reason
	}
	return fmt.Sprintf("%d quota exhausted", status)
}
