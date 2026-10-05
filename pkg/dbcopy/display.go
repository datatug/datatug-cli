package dbcopy

import (
	"net/url"
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

// SourceIDNotShown is what SourceIDDisplay returns for a source ID that is not
// a plain name. It does not say the ID is wrong: a valid ID may hold a space or a
// slash, and a client may send a source string where an ID belongs; either way
// the text is not echoed.
const SourceIDNotShown = "<source id not shown>"

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
	plainSourceID = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._-]{0,127}$`)
)

// localSchemes are the schemes whose text after "://" or ":" is a path on this
// machine, not a host: a file, a project directory, an internal stand-in for a
// source that could not be resolved ("unavailable"), and every scheme dburl reads
// as a file (sqlite3, file, duckdb, moderncsqlite and their aliases), which the
// `datatug db <url>` viewer command takes.
var localSchemes = sync.OnceValue(func() map[string]bool {
	local := map[string]bool{
		"sqlite": true, "ingitdb": true, "openvaultdb": true, "http": true, "https": true, unavailableScheme: true,
	}
	for _, scheme := range dburl.BaseSchemes() {
		if !scheme.Opaque {
			continue
		}
		local[strings.ToLower(scheme.Driver)] = true
		for _, alias := range scheme.Aliases {
			local[strings.ToLower(alias)] = true
		}
	}
	return local
})

// knownSourceSchemes is every scheme SourceDisplay can show: the ones Parse
// dispatches, the postgres alias, the internal stand-in, and every scheme the
// `datatug db <url>` viewer command takes through dburl.
var knownSourceSchemes = sync.OnceValue(func() map[string]bool {
	known := map[string]bool{"postgresql": true, unavailableScheme: true}
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

// sourceTransports are the transports dburl reads after a "+" in a scheme
// ("mysql+unix"), and the only ones SourceDisplay shows: anything else after a "+"
// is text a user chose, and may be a token.
var sourceTransports = map[string]bool{"tcp": true, "udp": true, "unix": true}

// knownSourceScheme reports whether scheme (lower case) is one SourceDisplay can
// show: a scheme of knownSourceSchemes, or one dburl takes that its own list of
// base schemes leaves out: the short aliases it registers itself ("my" for mysql)
// and a scheme followed by a transport ("mysql+unix"). A "+transport" is accepted
// only after a scheme dburl knows, and only when it is tcp, udp or unix.
func knownSourceScheme(scheme string) bool {
	if knownSourceSchemes()[scheme] {
		return true
	}
	base, transport, hasTransport := strings.Cut(scheme, "+")
	if hasTransport && !sourceTransports[transport] {
		return false
	}
	return dburl.Protocols(base) != nil
}

// isLocalScheme reports whether the text after scheme (lower case) is a path on
// this machine and not a host: a scheme of localSchemes, or a short alias dburl
// registers itself ("sq" for sqlite3) for a scheme that is. dburl lists the
// scheme an alias resolves to first, and a scheme with a transport ("sq+unix")
// resolves to nothing, so it is a host.
func isLocalScheme(scheme string) bool {
	if localSchemes()[scheme] {
		return true
	}
	protocols := dburl.Protocols(scheme)
	return len(protocols) > 0 && localSchemes()[strings.ToLower(protocols[0])]
}

// SourceDisplay returns the text to show for the source string raw. It is the
// only way the CLI names a source in a message.
//
// The result is the scheme (lower-cased, and on the known list), then, for a
// database URL, the host, the port and a one-segment path, or, for a file or a
// directory, its path with the query string and the fragment cut off. A
// "host:port/path" that follows a user name and a password is shown without
// them: whatever precedes the last "@" is userinfo, because a password may hold
// an "@", a "/", a "?" or only digits and a parser may read it as a host and a
// port. The same holds for a file or a directory unless the text is certainly a
// path (see readsAsPath): an explicit path ("/abs/a@b", "./a@b", "../a@b",
// "~/a@b", a UNC path, a drive path) is shown as typed, unless it holds a second
// "scheme://" in front of its last "@" or is a UNC start with a ":" in front of it
// (that is a URL or credentials, whatever the text starts with); any other text
// that holds an "@" is userinfo and a host, even when a slash comes before the
// colon or the "@". Two shapes stay paths by design: an explicit path, and "X:/..."
// or "X:\..." (read as a Windows drive path even when it was meant as user X with a
// password that starts with a slash). An "env:NAME" source is shown as it is,
// because it names a variable and holds no value. A string that is not led by a
// known scheme yields UnparsableSource.
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
	if !knownSourceScheme(scheme) {
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
	case isLocalScheme(scheme) && schemePrefix.MatchString(rest):
		// A path scheme that wraps a URL ("ingitdb://https://u:p@host/x"). Parse
		// refuses it, and nothing may stand in front of the host: even a user
		// name alone is a token, so every "@" ends userinfo.
		prefix := schemePrefix.FindString(rest)
		inner := strings.ToLower(strings.TrimSuffix(prefix, "://"))
		if !knownSourceSchemes()[inner] {
			return "", false
		}
		return inner + "://" + hostPortPath(rest[len(prefix):], urlPathSegments), true
	case scheme == unavailableScheme:
		return unavailableDisplay(rest), true
	case isLocalScheme(scheme):
		return localTextDisplay(rest), true
	default:
		return hostPortPath(rest, 1), true
	}
}

// unavailableScheme is the scheme of the internal stand-in for a source that
// could not be resolved: "unavailable://" and the ID the source was asked for,
// escaped as one path segment. Nothing opens it.
const unavailableScheme = "unavailable"

// unavailableDisplay returns what SourceDisplay shows after "unavailable://":
// the text itself when it is one escaped path segment whose ID is a plain name
// (see SourceIDDisplay), and nothing otherwise. The ID is what a user typed
// where a catalog ID belongs, and may be a whole source string with its password:
// escaping its "?" and "#" keeps them out of the query and fragment cut, so the
// text cannot be shown as a path the way a file's is.
func unavailableDisplay(rest string) string {
	if id, err := url.PathUnescape(rest); err == nil && plainSourceID.MatchString(id) {
		return rest
	}
	return ""
}

// explicitPathStart is how a text starts when it is certainly a path: absolute
// ("/"), relative with a dot ("./" and "../"), in the home directory ("~/") or a
// Windows UNC path. A Windows drive path ("C:\dir", "C:/dir") is the other
// certain form; see driveLetterPath.
var explicitPathStart = regexp.MustCompile(`^(?:/|\./|\.\./|~/|\\\\)`)

// secondURL finds a "scheme://" anywhere in a text.
var secondURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://`)

// holdsSecondURL reports whether the part of text in front of its last "@" holds
// a "scheme://": a URL written after the start of what was meant as a path. Whoever
// wrote one meant a remote server with credentials, whatever the text starts with.
// The drive letter of a drive path is a drive and not a scheme, so the search starts
// after "C:": "C://work/a@b" is a path, and "C:/https://tok@host" holds a URL.
func holdsSecondURL(text string) bool {
	at := strings.LastIndexByte(text, '@')
	if at < 0 {
		return false
	}
	from := 0
	if driveLetterPath.MatchString(text) {
		from = len("C:")
	}
	return secondURL.MatchString(text[from:at])
}

// PathHoldsURL reports whether path, the path field of a catalog, is a URL
// ("https://tok@host/x") or holds one in front of its last "@" ("./https://tok@host"):
// it is then not a file or a directory, and what follows its scheme may be
// credentials, so a caller refuses it before it is joined to a folder, which would
// turn it into a relative path that a message shows whole. A drive path
// ("C://work/a@b.db") is a path: its drive letter is not a scheme.
func PathHoldsURL(path string) bool {
	if driveLetterPath.MatchString(path) {
		path = path[len("C:"):]
	}
	return schemePrefix.MatchString(path) || holdsSecondURL(path)
}

// readsAsPath reports whether text, what follows "scheme://" of a scheme that
// names a file or a directory (or a bare path), is a path and not userinfo
// followed by a host. It decides by what is certain, not by what looks like
// credentials: text without an "@" is a path, and text with one is a path only
// when it starts like one (see explicitPathStart and driveLetterPath) and holds no
// second URL in front of its last "@" (see holdsSecondURL). Otherwise everything
// up to the last "@" is userinfo, because a user name, a password or a token may
// hold a ":" or a "/" in any place, so no shape of the text in front of the "@"
// tells userinfo from a directory.
//
// Two shapes stay paths by design. An explicit path ("/abs/a@b", "./a@b") is
// shown as it was typed. "X:/..." and "X:\..." read as a Windows drive path, even
// when it was meant as user X with a password that starts with a slash. A
// relative directory that holds an "@" and starts with neither is written with
// a dot: ./dir (see LocalSourceURL).
//
// A second URL in front of the last "@" ("/https://tok@host", "./https://u:p@host",
// "C:/https://tok@host") is not a path whatever the text starts with: an explicit
// start does not make a URL written after it a directory. A UNC start
// ("\\server\share") is explicit only while no ":" stands in front of the last
// "@" and the server name holds no "@": "\\alice:s3cret@host", "\\corp/u:p@host"
// and "\\tok@host" start with two backslashes and are credentials all the same, and
// no server or share name holds a colon, nor a server name an "@".
//
// Known limits, shapes that stay paths because nothing in the text tells them from
// one: a token with a slash and no colon after a UNC start ("\\team/tok@host/x":
// "team" is the server and "tok@host" a share), and user information straight
// after an absolute start with no second scheme ("sqlite:///user:pw@host/db": an
// absolute path may hold an "@" anywhere). Both are shown as typed.
func readsAsPath(text string) bool {
	at := strings.LastIndexByte(text, '@')
	if at < 0 {
		return true
	}
	if holdsSecondURL(text) {
		return false
	}
	if driveLetterPath.MatchString(text) {
		return true
	}
	return explicitPathStart.MatchString(text) && (!strings.HasPrefix(text, `\\`) || uncReadsAsPath(text, at))
}

// uncReadsAsPath reports whether text, which starts with "\\" and holds an "@"
// at index at (the last), is a UNC path and not credentials: no ":" stands in
// front of the "@", and the server name, the first segment, holds no "@" ("\\tok@host/x"
// is a token as the user name: no server name holds one).
func uncReadsAsPath(text string, at int) bool {
	if strings.Contains(text[:at], ":") {
		return false
	}
	server := text[2:]
	if end := strings.IndexAny(server, `\/`); end >= 0 {
		server = server[:end]
	}
	return !strings.Contains(server, "@")
}

// localTextDisplay returns what to show for rest, the text after "scheme://" of
// a path scheme, or a bare path: the path as it is, minus its query string and
// fragment. When rest does not read as a path (see readsAsPath) the text before
// the last "@" is userinfo, and only what follows it is shown, read as a host, a
// port and a path.
func localTextDisplay(rest string) string {
	if readsAsPath(rest) {
		return localPathDisplay(rest)
	}
	return hostPortPath(rest, urlPathSegments)
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

// SourceIDDisplay returns id when it is a plain name (letters and digits of any
// script, "." "_" and "-", at most 128 characters, starting with a letter or a
// digit) and SourceIDNotShown otherwise. Use it to name a source ID a client sent in a
// message: a client may send a whole source string where an ID belongs.
func SourceIDDisplay(id string) string {
	if IsPlainSourceID(id) {
		return id
	}
	return SourceIDNotShown
}

// QueryIDDisplay returns id, a saved query's ID, when every "/"-separated part of it
// is a plain name (see IsPlainSourceID) and SourceIDNotShown otherwise: a query ID is
// the folders of the query and its name, and a client may send a whole source string
// where one belongs. Use it to name a query ID a client sent in a message.
func QueryIDDisplay(id string) string {
	for _, part := range strings.Split(id, "/") {
		if !IsPlainSourceID(part) {
			return SourceIDNotShown
		}
	}
	return id
}

// IsPlainSourceID reports whether id is a plain name for a source in a project:
// letters and digits of any script, "." "_" and "-", at most 128 characters,
// starting with a letter or a digit. It is the one definition of a plain name:
// SourceIDDisplay shows an ID only when it holds, and an ID that becomes a folder
// or file name must satisfy it. Compare with it, not with SourceIDDisplay(id) ==
// id, because SourceIDNotShown is itself text a client can send.
func IsPlainSourceID(id string) bool {
	return plainSourceID.MatchString(id)
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
