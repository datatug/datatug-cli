package api

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reader of dalgo2postgres lists the views of a server with its tables and cannot tell the two
// apart (it is no dalgoschema.ViewLister), and the server it opens runs COUNT(*) natively. A scan that
// counted for it would run a count on every table and every view of somebody's database, and no
// count is stored anywhere: a scan counts only through a reader that can tell its views, so that
// a view is never counted, and through the reader that ships it counts nothing.
func TestScanDbCatalog_PostgresRunsNoCountThroughAReaderThatCannotTellItsViews(t *testing.T) {
	db := &nativeCountScanDB{fakeScanDB: &fakeScanDB{}}
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) { return db, nil })
	logged := captureLog(t)

	catalog, err := scanDbCatalog(shopServer(), newShopParams(t))

	require.NoError(t, err)
	require.Len(t, catalog.Schemas, 1)
	require.Len(t, catalog.Schemas[0].Tables, 2, "both relations are read, each as a table: the reader cannot tell a view")
	for _, table := range catalog.Schemas[0].Tables {
		assert.Nil(t, table.RecordsCount, table.Name())
	}
	assert.Zero(t, db.counts.Load(), "no COUNT(*) is sent to a server whose views cannot be told from its tables")
	assert.NotContains(t, logged.String(), "records count")
}

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
