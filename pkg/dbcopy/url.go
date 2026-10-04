// Package dbcopy implements `datatug db copy --from <url> --to <url>`.
//
// This file covers the URL scheme dispatcher: parsing --from/--to arguments
// into a typed BackendRef and opening the underlying DALgo dal.DB.
//
// Scheme support per spec/features/cli/db/copy/README.md (REQ:supported-schemes) —
// that spec predates http/https and does not document them yet:
//
//   - sqlite://         fully wired via dalgo2sqlite
//   - ingitdb://        fully wired via dalgo2ingitdb; local-paths-only
//     (REQ:ingitdb-url-local-only)
//   - postgres://       parses; Open returns ErrPostgresNotWired until a
//     PostgreSQL DALgo driver implements the three capability
//     interfaces (dbschema.SchemaReader, ddl.SchemaModifier,
//     dal.ConcurrencyAware)
//   - env:NAME          resolves the environment variable NAME, which holds
//     any other supported URL (the way to give PostgreSQL its password
//     without writing it in a project file or on a command line); see
//     parseEnvSource
//   - http:// https://  fully wired via dal-go/dalgo2http (pkg/httpsource);
//     local-paths-only, same convention as ingitdb:// — see
//     parseHTTPSource
package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-cli/pkg/openvaultdb"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	"github.com/xo/dburl"
)

// supportedSchemes is the MVP-supported scheme list, used both for dispatch
// and to construct the error message for REQ:unknown-scheme-rejected.
var supportedSchemes = []string{"sqlite", "ingitdb", "postgres", "http", "https", "openvaultdb"}

// SupportedSchemes returns the exact schemes Parse/Open dispatch, in
// dispatch order. It is the single source of truth other packages should
// build user-facing scheme lists from (e.g. a `--db` flag's help text) so
// that text cannot drift from what Open actually accepts the way it did
// when http/https were wired in here without every caller's help text being
// updated to match.
func SupportedSchemes() []string {
	out := make([]string, len(supportedSchemes))
	copy(out, supportedSchemes)
	return out
}

// envPrefix opens the "env:NAME" source form: NAME is an environment variable
// that holds a supported source URL.
const envPrefix = "env:"

// EnvSourceForm is how the env source form is spelled in help text.
const EnvSourceForm = envPrefix + "NAME"

// envNamePattern is the rule for the NAME in "env:NAME" and for a descriptor's
// dsnEnv: upper-case letters, digits and underscores, starting with a letter.
// It keeps the name inert (no "=", no NUL, no path or URL text), so an error
// can name it safely.
var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// ValidEnvName reports whether name is an acceptable environment variable name
// for an "env:NAME" source or a PostgreSQL descriptor's dsnEnv.
func ValidEnvName(name string) bool {
	return envNamePattern.MatchString(name)
}

// ErrPostgresNotWired is returned by BackendRef.Open for postgres:// URLs
// until a PostgreSQL DALgo driver implements the three capability interfaces
// (dbschema.SchemaReader, ddl.SchemaModifier, dal.ConcurrencyAware).
var ErrPostgresNotWired = errors.New("PostgreSQL backend not yet wired")

// ErrSourceFileMissing is wrapped by CheckSourceFile (and, through it, by
// Open's sqlite/ingitdb branches) when a file-backed source does not exist
// on disk — e.g. `datatug serve --project` against a demo project before
// `datatug demo` has fetched ~/datatug/dbs/chinook-local.sqlite. Checked
// with errors.Is so a caller (pkg/secureread, pkg/server/endpoints) can map
// it to api-contract.md's SOURCE_UNAVAILABLE (503) instead of letting the
// driver's own opaque "unable to open database file" text reach an HTTP 500.
var ErrSourceFileMissing = errors.New("source file does not exist")

// CheckSourceFile reports ErrSourceFileMissing (wrapping the path and a
// `datatug demo` recovery hint) when path does not exist on disk, and nil
// when it does or when the stat fails for any other reason (permissions,
// etc. — left for the underlying driver to report on its own terms). It is
// exported so callers that open a file-backed source through a path other
// than BackendRef.Open (pkg/secureread's read-only native-SQL connection)
// can perform the identical check.
//
// The path in the message is the display form of path (see pathDisplay): the
// query string and the fragment are cut off, a path that starts like userinfo
// is shown without it, and a URL handed in by mistake is shown as SourceDisplay
// shows it, so no caller can put a secret in the message by passing what the
// source string held.
func CheckSourceFile(path string) error {
	if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
		return &missingSourceFileError{shown: pathDisplay(path)}
	}
	return nil
}

// missingSourceFileError is the error CheckSourceFile returns. Its type is how
// BackendRef.OpenFailure knows the text is this package's own: it is built from
// the display form of the path and from nothing a driver wrote.
type missingSourceFileError struct{ shown string }

func (e *missingSourceFileError) Error() string {
	return fmt.Sprintf("%s: %s (run `datatug demo` to fetch the demo project's data fixtures)", ErrSourceFileMissing, e.shown)
}

func (e *missingSourceFileError) Unwrap() error { return ErrSourceFileMissing }

// unprintablePath is shown in place of a path that SourceDisplay's checks leave
// out.
const unprintablePath = "<unprintable path>"

// pathDisplay returns the text to show for path, a file or directory path a
// source resolved to (BackendRef.Path), or a URL when a caller hands in a Path
// that is one.
func pathDisplay(path string) string {
	var shown string
	if schemePrefix.MatchString(path) {
		shown = SourceDisplay(path)
	} else {
		shown = localTextDisplay(path)
	}
	if shown == "" {
		return unprintablePath
	}
	return shown
}

// BackendRef is a parsed --from/--to URL.
type BackendRef struct {
	Scheme string
	// Path holds the scheme-specific resource locator.
	// - sqlite:      filesystem path to the .db file (e.g. "/tmp/foo.db" or "./rel.db")
	// - ingitdb:     filesystem path to the project directory
	// - postgres:    full original URL (passed verbatim to the future driver).
	//                It can hold the password: never print or store Path; use
	//                Raw or String for any message.
	// - http/https:  filesystem path to the datatug project directory whose
	//                queries/ tree declares the HTTP QueryDefs to serve (see
	//                pkg/httpsource) — same local-path convention as ingitdb,
	//                NOT a literal remote endpoint; the project's own query
	//                definitions name the actual remote endpoints.
	Path string
	// Raw is the text to show for the source in a message: the display form of
	// the input (see SourceDisplay), built from its scheme, host, port and path
	// and never holding userinfo, a query string or a fragment. An "env:NAME"
	// input stays "env:NAME" because the value of the variable is never copied
	// here. The name is historical: Raw is never the input as typed.
	Raw string
}

// String returns the display form of the source (see Display), so printing a
// BackendRef with %v or %+v can never put a password in a log or message.
func (r BackendRef) String() string { return r.Display() }

// GoString makes %#v print the same display text as String.
func (r BackendRef) GoString() string { return r.String() }

// Parse parses a CLI URL argument into a BackendRef. It returns an error for
// unknown schemes (REQ:unknown-scheme-rejected), malformed URLs, and remote
// ingitdb:// URLs (REQ:ingitdb-url-local-only).
//
// The unknown-scheme error message names BOTH the unsupported scheme AND
// the supported list, as required by REQ:unknown-scheme-rejected.
//
// An input of the form env:NAME is resolved first: NAME must match
// ^[A-Z][A-Z0-9_]*$ and name an environment variable holding any other
// supported URL. No error ever echoes the variable's value.
func Parse(rawURL string) (BackendRef, error) {
	return parseSource(rawURL, os.LookupEnv)
}

// parseSource is Parse over an injected environment lookup, so tests never
// touch the process environment.
func parseSource(rawURL string, lookupEnv func(string) (string, bool)) (BackendRef, error) {
	if strings.HasPrefix(rawURL, envPrefix) {
		return parseEnvSource(rawURL, lookupEnv)
	}
	return parseURL(rawURL)
}

// parseEnvSource resolves "env:NAME" to the URL the variable holds and parses
// that. Every error names the variable and none shows its value: the value is
// the connection string and holds the password.
func parseEnvSource(rawURL string, lookupEnv func(string) (string, bool)) (BackendRef, error) {
	name := strings.TrimPrefix(rawURL, envPrefix)
	if !ValidEnvName(name) {
		return BackendRef{}, fmt.Errorf("invalid env source: the variable name must match %s", envNamePattern)
	}
	value, found := lookupEnv(name)
	if !found {
		return BackendRef{}, fmt.Errorf("environment variable %s is not set", name)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return BackendRef{}, fmt.Errorf("environment variable %s is empty", name)
	}
	if strings.HasPrefix(value, envPrefix) {
		return BackendRef{}, fmt.Errorf("environment variable %s must hold a URL, not another env: reference", name)
	}
	ref, err := parseURL(value)
	if err != nil {
		return BackendRef{}, fmt.Errorf("environment variable %s does not hold a supported source URL (supported schemes: %s)", name, strings.Join(supportedSchemes, ", "))
	}
	ref.Raw = rawURL
	return ref, nil
}

// parseURL dispatches rawURL on its scheme, which is read case-insensitively
// (RFC 3986), and builds every error from fixed sentences and the display form
// of rawURL (see SourceDisplay): nothing the user typed is quoted, and no parser
// error is wrapped, because both can hold a password in a shape no pattern
// recognises.
func parseURL(rawURL string) (BackendRef, error) {
	head := schemeHead.FindStringSubmatch(rawURL)
	if head == nil {
		return BackendRef{}, errUnsupportedSource()
	}
	scheme, separator, rest := strings.ToLower(head[1]), head[2], rawURL[len(head[0]):]
	if separator == ":" {
		// "scheme:rest" without "//": sqlite:file.db is the one form that means
		// something. Any other is not named, because what stands before the colon
		// may be a user name or a token ("TOKEN:x-oauth-basic").
		if scheme == "sqlite" {
			return parseSQLite(rawURL, scheme+separator+rest, rest, false)
		}
		return BackendRef{}, errUnsupportedSource()
	}
	display := SourceDisplay(rawURL)
	switch scheme {
	case "openvaultdb":
		if rest == "" {
			return BackendRef{}, fmt.Errorf("OpenVaultDB connection descriptor path is required")
		}
		if err := refuseUserinfo(scheme, display, rest); err != nil {
			return BackendRef{}, err
		}
		return BackendRef{Scheme: scheme, Path: rest, Raw: display}, nil
	case "ingitdb":
		return parseInGitDB(display, rest)
	case "http", "https":
		return parseHTTPSource(scheme, display, rest)
	case "postgres", "postgresql":
		// Recognized but not opened. Parse via dburl to validate shape; carry the
		// full URL, its scheme in lower case, in Path for the driver.
		path := scheme + "://" + rest
		if _, err := dburl.Parse(path); err != nil {
			// The parser's own error quotes the URL, and so would any fragment of
			// it: say only that the URL is malformed.
			return BackendRef{}, fmt.Errorf("invalid postgres URL %q: not a valid connection URL (check the host, the port and the percent-encoding of the user and password)", display)
		}
		return BackendRef{Scheme: "postgres", Path: path, Raw: display}, nil
	case "sqlite":
		return parseSQLite(rawURL, scheme+separator+rest, rest, true)
	}
	return BackendRef{}, errUnsupportedScheme(scheme)
}

// parseSQLite handles sqlite://path and sqlite:path. dburl, which understands
// the scheme, reads the path; the scheme it is given is lower case.
func parseSQLite(rawURL, lowerURL, rest string, slashes bool) (BackendRef, error) {
	display := SourceDisplay(rawURL)
	if slashes {
		if err := refuseUserinfo("sqlite", display, rest); err != nil {
			return BackendRef{}, err
		}
	}
	u, err := dburl.Parse(lowerURL)
	if err != nil {
		return BackendRef{}, fmt.Errorf("invalid sqlite URL %q: not a valid sqlite URL (check the path and the percent-encoding)", display)
	}
	// dburl puts the filesystem path in DSN for sqlite.
	return BackendRef{Scheme: "sqlite", Path: u.DSN, Raw: display}, nil
}

// errUnsupportedScheme is the error for a "scheme://" that Parse does not
// dispatch. It names the scheme, as REQ:unknown-scheme-rejected asks, and
// shows nothing else of the URL.
func errUnsupportedScheme(scheme string) error {
	return fmt.Errorf(
		"unsupported scheme %q: supported schemes are %s, or %s",
		scheme, strings.Join(supportedSchemes, ", "), EnvSourceForm,
	)
}

// errUnsupportedSource is the error for a string that does not start with a
// "scheme://". Whatever it is, it may hold a password, so none of it is shown.
func errUnsupportedSource() error {
	return fmt.Errorf(
		"unsupported source: expected a URL such as sqlite://..., or %s; supported schemes are %s",
		EnvSourceForm, strings.Join(supportedSchemes, ", "),
	)
}

// refuseUserinfo returns an error when rest, the text after "scheme://" of a
// scheme that names a file or directory, is shaped like credentials: either
// "user:password@host" (the user name may be empty) or, for a project directory
// (ingitdb, http, https), a first path segment that holds an "@". Whoever writes
// that means a remote server with credentials, which these schemes do not take;
// refusing it keeps the password out of every later message that quotes the
// path. The error shows the display form of the source (see SourceDisplay), so
// it names the host and the path that follow the credentials and never the
// credentials.
func refuseUserinfo(scheme, display, rest string) error {
	userinfoShaped := looksLikeUserinfo(rest)
	projectDirectory := scheme == "ingitdb" || scheme == "http" || scheme == "https"
	tokenShaped := projectDirectory && holdsAtInFirstSegment(rest)
	if !userinfoShaped && !tokenShaped {
		return nil
	}
	var advice string
	switch {
	case scheme == "http" || scheme == "https":
		advice = fmt.Sprintf(`%s:// names a local datatug project directory, not a remote endpoint; write a relative directory that holds an "@" as ./dir`, scheme)
	case scheme == "ingitdb" && !userinfoShaped:
		advice = `ingitdb:// names a local directory, not a remote repository; write a relative directory that holds an "@" as ./dir`
	default:
		advice = fmt.Sprintf("%s:// names a local path; write a relative path as ./path", scheme)
	}
	return fmt.Errorf("invalid %s URL %q: credentials are not supported (%s)", scheme, display, advice)
}

// parseInGitDB handles the ingitdb:// scheme. MVP is local-paths-only per
// REQ:ingitdb-url-local-only: remote-looking URLs must be rejected with a
// message naming "local paths only". display is the display form of the whole
// source and rest the text after "ingitdb://".
func parseInGitDB(display, rest string) (BackendRef, error) {
	if rest == "" {
		return BackendRef{}, fmt.Errorf(
			"invalid ingitdb URL %q: missing local path (ingitdb:// MVP supports local paths only)",
			display,
		)
	}

	// Reject obvious remote forms: a well-known host, or any URL wrapped in the
	// scheme ("ingitdb://https://TOKEN@github.com/org/repo").
	lower := strings.ToLower(rest)
	switch {
	case strings.HasPrefix(lower, "github.com/"),
		strings.HasPrefix(lower, "gitlab.com/"),
		strings.HasPrefix(lower, "bitbucket.org/"),
		schemePrefix.MatchString(rest):
		return BackendRef{}, fmt.Errorf(
			"ingitdb URL %q looks remote; ingitdb:// MVP supports local paths only",
			display,
		)
	}

	// "ingitdb://TOKEN@github.com/org/repo": a token written as the user name has
	// no colon. A local directory never starts with "user@host"; one that holds
	// an "@" is written ./dir (see LocalSourceURL).
	if err := refuseUserinfo("ingitdb", display, rest); err != nil {
		return BackendRef{}, err
	}

	// ingitdb://./relative  → rest = "./relative"      (local, OK)
	// ingitdb://relative    → rest = "relative"        (local, OK)
	// ingitdb:///absolute   → rest = "/absolute"       (local, OK)
	return BackendRef{Scheme: "ingitdb", Path: rest, Raw: display}, nil
}

// parseHTTPSource handles the http:// and https:// schemes.
//
// Unlike every other scheme here, the "resource" a db-copy http(s) URL
// names is not the URL itself: it is a LOCAL datatug project directory,
// exactly the convention ingitdb:// already uses. Opening it (see Open,
// below) reads every HTTP-type QueryDef under that project's queries/ tree
// and builds one dalgo2http collection per query — the query definitions
// carry the real remote endpoint URLs (their .query.http sibling files),
// not this --from/--to argument. This is a design decision this stream
// made, not a founder ruling; see the PR body for the reasoning (it mirrors
// ingitdb:// deliberately, rather than inventing a different shape for the
// one other scheme whose "database" is a whole project instead of a single
// file).
//
// Parse only requires a non-empty path; it does not attempt to distinguish
// "looks like a real remote host" from "looks like a local path" the way
// parseInGitDB does; scheme is already unambiguous (http/https), so any
// unresolvable path simply fails later, in Open, with a clear error naming
// the path.
func parseHTTPSource(scheme, display, rest string) (BackendRef, error) {
	if rest == "" {
		return BackendRef{}, fmt.Errorf(
			"invalid %s URL %q: missing project path (%s:// opens the datatug project at this local path, serving its HTTP QueryDefs as dalgo2http collections)",
			scheme, display, scheme,
		)
	}
	// A project directory never starts with "user:password@host" or "token@host".
	// Anyone who writes that means a remote endpoint with credentials, which this
	// scheme does not take; refusing it keeps the password out of every later
	// error that quotes the path. A text redactor cannot do this job for http(s):
	// it cannot tell "alice:42/abc@host" (a password) from "localhost:8080/@me" (a
	// path), so Parse refuses the userinfo shape first, as the path schemes do.
	if err := refuseUserinfo(scheme, display, rest); err != nil {
		return BackendRef{}, err
	}
	return BackendRef{Scheme: scheme, Path: rest, Raw: display}, nil
}

// ProjectSourceURL returns the http:// source URL that names the datatug
// project directory projectDir, the way the commands and the server build one
// from a directory they were given verbatim. A relative directory whose first
// segment holds an "@" (my@proj, @acme/proj) is written "./dir", because Parse
// refuses "http://my@proj" as a URL that carries credentials.
func ProjectSourceURL(projectDir string) string {
	return LocalSourceURL("http", projectDir)
}

// LocalSourceURL returns the "scheme://path" source URL that names the local
// directory path for a scheme that takes one (http, https, ingitdb). A relative
// path whose first segment holds an "@" or that is shaped like
// "user:password@host" (a:b/c@d) is written "./path", because Parse refuses
// "scheme://my@proj" and "scheme://a:b/c@d" as URLs that carry credentials.
// Every caller that builds such a URL from a directory it was given verbatim
// goes through here, so a directory such as my@proj keeps working.
func LocalSourceURL(scheme, path string) string {
	if holdsAtInFirstSegment(path) || looksLikeUserinfo(path) {
		return scheme + "://./" + path
	}
	return scheme + "://" + path
}

// holdsAtInFirstSegment reports whether the first path segment of rest holds an
// "@" and so reads as "user@host". A Windows drive path never does.
func holdsAtInFirstSegment(rest string) bool {
	if driveLetterPath.MatchString(rest) {
		return false
	}
	first, _, _ := strings.Cut(rest, "/")
	return strings.Contains(first, "@")
}

// Open opens the underlying DALgo dal.DB for this BackendRef.
//
// Dispatch:
//   - sqlite:      opens via dalgo2sqlite.NewDatabase.
//   - ingitdb:     opens via dalgo2ingitdb.NewDatabase with the default
//     validator-backed CollectionsReader.
//   - postgres:    returns ErrPostgresNotWired (no DALgo Postgres driver
//     yet exposes the three capability interfaces).
//   - http/https:  opens via httpsource.Open, translating every HTTP
//     QueryDef under the project directory (r.Path) into a
//     dalgo2http collection.
//
// Open never applies the provider-side read hardening OpenProtected does
// (see its doc comment): every caller here — `datatug db copy`, and schema
// introspection in pkg/server/endpoints — is a trusted, operator-level
// caller with no pkg/accesspolicies wrapper above it, so a formula/computed
// column is returned exactly as the provider evaluates it, matching every
// dalgo2sql/dalgo2ingitdb release before Task 13 (S110).
//
// The context is reserved for future use; today's driver constructors are
// synchronous and do not honor cancellation. That's acceptable for the MVP
// CLI verb.
func (r BackendRef) Open(ctx context.Context) (dal.DB, error) {
	return r.open(ctx, false, false)
}

// OpenForTest is Open, except that for an "http"/"https" BackendRef every
// dalgo2http.Collection it builds gets Collection.InsecureAllowLoopback set
// (dal-go/dalgo2http v0.2.0's TEST-ONLY escape hatch — see
// httpsource.AllowInsecureLoopback's doc comment). Every other scheme
// behaves identically to Open.
//
// It exists so a test that drives the full sourceURL -> Parse -> Open
// pipeline in-process (e.g. apps/datatugapp/commands's
// cmd_query_http_provenance_test.go, pkg/secureread's
// executor_provenance_test.go via Executor.RunStructuredInsecureForTest)
// can point an HTTP QueryDef's .query.http file at a loopback
// httptest.Server or an intentionally-unreachable loopback address (e.g.
// 127.0.0.1:1, for a fast deterministic live-failure), without any project
// descriptor file ever requesting that itself — the field is set here, in
// Go code, only when a caller explicitly calls THIS method instead of
// Open. NEVER call this from production code.
func (r BackendRef) OpenForTest(ctx context.Context) (dal.DB, error) {
	return r.open(ctx, true, false)
}

// OpenProtected is Open, except an "ingitdb" BackendRef is opened with
// dalgo2ingitdb.WithStoredOnlyReads(). SQLite uses the validated,
// parameter-bound structured-query dialect on both paths. Other schemes
// behave like Open.
//
// pkg/secureread.openSource uses this for policy-secured sessions; direct
// `query run` uses it for SQLite structured queries. A policy-secured session
// wraps the returned dal.DB with pkg/accesspolicies before any row reaches a
// caller. dalgo2ingitdb's own formula
// evaluator has no way to know which of a computed column's dependencies
// pkg/accesspolicies would have redacted — the adapter computes the value
// from the FULL underlying record and hands back the (correct) result,
// which can leak a hidden field's value through an allowed computed column
// (see dal-go/dalgo2ingitdb#8 / this repo's README "Owner access
// policies" section). WithStoredOnlyReads keeps dalgo2ingitdb from
// evaluating or returning any formula column at all under an outer policy
// wrapper, so pkg/accesspolicies' field allow-list is the only thing that
// can ever put a value on the wire — it stays the single enforcement
// point. This adapter never writes an .ingitdb/access/manifest.yaml file
// of its own, so dalgo2ingitdb's persisted owner-policy layer (also new in
// v0.4.0) never activates for a datatug-cli-opened project; policies do
// NOT layer under pkg/accesspolicies here — pkg/accesspolicies remains the
// sole enforcement layer for every source this CLI opens.
//
// The sqlite scheme opts every read, protected or not, into dalgo2sql's
// DbOptions.StructuredQueryDialect: "sqlite" (bounded, parameter-bound
// structured-query compilation): dal-go/dalgo2sql#179 added FROM-source
// alias support to compileStructuredSQL, which used to unconditionally
// reject any structured query whose FROM source carried an alias — this
// project's own demo query (queries/customers/customer-invoices.query.dtql,
// `from: {name: Invoice, alias: i}`) uses exactly that shape, and used to
// turn into a hard error the moment this dialect was enabled. With #179
// fixed, existing non-aggregate DTQL queries (see dalgo2sql's own
// dtql_datatug_inventory_test.go) compile cleanly, so protected reads now
// get the dialect's real guarantees: every dal.Constant value becomes a
// genuine `?` placeholder + bound arg (not a quoted-string literal), and
// unsupported shapes (such as joins and cursors) fail closed. GROUP BY and
// HAVING are compiled by dalgo2sql's aggregation path. The plain Open path
// (db copy / introspection) sets the same dialect, so it never reaches the
// legacy emitSQL renderer; only the ingitdb hardening differs between Open
// and OpenProtected.
func (r BackendRef) OpenProtected(ctx context.Context) (dal.DB, error) {
	return r.open(ctx, false, true)
}

// OpenProtectedForTest combines OpenProtected's provider-side read
// hardening with OpenForTest's http(s) loopback escape hatch. It exists for
// pkg/secureread's Executor.RunStructuredInsecureForTest, which must drive
// the exact same openSource -> BackendRef -> pkg/accesspolicies pipeline
// RunStructured uses in production, just against a loopback test server.
// NEVER call this from production code.
func (r BackendRef) OpenProtectedForTest(ctx context.Context) (dal.DB, error) {
	return r.open(ctx, true, true)
}

var (
	newSQLiteDatabaseWithOptions = dalgo2sqlite.NewDatabaseWithOptions
	newInGitDBDatabase           = dalgo2ingitdb.NewDatabase
)

// open opens the source and turns whatever it returns as an error into one
// that is safe to show (see OpenFailure). r.Path holds the real URL (a
// PostgreSQL password included), and a driver quotes the DSN it was given in its
// open and ping errors in whatever shape it likes, so a driver's own message is
// never shown. errors.Is and errors.As still see the driver's own error.
func (r BackendRef) open(ctx context.Context, insecureAllowLoopback, protected bool) (dal.DB, error) {
	db, err := r.openSource(ctx, insecureAllowLoopback, protected)
	if err != nil {
		return nil, r.OpenFailure(err)
	}
	return db, nil
}

// CheckFile reports ErrSourceFileMissing when this source is backed by a file or
// directory (sqlite, ingitdb) and that does not exist; for every other source it
// returns nil. It is CheckSourceFile for a caller that holds the ref: a
// PostgreSQL ref's Path is its URL and is not a path to stat.
func (r BackendRef) CheckFile() error {
	if r.Scheme != "sqlite" && r.Scheme != "ingitdb" {
		return nil
	}
	return CheckSourceFile(r.Path)
}

func (r BackendRef) openSource(ctx context.Context, insecureAllowLoopback, protected bool) (dal.DB, error) {
	switch r.Scheme {
	case "openvaultdb":
		return openvaultdb.OpenSource(r.Path)
	case "sqlite":
		if err := r.CheckFile(); err != nil {
			return nil, err
		}
		// Every SQLite open, protected or not, compiles structured reads
		// with the validated dialect: it binds values and supports native
		// aggregation, and unlike the legacy text emitter it has no limit
		// on non-ASCII identifiers or on tab, newline and backslash values
		// (dalgo2sql SQL-01 makes the legacy emitter refuse those).
		// Caller-side policies, when enabled, remain a separate
		// enforcement layer.
		opts := dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite"}
		db, err := newSQLiteDatabaseWithOptions(r.Path, dal.NewSchema(nil, nil), opts)
		if err != nil {
			return nil, err
		}
		return db, nil

	case "ingitdb":
		if err := r.CheckFile(); err != nil {
			return nil, err
		}
		var opts []dalgo2ingitdb.DatabaseOption
		if protected {
			// See OpenProtected's doc comment: the inGitDB protected path
			// layers pkg/accesspolicies above the returned dal.DB.
			opts = append(opts, dalgo2ingitdb.WithStoredOnlyReads())
		}
		db, err := newInGitDBDatabase(r.Path, validator.NewCollectionsReader(), opts...)
		if err != nil {
			return nil, err
		}
		return db, nil

	case "postgres":
		return nil, ErrPostgresNotWired

	case "http", "https":
		var opts []httpsource.Option
		if insecureAllowLoopback {
			opts = append(opts, httpsource.AllowInsecureLoopback())
		}
		db, err := httpsource.Open(ctx, r.Path, opts...)
		if err != nil {
			return nil, err
		}
		return db, nil

	default:
		// Defense in depth — Parse should have rejected this already.
		return nil, errUnsupportedBackend
	}
}
