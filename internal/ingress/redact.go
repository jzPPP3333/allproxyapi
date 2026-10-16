package ingress

import (
	"regexp"
	"strings"
)

// The request log is meant to be read and pasted into a bug report, so it must
// not carry credentials. Logged fields are redacted on the way in rather than
// on display, so a secret never reaches the in-memory record at all.

// Redacted is what replaces a sensitive value.
const Redacted = "[REDACTED]"

// sensitiveHeaderNames are header names whose values are always masked.
var sensitiveHeaderNames = map[string]bool{
	"authorization":       true,
	"x-api-key":           true,
	"api-key":             true,
	"x-auth-token":        true,
	"x-refresh-token":     true,
	"x-machine-token":     true,
	"cookie":              true,
	"set-cookie":          true,
	"proxy-authorization": true,
}

// keyPatterns match token-shaped values that can appear in a URL query or an
// error message regardless of the field they came from.
var keyPatterns = []*regexp.Regexp{
	// Bearer tokens, and the token families the providers issue.
	regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\b(?:sk|pt|jt|dt|drt)-[A-Za-z0-9._~+/=-]{6,}`),
	// A JWT: three dot-separated base64url segments.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}`),
	// Google OAuth artifacts.
	regexp.MustCompile(`\bya29\.[A-Za-z0-9._-]{10,}`),
	regexp.MustCompile(`\b1//[A-Za-z0-9._-]{20,}`),
	// Query parameters that carry credentials.
	regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|key|secret|password)=)[^&\s"]+`),
}

// RedactString masks credentials that appear in free text such as a URL or an
// upstream error message.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, re := range keyPatterns {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			// Preserve a leading prefix (the "Bearer " or the query key) so the
			// line still reads sensibly.
			if idx := strings.IndexAny(match, " "); idx > 0 && strings.EqualFold(match[:idx], "bearer") {
				return match[:idx+1] + Redacted
			}
			if i := strings.Index(match, "="); i > 0 {
				return match[:i+1] + Redacted
			}
			return Redacted
		})
	}
	return out
}

// RedactPath returns a path with any credential-bearing query values masked.
func RedactPath(p string) string {
	return RedactString(p)
}

// RedactError returns an error message that is safe to store.
func RedactError(msg string) string {
	return RedactString(msg)
}

// isSensitiveHeader reports whether a header must never be logged verbatim.
func isSensitiveHeader(name string) bool {
	return sensitiveHeaderNames[strings.ToLower(strings.TrimSpace(name))]
}
