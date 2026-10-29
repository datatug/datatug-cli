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
	"github.com/dal-go/dalgo2typesense"
)

// Typesense support is one existing Typesense collection read through the
// dal-go/dalgo2typesense adapter. The URI names the server origin and exactly one
// collection; credentials are never part of it. The scoped API key comes from the
// operator's environment for every request:
//
//	DATATUG_TYPESENSE_API_KEY   X-TYPESENSE-API-KEY: <key>
//
// The variable is read for every request, so a rotated key needs no restart, and
// a missing or unsendable value fails before any request. No credential is ever
// stored in a project, an error message, a log line or a shell history entry, and
// neither is a request body.
const (
	// TypesenseAPIKeyEnv holds the scoped Typesense API key sent as
	// X-TYPESENSE-API-KEY. It is required: unlike an anonymous read, a Typesense
	// request without a key cannot be authorized by the server's own policy.
	TypesenseAPIKeyEnv = "DATATUG_TYPESENSE_API_KEY"

	// typesenseAPIKeyHeader is the one place a Typesense credential becomes a
	// request header.
	typesenseAPIKeyHeader = "X-TYPESENSE-API-KEY"
)

// The Typesense URI forms. "typesense://" means HTTPS: plain HTTP must be asked
// for by name, and only for a loopback endpoint, because a local fixture is the
// only place it is safe to use. Ordinary https:// keeps its existing meaning in
// this package (a local DataTug project directory).
const (
	TypesenseScheme      = "typesense"
	TypesenseHTTPSScheme = "typesense+https"
	TypesenseHTTPScheme  = "typesense+http"
)

// typesenseCollectionName is the collection grammar the adapter accepts: one
// inert native Typesense collection name, and nothing that could be read as a
// path, an API suffix or a second segment.
var typesenseCollectionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// IsTypesenseScheme reports whether scheme is one of the Typesense URI forms.
func IsTypesenseScheme(scheme string) bool {
	switch scheme {
	case TypesenseScheme, TypesenseHTTPSScheme, TypesenseHTTPScheme:
		return true
	}
	return false
}

// IsTypesenseCollectionName reports whether name is a collection name the
// adapter accepts. One source pins exactly one such name.
func IsTypesenseCollectionName(name string) bool {
	return typesenseCollectionName.MatchString(name)
}

// TypesenseSourceURL is the source URL of a parsed Typesense ref: scheme, host,
// port and the one collection only. It never holds credentials, a query or a
// fragment because parseTypesenseSource refuses all three.
func (r BackendRef) TypesenseSourceURL() string {
	if !IsTypesenseScheme(r.Scheme) || r.TypesenseHost == "" || r.TypesenseCollection == "" {
		return ""
	}
	return r.Scheme + "://" + r.TypesenseHost + "/" + r.TypesenseCollection
}

// parseTypesenseSource parses typesense://HOST/COLLECTION,
// typesense+https://HOST/COLLECTION or typesense+http://LOOPBACK/COLLECTION. rest
// is the text after "scheme://". Every refusal quotes only the display form of the
// source, so a key typed into the URL never reaches an error.
//
// The endpoint the adapter is given is the origin only: the collection is a path
// segment, and a base path, a second segment, an API suffix, a trailing slash and
// percent-encoding are all refused before anything is opened.
func parseTypesenseSource(scheme, rest, display string) (BackendRef, error) {
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return BackendRef{}, fmt.Errorf("invalid Typesense URL %q: expected %s://HOST/COLLECTION", display, scheme)
	}
	if u.User != nil {
		return BackendRef{}, fmt.Errorf("invalid Typesense URL %q: credentials are not supported in the URL; set %s in the environment", display, TypesenseAPIKeyEnv)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return BackendRef{}, fmt.Errorf("invalid Typesense URL %q: the URL takes no query or fragment (the API key belongs in %s)", display, TypesenseAPIKeyEnv)
	}
	hostname := u.Hostname()
	if hostname == "" || strings.ContainsAny(u.Host, " \t\r\n") {
		return BackendRef{}, fmt.Errorf("invalid Typesense URL %q: it names no usable host", display)
	}
	if scheme == TypesenseHTTPScheme && !typesenseLoopbackHost(hostname) {
		return BackendRef{}, fmt.Errorf("invalid Typesense URL %q: plain HTTP (%s://) is allowed only for a loopback endpoint (localhost, 127.0.0.1 or ::1); use %s:// or %s://", display, TypesenseHTTPScheme, TypesenseScheme, TypesenseHTTPSScheme)
	}
	collection, err := typesenseCollectionOf(u, display)
	if err != nil {
		return BackendRef{}, err
	}
	return BackendRef{
		Scheme:                     scheme,
		Path:                       u.Host + "/" + collection,
		TypesenseHost:              u.Host,
		TypesenseCollection:        collection,
		TypesenseAllowLoopbackHTTP: scheme == TypesenseHTTPScheme,
		Raw:                        display,
	}, nil
}

// typesenseCollectionOf validates the one path segment of the URI: the root and
// any other shape are refused, because the adapter's endpoint is an origin and
// its collection is a configured name, not a URL path.
func typesenseCollectionOf(u *url.URL, display string) (string, error) {
	path := u.Path
	if path == "" || path == "/" {
		return "", fmt.Errorf("invalid Typesense URL %q: it names no collection; expected %s://HOST/COLLECTION", display, strings.TrimSuffix(display, path))
	}
	if strings.HasSuffix(path, "/") {
		return "", fmt.Errorf("invalid Typesense URL %q: the collection takes no trailing slash", display)
	}
	if strings.ContainsRune(path, '\\') {
		return "", fmt.Errorf("invalid Typesense URL %q: the collection must be one plain path segment", display)
	}
	if u.EscapedPath() != path {
		return "", fmt.Errorf("invalid Typesense URL %q: the collection must not be percent-encoded", display)
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) != 1 {
		return "", fmt.Errorf("invalid Typesense URL %q: the URL must name the server origin and exactly one collection, not a base path or an API endpoint", display)
	}
	collection := segments[0]
	if collection == "." || collection == ".." || !typesenseCollectionName.MatchString(collection) {
		return "", fmt.Errorf("invalid Typesense URL %q: the collection must be a plain Typesense collection name such as books", display)
	}
	return collection, nil
}

// typesenseLoopbackHost reports whether host names this machine: "localhost", an
// IPv4 loopback address or an IPv6 loopback address.
func typesenseLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// typesenseEndpoint is the HTTPS (or deliberately loopback HTTP) origin the ref
// names. It holds no credentials, no path and no query.
func (r BackendRef) typesenseEndpoint() string {
	scheme := "https"
	if r.TypesenseAllowLoopbackHTTP {
		scheme = "http"
	}
	return scheme + "://" + r.TypesenseHost
}

// typesenseHeaders is the one place a Typesense credential becomes a request
// header. It reads the environment for every request, so no credential is
// captured at open time or copied into a project, and a value that cannot be sent
// as a header is refused without being echoed. A missing or empty key is refused
// before any request: Typesense does not authorize an anonymous read here.
func typesenseHeaders(lookupEnv func(string) (string, bool)) (map[string]string, error) {
	value, ok := lookupEnv(TypesenseAPIKeyEnv)
	if !ok || value == "" {
		return nil, &typesenseCredentialError{name: TypesenseAPIKeyEnv, reason: typesenseCredentialMissing}
	}
	if !safeHeaderValue(value) {
		return nil, &typesenseCredentialError{name: TypesenseAPIKeyEnv, reason: typesenseCredentialUnsafe}
	}
	return map[string]string{typesenseAPIKeyHeader: value}, nil
}

const (
	typesenseCredentialMissing = "is not set: it must hold a Typesense API key with a scoped read grant"
	typesenseCredentialUnsafe  = "contains a character that cannot be sent in an HTTP header"
)

// typesenseCredentialError is the typed failure of a Typesense credential: the
// variable whose value is missing or cannot be sent, and no part of the value.
type typesenseCredentialError struct {
	name   string
	reason string
}

func (e *typesenseCredentialError) Error() string {
	return "Typesense credentials: " + e.name + " " + e.reason
}

// IsTypesenseCredentialError reports whether err is the check of a Typesense
// credential that is missing or cannot be sent in an HTTP header. The sentence a
// command shows for it names the environment variable and no part of its value.
func IsTypesenseCredentialError(err error) bool {
	var credential *typesenseCredentialError
	return errors.As(err, &credential)
}

// CheckTypesenseCredentials reports the configuration a request would refuse,
// before the first request is made. It reads only the variable and never returns
// or quotes a value.
func CheckTypesenseCredentials(lookupEnv func(string) (string, bool)) error {
	_, err := typesenseHeaders(lookupEnv)
	return err
}

// newTypesenseDatabase is the adapter's constructor, a seam so tests reach Open,
// OpenProtected and OpenSchemaScan without a server. Always
// dalgo2typesense.NewDB in production.
var newTypesenseDatabase = dalgo2typesense.NewDB

// openTypesense opens the origin of the ref and pins the one collection through
// the Typesense adapter. The adapter is read-only and sealed: it exposes no write,
// raw HTTP, management, multi-search or RPC accessor, so a copy destination is
// refused before any request. The protected mode needs no provider-side option of
// its own — the scoped key is the server-side authorization — so an outer
// access-policy wrapper remains the one place a row or a column can be withheld
// for a structured read, and a native search is deliberately outside it.
//
// The adapter's own bounds stay in force (one operation, a positive limit, the
// page, offset, request, response and row budgets and the operation deadline);
// this package sets no query limit here and no local aggregate or full-collection
// fallback.
func openTypesense(ctx context.Context, r BackendRef, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, &refusedError{"Typesense sources are read-only; copying into Typesense is unavailable"}
	}
	if !IsTypesenseScheme(r.Scheme) || r.TypesenseHost == "" || r.TypesenseCollection == "" {
		return nil, errUnsupportedBackend
	}
	if err := CheckTypesenseCredentials(os.LookupEnv); err != nil {
		return nil, err
	}
	return newTypesenseDatabase(ctx, dalgo2typesense.Config{
		Endpoint:          r.typesenseEndpoint(),
		Collection:        r.TypesenseCollection,
		Headers:           func(context.Context) (map[string]string, error) { return typesenseHeaders(os.LookupEnv) },
		AllowLoopbackHTTP: r.TypesenseAllowLoopbackHTTP,
	})
}

// TypesenseFailureReason is the fixed, safe sentence a caller shows for a
// Typesense read or open failure. It never includes the endpoint, the request, a
// response body or a credential even when err wraps one of them; it names only a
// classification, plus the cancellation/deadline diagnosis that is useful to tell
// a caller what happened.
func TypesenseFailureReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "the read was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "the read exceeded its deadline"
	}
	return typesenseFailureReason(err)
}

// IsTypesenseMetadataForbidden reports whether a Typesense metadata read was
// refused by the credential. A search-only key can read documents; it cannot list
// collections or describe a schema, and the refusal is never turned into an empty
// catalog.
func IsTypesenseMetadataForbidden(err error) bool {
	return errors.Is(err, dalgo2typesense.ErrMetadataForbidden)
}

// typesenseFailureReason is what OpenFailure says about a Typesense failure: a
// fixed sentence, never the adapter's response body (which it does not expose
// anyway), never the endpoint and never a credential.
func typesenseFailureReason(err error) string {
	var credential *typesenseCredentialError
	switch {
	case errors.As(err, &credential):
		// The typed error's own sentence names the variable and nothing else.
		// err.Error() would include whatever an outer wrapper added around it.
		return credential.Error()
	case errors.Is(err, dalgo2typesense.ErrMetadataForbidden):
		return "the Typesense credential is not permitted to read collection metadata; a scan or schema read needs the collections:get and collections:list actions on the collection"
	case errors.Is(err, dalgo2typesense.ErrSemanticMismatch):
		return "the exact structured predicate cannot be matched by the native Typesense candidate within the adapter's bounded prefix; narrow the filter instead of relaxing it (the adapter refuses a candidate rather than dropping a row or scanning around it)"
	case errors.Is(err, dal.ErrNotSupported):
		return "the Typesense adapter does not support this operation: it reads one configured collection with a positive limit, with no joins, grouping, aggregation, cursors, transactions or writes"
	case errors.Is(err, dalgo2typesense.ErrPermissionDenied):
		return "the Typesense server refused the credential; check " + TypesenseAPIKeyEnv + " and the key's scoped read grant on the collection (the adapter does not quote the endpoint, the request or the key)"
	case errors.Is(err, dalgo2typesense.ErrRequestTooLarge),
		errors.Is(err, dalgo2typesense.ErrResponseTooLarge),
		errors.Is(err, dalgo2typesense.ErrRowTooLarge),
		errors.Is(err, dalgo2typesense.ErrTooManyRows),
		errors.Is(err, dalgo2typesense.ErrTooManyPages):
		return "the Typesense read exceeded the adapter's bounded request, response, row or page budget"
	case errors.Is(err, dalgo2typesense.ErrPartialResult):
		return "the Typesense server returned fewer hits than its own found count promised; the adapter refuses to present a capped result as a clean end of data"
	case errors.Is(err, dalgo2typesense.ErrDuplicateID):
		return "the Typesense server repeated a document id across pages, so the adapter refused the unstable page boundary"
	case errors.Is(err, dalgo2typesense.ErrRedirectRefused):
		return "the Typesense server attempted a redirect, which the adapter refuses so the credential cannot reach another origin"
	case errors.Is(err, dalgo2typesense.ErrMalformedResponse):
		return "the Typesense server returned a response the adapter does not recognise"
	case errors.Is(err, dalgo2typesense.ErrClosed):
		return "the Typesense handle is closed"
	case errors.Is(err, dalgo2typesense.ErrCollectionNotConfigured):
		return "the Typesense source names no collection; use typesense://HOST/COLLECTION"
	case errors.Is(err, dalgo2typesense.ErrRequestFailed):
		return "the Typesense server could not be reached or the connection failed"
	case errors.Is(err, dalgo2typesense.ErrUnsafeValue):
		return "the adapter refused a value it will not send to Typesense"
	}
	return openFailureReason(err)
}
