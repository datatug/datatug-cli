package dbcopy

import (
	"context"
	"fmt"
	"io"

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
// else. It is not Open: Open is behind the preview switch (PostgresPreviewEnv)
// and keeps one handle for the process, and the handle returned here is for the
// scan only, which is not behind the switch (it reads the catalog). DataTug opens
// it with exact identifiers, so a table is looked up under the name PostgreSQL
// reports (the driver's default folds every name to lower case).
//
// The URL is checked before the driver sees it (ParsePostgresTarget): a URL that
// net/url and pgx read differently from how it was written, such as a password
// with an unescaped "/" after digits, is refused, because the driver would
// connect to the wrong host and print the rest of the password as the database.
//
// The driver quotes the URL, password included, in its open and ping errors, so
// its message is never shown: the error is OpenFailure's, which for a PostgreSQL
// source is the adapter's own fixed sentence for the failure (told by its kind and
// SQLSTATE) and where the connection string is read from (the variable of the
// "env:NAME" it was opened from, never a part of the URL: it carries every
// connection option, not only the password, and its host and port). The
// driver's own error stays reachable through errors.Is and errors.As, exactly as
// it does for Open. The context is reserved for future use: this open is
// synchronous and does not honor cancellation (Open's does, for a postgres source).
func (r BackendRef) OpenSchemaScan(_ context.Context) (SchemaScanDB, error) {
	if r.Scheme != "postgres" {
		return nil, fmt.Errorf("a schema scan through DALgo is available for postgres sources only, not %s", r.Scheme)
	}
	if _, err := ParsePostgresTarget(r.Path); err != nil {
		return nil, fmt.Errorf("source %s: %w", r, err)
	}
	db, err := newPostgresDatabase(r.Path, dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact))
	if err != nil {
		return nil, r.OpenFailure(err)
	}
	return db, nil
}
