package pool

import (
	"errors"
	"testing"
	"time"
)

// desktopFile builds a WorkBuddy desktop state file, the shape the client
// writes to CodeBuddyExtension/Data/Public/auth/*.info. Values are plain
// strings unless replaced with an encrypted envelope.
func desktopFile(accessToken, refreshToken, uid, nickname, phone, domain string, expiresAt int64) string {
	return `{
  "account": {
    "uid": "` + uid + `",
    "nickname": "` + nickname + `",
    "uin": 330110933657,
    "type": "personal",
    "phoneNumber": "` + phone + `"
  },
  "accounts": [],
  "allAccounts": [],
  "auth": {
    "accessToken": "` + accessToken + `",
    "refreshToken": "` + refreshToken + `",
    "expiresIn": 2419200,
    "tokenType": "Bearer",
    "domain": "` + domain + `",
    "expiresAt": ` + itoa(expiresAt) + `
  }
}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestDesktopFormatLoads is the regression guard: this shape used to load as
// zero accounts because the tokens are nested under "auth".
func TestDesktopFormatLoads(t *testing.T) {
	expiry := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC).UnixMilli()
	raw := desktopFile("jwe-access-token", "jwe-refresh-token",
		"00000000-1111-2222-3333-444444444444", "13800000000", "13800000000",
		"www.workbuddy.cn", expiry)

	acc, err := ParseAccountData("/tmp/workbuddy-desktop-x.info", []byte(raw))
	if err != nil {
		t.Fatalf("ParseAccountData: %v", err)
	}
	if acc == nil {
		t.Fatal("no account parsed")
	}

	if acc.Type != TypeWorkBuddy {
		t.Errorf("type = %q, want workbuddy", acc.Type)
	}
	if acc.AccessToken != "jwe-access-token" {
		t.Errorf("access token = %q", acc.AccessToken)
	}
	if acc.RefreshToken != "jwe-refresh-token" {
		t.Errorf("refresh token = %q", acc.RefreshToken)
	}
	if acc.UID != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("uid = %q", acc.UID)
	}
	if acc.Name != "WorkBuddy (13800000000)" {
		t.Errorf("name = %q", acc.Name)
	}
	if acc.ExpiresAt.UnixMilli() != expiry {
		t.Errorf("expires = %v, want %v", acc.ExpiresAt, time.UnixMilli(expiry))
	}
}

// TestDesktopFormatRegionFromDomain checks the auth domain names the region.
func TestDesktopFormatRegionFromDomain(t *testing.T) {
	cases := map[string]string{
		"www.workbuddy.cn": "cn",
		"www.workbuddy.ai": "intl",
	}
	for domain, want := range cases {
		raw := desktopFile("a", "r", "uid-1", "nick", "", domain, 0)
		acc, err := ParseAccountData("/tmp/wb.info", []byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", domain, err)
		}
		if acc.Realm != want {
			t.Errorf("domain %s -> realm %q, want %q", domain, acc.Realm, want)
		}
	}
}

// TestDesktopFormatFallsBackToPhone checks the name falls back when nickname is
// absent, and that a phone-only account still gets a usable label.
func TestDesktopFormatFallsBackToPhone(t *testing.T) {
	raw := desktopFile("a", "r", "uid-1", "", "13900000000", "www.workbuddy.cn", 0)
	acc, err := ParseAccountData("/tmp/wb.info", []byte(raw))
	if err != nil {
		t.Fatalf("ParseAccountData: %v", err)
	}
	if acc.Name != "WorkBuddy (13900000000)" {
		t.Errorf("name = %q, want the phone fallback", acc.Name)
	}
}

// TestEncryptedDesktopFileIsReported is the guard for a bug found against real
// files: phoneNumber is an encrypted object in newer versions, and typing that
// field as a string made the whole file fail to unmarshal first, so the
// encrypted credential was reported as an ordinary skip with a confusing
// message instead of as encrypted.
func TestEncryptedDesktopFileIsReported(t *testing.T) {
	env := `{"$wbEncrypted":1,"envelope":"eyJzdWl0ZSI6MX0="}`
	raw := `{
  "account": {"uid":"6461aca0-1e34-4f9b-8888-4a4c9a286ef0","nickname":` + env + `,"phoneNumber":` + env + `},
  "auth": {"accessToken":` + env + `,"refreshToken":` + env + `,"domain":"www.workbuddy.ai","expiresAt":1823036210976}
}`

	_, err := ParseAccountData("/tmp/workbuddy-desktop-ai.info", []byte(raw))
	if err == nil {
		t.Fatal("an encrypted credential must not parse as a usable account")
	}
	if !errors.Is(err, ErrEncryptedCredential) {
		t.Fatalf("error = %v, want ErrEncryptedCredential so it is reported as encrypted", err)
	}
}

// TestEncryptedEnvelopeDetection covers the helper's edge cases.
func TestEncryptedEnvelopeDetection(t *testing.T) {
	cases := map[string]bool{
		`"a-plain-string"`:                  false,
		`  "with-leading-space"`:            false,
		`{"$wbEncrypted":1,"envelope":"x"}`: true,
		`{"anything":"else"}`:               true,
		``:                                  false,
	}
	for raw, want := range cases {
		if got := isEncryptedEnvelope([]byte(raw)); got != want {
			t.Errorf("isEncryptedEnvelope(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestAsStringRejectsObjects checks a sealed value never leaks as a token.
func TestAsStringRejectsObjects(t *testing.T) {
	if got := asString([]byte(`{"$wbEncrypted":1}`)); got != "" {
		t.Errorf("asString(object) = %q, want empty", got)
	}
	if got := asString([]byte(`"literal"`)); got != "literal" {
		t.Errorf("asString(string) = %q", got)
	}
	if got := asString(nil); got != "" {
		t.Errorf("asString(nil) = %q", got)
	}
}

// TestPlainTopLevelFormatStillLoads guards the older export shape against
// regression from the nested support.
func TestPlainTopLevelFormatStillLoads(t *testing.T) {
	raw := `{"type":"workbuddy","access_token":"tok","refresh_token":"ref","uid":"u1","realm":"cn"}`
	acc, err := ParseAccountData("/tmp/wb.json", []byte(raw))
	if err != nil {
		t.Fatalf("ParseAccountData: %v", err)
	}
	if acc.AccessToken != "tok" || acc.RefreshToken != "ref" {
		t.Errorf("top-level tokens lost: %+v", acc)
	}
	if acc.Realm != "cn" {
		t.Errorf("realm = %q", acc.Realm)
	}
}

// TestDesktopFileWithoutTokensIsNotEncrypted checks a desktop-shaped file that
// simply has no credential is not misreported as encrypted.
func TestDesktopFileWithoutTokensIsNotEncrypted(t *testing.T) {
	raw := `{"account":{"uid":"u1","nickname":"nick"},"auth":{"domain":"www.workbuddy.cn"}}`
	_, err := ParseAccountData("/tmp/wb.info", []byte(raw))
	if err == nil {
		t.Fatal("expected an error for a file with no tokens")
	}
	if errors.Is(err, ErrEncryptedCredential) {
		t.Fatal("a file with no tokens must not be reported as encrypted")
	}
}
