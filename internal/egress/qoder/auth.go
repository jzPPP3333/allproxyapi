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

// TokenPrefixes are the credential families Qoder issues.
//
//	pt-  personal access token, exchanged once for a job token
//	jt-  short-lived job token, used directly on the inference host
//	dt-  device-flow token, accepted by the inference host directly
const (
	PrefixPersonal      = "pt-"
	PrefixJob           = "jt-"
	PrefixDevice        = "dt-"
	PrefixDeviceRefresh = "drt-"
)

type tokenExchangeResponse struct {
	Token        string `json:"token"`
	JobToken     string `json:"job_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	ExpiresIn    int64  `json:"expires_in"`
	UserID       string `json:"user_id"`
	Code         int    `json:"code"`
	Message      string `json:"message"`
	Msg          string `json:"msg"`
}

func (t tokenExchangeResponse) accessToken() string {
	for _, candidate := range []string{t.Token, t.JobToken, t.AccessToken} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func (t tokenExchangeResponse) message() string {
	if t.Message != "" {
		return t.Message
	}
	return t.Msg
}

func (t tokenExchangeResponse) expiry() time.Time {
	if t.ExpiresAt > 0 {
		return time.Unix(t.ExpiresAt, 0)
	}
	if t.ExpiresIn > 0 {
		return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return time.Time{}
}

// ExchangePersonalToken converts a pt- personal access token into a short-lived
// job token. The exchange endpoint needs no request signing.
func ExchangePersonalToken(ctx context.Context, personalToken, region, authURLOverride, proxyURL string) (string, time.Time, error) {
	authURL := resolveAuthURL(region, authURLOverride)

	body, err := json.Marshal(map[string]string{"personal_token": personalToken})
	if err != nil {
		return "", time.Time{}, wrapErr("marshal exchange body", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL+"/api/v1/jobToken/exchange", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, wrapErr("build exchange request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := newHTTPClient(proxyURL, 30*time.Second).Do(req)
	if err != nil {
		return "", time.Time{}, wrapErr("exchange request", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("qoder token exchange returned %d: %s", resp.StatusCode, truncate(raw, 512))
	}

	var parsed tokenExchangeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", time.Time{}, wrapErr("decode exchange response", err)
	}

	token := parsed.accessToken()
	if token == "" {
		return "", time.Time{}, fmt.Errorf("qoder token exchange returned no token (message: %s)", parsed.message())
	}
	return token, parsed.expiry(), nil
}

// RefreshJobToken exchanges a drt- device refresh token for a fresh access
// token. Accounts holding a jt- token have no refresh path and are re-imported
// instead.
func RefreshJobToken(ctx context.Context, refreshToken, region, authURLOverride, proxyURL string) (string, string, time.Time, error) {
	authURL := resolveAuthURL(region, authURLOverride)

	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return "", "", time.Time{}, wrapErr("marshal refresh body", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL+"/api/v1/jobToken/refresh", bytes.NewReader(body))
	if err != nil {
		return "", "", time.Time{}, wrapErr("build refresh request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := newHTTPClient(proxyURL, 30*time.Second).Do(req)
	if err != nil {
		return "", "", time.Time{}, wrapErr("refresh request", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", time.Time{}, fmt.Errorf("qoder token refresh returned %d: %s", resp.StatusCode, truncate(raw, 512))
	}

	var parsed tokenExchangeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", time.Time{}, wrapErr("decode refresh response", err)
	}

	token := parsed.accessToken()
	if token == "" {
		return "", "", time.Time{}, fmt.Errorf("qoder token refresh returned no token (message: %s)", parsed.message())
	}
	return token, parsed.RefreshToken, parsed.expiry(), nil
}

// RefreshAccount obtains a fresh usable token for an account, in place.
//
// A pt- personal token is long-lived but not directly usable: it is exchanged
// for a short-lived job token. The pt- is retained in the refresh field so the
// exchange can be repeated indefinitely, which is what makes a pt- import
// self-renewing rather than something that dies when its first job token does.
// A drt- device refresh token is refreshed normally. Nothing else has a
// renewal path.
func RefreshAccount(ctx context.Context, acc *pool.Account, region, authURLOverride, proxyURL string) error {
	switch {
	case strings.HasPrefix(acc.RefreshToken, PrefixDeviceRefresh):
		token, newRefresh, expiry, err := RefreshJobToken(ctx, acc.RefreshToken, region, authURLOverride, proxyURL)
		if err != nil {
			return err
		}
		if newRefresh == "" {
			newRefresh = acc.RefreshToken
		}
		acc.SetTokens(token, newRefresh, expiry)
		return nil

	case strings.HasPrefix(acc.AccessToken, PrefixPersonal):
		personal := acc.AccessToken
		token, expiry, err := ExchangePersonalToken(ctx, personal, region, authURLOverride, proxyURL)
		if err != nil {
			return err
		}
		acc.SetTokens(token, personal, expiry)
		return nil

	case strings.HasPrefix(acc.RefreshToken, PrefixPersonal):
		// A job token obtained earlier has expired; re-exchange the personal
		// token kept in the refresh field.
		personal := acc.RefreshToken
		token, expiry, err := ExchangePersonalToken(ctx, personal, region, authURLOverride, proxyURL)
		if err != nil {
			return err
		}
		acc.SetTokens(token, personal, expiry)
		return nil

	default:
		return fmt.Errorf("qoder account %s holds a non-refreshable credential", acc.ID)
	}
}

// NeedsExchange reports whether a credential must be exchanged before first
// use, which is true for a personal token that has not been converted yet.
func NeedsExchange(acc *pool.Account) bool {
	if acc == nil {
		return false
	}
	return strings.HasPrefix(acc.AccessToken, PrefixPersonal) && !strings.HasPrefix(acc.RefreshToken, PrefixPersonal)
}

func truncate(b []byte, max int) string {
	s := string(b)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
