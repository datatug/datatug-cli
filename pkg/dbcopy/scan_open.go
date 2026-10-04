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
// else. It is not Open: Open keeps answering that PostgreSQL is not available
// for queries, and the handle returned here is for the scan only. DataTug opens
// it with exact identifiers, so a table is looked up under the name PostgreSQL
// reports (the driver's default folds every name to lower case).
//
// The driver quotes the URL, password included, in its open and ping errors, so
// every error leaves here scrubbed with the real URL, exactly as Open's are.
// The context is reserved for future use, as in Open.
func (r BackendRef) OpenSchemaScan(_ context.Context) (SchemaScanDB, error) {
	if r.Scheme != "postgres" {
		return nil, fmt.Errorf("a schema scan through DALgo is available for postgres sources only, not %s", r.Scheme)
	}
	db, err := newPostgresDatabase(r.Path, dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact))
	if err != nil {
		return nil, RedactErrorWithSecrets(err, r.Path)
	}
	return db, nil
}
