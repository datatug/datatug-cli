//go:build !cgo

package dbcopy

import (
	"github.com/dal-go/dalgo/dal"
)

func newDuckDBDatabase(string) (dal.DB, error) { return nil, errDuckDBRequiresCGO }

// OpenReadOnlyDuckDB reports the missing capability in custom CGO-off builds.
func OpenReadOnlyDuckDB(string) (ReadOnlyDuckDB, error) { return nil, errDuckDBRequiresCGO }
