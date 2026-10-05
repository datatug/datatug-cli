package dbcopy

import (
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
)

// postgresOpener is the signature of the PostgreSQL driver's constructor
// (dalgo2postgres.NewDatabaseWithOptions).
type postgresOpener func(dsn string, schema dal.Schema, opts dalgo2sql.DbOptions, options ...dalgo2postgres.Option) (*dalgo2postgres.Database, error)

// newPostgresDatabaseWithOptions is the constructor of a PostgreSQL database that BackendRef
// opens a source through, a seam so a test reaches the open without a server. Always
// dalgo2postgres.NewDatabaseWithOptions in production. The test binary of this package replaces
// it with one that stops the run (hermetic_testmain_test.go).
var newPostgresDatabaseWithOptions postgresOpener = dalgo2postgres.NewDatabaseWithOptions

// errPostgresOpenFailed is what stands for any failure of the constructor that is not the
// adapter's own ConnectionError. The adapter returns nothing else for a connection, and what
// else a function returns (a driver's own error, quoting the URL in whatever shape it likes) is
// dropped here and never wrapped.
var errPostgresOpenFailed = errors.New("the PostgreSQL driver could not open the source")

// sharedPostgres is a PostgreSQL database that lives as long as the process: the cache hands the
// same one to every open of a source. Closing it does nothing, because callers close what they
// opened (pkg/secureread does after every read) and the next open must find the pool open; the
// process's exit closes it. The embedded database keeps every capability the adapter has.
type sharedPostgres struct{ *dalgo2postgres.Database }

// Close does nothing: see sharedPostgres.
func (*sharedPostgres) Close() error { return nil }

// handleCache keeps one opened PostgreSQL database for each resolved connection string, for the
// life of the process. The key is a hash of the connection string, so that no dump of the cache
// (a debugger, a %+v of a struct that holds it) shows a user name, a password or a host; it never
// appears in an error or a log.
type handleCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]*handleEntry
}

// handleEntry is one source's slot: its own lock serialises the opens of that source only, so a
// slow connect to one server does not hold up another.
type handleEntry struct {
	mu sync.Mutex
	db *sharedPostgres
}

// postgresHandles is the process's cache of PostgreSQL databases.
var postgresHandles = &handleCache{}

// get returns the handle for connection, opening it with open when the cache holds none. A failed
// open is not remembered: the next call opens again.
func (c *handleCache) get(connection string, open func() (*dalgo2postgres.Database, error)) (*sharedPostgres, error) {
	key := sha256.Sum256([]byte(connection))
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[[sha256.Size]byte]*handleEntry{}
	}
	entry := c.entries[key]
	if entry == nil {
		entry = &handleEntry{}
		c.entries[key] = entry
	}
	c.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.db != nil {
		return entry.db, nil
	}
	db, err := open()
	if err != nil {
		return nil, err
	}
	entry.db = &sharedPostgres{Database: db}
	return entry.db, nil
}

// openPostgres opens this postgres source through the adapter, in exact identifier mode (names are
// used as the scan stored them; nothing is folded), with no recordset declared (a record is keyed
// by the primary key the adapter reads from the catalog, and by its position when the source has
// none: there is no second key rule here) and the session defaults of postgresConnectionString.
// forWrite is true only for the target of `datatug db copy`.
//
// The preview switch is asked first, before the URL is read any further. One handle is kept for each
// connection string (see handleCache). A failure of the constructor is the adapter's own
// *ConnectionError, which says what failed by its kind and its SQLSTATE and holds nothing of the
// connection string, or errPostgresOpenFailed; BackendRef.OpenFailure turns either into a fixed
// sentence.
func (r BackendRef) openPostgres(forWrite bool) (dal.DB, error) {
	if err := CheckPostgresPreview(); err != nil {
		return nil, err
	}
	connection, err := postgresConnectionString(r.Path, forWrite)
	if err != nil {
		return nil, err
	}
	db, err := postgresHandles.get(connection, func() (*dalgo2postgres.Database, error) {
		return newPostgresDatabaseWithOptions(connection, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{},
			dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact))
	})
	if err != nil {
		var adapterErr *dalgo2postgres.ConnectionError
		if errors.As(err, &adapterErr) {
			return nil, adapterErr
		}
		return nil, errPostgresOpenFailed
	}
	return db, nil
}
