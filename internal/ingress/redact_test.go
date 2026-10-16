package ingress

import (
	"strings"
	"testing"
)

// TestRedactRemovesCredentials is the guarantee that matters: a credential must
// not survive into the request log or a printed line.
func TestRedactRemovesCredentials(t *testing.T) {
	cases := []struct {
		name  string
		input string
		// secret must not appear in the output
		secret string
	}{
		{"bearer header", `Authorization: Bearer sk-abcdef1234567890`, "sk-abcdef1234567890"},
		{"personal token", `token=pt-9f8e7d6c5b4a3210`, "pt-9f8e7d6c5b4a3210"},
		{"job token", `using jt-abcdef1234567890 for auth`, "jt-abcdef1234567890"},
		{"device refresh token", `drt-abcdef1234567890`, "drt-abcdef1234567890"},
		{"jwt", `Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSM`, "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSM"},
		{"google access token", `ya29.a0AfH6SMBxxxxxxxxxxxx`, "ya29.a0AfH6SMBxxxxxxxxxxxx"},
		{"google refresh token", `1//0gLbC6xxxxxxxxxxxxxxxxxxxxxxxx`, "1//0gLbC6xxxxxxxxxxxxxxxxxxxxxxxx"},
		{"query access_token", `/v1/x?access_token=supersecretvalue&y=1`, "supersecretvalue"},
		{"query api_key", `/v1/x?api_key=abcdef123456`, "abcdef123456"},
		{"query secret", `/v1/x?secret=hunter2value`, "hunter2value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactString(tc.input)
			if strings.Contains(out, tc.secret) {
				t.Fatalf("secret survived redaction:\n in: %s\nout: %s", tc.input, out)
			}
			if !strings.Contains(out, Redacted) {
				t.Fatalf("nothing was marked as redacted: %s", out)
			}
		})
	}
}

// TestRedactKeepsTextReadable checks redaction does not destroy the line, so a
// log entry is still useful for diagnosis.
func TestRedactKeepsTextReadable(t *testing.T) {
	out := RedactString(`POST /v1/chat/completions?access_token=abc123def456 failed`)
	if !strings.Contains(out, "/v1/chat/completions") {
		t.Errorf("the path was lost: %s", out)
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("the message was lost: %s", out)
	}
	if !strings.Contains(out, "access_token=") {
		t.Errorf("the parameter name was lost: %s", out)
	}

	out = RedactString(`Authorization: Bearer sk-abcdef1234567890`)
	if !strings.HasPrefix(out, "Authorization: Bearer ") {
		t.Errorf("the scheme was lost: %s", out)
	}
}

// TestRedactLeavesCleanTextAlone checks ordinary text is untouched.
func TestRedactLeavesCleanTextAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"POST /v1/chat/completions",
		"model=gemini-2.5-pro provider=antigravity",
		"upstream returned status 429",
	} {
		if got := RedactString(s); got != s {
			t.Errorf("RedactString(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestRedactErrorAndPath(t *testing.T) {
	errMsg := `dial tcp: auth failed with Bearer sk-abcdef1234567890`
	if strings.Contains(RedactError(errMsg), "sk-abcdef1234567890") {
		t.Error("RedactError let a token through")
	}
	p := `/v1/models?api_key=abcdef123456`
	if strings.Contains(RedactPath(p), "abcdef123456") {
		t.Error("RedactPath let a key through")
	}
}

func TestIsSensitiveHeader(t *testing.T) {
	for _, h := range []string{"Authorization", "authorization", "X-API-Key", "COOKIE", "X-Machine-Token"} {
		if !isSensitiveHeader(h) {
			t.Errorf("%q should be treated as sensitive", h)
		}
	}
	for _, h := range []string{"Content-Type", "User-Agent", "Accept", ""} {
		if isSensitiveHeader(h) {
			t.Errorf("%q should not be treated as sensitive", h)
		}
	}
}
