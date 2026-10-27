package dbcopy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2couchdb"
)

// CouchDB support is one existing CouchDB database, read as one logical DALgo
// collection. The URI names the endpoint and the database; credentials are never
// part of it. They come from the operator's environment at request time:
//
//	DATATUG_COUCHDB_USERNAME + DATATUG_COUCHDB_PASSWORD   Basic authentication
//	DATATUG_COUCHDB_BEARER                                Bearer authentication
//
// Setting both forms, or only one half of Basic, is refused before a request is
// made. With none set the request is unauthenticated, which a CouchDB server
// answers with its own 401; no credential is ever stored in a project, an error
// message, a log line or a shell history entry.
const (
	// CouchDBUsernameEnv and CouchDBPasswordEnv hold a CouchDB user's Basic
	// credentials. Both must be set together.
	CouchDBUsernameEnv = "DATATUG_COUCHDB_USERNAME"
	CouchDBPasswordEnv = "DATATUG_COUCHDB_PASSWORD"

	// CouchDBBearerEnv holds a bearer token. It cannot be combined with the Basic
	// pair.
	CouchDBBearerEnv = "DATATUG_COUCHDB_BEARER"
)

// The CouchDB URI forms. "couchdb://" means HTTPS: plain HTTP must be asked for
// by name, and only for a loopback endpoint, because a development server is
// the only place it is safe to use.
const (
	CouchDBScheme      = "couchdb"
	CouchDBHTTPSScheme = "couchdb+https"
	CouchDBHTTPScheme  = "couchdb+http"
)

// couchDBDatabaseName is CouchDB's own limit for a database name, the same
// shape the adapter validates. It keeps a name inert: no "/", "?", "#", "@",
// percent-encoding or control character reaches a request path or a message.
var couchDBDatabaseName = regexp.MustCompile(`^[a-z][a-z0-9_$()+-]{0,237}$`)

// IsCouchDBScheme reports whether scheme is one of the CouchDB URI forms.
func IsCouchDBScheme(scheme string) bool {
	switch scheme {
	case CouchDBScheme, CouchDBHTTPSScheme, CouchDBHTTPScheme:
		return true
	}
	return false
}

// CouchDBSourceURL is the source URL of a parsed CouchDB ref: scheme, host, port
// and database only. It never holds credentials, a query or a fragment because
// Parse refuses all three.
func (r BackendRef) CouchDBSourceURL() string {
	if !IsCouchDBScheme(r.Scheme) || r.Path == "" {
		return ""
	}
	return r.Scheme + "://" + r.Path
}

// parseCouchDBSource parses couchdb://HOST/DATABASE, couchdb+https://HOST/DATABASE
// or couchdb+http://LOOPBACK/DATABASE. rest is the text after "scheme://".
func parseCouchDBSource(scheme, rest, display string) (BackendRef, error) {
	u, err := url.Parse(scheme + "://" + rest)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: expected %s://HOST/DATABASE", display, scheme)
	}
	if u.User != nil {
		return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: credentials are not supported in the URL; set %s/%s or %s in the environment", display, CouchDBUsernameEnv, CouchDBPasswordEnv, CouchDBBearerEnv)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: the URL takes no query or fragment (credentials belong in the environment, and the database name is the collection)", display)
	}
	hostname := u.Hostname()
	if hostname == "" || strings.ContainsAny(u.Host, " \t\r\n") {
		return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: it names no usable host", display)
	}
	if scheme == CouchDBHTTPScheme {
		if !couchDBLoopbackHost(hostname) {
			return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: plain HTTP (%s://) is allowed only for a loopback endpoint (localhost, 127.0.0.1 or ::1); use %s:// or %s://", display, CouchDBHTTPScheme, CouchDBScheme, CouchDBHTTPSScheme)
		}
	}
	path := u.Path
	if path == "" || path == "/" || strings.HasSuffix(path, "/") {
		return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: expected exactly one database, as %s://HOST/DATABASE", display, scheme)
	}
	database := strings.TrimPrefix(path, "/")
	if strings.Contains(database, "/") || !couchDBDatabaseName.MatchString(database) {
		return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: the database name must be one CouchDB database name (lower-case letters, digits and _ $ ( ) + -)", display)
	}
	if u.EscapedPath() != path {
		return BackendRef{}, fmt.Errorf("invalid CouchDB URL %q: the database name must not be percent-encoded", display)
	}
	return BackendRef{
		Scheme:                   scheme,
		Path:                     u.Host + "/" + database,
		CouchDBHost:              u.Host,
		CouchDBDatabase:          database,
		CouchDBAllowLoopbackHTTP: scheme == CouchDBHTTPScheme,
		Raw:                      display,
	}, nil
}

// couchDBLoopbackHost reports whether host names this machine: "localhost", an
// IPv4 loopback address or an IPv6 loopback address.
func couchDBLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// couchDBEndpoint is the HTTPS (or deliberately loopback HTTP) URL of the one
// database the ref names. It holds no credentials.
func (r BackendRef) couchDBEndpoint() string {
	scheme := "https"
	if r.CouchDBAllowLoopbackHTTP {
		scheme = "http"
	}
	return scheme + "://" + r.CouchDBHost + "/" + r.CouchDBDatabase
}

// couchDBHeaders is the one place credentials become request headers. It reads
// the environment for every request, so no credential is captured at open time
// or copied into a project, and it refuses an ambiguous configuration instead of
// guessing which credential the person meant.
func couchDBHeaders(lookupEnv func(string) (string, bool)) (map[string]string, error) {
	username, password, bearer := couchDBCredentialValues(lookupEnv)
	switch {
	case bearer != "" && (username != "" || password != ""):
		return nil, errCouchDBMixedCredentials
	case bearer != "":
		return map[string]string{"Authorization": "Bearer " + bearer}, nil
	case username == "" && password == "":
		return nil, nil
	case username == "" || password == "":
		return nil, errCouchDBIncompleteBasic
	default:
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		return map[string]string{"Authorization": "Basic " + encoded}, nil
	}
}

// couchDBCredentialValues reads the three credential variables. A variable set
// to an empty string is treated as unset, so an exported-but-empty shell slot
// does not turn a usable Basic pair into an incomplete one.
func couchDBCredentialValues(lookupEnv func(string) (string, bool)) (username, password, bearer string) {
	if value, ok := lookupEnv(CouchDBUsernameEnv); ok {
		username = value
	}
	if value, ok := lookupEnv(CouchDBPasswordEnv); ok {
		password = value
	}
	if value, ok := lookupEnv(CouchDBBearerEnv); ok {
		bearer = value
	}
	return
}

// checkCouchDBCredentials reports the configuration a request would refuse, before
// the first request is made. It reads only presence and never returns a value.
func checkCouchDBCredentials(lookupEnv func(string) (string, bool)) error {
	_, err := couchDBHeaders(lookupEnv)
	return err
}

var (
	errCouchDBMixedCredentials = errors.New("CouchDB credentials: set either " + CouchDBUsernameEnv + " and " + CouchDBPasswordEnv + ", or " + CouchDBBearerEnv + ", never both")
	errCouchDBIncompleteBasic  = errors.New("CouchDB credentials: " + CouchDBUsernameEnv + " and " + CouchDBPasswordEnv + " must be set together")
)

// newCouchDBDatabase is the adapter's constructor, a seam so tests reach Open,
// OpenProtected and OpenSchemaScan without a server. Always
// dalgo2couchdb.NewDB in production.
var newCouchDBDatabase = dalgo2couchdb.NewDB

// openCouchDB opens the one database of the ref through the CouchDB adapter. The
// adapter is read-only: a copy destination is refused before any request. The
// protected mode needs no provider-side option of its own — the adapter never
// writes, a filtered query must be served by a real Mango index (a predicate is
// never rerouted to a scan), and only an unfiltered read enumerates CouchDB's
// native primary index — so an outer access-policy wrapper remains the one place
// a row or a column can be withheld.
func openCouchDB(ctx context.Context, r BackendRef, mode openMode) (dal.DB, error) {
	if mode.forWrite {
		return nil, &refusedError{"CouchDB sources are read-only; copying into CouchDB is unavailable"}
	}
	if !IsCouchDBScheme(r.Scheme) || r.CouchDBHost == "" || r.CouchDBDatabase == "" {
		return nil, errUnsupportedBackend
	}
	if err := checkCouchDBCredentials(os.LookupEnv); err != nil {
		return nil, err
	}
	return newCouchDBDatabase(ctx, dalgo2couchdb.Config{
		DatabaseURL:       r.couchDBEndpoint(),
		Collection:        r.CouchDBDatabase,
		Headers:           func(context.Context) (map[string]string, error) { return couchDBHeaders(os.LookupEnv) },
		AllowLoopbackHTTP: r.CouchDBAllowLoopbackHTTP,
	})
}

// IsCouchDBIndexRequired reports whether err is the CouchDB adapter's refusal of
// a query no Mango index can serve. A command can then say what to do instead of
// showing only the adapter's sentence, and must never answer by scanning around
// the limitation.
func IsCouchDBIndexRequired(err error) bool {
	return errors.Is(err, dalgo2couchdb.ErrIndexRequired)
}

// couchDBFailureReason is what OpenFailure says about a CouchDB failure: a fixed
// sentence, never the adapter's response body (which it does not expose anyway)
// and never a credential.
func couchDBFailureReason(err error) string {
	var httpError *dalgo2couchdb.HTTPError
	switch {
	case errors.As(err, &httpError):
		switch httpError.StatusCode {
		case 401, 403:
			return "the CouchDB server refused the request (HTTP " + fmt.Sprint(httpError.StatusCode) + "); check " + CouchDBUsernameEnv + "/" + CouchDBPasswordEnv + " or " + CouchDBBearerEnv + " and the reader's membership in the database"
		case 404:
			return "the CouchDB database was not found (HTTP 404)"
		default:
			return "the CouchDB server answered with HTTP " + fmt.Sprint(httpError.StatusCode)
		}
	case errors.Is(err, dalgo2couchdb.ErrDatabaseNotFound):
		return "the CouchDB database was not found"
	case errors.Is(err, dalgo2couchdb.ErrIndexRequired):
		return "the query needs a usable CouchDB Mango index because it has a filter; create one outside DataTug (the adapter never creates an index, and a predicate never falls back to the primary-index scan)"
	case errors.Is(err, dalgo2couchdb.ErrHeaderProvider):
		return "the CouchDB credential environment variables are incomplete or incompatible"
	case errors.Is(err, dalgo2couchdb.ErrRequestTooLarge), errors.Is(err, dalgo2couchdb.ErrResponseTooLarge), errors.Is(err, dalgo2couchdb.ErrRowTooLarge), errors.Is(err, dalgo2couchdb.ErrTooManyRows), errors.Is(err, dalgo2couchdb.ErrTooManyPages):
		return "the request or its result exceeds the CouchDB adapter's configured bound"
	case errors.Is(err, dalgo2couchdb.ErrMalformedResponse):
		return "the CouchDB server returned a response the adapter does not recognise"
	case errors.Is(err, dalgo2couchdb.ErrUnsafeValue):
		return "the CouchDB adapter refused a value that is not safe to send"
	case errors.Is(err, dalgo2couchdb.ErrContinuationMismatch):
		return "the CouchDB pagination continuation does not belong to this query"
	case errors.Is(err, dalgo2couchdb.ErrRequestFailed):
		return "the CouchDB request failed; its details are not shown because a transport error can quote the endpoint"
	case errors.Is(err, errCouchDBMixedCredentials) || errors.Is(err, errCouchDBIncompleteBasic):
		return err.Error()
	}
	return openFailureReason(err)
}
