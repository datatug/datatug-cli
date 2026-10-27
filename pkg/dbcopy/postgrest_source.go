package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgrest"
)

// PostgREST support is one existing PostgREST API root read through the
// dal-go/dalgo2postgrest adapter. PostgREST exposes PostgreSQL relations (tables
// and views) as HTTP resources, so a query collection is one relation of the
// schema the API exposes. The URI names the endpoint and its API path;
// credentials are never part of it. They come from the operator's environment at
// request time:
//
//	DATATUG_POSTGREST_BEARER    Authorization: Bearer <token>
//	DATATUG_POSTGREST_API_KEY   apikey: <key> (Supabase)
//
// Both may be set together (a Supabase project needs both); either may be absent,
// in which case the request is anonymous and the service's own policy decides. No
// credential is ever stored in a project, an error message, a log line or a shell
// history entry, and neither is the endpoint.
const (
	// PostgRESTBearerEnv holds a Bearer token placed in the Authorization header
	// of every request.
	PostgRESTBearerEnv = "DATATUG_POSTGREST_BEARER"

	// PostgRESTAPIKeyEnv holds a Supabase API key placed in the apikey header of
	// every request. It is sent alongside the Bearer token when both are set.
	PostgRESTAPIKeyEnv = "DATATUG_POSTGREST_API_KEY"
)

// The PostgREST URI forms. "postgrest://" means HTTPS: plain HTTP must be asked
// for by name, and only for a loopback endpoint, because a development server is
// the only place it is safe to use. http:// and https:// keep their existing
// meaning in this package (a local DataTug project directory).
const (
	PostgRESTScheme      = "postgrest"
	PostgRESTHTTPSScheme = "postgrest+https"
	PostgRESTHTTPScheme  = "postgrest+http"
)

// postgrestAPISegment is one segment of an API path: a plain path segment, never
// "." or ".." and never percent-encoded, so the endpoint the adapter is given is
// exactly the one that was validated.
var postgrestAPISegment = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,64}$`)

// postgrestIdentifier is the rule the adapter applies to Schema and IDColumn: a
// simple SQL identifier of at most 63 bytes. Validating it here keeps a value out
// of a request and an error that the adapter would refuse only later.
var postgrestIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// postgrestOptionName is an option name that may be quoted in a refusal. Any
// other text in the query is refused without being echoed.
var postgrestOptionName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)

// IsPostgRESTScheme reports whether scheme is one of the PostgREST URI forms.
func IsPostgRESTScheme(scheme string) bool {
	switch scheme {
	case PostgRESTScheme, PostgRESTHTTPSScheme, PostgRESTHTTPScheme:
		return true
	}
	return false
}

// PostgRESTSourceURL is the source URL of a parsed PostgREST ref: scheme, host,
// API path and the non-secret schema/id options only. It never holds credentials,
// a fragment or any other option because parsePostgRESTSource refuses all three.
func (r BackendRef) PostgRESTSourceURL() string {
	if !IsPostgRESTScheme(r.Scheme) || r.PostgRESTHost == "" {
		return ""
	}
	source := r.Scheme + "://" + r.PostgRESTHost + r.PostgRESTAPIPath
	options := url.Values{}
	if r.PostgRESTIDColumn != "" {
		options.Set("id", r.PostgRESTIDColumn)
	}
	if r.PostgRESTSchema != "" {
		options.Set("schema", r.PostgRESTSchema)
	}
	if len(options) > 0 {
		source += "?" + options.Encode()
	}
	return source
}

// parsePostgRESTSource parses postgrest://HOST[/API-PATH],
// postgrest+https://HOST[/API-PATH] or postgrest+http://LOOPBACK[/API-PATH], with
// the optional non-secret options schema and id. rest is the text after
// "scheme://". Every refusal quotes only the display form of the source, so a
// password or token typed into the URL never reaches an error.
func parsePostgRESTSource(scheme, rest, display string) (BackendRef, error) {
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return BackendRef{}, fmt.Errorf("invalid PostgREST URL %q: expected %s://HOST/API-PATH", display, scheme)
	}
	if u.User != nil {
		return BackendRef{}, fmt.Errorf("invalid PostgREST URL %q: credentials are not supported in the URL; set %s and/or %s in the environment", display, PostgRESTBearerEnv, PostgRESTAPIKeyEnv)
	}
	if u.Fragment != "" {
		return BackendRef{}, fmt.Errorf("invalid PostgREST URL %q: the URL takes no fragment (credentials belong in the environment)", display)
	}
	hostname := u.Hostname()
	if hostname == "" || strings.ContainsAny(u.Host, " \t\r\n") {
		return BackendRef{}, fmt.Errorf("invalid PostgREST URL %q: it names no usable host", display)
	}
	if scheme == PostgRESTHTTPScheme {
		if !postgrestLoopbackHost(hostname) {
			return BackendRef{}, fmt.Errorf("invalid PostgREST URL %q: plain HTTP (%s://) is allowed only for a loopback endpoint (localhost, 127.0.0.1 or ::1); use %s:// or %s://", display, PostgRESTHTTPScheme, PostgRESTScheme, PostgRESTHTTPSScheme)
		}
	}
	path, err := postgrestAPIPath(u, display)
	if err != nil {
		return BackendRef{}, err
	}
	schema, idColumn, err := postgrestOptions(u, display)
	if err != nil {
		return BackendRef{}, err
	}
	return BackendRef{
		Scheme:                     scheme,
		Path:                       u.Host + path,
		PostgRESTHost:              u.Host,
		PostgRESTAPIPath:           path,
		PostgRESTSchema:            schema,
		PostgRESTIDColumn:          idColumn,
		PostgRESTAllowLoopbackHTTP: scheme == PostgRESTHTTPScheme,
		Raw:                        display,
	}, nil
}

// postgrestAPIPath validates and normalizes the API path of the endpoint: the
// root ("", "/") stays empty, every other path is one or more plain segments with
// no trailing slash, no "." or ".." segment and no percent-encoding. The path is
// passed to the adapter exactly as written, so it cannot be a value that net/url
// and the server read differently.
func postgrestAPIPath(u *url.URL, display string) (string, error) {
	path := u.Path
	if path == "/" {
		path = ""
	}
	if path == "" {
		return "", nil
	}
	if strings.HasSuffix(path, "/") {
		return "", fmt.Errorf("invalid PostgREST URL %q: the API path takes no trailing slash", display)
	}
	if u.EscapedPath() != path {
		return "", fmt.Errorf("invalid PostgREST URL %q: the API path must not be percent-encoded", display)
	}
	if strings.ContainsRune(path, '\\') {
		return "", fmt.Errorf("invalid PostgREST URL %q: the API path takes no backslash", display)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if !postgrestAPISegment.MatchString(segment) || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid PostgREST URL %q: the API path must be plain segments such as /rest/v1", display)
		}
	}
	return path, nil
}

// postgrestOptions reads the two non-secret options a PostgREST URL may carry:
// schema (the Accept-Profile the adapter sends) and id (the record ID column).
// Every other option is refused, as is an option given twice, so a source never
// carries an option that is silently ignored.
func postgrestOptions(u *url.URL, display string) (schema, idColumn string, err error) {
	if u.ForceQuery || u.RawQuery == "" {
		if u.ForceQuery {
			return "", "", fmt.Errorf("invalid PostgREST URL %q: the URL takes no empty query; remove the \"?\" or give schema= or id=", display)
		}
		return "", "", nil
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", "", fmt.Errorf("invalid PostgREST URL %q: the URL options cannot be read", display)
	}
	for name, list := range values {
		if !postgrestOptionName.MatchString(name) {
			return "", "", fmt.Errorf("invalid PostgREST URL %q: unknown option", display)
		}
		if len(list) != 1 {
			return "", "", fmt.Errorf("invalid PostgREST URL %q: option %q is given more than once", display, name)
		}
		switch name {
		case "schema":
			schema = list[0]
			if !postgrestIdentifier.MatchString(schema) {
				return "", "", fmt.Errorf("invalid PostgREST URL %q: schema must be a simple SQL identifier of at most 63 bytes", display)
			}
		case "id":
			idColumn = list[0]
			if !postgrestIdentifier.MatchString(idColumn) {
				return "", "", fmt.Errorf("invalid PostgREST URL %q: id must be a simple SQL identifier of at most 63 bytes", display)
			}
		default:
			return "", "", fmt.Errorf("invalid PostgREST URL %q: unknown option %q (the URL takes only schema and id)", display, name)
		}
	}
	return schema, idColumn, nil
}

// postgrestLoopbackHost reports whether host names this machine: "localhost", an
// IPv4 loopback address or an IPv6 loopback address.
func postgrestLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// postgRESTEndpoint is the HTTPS (or deliberately loopback HTTP) API root the
// ref names. It holds no credentials and no query.
func (r BackendRef) postgRESTEndpoint() string {
	scheme := "https"
	if r.PostgRESTAllowLoopbackHTTP {
		scheme = "http"
	}
	return scheme + "://" + r.PostgRESTHost + r.PostgRESTAPIPath
}

// postgRESTHeaders is the one place credentials become request headers. It reads
// the environment for every request, so no credential is captured at open time or
// copied into a project. A variable set to an empty string is treated as unset.
// The two headers are independent: a Supabase endpoint uses both.
func postgRESTHeaders(lookupEnv func(string) (string, bool)) (http.Header, error) {
	var headers http.Header
	if value, ok := lookupEnv(PostgRESTBearerEnv); ok && value != "" {
		if !safeHeaderValue(value) {
			return nil, postgRESTUnsafeCredential(PostgRESTBearerEnv)
		}
		headers = http.Header{"Authorization": {"Bearer " + value}}
	}
	if value, ok := lookupEnv(PostgRESTAPIKeyEnv); ok && value != "" {
		if !safeHeaderValue(value) {
			return nil, postgRESTUnsafeCredential(PostgRESTAPIKeyEnv)
		}
		if headers == nil {
			headers = http.Header{}
		}
		headers.Set("apikey", value)
	}
	return headers, nil
}

// safeHeaderValue reports whether value can be placed in an HTTP header: no CR,
// LF or NUL. The adapter applies the same rule to every request; checking it here
// makes an unsafe environment a failure before the first request and gives the
// operator a sentence that names the variable and never its value.
func safeHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n\x00")
}

// postgRESTUnsafeCredential is the error for a credential that cannot be sent as
// a header. It names the variable and no part of its value.
func postgRESTUnsafeCredential(name string) error {
	return &postgRESTCredentialError{name: name}
}

// postgRESTCredentialError is the typed form of postgRESTUnsafeCredential: the
// variable whose value cannot be sent, and no part of the value.
type postgRESTCredentialError struct{ name string }

func (e *postgRESTCredentialError) Error() string {
	return "PostgREST credentials: " + e.name + " contains a character that cannot be sent in an HTTP header"
}

// checkPostgRESTCredentials reports the configuration a request would refuse,
// before the first request is made. It reads only the variables and never returns
// or quotes a value.
func checkPostgRESTCredentials(lookupEnv func(string) (string, bool)) error {
	_, err := postgRESTHeaders(lookupEnv)
	return err
}

// newPostgRESTDatabase is the adapter's constructor, a seam so tests reach Open,
// OpenProtected and OpenSchemaScan without a server. Always
// dalgo2postgrest.NewDB in production.
var newPostgRESTDatabase = dalgo2postgrest.NewDB

// openPostgREST opens the API root of the ref through the PostgREST adapter. The
// adapter is read-only: a copy destination is refused before any request. The
// protected mode needs no provider-side option of its own — the adapter exposes no
// write path, every read requires a positive bounded limit, and a missing
// credential is an anonymous request whose policy the service itself decides — so
// an outer access-policy wrapper remains the one place a row or a column can be
// withheld.
func openPostgREST(ctx context.Context, r BackendRef, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, &refusedError{"PostgREST sources are read-only; copying into PostgREST is unavailable"}
	}
	if !IsPostgRESTScheme(r.Scheme) || r.PostgRESTHost == "" {
		return nil, errUnsupportedBackend
	}
	if err := checkPostgRESTCredentials(os.LookupEnv); err != nil {
		return nil, err
	}
	return newPostgRESTDatabase(ctx, dalgo2postgrest.Config{
		Endpoint:              r.postgRESTEndpoint(),
		IDColumn:              r.PostgRESTIDColumn,
		Schema:                r.PostgRESTSchema,
		Headers:               func(context.Context) (http.Header, error) { return postgRESTHeaders(os.LookupEnv) },
		AllowInsecureLoopback: r.PostgRESTAllowLoopbackHTTP,
	})
}

// postgRESTFailureReason is what OpenFailure says about a PostgREST failure: a
// fixed sentence, never the adapter's response body (which it does not expose
// anyway), never the endpoint and never a credential. The adapter's own sentinel
// errors carry no endpoint and no header value either; they are still mapped to
// sentences of this package so a command shows the same shape for every cause.
func postgRESTFailureReason(err error) string {
	if IsPostgRESTUnsafeCredential(err) {
		return err.Error()
	}
	var httpError *dalgo2postgrest.HTTPError
	switch {
	case errors.As(err, &httpError):
		switch httpError.StatusCode {
		case 401, 403:
			return "the PostgREST server refused the request (HTTP " + fmt.Sprint(httpError.StatusCode) + "); check " + PostgRESTBearerEnv + "/" + PostgRESTAPIKeyEnv + " and the reader's grants in the exposed schema"
		case 404:
			return "the PostgREST resource was not found (HTTP 404); check the API path and the relation"
		default:
			return "the PostgREST server answered with HTTP " + fmt.Sprint(httpError.StatusCode)
		}
	case errors.Is(err, dalgo2postgrest.ErrUnauthorized):
		return "the PostgREST server refused the request; check " + PostgRESTBearerEnv + "/" + PostgRESTAPIKeyEnv + " and the reader's grants in the exposed schema"
	case errors.Is(err, dalgo2postgrest.ErrMetadataUnauthorized):
		return "the PostgREST server refused the OpenAPI metadata document to this role; grant it the same read access as the relations"
	case errors.Is(err, dalgo2postgrest.ErrMetadataUnavailable):
		return "the PostgREST server did not expose a usable OpenAPI document at its API root"
	case errors.Is(err, dalgo2postgrest.ErrRelationNotFound):
		return "the relation was not found in the PostgREST schema metadata the role can see"
	case errors.Is(err, dalgo2postgrest.ErrInvalidConfig):
		return "the PostgREST source configuration is invalid; check the API path, schema and id options"
	case errors.Is(err, dalgo2postgrest.ErrUnsafeHeader):
		return "a PostgREST credential or configured header value is not safe to send"
	case errors.Is(err, dalgo2postgrest.ErrRedirectRefused):
		return "the PostgREST server answered with a redirect, which the adapter never follows"
	case errors.Is(err, dalgo2postgrest.ErrResponseTooLarge), errors.Is(err, dalgo2postgrest.ErrBudgetExceeded):
		return "the request or its result exceeds the PostgREST adapter's configured bound"
	case errors.Is(err, dalgo2postgrest.ErrInvalidResponse):
		return "the PostgREST server returned a response the adapter does not recognise"
	case errors.Is(err, dalgo2postgrest.ErrRequestFailed):
		return "the PostgREST request failed; its details are not shown because a transport error can quote the endpoint"
	case errors.Is(err, dalgo2postgrest.ErrClosed):
		return "the PostgREST source is closed"
	default:
		return openFailureReason(err)
	}
}

// IsPostgRESTUnsafeCredential reports whether err is the check of a credential
// that cannot be sent in an HTTP header (see postgRESTUnsafeCredential). A
// command can then say what to fix, and the sentence it shows names the
// environment variable and no part of its value.
func IsPostgRESTUnsafeCredential(err error) bool {
	var unsafe *postgRESTCredentialError
	return errors.As(err, &unsafe)
}
