package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

// Checkin performs WorkBuddy's daily check-in.
//
// The route is deployment-specific, so it is configured rather than hardcoded:
// callers supply the path in providers.workbuddy.checkin_path. When it is
// absent the provider reports the feature as unsupported instead of guessing a
// URL and failing against a live account.
func (c *Client) Checkin(ctx context.Context, acc *pool.Account, checkinPath string) (pool.CheckinResult, error) {
	result := pool.CheckinResult{
		AccountID:   acc.ID,
		AccountName: acc.Name,
		At:          time.Now(),
	}

	if strings.TrimSpace(checkinPath) == "" {
		result.Status = pool.CheckinUnsupported
		result.Message = "providers.workbuddy.checkin_path is not configured"
		return result, nil
	}

	realm := "cn"
	if acc.Realm != "" {
		realm = acc.Realm
	}
	cfg := GetRealmConfig(realm)
	endpoint := cfg.ChatUpstream + checkinPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return result, fmt.Errorf("workbuddy checkin: build request: %w", err)
	}

	// The check-in endpoint sits behind the same identity gate as inference, so
	// the same header set applies.
	for k, v := range BuildIdentityHeaders(acc) {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("workbuddy checkin: request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		if refreshErr := RefreshToken(ctx, acc); refreshErr != nil {
			return result, fmt.Errorf("workbuddy checkin: unauthorized and refresh failed: %w", refreshErr)
		}
		// Retry once with the refreshed credential.
		retry, retryErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
		if retryErr != nil {
			return result, fmt.Errorf("workbuddy checkin: rebuild request: %w", retryErr)
		}
		for k, v := range BuildIdentityHeaders(acc) {
			retry.Header.Set(k, v)
		}
		resp2, doErr := c.httpClient.Do(retry)
		if doErr != nil {
			return result, fmt.Errorf("workbuddy checkin: retry request: %w", doErr)
		}
		defer resp2.Body.Close()
		raw, _ = io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
		result = classifyCheckin(result, resp2.StatusCode, raw)
		return result, nil
	}

	result = classifyCheckin(result, resp.StatusCode, raw)
	return result, nil
}

// checkinResponse is the shape WorkBuddy replies with. The reward fields vary
// by deployment, so every known spelling is probed.
type checkinResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Already   bool     `json:"already"`
		Checked   bool     `json:"checkedIn"`
		Rewards   []string `json:"rewards"`
		Reward    string   `json:"reward"`
		Integral  int      `json:"integral"`
		Message   string   `json:"message"`
		SignedDay int      `json:"signedDay"`
	} `json:"data"`
}

// classifyCheckin turns an HTTP status plus body into a result status.
func classifyCheckin(result pool.CheckinResult, status int, raw []byte) pool.CheckinResult {
	var parsed checkinResponse
	_ = json.Unmarshal(raw, &parsed)

	message := parsed.Msg
	if message == "" {
		message = parsed.Data.Message
	}
	if message != "" {
		result.Message = message
	}

	// Reward strings are surfaced verbatim so the dashboard shows what arrived.
	if parsed.Data.Reward != "" && !containsString(parsed.Data.Rewards, parsed.Data.Reward) {
		result.Rewards = append(result.Rewards, parsed.Data.Reward)
	}
	result.Rewards = append(result.Rewards, parsed.Data.Rewards...)
	if parsed.Data.Integral > 0 {
		result.Rewards = append(result.Rewards, fmt.Sprintf("%d 积分", parsed.Data.Integral))
	}

	alreadyDone := parsed.Data.Already ||
		strings.Contains(strings.ToLower(message), "already") ||
		strings.Contains(message, "已签") ||
		strings.Contains(message, "重复")

	switch {
	case alreadyDone:
		result.Status = pool.CheckinAlready
		return result
	case status >= 200 && status < 300 && parsed.Code == 0:
		result.Status = pool.CheckinOK
		return result
	case quotaCodeOf(raw) != 0:
		result.Status = pool.CheckinFailed
		if result.Message == "" {
			result.Message = "quota exhausted"
		}
		return result
	default:
		result.Status = pool.CheckinFailed
		if result.Message == "" {
			result.Message = fmt.Sprintf("status %d: %s", status, truncateForMessage(raw))
		}
		return result
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func truncateForMessage(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "..."
}
