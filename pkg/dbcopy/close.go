package dbcopy

import (
	"io"
	"sync"

	"github.com/dal-go/dalgo/dal"
)

// CloseSource releases a source handle that the caller opened and owns, when
// the adapter exposes Close. It goes through dal.As rather than a plain
// type assertion because DALgo's NewDB wraps an adapter's backend in a sealed
// DB value: Close is on the backend, so db.(io.Closer) misses it (the CouchDB
// and PostgREST adapters return such a handle, and their transports would
// otherwise never be released).
//
// "Owns" is what keeps this safe for every provider: a PostgreSQL handle is the
// process's cached one and its Close is a deliberate no-op (sharedPostgres),
// while a Firestore handle has no Close at all. Only a fresh per-open adapter
// (SQLite, CouchDB, MongoDB, PostgREST) actually releases a transport here.
func CloseSource(db dal.DB) {
	if db == nil {
		return
	}
	if closer, ok := dal.As[io.Closer](db); ok {
		_ = closer.Close()
	}
}

// SourceCloser returns the cleanup of one owned source handle. It closes the
// handle at most once, because a caller can reach cleanup both from a defer and
// from an error path in the same function.
func SourceCloser(db dal.DB) func() {
	var once sync.Once
	return func() {
		once.Do(func() { CloseSource(db) })
	}
}
