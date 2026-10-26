//go:build cgo

package dbcopy

import (
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2duckdb"
)

func newDuckDBDatabase(path string) (dal.DB, error) { return dalgo2duckdb.NewDatabase(path) }

// OpenReadOnlyDuckDB opens a native DuckDB read-only query session.
func OpenReadOnlyDuckDB(path string) (ReadOnlyDuckDB, error) {
	return dalgo2duckdb.NewReadOnlyDatabase(path)
}
