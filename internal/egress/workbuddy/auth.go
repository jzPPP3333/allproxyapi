package workbuddy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

type RefreshResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		Data         *struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"data"`
	} `json:"data"`
}

func RefreshToken(ctx context.Context, acc *pool.Account) error {
	if acc.RefreshToken == "" {
		return fmt.Errorf("no refresh token available for account %s", acc.ID)
	}

	realm := "cn"
	if acc.Realm != "" {
		realm = acc.Realm
	}
	cfg := GetRealmConfig(realm)

	url := cfg.ChatUpstream + "/v2/plugin/auth/token/refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Origin", cfg.Origin)
	req.Header.Set("Referer", cfg.Origin+"/")
	req.Header.Set("X-Refresh-Token", acc.RefreshToken)
	if realm == "cn" {
		req.Header.Set("X-Auth-Refresh-Source", "workbuddy")
		req.Header.Set("Accept-Language", "zh-CN")
	} else {
		req.Header.Set("X-Auth-Refresh-Source", "plugin")
		req.Header.Set("Accept-Language", "en-US")
	}
	req.Header.Set("X-User-Id", acc.UID)
	domain := acc.Domain
	if domain == "" {
		domain = cfg.Domain
	}
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-CodeBuddy-Request", "1")

	if acc.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", acc.EnterpriseID)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read refresh response body failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var parsed RefreshResponse
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		return fmt.Errorf("unmarshal refresh response failed: %w", err)
	}

	accessToken := parsed.Data.AccessToken
	refreshToken := parsed.Data.RefreshToken
	if accessToken == "" && parsed.Data.Data != nil {
		accessToken = parsed.Data.Data.AccessToken
		refreshToken = parsed.Data.Data.RefreshToken
	}

	if accessToken == "" {
		return fmt.Errorf("refresh returned empty accessToken (msg: %s)", parsed.Msg)
	}

	expiresAt := ParseJWTExp(accessToken)
	acc.SetTokens(accessToken, refreshToken, expiresAt)
	return nil
}

// ParseJWTExp extracts exp claim from JWT token without cryptographic verification.
func ParseJWTExp(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}

	payloadPart := parts[1]
	// Add padding if missing
	switch len(payloadPart) % 4 {
	case 2:
		payloadPart += "=="
	case 3:
		payloadPart += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payloadPart)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}
		}
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}

	return time.Unix(claims.Exp, 0)
}
