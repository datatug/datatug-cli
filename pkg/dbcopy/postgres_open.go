package dbcopy

import (
	"context"
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

// handleEntry is one source's slot: its opened handle, and the open that is in flight when there is
// no handle yet.
type handleEntry struct {
	db      *sharedPostgres
	attempt *openAttempt
}

// openAttempt is one open of a source, run once however many callers want the source while it runs:
// each of them waits for the same outcome, success or failure, so that a burst of callers of an
// unreachable source dials it once and not once after another, each for as long as the connect
// timeout. The attempt ends when the constructor returns, which no caller's context can hurry (the
// constructor takes none; the connect timeout of the session bounds it): a caller whose context ends
// stops waiting, and the attempt goes on and keeps the handle it opens for the next caller.
type openAttempt struct {
	done chan struct{}
	db   *sharedPostgres
	err  error
	// panicked is what the constructor panicked with (the stop-the-run opener of a test binary
	// does), re-raised in every caller that waited: a panic is not an outcome to hand around, and
	// it must not end only the goroutine that happened to run the attempt.
	panicked any
}

// postgresHandles is the process's cache of PostgreSQL databases.
var postgresHandles = &handleCache{}

// get returns the handle for connection, opening it with open when the cache holds none and no
// other caller is opening it, and waiting for that caller's attempt when one is. It returns when the
// outcome is known, or when ctx ends. A failed attempt is not remembered: the next call, after the
// attempt ended, opens again.
func (c *handleCache) get(ctx context.Context, connection string, open func() (*dalgo2postgres.Database, error)) (*sharedPostgres, error) {
	db, attempt, err := c.join(ctx, sha256.Sum256([]byte(connection)), open)
	if attempt == nil {
		return db, err
	}
	return attempt.await(ctx)
}

// join returns the handle the cache holds for key, or the attempt that is opening it, starting one
// with open when there is none, or the error of a context that has ended before one was started (a
// request that is gone does not dial).
func (c *handleCache) join(ctx context.Context, key [sha256.Size]byte, open func() (*dalgo2postgres.Database, error)) (*sharedPostgres, *openAttempt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[[sha256.Size]byte]*handleEntry{}
	}
	entry := c.entries[key]
	if entry == nil {
		entry = &handleEntry{}
		c.entries[key] = entry
	}
	if entry.db != nil {
		return entry.db, nil, nil
	}
	if entry.attempt == nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		entry.attempt = &openAttempt{done: make(chan struct{})}
		go c.run(entry, entry.attempt, open)
	}
	return nil, entry.attempt, nil
}

// run is the attempt: it calls open, keeps the handle when there is one, and lets the next call
// start another attempt when there is not.
//
// Its bookkeeping is a defer, so that every way the goroutine can end ends the attempt: open returns,
// open panics (recovered here and handed to the callers that wait), or open ends the goroutine through
// runtime.Goexit, which neither returns nor panics (a failing test does it with FailNow when it builds
// its stand-in inside the opener). A Goexit is a failure of the open, the fixed one: without that, the
// attempt would stay in its entry for good and a caller whose context never ends would wait for it
// for ever.
func (c *handleCache) run(entry *handleEntry, attempt *openAttempt, open func() (*dalgo2postgres.Database, error)) {
	var db *dalgo2postgres.Database
	var err error
	returned := false
	defer func() {
		attempt.panicked = recover()
		if !returned && attempt.panicked == nil {
			err = errPostgresOpenFailed
		}
		attempt.err = err
		c.mu.Lock()
		if err == nil && attempt.panicked == nil {
			attempt.db = &sharedPostgres{Database: db}
			entry.db = attempt.db
		}
		entry.attempt = nil
		c.mu.Unlock()
		close(attempt.done)
	}()
	db, err = open()
	returned = true
}

// await waits for the outcome of the attempt, or for ctx to end.
func (a *openAttempt) await(ctx context.Context) (*sharedPostgres, error) {
	select {
	case <-a.done:
		if a.panicked != nil {
			panic(a.panicked)
		}
		return a.db, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// openPostgres opens this postgres source through the adapter, in exact identifier mode (names are
// used as the scan stored them; nothing is folded), with no recordset declared (a record is keyed
// by the primary key the adapter reads from the catalog, and by its position when the source has
// none: there is no second key rule here) and the session defaults of postgresConnectionString.
// forWrite is true only for the target of `datatug db copy`.
//
// The preview switch is asked first, before the URL is read any further. One handle is kept for each
// connection string (see handleCache), and the callers of a source that is being opened share the
// outcome of that one open. A failure of the constructor is the adapter's own *ConnectionError,
// which says what failed by its kind and its SQLSTATE and holds nothing of the connection string;
// or a context error, bare, when the attempt ended with the context of the caller or the connect
// timeout; or errPostgresOpenFailed. BackendRef.OpenFailure turns each into a fixed sentence.
func (r BackendRef) openPostgres(ctx context.Context, forWrite bool) (dal.DB, error) {
	if err := CheckPostgresPreview(); err != nil {
		return nil, err
	}
	connection, err := postgresConnectionString(r.Path, forWrite)
	if err != nil {
		return nil, err
	}
	db, err := postgresHandles.get(ctx, connection, func() (*dalgo2postgres.Database, error) {
		return newPostgresDatabaseWithOptions(connection, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{},
			dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact))
	})
	if err != nil {
		var adapterErr *dalgo2postgres.ConnectionError
		switch {
		case errors.As(err, &adapterErr):
			return nil, adapterErr
		case errors.Is(err, context.DeadlineExceeded):
			return nil, context.DeadlineExceeded
		case errors.Is(err, context.Canceled):
			return nil, context.Canceled
		}
		return nil, errPostgresOpenFailed
	}
	return db, nil
}
