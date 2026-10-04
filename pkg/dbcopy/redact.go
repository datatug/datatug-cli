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

	// quotedURLInText finds a URL inside a pair of double or single quotes, the
	// way a %q-formatted error quotes it. A quoted URL may hold spaces and
	// backslash-escaped quotes, which urlInText would stop at.
	quotedURLInText = regexp.MustCompile(`"[A-Za-z][A-Za-z0-9+.-]*://(?:[^"\\]|\\.)*"|'[A-Za-z][A-Za-z0-9+.-]*://(?:[^'\\]|\\.)*'`)

	// keywordSecret finds the libpq "password=..." form of a connection string
	// and its relatives: PGPASSWORD=..., db_password=..., sslpassword=....
	// A quoted value may hold spaces and backslash-escaped quotes.
	keywordSecret = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:password|passwd|pwd))(\s*=\s*)('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|[^\s&;,]+)`)

	// jsonSecret finds a JSON member whose name says it is a password.
	jsonSecret = regexp.MustCompile(`(?i)("[A-Za-z0-9_.-]*(?:password|passwd|pwd)"\s*:\s*)("(?:[^"\\]|\\.)*"|[^\s,}\]]+)`)

	// colonSecret finds "Password:value", a value written straight after the
	// colon. "password: required" (a space after the colon) is prose, not a
	// secret, and is left alone.
	colonSecret = regexp.MustCompile(`(?i)\b(password|passwd|pwd)(:)([^\s:/&;,"'][^\s&;,"']*)`)
)

// hostOnlyAuthority is an authority that is just "host" or "host:port": a name
// or IPv4 address, or a bracketed IPv6 address, and an optional numeric port.
var hostOnlyAuthority = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]*\]|[^@:\[\]]*)(:[0-9]+)?$`)

// isHostOnly reports whether the authority of rest, the text after "scheme://",
// is a plain "host" or "host:port", so that there is no userinfo and any "@"
// further on belongs to the path, the query or the fragment.
//
// Only http and https get the exemption: their URLs name a local project
// directory or a web endpoint, never hold a password, and are full of "@" in
// paths ("/users/@me") and queries ("?email=a@b.com"). For every other scheme
// the userinfo ends at the last "@" and nothing is exempt, because a PostgreSQL
// password may hold an unescaped "/" (postgres://alice:42/abc@host/db reads as
// host "alice", port 42, but is a password "42/abc") and hiding it is the point.
func isHostOnly(scheme, rest string) bool {
	if scheme != "http" && scheme != "https" {
		return false
	}
	authority := rest
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		authority = rest[:end]
	}
	return hostOnlyAuthority.MatchString(authority)
}

// userinfoShape is "user:password@..." at the start of a string: a user name,
// a colon, and anything up to an "@". A password may hold "/", so the match
// does not stop at one.
var userinfoShape = regexp.MustCompile(`^[^/@:]+:[^@]*@`)

// driveLetterPath is a Windows path such as C:\dir or C:/dir.
var driveLetterPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// looksLikeUserinfo reports whether rest, the text after "scheme://" of a
// scheme that names a file or directory, is shaped like "user:password@host"
// instead of like a path. A path that starts "./" or "/" never is; a Windows
// drive path never is.
func looksLikeUserinfo(rest string) bool {
	return !driveLetterPath.MatchString(rest) && userinfoShape.MatchString(rest)
}

// pathSchemes name a file or directory after "://": what follows is a path, not
// userinfo, unless it is shaped like "user:password@host" (looksLikeUserinfo),
// which Parse refuses and the redactor still hides.
var pathSchemes = map[string]bool{"sqlite": true, "ingitdb": true, "openvaultdb": true}

// sourceSecrets returns the literal secrets raw holds, in every spelling a
// driver might echo: the password of a "user:password@" prefix and the value of
// every secret-named query parameter, each raw, percent-decoded and
// percent-encoded again. A caller that holds the real URL uses it to scrub an
// error the driver wrote, whatever shape the driver gave the URL in.
func sourceSecrets(raw string) []string {
	var secrets []string
	add := func(value string) { secrets = append(secrets, secretSpellings(value)...) }
	rest := raw
	if location := schemePrefix.FindStringIndex(raw); location != nil {
		rest = raw[location[1]:]
		// Only a URL names a password in front of an "@": in a bare path (a
		// sqlite file, a project directory) ":" and "@" are just characters.
		if scheme := strings.ToLower(strings.TrimSuffix(raw[:location[1]], "://")); !pathSchemes[scheme] {
			if at := strings.LastIndexByte(rest, '@'); at >= 0 && !isHostOnly(scheme, rest) {
				if _, password, found := strings.Cut(rest[:at], ":"); found {
					add(password)
				}
			}
		}
	}
	if _, query, found := strings.Cut(rest, "?"); found {
		query, _, _ = strings.Cut(query, "#")
		for _, pair := range strings.Split(query, "&") {
			if key, value, hasValue := strings.Cut(pair, "="); hasValue && isSensitiveQueryKey(key) {
				add(value)
			}
		}
	}
	for _, match := range keywordSecret.FindAllStringSubmatch(raw, -1) {
		add(strings.Trim(match[3], `'"`))
	}
	return secrets
}

// secretSpellings returns value as written, percent-decoded and percent-encoded
// again: the spellings a driver may use when it echoes a secret. An empty value
// has none.
func secretSpellings(value string) []string {
	if value == "" {
		return nil
	}
	spellings := []string{value}
	if decoded, err := url.PathUnescape(value); err == nil && decoded != value {
		spellings = append(spellings, decoded)
		value = decoded
	}
	for _, encoded := range []string{url.QueryEscape(value), url.PathEscape(value)} {
		if encoded != value {
			spellings = append(spellings, encoded)
		}
	}
	return spellings
}

// RedactTextWithSecrets is RedactText for text that may hold secrets the
// caller knows as plain values (a password it was given as a flag): besides
// everything RedactText removes it replaces every literal in secrets, in the
// spellings secretSpellings lists.
func RedactTextWithSecrets(text string, secrets ...string) string {
	var spellings []string
	for _, secret := range secrets {
		spellings = append(spellings, secretSpellings(secret)...)
	}
	return RedactText(scrubSecrets(text, spellings))
}

// RedactErrorWithSecrets is RedactError for an error written by code that was
// handed source, the real URL: besides the URL-shaped text RedactError finds,
// it removes every literal secret source holds, so a driver that formats the
// connection string in its own way still cannot put the password in a message.
func RedactErrorWithSecrets(err error, source string) error {
	return redactErrorWith(err, sourceSecrets(source))
}

// RedactErrorWithLiterals is RedactError for an error written by code that was
// handed secrets as plain values (a password given as a flag): it also removes
// every literal in secrets, in the spellings secretSpellings lists.
func RedactErrorWithLiterals(err error, secrets ...string) error {
	var spellings []string
	for _, secret := range secrets {
		spellings = append(spellings, secretSpellings(secret)...)
	}
	return redactErrorWith(err, spellings)
}

func redactErrorWith(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	if RedactText(scrubSecrets(err.Error(), secrets)) == err.Error() {
		return err
	}
	return redactedError{err: err, secrets: secrets}
}

// scrubSecrets replaces every literal secret in text with the marker.
func scrubSecrets(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, redactedMarker)
	}
	return text
}

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
// still removed whole. The one exemption is an http or https URL whose
// authority is a plain host or host:port (see isHostOnly), whose "@" belongs to
// the path.
func RedactSourceURL(raw string) string {
	location := schemePrefix.FindStringIndex(raw)
	if location == nil {
		return redactKeywordSecrets(raw)
	}
	prefix := raw[:location[1]]
	rest := raw[location[1]:]
	scheme := strings.ToLower(strings.TrimSuffix(prefix, "://"))
	switch {
	case !pathSchemes[scheme]:
		rest = redactUserinfo(scheme, rest)
	case schemePrefix.MatchString(rest):
		// A path scheme wrapping a URL ("ingitdb://https://u:p@host/x"): the
		// wrapped URL carries the userinfo. Nothing may stand in front of the
		// host there, so a user name alone ("https://TOKEN@host/x") is a token
		// and is masked whole.
		rest = redactWholeUserinfo(RedactSourceURL(rest))
	case looksLikeUserinfo(rest):
		rest = redactUserinfo(scheme, rest)
	}
	rest = redactQuery(rest)
	return redactKeywordSecrets(prefix + rest)
}

// RedactText returns text with every URL inside it passed through
// RedactSourceURL and every "password=..." keyword value removed. Use it on
// any message, log line or stored string that could carry a source URL.
func RedactText(text string) string {
	text = quotedURLInText.ReplaceAllStringFunc(text, func(quoted string) string {
		quote := quoted[:1]
		return quote + RedactSourceURL(quoted[1:len(quoted)-1]) + quote
	})
	text = urlInText.ReplaceAllStringFunc(text, RedactSourceURL)
	return redactKeywordSecrets(text)
}

// RedactError returns err with a message passed through RedactText. It wraps
// err, so errors.Is and errors.As still see the original; only Error() text is
// redacted. A nil err stays nil.
func RedactError(err error) error {
	return redactErrorWith(err, nil)
}

type redactedError struct {
	err     error
	secrets []string
}

func (e redactedError) Error() string { return RedactText(scrubSecrets(e.err.Error(), e.secrets)) }
func (e redactedError) Unwrap() error { return e.err }

func redactKeywordSecrets(text string) string {
	text = keywordSecret.ReplaceAllString(text, "${1}${2}"+redactedMarker)
	text = jsonSecret.ReplaceAllString(text, `${1}"`+redactedMarker+`"`)
	return colonSecret.ReplaceAllString(text, "${1}${2}"+redactedMarker)
}

// redactUserinfo replaces the password in a leading "user:password@" with the
// marker. A user name alone ("user@host") holds no secret and is kept.
func redactUserinfo(scheme, rest string) string {
	at := strings.LastIndexByte(rest, '@')
	if at < 0 {
		return rest
	}
	// "host:8080/users/@me" is a path that holds an "@", not userinfo (see
	// isHostOnly for the schemes that get this reading).
	if isHostOnly(scheme, rest) {
		return rest
	}
	userinfo := rest[:at]
	colon := strings.IndexByte(userinfo, ':')
	if colon < 0 {
		return rest
	}
	return userinfo[:colon+1] + redactedMarker + rest[at:]
}

// redactWholeUserinfo masks the whole userinfo of rest, a wrapped URL such as
// "https://TOKEN@host/x" (what follows "ingitdb://"). redactUserinfo has
// already hidden a password; this hides a user name that is itself the secret.
// The wrapped scheme decides whether an "@" is userinfo or path.
func redactWholeUserinfo(rest string) string {
	prefix := schemePrefix.FindString(rest)
	inner := rest[len(prefix):]
	scheme := strings.ToLower(strings.TrimSuffix(prefix, "://"))
	at := strings.LastIndexByte(inner, '@')
	if at < 0 || isHostOnly(scheme, inner) || strings.IndexByte(inner[:at], ':') >= 0 {
		return rest
	}
	return prefix + redactedMarker + inner[at:]
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
