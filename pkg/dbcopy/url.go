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
//   - http:// https://  fully wired via dal-go/dalgo2http (pkg/httpsource);
//     local-paths-only, same convention as ingitdb:// — see
//     parseHTTPSource
package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// CheckSourceFile reports ErrSourceFileMissing (wrapping path and a
// `datatug demo` recovery hint) when path does not exist on disk, and nil
// when it does or when the stat fails for any other reason (permissions,
// etc. — left for the underlying driver to report on its own terms). It is
// exported so callers that open a file-backed source through a path other
// than BackendRef.Open (pkg/secureread's read-only native-SQL connection)
// can perform the identical check.
func CheckSourceFile(path string) error {
	if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
		return fmt.Errorf("%w: %s (run `datatug demo` to fetch the demo project's data fixtures)", ErrSourceFileMissing, path)
	}
	return nil
}

// BackendRef is a parsed --from/--to URL.
type BackendRef struct {
	Scheme string
	// Path holds the scheme-specific resource locator.
	// - sqlite:      filesystem path to the .db file (e.g. "/tmp/foo.db" or "./rel.db")
	// - ingitdb:     filesystem path to the project directory
	// - postgres:    full original URL (passed verbatim to the future driver)
	// - http/https:  filesystem path to the datatug project directory whose
	//                queries/ tree declares the HTTP QueryDefs to serve (see
	//                pkg/httpsource) — same local-path convention as ingitdb,
	//                NOT a literal remote endpoint; the project's own query
	//                definitions name the actual remote endpoints.
	Path string
	// Raw is the original input string, preserved for error messages.
	Raw string
}

// Parse parses a CLI URL argument into a BackendRef. It returns an error for
// unknown schemes (REQ:unknown-scheme-rejected), malformed URLs, and remote
// ingitdb:// URLs (REQ:ingitdb-url-local-only).
//
// The unknown-scheme error message names BOTH the unsupported scheme AND
// the supported list, as required by REQ:unknown-scheme-rejected.
func Parse(rawURL string) (BackendRef, error) {
	if strings.HasPrefix(rawURL, "openvaultdb://") {
		path := strings.TrimPrefix(rawURL, "openvaultdb://")
		if path == "" {
			return BackendRef{}, fmt.Errorf("OpenVaultDB connection descriptor path is required")
		}
		return BackendRef{Scheme: "openvaultdb", Path: path, Raw: rawURL}, nil
	}
	// Intercept ingitdb:// before delegating to dburl — dburl doesn't know
	// the scheme and would reject it.
	if strings.HasPrefix(rawURL, "ingitdb://") {
		return parseInGitDB(rawURL)
	}

	// http:// / https:// — see parseHTTPSource.
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return parseHTTPSource(rawURL)
	}

	// postgres:// — recognized but not opened. Parse via dburl to validate
	// shape; carry the full original URL in Path for the future driver.
	if strings.HasPrefix(rawURL, "postgres://") || strings.HasPrefix(rawURL, "postgresql://") {
		if _, err := dburl.Parse(rawURL); err != nil {
			return BackendRef{}, fmt.Errorf("invalid postgres URL %q: %w", rawURL, err)
		}
		return BackendRef{Scheme: "postgres", Path: rawURL, Raw: rawURL}, nil
	}

	// sqlite:// — delegated to dburl, which understands the scheme.
	if strings.HasPrefix(rawURL, "sqlite://") || strings.HasPrefix(rawURL, "sqlite:") {
		u, err := dburl.Parse(rawURL)
		if err != nil {
			return BackendRef{}, fmt.Errorf("invalid sqlite URL %q: %w", rawURL, err)
		}
		// dburl puts the filesystem path in DSN for sqlite.
		return BackendRef{Scheme: "sqlite", Path: u.DSN, Raw: rawURL}, nil
	}

	// Everything else: unknown scheme. Extract the scheme substring for the
	// error message even if dburl rejects the URL.
	scheme := extractScheme(rawURL)
	return BackendRef{}, fmt.Errorf(
		"unsupported scheme %q in URL %q: supported schemes are %s",
		scheme, rawURL, strings.Join(supportedSchemes, ", "),
	)
}

// parseInGitDB handles the ingitdb:// scheme. MVP is local-paths-only per
// REQ:ingitdb-url-local-only: remote-looking URLs must be rejected with a
// message naming "local paths only".
func parseInGitDB(rawURL string) (BackendRef, error) {
	const prefix = "ingitdb://"
	rest := strings.TrimPrefix(rawURL, prefix)

	if rest == "" {
		return BackendRef{}, fmt.Errorf(
			"invalid ingitdb URL %q: missing local path (ingitdb:// MVP supports local paths only)",
			rawURL,
		)
	}

	// Reject obvious remote forms.
	switch {
	case strings.HasPrefix(rest, "github.com/"),
		strings.HasPrefix(rest, "gitlab.com/"),
		strings.HasPrefix(rest, "bitbucket.org/"),
		strings.HasPrefix(rest, "http://"),
		strings.HasPrefix(rest, "https://"):
		return BackendRef{}, fmt.Errorf(
			"ingitdb URL %q looks remote; ingitdb:// MVP supports local paths only",
			rawURL,
		)
	}

	// ingitdb://./relative  → rest = "./relative"      (local, OK)
	// ingitdb://relative    → rest = "relative"        (local, OK)
	// ingitdb:///absolute   → rest = "/absolute"       (local, OK)
	return BackendRef{Scheme: "ingitdb", Path: rest, Raw: rawURL}, nil
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
func parseHTTPSource(rawURL string) (BackendRef, error) {
	scheme := extractScheme(rawURL)
	rest := strings.TrimPrefix(rawURL, scheme+"://")
	if rest == "" {
		return BackendRef{}, fmt.Errorf(
			"invalid %s URL %q: missing project path (%s:// opens the datatug project at this local path, serving its HTTP QueryDefs as dalgo2http collections)",
			scheme, rawURL, scheme,
		)
	}
	return BackendRef{Scheme: scheme, Path: rest, Raw: rawURL}, nil
}

// extractScheme pulls the scheme prefix from a URL string for use in error
// messages, without depending on a successful parse. Returns the substring
// before the first ":" or the full string if no colon.
func extractScheme(rawURL string) string {
	if i := strings.Index(rawURL, ":"); i > 0 {
		return rawURL[:i]
	}
	return rawURL
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
// dalgo2ingitdb v0.4.0's dalgo2ingitdb.WithStoredOnlyReads() database
// option. Every other scheme behaves identically to Open.
//
// pkg/secureread.openSource is the only caller: it always wraps the
// returned dal.DB with pkg/accesspolicies (the session's local YAML
// policies) before any row reaches a caller. dalgo2ingitdb's own formula
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
// The sqlite scheme opts protected reads into dalgo2sql's
// DbOptions.StructuredQueryDialect: "sqlite" (bounded, parameter-bound
// structured-query compilation): dal-go/dalgo2sql#179 added FROM-source
// alias support to compileStructuredSQL, which used to unconditionally
// reject any structured query whose FROM source carried an alias — this
// project's own demo query (queries/customers/customer-invoices.query.dtql,
// `from: {name: Invoice, alias: i}`) uses exactly that shape, and used to
// turn into a hard error the moment this dialect was enabled. With #179
// fixed, every DTQL query datatug ships or tests (see dalgo2sql's own
// dtql_datatug_inventory_test.go) compiles cleanly, so protected reads now
// get the dialect's real guarantees: every dal.Constant value becomes a
// genuine `?` placeholder + bound arg (not a quoted-string literal), and
// an unsupported shape (a join, GROUP BY, HAVING, a cursor) fails closed
// instead of silently falling back to legacy string rendering. The plain
// Open path (db copy / introspection) is unaffected — it never sets
// StructuredQueryDialect, so it keeps using the legacy emitSQL rendering.
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

func (r BackendRef) open(ctx context.Context, insecureAllowLoopback, protected bool) (dal.DB, error) {
	switch r.Scheme {
	case "openvaultdb":
		return openvaultdb.OpenSource(r.Path)
	case "sqlite":
		if err := CheckSourceFile(r.Path); err != nil {
			return nil, err
		}
		var opts dalgo2sql.DbOptions
		if protected {
			// See OpenProtected's doc comment: pkg/secureread is the only
			// caller that sets protected=true, and it always layers
			// pkg/accesspolicies above the returned dal.DB — the dialect's
			// bound-value, validated-source compilation is additional
			// hardening under that same enforcement point, not a
			// replacement for it.
			opts.StructuredQueryDialect = "sqlite"
		}
		db, err := dalgo2sqlite.NewDatabaseWithOptions(r.Path, dal.NewSchema(nil, nil), opts)
		if err != nil {
			return nil, fmt.Errorf("open sqlite %q: %w", r.Path, err)
		}
		return db, nil

	case "ingitdb":
		if err := CheckSourceFile(r.Path); err != nil {
			return nil, err
		}
		var opts []dalgo2ingitdb.DatabaseOption
		if protected {
			// See OpenProtected's doc comment: pkg/secureread is the only
			// caller that sets protected=true, and it always layers
			// pkg/accesspolicies above the returned dal.DB.
			opts = append(opts, dalgo2ingitdb.WithStoredOnlyReads())
		}
		db, err := dalgo2ingitdb.NewDatabase(r.Path, validator.NewCollectionsReader(), opts...)
		if err != nil {
			return nil, fmt.Errorf("open ingitdb %q: %w", r.Path, err)
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
			return nil, fmt.Errorf("open %s source %q: %w", r.Scheme, r.Path, err)
		}
		return db, nil

	default:
		// Defense in depth — Parse should have rejected this already.
		return nil, fmt.Errorf("unsupported scheme %q (this is a bug; Parse should have caught it)", r.Scheme)
	}
}
