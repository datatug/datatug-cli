package dbcopy

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dal-go/dalgo/dal"
)

// ReadOnlyDuckDB is the native SQL query session used by secureread. All normal
// release artifacts include this surface; custom CGO_ENABLED=0 builds return
// an explicit capability error when opening a DuckDB source.
type ReadOnlyDuckDB interface {
	dal.DB
	Close() error
	SQLDB() *sql.DB
	ValidateReadQuery(context.Context, string) error
}

var errDuckDBRequiresCGO = errors.New("DuckDB requires a cgo-enabled build; use an official DataTug release or build with CGO_ENABLED=1")
