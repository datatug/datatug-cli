package dbcopy

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/xo/dburl"
)

// This file is how the CLI names a source in anything it shows or stores: an
// error, a log line, a message, an HTTP response body, a stored chat source.
//
// A source string is what the user typed, and the user may have typed a
// password in it, in a form no pattern can recognise for sure. So nothing
// shown is ever a scrubbed copy of what was typed. What is shown is built from
// parts that were read out of the string and each passed a strict check; a part
// that fails its check is left out, and a string that does not read as a source
// at all is replaced by UnparsableSource. Userinfo, query string and fragment
// are never parts.

// UnparsableSource is what SourceDisplay returns for a string that does not
// read as a source. Nothing from the input is in it.
const UnparsableSource = "<unparsable source>"

// InvalidSourceID is what SourceIDDisplay returns for a source ID that is not
// a plain name.
const InvalidSourceID = "<invalid source id>"

// maxLocalPath bounds the path SourceDisplay shows for a file or a directory.
const maxLocalPath = 4096

var (
	// schemeHead is the "scheme://" or "scheme:" a source starts with. A scheme
	// longer than 32 characters is not one.
	schemeHead = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]{0,31})(://|:)`)

	// hostName is a DNS name or an IPv4 address.
	hostName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$`)

	// hostIPv6 is a bracketed IPv6 address.
	hostIPv6 = regexp.MustCompile(`^\[[0-9A-Fa-f:.]{2,45}\]$`)

	// portDigits is the digits of a port number.
	portDigits = regexp.MustCompile(`^[0-9]{1,5}$`)

	// urlPathSegment is one segment of the path of a URL as shown: a database
	// name, or a directory name on a host.
	urlPathSegment = regexp.MustCompile(`^[A-Za-z0-9._~%+-]{0,128}$`)

	// plainSourceID is a plain name for a source in a project: a database model
	// or a catalog ID.
	plainSourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// localSchemes are the schemes whose text after "://" is a path on this
// machine, not a host: a file, a project directory, or an internal stand-in
// for a source that could not be resolved ("unavailable").
var localSchemes = map[string]bool{
	"sqlite": true, "ingitdb": true, "openvaultdb": true, "http": true, "https": true, "unavailable": true,
}

// knownSourceSchemes is every scheme SourceDisplay can show: the ones Parse
// dispatches, the postgres alias, the internal stand-in, and every scheme the
// `datatug db <url>` viewer command takes through dburl.
var knownSourceSchemes = sync.OnceValue(func() map[string]bool {
	known := map[string]bool{"postgresql": true, "unavailable": true}
	for _, scheme := range supportedSchemes {
		known[scheme] = true
	}
	for _, scheme := range dburl.BaseSchemes() {
		known[strings.ToLower(scheme.Driver)] = true
		for _, alias := range scheme.Aliases {
			known[strings.ToLower(alias)] = true
		}
	}
	return known
})

// SourceDisplay returns the text to show for the source string raw. It is the
// only way the CLI names a source in a message.
//
// The result is the scheme (lower-cased, and on the known list), then, for a
// database URL, the host, the port and a one-segment path, or, for a file or a
// directory, its path with the query string and the fragment cut off. A
// "host:port/path" that follows a user name and a password is shown without
// them: whatever precedes the last "@" is userinfo, because a password may hold
// an "@", a "/", a "?" or only digits and a parser may read it as a host and a
// port. An "env:NAME" source is shown as it is, because it names a variable and
// holds no value. A string that is not led by a known scheme yields
// UnparsableSource.
func SourceDisplay(raw string) string {
	if name, isEnv := strings.CutPrefix(raw, envPrefix); isEnv {
		if ValidEnvName(name) {
			return raw
		}
		return UnparsableSource
	}
	head := schemeHead.FindStringSubmatch(raw)
	if head == nil {
		return UnparsableSource
	}
	scheme := strings.ToLower(head[1])
	if !knownSourceSchemes()[scheme] {
		return UnparsableSource
	}
	shown, ok := displayAfterScheme(scheme, raw[len(head[0]):])
	if !ok {
		return UnparsableSource
	}
	return scheme + head[2] + shown
}

// displayAfterScheme returns what SourceDisplay shows after the scheme, and
// false when what follows cannot be shown at all.
func displayAfterScheme(scheme, rest string) (string, bool) {
	switch {
	case localSchemes[scheme] && schemePrefix.MatchString(rest):
		// A path scheme that wraps a URL ("ingitdb://https://u:p@host/x"). Parse
		// refuses it, and nothing may stand in front of the host: even a user
		// name alone is a token, so every "@" ends userinfo.
		prefix := schemePrefix.FindString(rest)
		inner := strings.ToLower(strings.TrimSuffix(prefix, "://"))
		if !knownSourceSchemes()[inner] {
			return "", false
		}
		return inner + "://" + hostPortPath(rest[len(prefix):], urlPathSegments), true
	case localSchemes[scheme]:
		return localTextDisplay(rest), true
	default:
		return hostPortPath(rest, 1), true
	}
}

// localTextDisplay returns what to show for rest, the text after "scheme://" of
// a path scheme, or a bare path: the path as it is, minus its query string and
// fragment. When rest starts like credentials ("user:password@host", with the
// user name possibly empty, or a first segment that holds an "@") the text
// before the last "@" is not a path but userinfo, and only what follows it is
// shown, read as a host, a port and a path.
func localTextDisplay(rest string) string {
	if looksLikeUserinfo(rest) || holdsAtInFirstSegment(rest) {
		return hostPortPath(rest, urlPathSegments)
	}
	return localPathDisplay(rest)
}

// urlPathSegments is how many path segments the text after a host may have
// when it follows a userinfo that Parse refused or sits in a wrapped URL: a
// repository path has several, a database name has one.
const urlPathSegments = 16

// hostPortPath returns the host, the port and the path of rest, the text after
// "scheme://", each only when it passes its check and with no userinfo, query
// or fragment. maxSegments bounds the segments of the path.
func hostPortPath(rest string, maxSegments int) string {
	tail := rest
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		if cut := strings.IndexAny(rest, "?#"); cut >= 0 && cut < at {
			// The "@" may close a userinfo that holds a "?" or a "#", or sit in
			// the query of a URL with none: the two cannot be told apart.
			return ""
		}
		tail = rest[at+1:]
	}
	if cut := strings.IndexAny(tail, "?#"); cut >= 0 {
		tail = tail[:cut]
	}
	authority, path := tail, ""
	if slash := strings.IndexByte(tail, '/'); slash >= 0 {
		authority, path = tail[:slash], tail[slash:]
	}
	var shown strings.Builder
	if host, port := splitHostPort(authority); validHost(host) {
		shown.WriteString(host)
		if validPort(port) {
			shown.WriteString(":" + port)
		}
	}
	if validURLPath(path, maxSegments) {
		shown.WriteString(path)
	}
	return shown.String()
}

// splitHostPort splits an authority into host and port. Text that is not a
// host followed by an optional ":port" gives an empty host.
func splitHostPort(authority string) (host, port string) {
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", ""
		}
		host, remainder := authority[:end+1], authority[end+1:]
		if remainder == "" {
			return host, ""
		}
		if remainder[0] != ':' {
			return "", ""
		}
		return host, remainder[1:]
	}
	if colon := strings.LastIndexByte(authority, ':'); colon >= 0 {
		return authority[:colon], authority[colon+1:]
	}
	return authority, ""
}

func validHost(host string) bool {
	return hostName.MatchString(host) || hostIPv6.MatchString(host)
}

func validPort(port string) bool {
	if !portDigits.MatchString(port) {
		return false
	}
	number, _ := strconv.Atoi(port) // at most five digits: it cannot fail
	return number <= 65535
}

// validURLPath reports whether path is empty or "/" followed by at most
// maxSegments segments of plain characters.
func validURLPath(path string, maxSegments int) bool {
	if path == "" {
		return true
	}
	segments := strings.Split(path[1:], "/")
	if len(segments) > maxSegments {
		return false
	}
	for _, segment := range segments {
		if !urlPathSegment.MatchString(segment) {
			return false
		}
	}
	return true
}

// localPathDisplay returns the path of a file or a directory as it is, with
// the query string and the fragment cut off. A path with a control character,
// bytes that are not UTF-8 or more than maxLocalPath bytes is left out.
func localPathDisplay(path string) string {
	if cut := strings.IndexAny(path, "?#"); cut >= 0 {
		path = path[:cut]
	}
	if len(path) > maxLocalPath || !utf8.ValidString(path) {
		return ""
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return path
}

// SourceIDDisplay returns id when it is a plain name (letters, digits, "." "_"
// and "-", at most 128 characters, starting with a letter or a digit) and
// InvalidSourceID otherwise. Use it to name a source ID a client sent in a
// message: a client may send a whole source string where an ID belongs.
func SourceIDDisplay(id string) string {
	if plainSourceID.MatchString(id) {
		return id
	}
	return InvalidSourceID
}

// Display returns the text to show for this source in a message. It is built
// by SourceDisplay and never holds userinfo, a query string or a fragment.
func (r BackendRef) Display() string {
	if r.Raw != "" {
		return SourceDisplay(r.Raw)
	}
	if schemePrefix.MatchString(r.Path) {
		return SourceDisplay(r.Path)
	}
	return SourceDisplay(r.Scheme + "://" + r.Path)
}
