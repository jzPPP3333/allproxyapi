package workbuddy

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/pool"
)

type RealmConfig struct {
	ChatUpstream string
	Origin       string
	Domain       string
	UserAgent    string
	Version      string
}

var Realms = map[string]RealmConfig{
	"cn": {
		ChatUpstream: "https://copilot.tencent.com",
		Origin:       "https://www.codebuddy.cn",
		Domain:       "copilot.tencent.com",
		UserAgent:    "WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1",
		Version:      "5.5.6",
	},
	"intl": {
		ChatUpstream: "https://www.workbuddy.ai",
		Origin:       "https://www.workbuddy.ai",
		Domain:       "www.workbuddy.ai",
		UserAgent:    "WorkBuddy/5.5.2 WorkBuddy AI/5.5.2 CLI/5.5.2",
		Version:      "5.5.2",
	},
}

// DeriveID derives a deterministic 36-char hex machine/session identifier from uid + salt.
// This prevents upstream WAF anti-fraud triggers caused by rotating random machine IDs.
func DeriveID(uid, salt string) string {
	if uid == "" {
		uid = "anonymous"
	}
	seed := fmt.Sprintf("%s:%s", salt, uid)
	h := md5.Sum([]byte(seed))
	encoded := hex.EncodeToString(h[:])
	if len(encoded) > 36 {
		return encoded[:36]
	}
	return encoded
}

func GenerateRequestID(uid string) string {
	prefix := DeriveID(uid, "req")
	suffix := fmt.Sprintf("%06d", (time.Now().UnixNano()%1000000+1000000)%1000000)
	return fmt.Sprintf("%s-%s", prefix, suffix)
}

func GetRealmConfig(realm string) RealmConfig {
	if realm == "cn" {
		return Realms["cn"]
	}
	return Realms["intl"]
}

func BuildIdentityHeaders(acc *pool.Account) map[string]string {
	realm := "cn"
	if acc.Realm != "" {
		realm = acc.Realm
	}
	cfg := GetRealmConfig(realm)

	uid := acc.UID
	if uid == "" {
		uid = "anonymous"
	}

	domain := acc.Domain
	if domain == "" {
		domain = cfg.Domain
	}

	headers := map[string]string{
		"Authorization":       "Bearer " + acc.AccessToken,
		"X-User-Id":           uid,
		"X-IDE-Name":          "WorkBuddy",
		"X-IDE-Type":          "WorkBuddy",
		"X-IDE-Version":       cfg.Version,
		"X-Product":           "WorkBuddy",
		"X-Domain":            domain,
		"Origin":              cfg.Origin,
		"Referer":             cfg.Origin + "/",
		"User-Agent":          cfg.UserAgent,
		"X-CodeBuddy-Request": "1",
		"X-Agent-Purpose":     "conversation",
		"X-Machine-ID":        DeriveID(uid, "machine"),
		"X-Session-ID":        DeriveID(uid, "session"),
		"X-Request-ID":        GenerateRequestID(uid),
		"Content-Type":        "application/json",
		"Accept":              "text/event-stream, application/json, */*",
	}

	if acc.EnterpriseID != "" {
		headers["X-Enterprise-Id"] = acc.EnterpriseID
	}

	return headers
}
