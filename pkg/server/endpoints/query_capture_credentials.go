package endpoints

import (
	"net/url"
	"regexp"
	"strings"
)

// Credential screening for a captured query's free-text fields.
//
// A captured query lands in git-tracked project files, so none of its text
// may carry a secret. datatug-core screens a QueryDefTarget's connection
// fields and parameter defaults (pkg/datatug/query_credentials.go on the
// Phase 2 task 2 storage branch), but not a query's own text, title or
// purpose - the fields a capture actually submits. This screen applies the
// same three syntaxes to those fields:
//
//   - URL userinfo with a password, "postgres://user:secret@host/db",
//     including a URL inside a longer value; decided by net/url's
//     User.Password(), so a percent-encoded or explicitly empty password
//     counts and "postgres://user@host/db" is allowed.
//   - DSN userinfo without a "//" authority, "user:secret@tcp(host)/db".
//   - A key/value pair whose key ends in password, passwd or pwd with a
//     non-empty value ("Password=secret;", "?password=secret").
//
// It differs from core's screen in one way: a quote also delimits a token,
// because query text quotes its literals (value: "Password=secret"). Once
// datatug-core exports its screen, capture should call it instead of this
// copy.
var (
	captureDSNUserinfoPattern      = regexp.MustCompile(`(?:^|[\s;,(="'])([^\s:@/;,()="']+):([^\s@;,"']+)@`)
	captureKeyValuePasswordPattern = regexp.MustCompile(`(?i)(?:^|[;&?,\s"'])[\w.-]*(?:password|passwd|pwd)\s*=\s*[^;&\s"']`)
)

// captureCredentialReason reports why value appears to embed a password,
// or ("", false) when it does not.
func captureCredentialReason(value string) (reason string, found bool) {
	if reason, found := captureURLCredentialReason(value); found {
		return reason, true
	}
	for _, m := range captureDSNUserinfoPattern.FindAllStringSubmatch(value, -1) {
		if !strings.Contains(m[2], "//") {
			return "must not embed a password in a connection string (user:password@); a username alone is allowed", true
		}
	}
	if captureKeyValuePasswordPattern.MatchString(value) {
		return "must not embed a password key (password=, pwd=) in a connection string", true
	}
	return "", false
}

// captureURLCredentialReason checks every "scheme://" URL inside value.
func captureURLCredentialReason(value string) (reason string, found bool) {
	const refused = "must not embed a password in a URL (user:password@); a username alone is allowed"
	for offset := 0; ; {
		i := strings.Index(value[offset:], "://")
		if i < 0 {
			return "", false
		}
		sep := offset + i
		offset = sep + len("://")

		start := sep
		for start > 0 && isURLSchemeByte(value[start-1]) {
			start--
		}
		end := len(value)
		if j := strings.IndexAny(value[offset:], " \t\r\n\"'"); j >= 0 {
			end = offset + j
		}
		if u, err := url.Parse(value[start:end]); err == nil && u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword {
				return refused, true
			}
			continue
		}
		// net/url refused the candidate, or found no userinfo: read the
		// authority by hand, so a value that does not parse still cannot
		// smuggle a password past this check.
		authority := value[offset:end]
		if j := strings.IndexAny(authority, "/?#;"); j >= 0 {
			authority = authority[:j]
		}
		if at := strings.LastIndexByte(authority, '@'); at >= 0 && strings.ContainsRune(authority[:at], ':') {
			return refused, true
		}
	}
}

func isURLSchemeByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'
}
