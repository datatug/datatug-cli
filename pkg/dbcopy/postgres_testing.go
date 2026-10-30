package dbcopy

import (
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
)

// SetPostgresOpenerForTest replaces the constructor of a PostgreSQL database that BackendRef opens
// a source through, for the tests of the packages that reach a PostgreSQL source through it: a
// test stands in with a fake, or a whole test binary installs one that stops the run, and no test
// dials a server. It forgets every handle the process cache holds, so that a test starts from none,
// and the restore function it returns puts the constructor back and forgets them again.
//
// NEVER call this from production code.
func SetPostgresOpenerForTest(open func(dsn string, schema dal.Schema, opts dalgo2sql.DbOptions, options ...dalgo2postgres.Option) (*dalgo2postgres.Database, error)) (restore func()) {
	previous := newPostgresDatabaseWithOptions
	newPostgresDatabaseWithOptions = open
	postgresHandles = &handleCache{}
	return func() {
		newPostgresDatabaseWithOptions = previous
		postgresHandles = &handleCache{}
	}
}
