package api

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A PostgreSQL scan counts no records, through any reader: no project file holds a count, and a
// count is a full read of every table of somebody's database, with no timeout. The server it reads
// runs COUNT(*) natively (dalgo2sql declares it for the postgres dialect), and the reader of
// dalgo2postgres lists the views of a server with its tables and cannot tell the two apart (it is
// no dalgoschema.ViewLister); a reader that can tell them is not counted through either.
func TestScanDbCatalog_PostgresRunsNoCount(t *testing.T) {
	for name, db := range map[string]interface {
		dbcopy.SchemaScanDB
		countsSent() int32
	}{
		"a reader that cannot tell its views": &nativeCountScanDB{fakeScanDB: &fakeScanDB{}},
		"a reader that can tell its views":    &viewTellingCountScanDB{countingScanDB{fakeScanDB: &fakeScanDB{}, rows: 7, views: []string{"Order"}}},
	} {
		t.Run(name, func(t *testing.T) {
			stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) { return db, nil })
			logged := captureLog(t)

			catalog, err := scanDbCatalog(shopServer(), newShopParams(t))

			require.NoError(t, err)
			require.Len(t, catalog.Schemas, 1)
			for _, relation := range append(append([]*datatug.CollectionInfo{}, catalog.Schemas[0].Tables...), catalog.Schemas[0].Views...) {
				assert.Nil(t, relation.RecordsCount, relation.Name())
			}
			assert.NotEmpty(t, catalog.Schemas[0].Tables, "the tables are read")
			assert.Zero(t, db.countsSent(), "no COUNT(*) is sent to the server")
			assert.NotContains(t, logged.String(), "records count")
		})
	}
}

// viewTellingCountScanDB is a countingScanDB that says how many counts it was asked for.
type viewTellingCountScanDB struct{ countingScanDB }

func (c *viewTellingCountScanDB) countsSent() int32 { return c.counts.Load() }

func (c *nativeCountScanDB) countsSent() int32 { return c.counts.Load() }

// nativeCountScanDB is a fakeScanDB whose server runs COUNT(*) natively, as dalgo2sql declares for
// the postgres dialect, and that is no ViewLister, as the reader of dalgo2postgres is not. It counts
// the counts it is asked for.
type nativeCountScanDB struct {
	*fakeScanDB
	counts atomic.Int32
}

func (*nativeCountScanDB) QueryCapabilities() dal.QueryCapabilities {
	return dal.QueryCapabilities{Aggregate: dal.AggregateCapabilities{Count: true}}
}

func (c *nativeCountScanDB) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	c.counts.Add(1)
	return &oneCountReader{n: 7}, nil
}

func (*nativeCountScanDB) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, dal.ErrNotSupported
}
