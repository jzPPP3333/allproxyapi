package qoder

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

// Checkin performs Qoder's daily check-in.
//
// The route is deployment-specific and configured rather than hardcoded, so an
// unset path reports the feature as unsupported instead of probing a guessed
// URL against a live account.
func (c *Client) Checkin(ctx context.Context, acc *pool.Account, checkinPath string) (pool.CheckinResult, error) {
	result := pool.CheckinResult{
		AccountID:   acc.ID,
		AccountName: acc.Name,
		At:          time.Now(),
	}

	// The device identity is established regardless of whether a check-in route
	// is configured, so the account presents one stable fingerprint from its
	// first use onward.
	EnsureFingerprint(acc)

	if strings.TrimSpace(checkinPath) == "" {
		result.Status = pool.CheckinUnsupported
		result.Message = "providers.qoder.checkin_path is not configured"
		return result, nil
	}

	authURL := resolveAuthURL(c.regionOf(acc), c.cfg.AuthURL)
	endpoint := authURL + checkinPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return result, fmt.Errorf("qoder checkin: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+acc.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Request-ID", uuid())
	req.Header.Set("X-Client-Version", c.clientVersion())
	if acc.MachineID != "" {
		req.Header.Set("X-Machine-Id", acc.MachineID)
	}
	if acc.MachineToken != "" {
		req.Header.Set("X-Machine-Token", acc.MachineToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("qoder checkin: request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if refreshErr := RefreshAccount(ctx, acc, c.regionOf(acc), c.cfg.AuthURL, c.cfg.Proxy); refreshErr != nil {
			return result, fmt.Errorf("qoder checkin: unauthorized and refresh failed: %w", refreshErr)
		}
		retry, retryErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
		if retryErr != nil {
			return result, fmt.Errorf("qoder checkin: rebuild request: %w", retryErr)
		}
		retry.Header.Set("Authorization", "Bearer "+acc.AccessToken)
		retry.Header.Set("Content-Type", "application/json")
		retry.Header.Set("X-Machine-Id", acc.MachineID)
		retry.Header.Set("X-Machine-Token", acc.MachineToken)

		resp2, doErr := c.httpClient.Do(retry)
		if doErr != nil {
			return result, fmt.Errorf("qoder checkin: retry request: %w", doErr)
		}
		defer resp2.Body.Close()
		raw, _ = io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
		return classifyCheckin(result, resp2.StatusCode, raw), nil
	}

	return classifyCheckin(result, resp.StatusCode, raw), nil
}

// checkinResponse mirrors the Qoder check-in reply.
type checkinResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Msg     string `json:"msg"`
	Data    struct {
		Already  bool     `json:"already"`
		Signed   bool     `json:"signed"`
		Rewards  []string `json:"rewards"`
		Reward   string   `json:"reward"`
		Credits  int      `json:"credits"`
		Message  string   `json:"message"`
		SignDays int      `json:"signDays"`
	} `json:"data"`
}

func classifyCheckin(result pool.CheckinResult, status int, raw []byte) pool.CheckinResult {
	var parsed checkinResponse
	_ = json.Unmarshal(raw, &parsed)

	message := parsed.Message
	if message == "" {
		message = parsed.Msg
	}
	if message == "" {
		message = parsed.Data.Message
	}
	if message != "" {
		result.Message = message
	}

	if parsed.Data.Reward != "" && !containsString(parsed.Data.Rewards, parsed.Data.Reward) {
		result.Rewards = append(result.Rewards, parsed.Data.Reward)
	}
	result.Rewards = append(result.Rewards, parsed.Data.Rewards...)
	if parsed.Data.Credits > 0 {
		result.Rewards = append(result.Rewards, fmt.Sprintf("%d credits", parsed.Data.Credits))
	}

	lower := strings.ToLower(message)
	alreadyDone := parsed.Data.Already ||
		strings.Contains(lower, "already") ||
		strings.Contains(message, "已签") ||
		strings.Contains(message, "重复")

	switch {
	case alreadyDone:
		result.Status = pool.CheckinAlready
	case status >= 200 && status < 300 && parsed.Code == 0:
		result.Status = pool.CheckinOK
	case quotaCodeOf(raw) != 0:
		result.Status = pool.CheckinFailed
		if result.Message == "" {
			result.Message = "quota exhausted"
		}
	default:
		result.Status = pool.CheckinFailed
		if result.Message == "" {
			result.Message = fmt.Sprintf("status %d: %s", status, truncate(raw, 200))
		}
	}
	return result
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
