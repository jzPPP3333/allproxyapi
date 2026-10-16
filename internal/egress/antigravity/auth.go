package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
	"github.com/jzPPP3333/allproxyapi/internal/util"
)

const (
	DefaultOAuthTokenURL = "https://oauth2.googleapis.com/token"
	DefaultBaseURL       = "https://daily-cloudcode-pa.googleapis.com"
	DefaultProjectID     = "aicode-consumers"
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
}

func RefreshToken(ctx context.Context, acc *pool.Account, clientID, clientSecret, proxyURL string) error {
	if acc.RefreshToken == "" {
		return fmt.Errorf("no refresh token available for antigravity account %s", acc.ID)
	}

	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("antigravity account %s cannot refresh: providers.antigravity.client_id and client_secret must be set in the config file", acc.ID)
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", acc.RefreshToken)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Host", "oauth2.googleapis.com")
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("User-Agent", "Go-http-client/2.0")

	// Detect proxy if not explicitly passed
	effectiveProxy := util.DetectProxyURL(proxyURL)
	client := util.NewHTTPClient(effectiveProxy, true, 30*time.Second)

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("antigravity token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read token response body failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("token refresh returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return fmt.Errorf("unmarshal token response failed: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return fmt.Errorf("token refresh returned empty access_token")
	}

	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	acc.SetTokens(tokenResp.AccessToken, tokenResp.RefreshToken, expiresAt)

	if acc.ProjectID == "" {
		acc.ProjectID = DefaultProjectID
	}

	return nil
}
