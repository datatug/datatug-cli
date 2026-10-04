package dbcopy

import (
	"net/url"
	"regexp"
	"strings"
)

// redactedMarker replaces every secret RedactSourceURL and RedactText remove.
// It is the placeholder PostgreSQL's own tools print for a hidden password.
const redactedMarker = "xxxxx"

var (
	// schemePrefix matches the "scheme://" a URL starts with.
	schemePrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

	// urlInText finds a URL anywhere inside free text, up to the next space.
	urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)

	// keywordSecret finds the libpq "password=..." form of a connection string.
	// A quoted value may hold spaces and backslash-escaped quotes.
	keywordSecret = regexp.MustCompile(`(?i)\b(password|passwd|pwd|sslpassword)(\s*=\s*)('(?:[^'\\]|\\.)*'|[^\s&;]+)`)
)

// pathSchemes name a file or directory after "://", so what follows is never
// "user:password@host" and must not be read as userinfo.
var pathSchemes = map[string]bool{"sqlite": true, "ingitdb": true, "openvaultdb": true}

// sensitiveQueryKeys are the fragments that mark a query parameter as a secret.
var sensitiveQueryKeys = []string{"pass", "pwd", "secret", "token", "key", "auth", "credential"}

// RedactSourceURL returns raw with every secret replaced by "xxxxx": the
// password in "user:password@host", the value of any query parameter whose
// name says it is a secret (password, sslpassword, token, key ...), and the
// value of a libpq "password=..." keyword. A string that holds no secret comes
// back unchanged, byte for byte.
//
// It works on the text and never parses, so it cannot fail and a malformed URL
// is still redacted. It prefers to over-redact: the userinfo ends at the last
// "@" in the string, so a password that holds an unescaped "@", "/" or "?" is
// still removed whole.
func RedactSourceURL(raw string) string {
	location := schemePrefix.FindStringIndex(raw)
	if location == nil {
		return redactKeywordSecrets(raw)
	}
	prefix := raw[:location[1]]
	rest := raw[location[1]:]
	scheme := strings.ToLower(strings.TrimSuffix(prefix, "://"))
	if !pathSchemes[scheme] {
		rest = redactUserinfo(rest)
	}
	rest = redactQuery(rest)
	return redactKeywordSecrets(prefix + rest)
}

// RedactText returns text with every URL inside it passed through
// RedactSourceURL and every "password=..." keyword value removed. Use it on
// any message, log line or stored string that could carry a source URL.
func RedactText(text string) string {
	text = urlInText.ReplaceAllStringFunc(text, RedactSourceURL)
	return redactKeywordSecrets(text)
}

// RedactError returns err with a message passed through RedactText. It wraps
// err, so errors.Is and errors.As still see the original; only Error() text is
// redacted. A nil err stays nil.
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{err: err}
}

type redactedError struct{ err error }

func (e redactedError) Error() string { return RedactText(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }

func redactKeywordSecrets(text string) string {
	return keywordSecret.ReplaceAllString(text, "${1}${2}"+redactedMarker)
}

// redactUserinfo replaces the password in a leading "user:password@" with the
// marker. A user name alone ("user@host") holds no secret and is kept.
func redactUserinfo(rest string) string {
	at := strings.LastIndexByte(rest, '@')
	if at < 0 {
		return rest
	}
	userinfo := rest[:at]
	colon := strings.IndexByte(userinfo, ':')
	if colon < 0 {
		return rest
	}
	return userinfo[:colon+1] + redactedMarker + rest[at:]
}

// redactQuery replaces the value of every secret-named query parameter.
func redactQuery(rest string) string {
	question := strings.IndexByte(rest, '?')
	if question < 0 {
		return rest
	}
	head, query := rest[:question+1], rest[question+1:]
	fragment := ""
	if hash := strings.IndexByte(query, '#'); hash >= 0 {
		query, fragment = query[:hash], query[hash:]
	}
	pairs := strings.Split(query, "&")
	for i, pair := range pairs {
		key, _, hasValue := strings.Cut(pair, "=")
		if hasValue && isSensitiveQueryKey(key) {
			pairs[i] = key + "=" + redactedMarker
		}
	}
	return head + strings.Join(pairs, "&") + fragment
}

func isSensitiveQueryKey(key string) bool {
	decoded, err := url.QueryUnescape(key)
	if err != nil {
		decoded = key
	}
	decoded = strings.ToLower(decoded)
	for _, fragment := range sensitiveQueryKeys {
		if strings.Contains(decoded, fragment) {
			return true
		}
	}
	return false
}
