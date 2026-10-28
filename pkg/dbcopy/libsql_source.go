package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2libsql"
)

// libSQL support is one existing libSQL database read through the
// dal-go/dalgo2libsql adapter. libSQL speaks the SQLite dialect, so a query
// collection is one table or view of the database's single "main" schema. The
// URI names the server base address; credentials are never part of it. They come
// from the operator's environment at request time:
//
//	DATATUG_LIBSQL_BEARER   Authorization: Bearer <token>
//
// The variable is read for every request, so a rotating token needs no restart,
// and a value that cannot be sent as an HTTP header fails before any request. No
// credential is ever stored in a project, an error message, a log line or a
// shell history entry, and neither is the endpoint.
const (
	// LibSQLBearerEnv holds the Bearer token placed in the Authorization header
	// of every request. When it is unset the request is anonymous and the
	// server's own policy decides.
	LibSQLBearerEnv = "DATATUG_LIBSQL_BEARER"
)

// The libSQL URI forms. "libsql://" means HTTPS: plain HTTP must be asked for by
// name, and only for a loopback endpoint, because a local fixture is the only
// place it is safe to use. Ordinary https:// keeps its existing meaning in this
// package (a local DataTug project directory).
const (
	LibSQLScheme      = "libsql"
	LibSQLHTTPSScheme = "libsql+https"
	LibSQLHTTPScheme  = "libsql+http"
)

// libSQLBaseSegment is one segment of the server's optional base path: a plain
// path segment, never "." or ".." and never percent-encoded, so the endpoint the
// adapter is given is exactly the one that was validated.
var libSQLBaseSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,255}$`)

// libSQLAPISegments are the segments the adapter refuses because they name an
// API endpoint rather than a server base: a request to /v2, /v3 or /pipeline
// would be sent to a path the Hrana protocol reads differently from a base path.
var libSQLAPISegments = map[string]struct{}{
	"v2":       {},
	"v3":       {},
	"pipeline": {},
	"health":   {},
	"version":  {},
}

// IsLibSQLScheme reports whether scheme is one of the libSQL URI forms.
func IsLibSQLScheme(scheme string) bool {
	switch scheme {
	case LibSQLScheme, LibSQLHTTPSScheme, LibSQLHTTPScheme:
		return true
	}
	return false
}

// LibSQLSourceURL is the source URL of a parsed libSQL ref: scheme, host, port
// and base path only. It never holds credentials, a query or a fragment because
// parseLibSQLSource refuses all three.
func (r BackendRef) LibSQLSourceURL() string {
	if !IsLibSQLScheme(r.Scheme) || r.LibSQLHost == "" {
		return ""
	}
	return r.Scheme + "://" + r.LibSQLHost + r.LibSQLBasePath
}

// parseLibSQLSource parses libsql://HOST[/BASE-PATH], libsql+https://HOST[/BASE-PATH]
// or libsql+http://LOOPBACK[/BASE-PATH]. rest is the text after "scheme://".
// Every refusal quotes only the display form of the source, so a token typed into
// the URL never reaches an error.
func parseLibSQLSource(scheme, rest, display string) (BackendRef, error) {
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return BackendRef{}, fmt.Errorf("invalid libSQL URL %q: expected %s://HOST[/BASE-PATH]", display, scheme)
	}
	if u.User != nil {
		return BackendRef{}, fmt.Errorf("invalid libSQL URL %q: credentials are not supported in the URL; set %s in the environment", display, LibSQLBearerEnv)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return BackendRef{}, fmt.Errorf("invalid libSQL URL %q: the URL takes no query or fragment (credentials belong in %s)", display, LibSQLBearerEnv)
	}
	hostname := u.Hostname()
	if hostname == "" || strings.ContainsAny(u.Host, " \t\r\n") {
		return BackendRef{}, fmt.Errorf("invalid libSQL URL %q: it names no usable host", display)
	}
	if scheme == LibSQLHTTPScheme && !libSQLLoopbackHost(hostname) {
		return BackendRef{}, fmt.Errorf("invalid libSQL URL %q: plain HTTP (%s://) is allowed only for a loopback endpoint (localhost, 127.0.0.1 or ::1); use %s:// or %s://", display, LibSQLHTTPScheme, LibSQLScheme, LibSQLHTTPSScheme)
	}
	path, err := libSQLBasePath(u, display)
	if err != nil {
		return BackendRef{}, err
	}
	return BackendRef{
		Scheme:                  scheme,
		Path:                    u.Host + path,
		LibSQLHost:              u.Host,
		LibSQLBasePath:          path,
		LibSQLAllowLoopbackHTTP: scheme == LibSQLHTTPScheme,
		Raw:                     display,
	}, nil
}

// libSQLBasePath validates and normalizes the optional base path of the server:
// the root ("", "/") stays empty, every other path is one or more plain segments
// with no trailing slash, no "." or ".." segment, no percent-encoding and no Hrana
// API segment. The path is passed to the adapter exactly as written, so it cannot
// be a value that net/url and the server read differently.
func libSQLBasePath(u *url.URL, display string) (string, error) {
	path := u.Path
	if path == "/" {
		path = ""
	}
	if path == "" {
		return "", nil
	}
	if strings.HasSuffix(path, "/") {
		return "", fmt.Errorf("invalid libSQL URL %q: the base path takes no trailing slash", display)
	}
	if strings.ContainsRune(path, '\\') {
		return "", fmt.Errorf("invalid libSQL URL %q: the base path takes no backslash", display)
	}
	if u.EscapedPath() != path {
		return "", fmt.Errorf("invalid libSQL URL %q: the base path must not be percent-encoded", display)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if !libSQLBaseSegment.MatchString(segment) || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid libSQL URL %q: the base path must be plain segments such as /team/db", display)
		}
		if _, refused := libSQLAPISegments[strings.ToLower(segment)]; refused {
			return "", fmt.Errorf("invalid libSQL URL %q: the URL must name the server base, not an API endpoint such as /v2 or /v3/pipeline", display)
		}
	}
	return path, nil
}

// libSQLLoopbackHost reports whether host names this machine: "localhost", an
// IPv4 loopback address or an IPv6 loopback address.
func libSQLLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// libSQLEndpoint is the HTTPS (or deliberately loopback HTTP) base address the
// ref names. It holds no credentials and no query.
func (r BackendRef) libSQLEndpoint() string {
	scheme := "https"
	if r.LibSQLAllowLoopbackHTTP {
		scheme = "http"
	}
	return scheme + "://" + r.LibSQLHost + r.LibSQLBasePath
}

// libSQLHeaders is the one place a credential becomes a request header. It reads
// the environment for every request, so no credential is captured at open time or
// copied into a project, and a value that cannot be sent as a header is refused
// without being echoed.
func libSQLHeaders(lookupEnv func(string) (string, bool)) (map[string]string, error) {
	value, ok := lookupEnv(LibSQLBearerEnv)
	if !ok || value == "" {
		return nil, nil
	}
	if !safeHeaderValue(value) {
		return nil, libSQLUnsafeCredential(LibSQLBearerEnv)
	}
	return map[string]string{"Authorization": "Bearer " + value}, nil
}

// libSQLUnsafeCredential is the error for a credential that cannot be sent as a
// header. It names the variable and no part of its value.
func libSQLUnsafeCredential(name string) error {
	return &libSQLCredentialError{name: name}
}

// libSQLCredentialError is the typed form of libSQLUnsafeCredential: the variable
// whose value cannot be sent, and no part of the value.
type libSQLCredentialError struct{ name string }

func (e *libSQLCredentialError) Error() string {
	return "libSQL credentials: " + e.name + " contains a character that cannot be sent in an HTTP header"
}

// CheckLibSQLCredentials reports the configuration a request would refuse, before
// the first request is made. It reads only the variable and never returns or
// quotes a value.
func CheckLibSQLCredentials(lookupEnv func(string) (string, bool)) error {
	_, err := libSQLHeaders(lookupEnv)
	return err
}

// newLibSQLDatabase is the adapter's constructor, a seam so tests reach Open,
// OpenProtected and OpenSchemaScan without a server. Always dalgo2libsql.NewDB in
// production.
var newLibSQLDatabase = dalgo2libsql.NewDB

// openLibSQL opens the base address of the ref through the libSQL adapter. The
// adapter is read-only and sealed: it exposes no write, raw-SQL or handle
// accessor, so a copy destination is refused before any request. The protected
// mode needs no provider-side option of its own — a missing token is an anonymous
// request whose policy the server itself decides — so an outer access-policy
// wrapper remains the one place a row or a column can be withheld.
//
// The adapter's own bounds stay in force (one request, one statement result, one
// operation, the response and value sizes and the operation deadline); this
// package sets no SQL limit here and no local aggregate fallback.
func openLibSQL(ctx context.Context, r BackendRef, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, &refusedError{"libSQL sources are read-only; copying into libSQL is unavailable"}
	}
	if !IsLibSQLScheme(r.Scheme) || r.LibSQLHost == "" {
		return nil, errUnsupportedBackend
	}
	if err := CheckLibSQLCredentials(os.LookupEnv); err != nil {
		return nil, err
	}
	return newLibSQLDatabase(ctx, dalgo2libsql.Config{
		URL:               r.libSQLEndpoint(),
		Headers:           func(context.Context) (map[string]string, error) { return libSQLHeaders(os.LookupEnv) },
		AllowLoopbackHTTP: r.LibSQLAllowLoopbackHTTP,
	})
}

// IsLibSQLUnsafeCredential reports whether err is the check of a credential that
// cannot be sent in an HTTP header (see libSQLUnsafeCredential). A command can
// then say what to fix, and the sentence it shows names the environment variable
// and no part of its value.
func IsLibSQLUnsafeCredential(err error) bool {
	var unsafe *libSQLCredentialError
	return errors.As(err, &unsafe)
}

// libSQLFailureReason is what OpenFailure says about a libSQL failure: a fixed
// sentence, never the adapter's response body (which it does not expose anyway),
// never the endpoint and never a credential.
func libSQLFailureReason(err error) string {
	switch {
	case IsLibSQLUnsafeCredential(err):
		return err.Error()
	case errors.Is(err, dalgo2libsql.ErrUnsafeTarget):
		return "the libSQL source address is not a safe base URL: use libsql:// or https://, plain http:// only for a loopback host, and no credentials, query, fragment or API suffix"
	case errors.Is(err, dalgo2libsql.ErrInvalidConfig):
		return "the libSQL source configuration is invalid; check the base address and the adapter's bounds"
	case errors.Is(err, dalgo2libsql.ErrNotSupported):
		return "the libSQL adapter does not support this operation: it reads structured queries only, with no native SQL, joins, cursors or transactions"
	case errors.Is(err, dalgo2libsql.ErrUnsupportedResponse):
		return "the libSQL server returned a response the adapter does not recognise"
	case errors.Is(err, dalgo2libsql.ErrBackend):
		return "the libSQL server refused the read or could not be reached; check " + LibSQLBearerEnv + " and the token's read grant on the database (the adapter does not quote the endpoint, the SQL or the credential)"
	}
	return openFailureReason(err)
}
