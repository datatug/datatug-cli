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
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	"github.com/xo/dburl"
)

// supportedSchemes is the MVP-supported scheme list, used both for dispatch
// and to construct the error message for REQ:unknown-scheme-rejected.
var supportedSchemes = []string{"sqlite", "ingitdb", "postgres", "http", "https"}

// ErrPostgresNotWired is returned by BackendRef.Open for postgres:// URLs
// until a PostgreSQL DALgo driver implements the three capability interfaces
// (dbschema.SchemaReader, ddl.SchemaModifier, dal.ConcurrencyAware).
var ErrPostgresNotWired = errors.New("PostgreSQL backend not yet wired")

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
// The context is reserved for future use; today's driver constructors are
// synchronous and do not honor cancellation. That's acceptable for the MVP
// CLI verb.
func (r BackendRef) Open(ctx context.Context) (dal.DB, error) {
	switch r.Scheme {
	case "sqlite":
		db, err := dalgo2sqlite.NewDatabase(r.Path)
		if err != nil {
			return nil, fmt.Errorf("open sqlite %q: %w", r.Path, err)
		}
		return db, nil

	case "ingitdb":
		db, err := dalgo2ingitdb.NewDatabase(r.Path, validator.NewCollectionsReader())
		if err != nil {
			return nil, fmt.Errorf("open ingitdb %q: %w", r.Path, err)
		}
		return db, nil

	case "postgres":
		return nil, ErrPostgresNotWired

	case "http", "https":
		db, err := httpsource.Open(ctx, r.Path)
		if err != nil {
			return nil, fmt.Errorf("open %s source %q: %w", r.Scheme, r.Path, err)
		}
		return db, nil

	default:
		// Defense in depth — Parse should have rejected this already.
		return nil, fmt.Errorf("unsupported scheme %q (this is a bug; Parse should have caught it)", r.Scheme)
	}
}
