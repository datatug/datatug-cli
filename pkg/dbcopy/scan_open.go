package dbcopy

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2postgres"
)

// PostgresDefaultSchema is the PostgreSQL schema a schema scan reads: the one
// the DALgo reader inspects when it is not told another.
const PostgresDefaultSchema = dalgo2postgres.DefaultSchema

// SchemaScanDB is the handle a schema scan reads through: the DALgo database,
// its schema reader, and a Close that releases the connection pool.
type SchemaScanDB interface {
	dal.DB
	dbschema.SchemaReader
	io.Closer
}

// newPostgresDatabase is the PostgreSQL driver's constructor, a seam so tests
// reach OpenSchemaScan without a server. Always dalgo2postgres.NewDatabase in
// production.
var newPostgresDatabase = dalgo2postgres.NewDatabase

// ParseWithEnv is Parse over an injected environment lookup, so a caller's tests
// never touch the process environment. Parse is ParseWithEnv with os.LookupEnv.
func ParseWithEnv(rawURL string, lookupEnv func(string) (string, bool)) (BackendRef, error) {
	return parseSource(rawURL, lookupEnv)
}

// OpenSchemaScan opens a postgres source for reading its schema and nothing
// else. It is not Open: Open keeps answering that PostgreSQL is not available
// for queries, and the handle returned here is for the scan only. DataTug opens
// it with exact identifiers, so a table is looked up under the name PostgreSQL
// reports (the driver's default folds every name to lower case).
//
// The URL is checked before the driver sees it (ParsePostgresTarget): a URL that
// net/url and pgx read differently from how it was written, such as a password
// with an unescaped "/" after digits, is refused, because the driver would
// connect to the wrong host and print the rest of the password as the database.
//
// The driver quotes the URL, password included, in its open and ping errors, so
// every error leaves here with every PostgreSQL URL in it, as given or as pgx
// writes it again in a parse error, replaced by the name the source was given
// (the "env:NAME" it was opened from, never the URL: it carries every connection
// option, not only the password) and with every secret the URL holds scrubbed,
// exactly as Open's errors are. What the driver says about the target in words of
// its own, such as pgx's "user=alice database=shop" after a failed connection, or
// the value of an option it cannot read ("parsing \"soon\""), is not a secret and
// stays. The context is reserved for future use, as in Open.
func (r BackendRef) OpenSchemaScan(_ context.Context) (SchemaScanDB, error) {
	if r.Scheme != "postgres" {
		return nil, fmt.Errorf("a schema scan through DALgo is available for postgres sources only, not %s", r.Scheme)
	}
	if _, err := ParsePostgresTarget(r.Path); err != nil {
		return nil, fmt.Errorf("source %s: %w", r, err)
	}
	db, err := newPostgresDatabase(r.Path, dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact))
	if err != nil {
		return nil, RedactErrorWithSecrets(namedSourceError{err: err, url: r.Path, name: r.String()}, r.Path)
	}
	return db, nil
}

// namedSourceError is err with the URL of a source replaced, wherever the driver
// wrote it as text or as a Go string literal, by the name the source was given.
type namedSourceError struct {
	err  error
	url  string
	name string
}

func (e namedSourceError) Error() string {
	text := e.err.Error()
	if e.url != "" { // with no URL there is nothing to find: replacing "" would break the text up
		text = strings.ReplaceAll(text, strconv.Quote(e.url), strconv.Quote(e.name))
		text = strings.ReplaceAll(text, e.url, e.name)
	}
	// A driver may write the URL again in a spelling of its own (pgx's parse
	// error prints the URL as net/url renders it, with the password masked), which
	// is not the URL as it was given. Any PostgreSQL URL in an error of this open
	// is this source in some spelling.
	return postgresURLInText.ReplaceAllLiteralString(text, e.name)
}

// postgresURLInText finds a postgres:// or postgresql:// URL anywhere in a text:
// up to the next space, quote or backtick, and not counting the punctuation that
// follows it in a sentence. A URL holds no space (net/url escapes it).
var postgresURLInText = regexp.MustCompile("(?i)postgres(?:ql)?://[^\\s\"'`]*[^\\s\"'`.,:;)]")

func (e namedSourceError) Unwrap() error { return e.err }
