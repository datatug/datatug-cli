package dbcopy

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	dalrecord "github.com/dal-go/record"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLServerSources(t *testing.T) {
	ref, err := Parse("sqlserver://sa:" + serverSecret + "@db.example.com:1434/sales?encrypt=true&TrustServerCertificate=true")
	require.NoError(t, err)
	assert.Equal(t, "sqlserver", ref.Scheme)
	target, err := ParseServerTarget("sqlserver", ref.Path)
	require.NoError(t, err)
	assert.Equal(t, ServerTarget{"db.example.com", 1434, "sa", "sales"}, target)
	assert.Equal(t, "SQL Server", ServerTitle("sqlserver"))
	assert.True(t, IsServerScheme("sqlserver"))
	assert.EqualError(t, CheckServerPreview("sqlserver"), "SQL Server sources are a preview and are switched off: set DATATUG_PREVIEW_SQLSERVER=1 to use them")
	_, err = Parse("sqlserver://sa:" + serverSecret + "@db.example.com/sales?app+name=x")
	assert.ErrorContains(t, err, "accepts only connection timeout, encrypt, trustservercertificate")

	parsed := mustParseServer(t, sqlServerEngine, ref.Path)
	assert.Equal(t, "sqlserver://sa:"+serverSecret+"@db.example.com:1434?TrustServerCertificate=true&connection+timeout=10&database=sales&encrypt=true", sqlServerURL(parsed))
	parsed = mustParseServer(t, sqlServerEngine, "sqlserver://sa:"+serverSecret+"@db.example.com/sales?connection+timeout=30")
	assert.Equal(t, "sqlserver://sa:"+serverSecret+"@db.example.com:1433?connection+timeout=30&database=sales", sqlServerURL(parsed))

	dsns := standInForServers(t)
	t.Setenv(SQLServerPreviewEnv, "1")
	_, err = ref.Open(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{sqlServerURL(mustParseServer(t, sqlServerEngine, ref.Path))}, *dsns)
}

func mustParseServer(t *testing.T, engine serverEngine, raw string) serverURL {
	t.Helper()
	parsed, err := parseServerURL(engine, raw)
	require.NoError(t, err)
	return parsed
}

// recordingTxDB is a database whose read-only transaction hands over rows, and says whether it ran.
type recordingTxDB struct {
	fakeServerDB
	rows       []dalrecord.Record
	readOnlyTx bool
	txErr      error
	queryErr   error
}

func (r *recordingTxDB) RunReadonlyTransaction(ctx context.Context, worker dal.ROTxWorker, _ ...dal.TransactionOption) error {
	r.readOnlyTx = true
	if r.txErr != nil {
		return r.txErr
	}
	return worker(ctx, recordingTx{db: r})
}

// recordingTx is the read transaction of recordingTxDB: its records reads only.
type recordingTx struct {
	dal.ReadTransaction
	db *recordingTxDB
}

func (tx recordingTx) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	if tx.db.queryErr != nil {
		return nil, tx.db.queryErr
	}
	return dal.NewRecordsReader(tx.db.rows), nil
}

// An Oracle records read runs in a read-only transaction and its rows come back from memory.
func TestOracleReadsRunInAReadOnlyTransaction(t *testing.T) {
	inner := &recordingTxDB{rows: []dalrecord.Record{dalrecord.NewRecordWithData(dalrecord.NewKeyWithID("t", 1), map[string]any{"id": 1})}}
	reads := oracleReads{inner}
	reader, err := reads.ExecuteQueryToRecordsReader(context.Background(), dal.NewTextQuery("SELECT 1 FROM DUAL", nil))
	require.NoError(t, err)
	records, err := dal.ReadAllToRecords(context.Background(), reader)
	require.NoError(t, err)
	assert.Len(t, records, 1)
	assert.True(t, inner.readOnlyTx, "the read ran in RunReadonlyTransaction")

	inner.queryErr = errors.New("ORA-00942: table or view does not exist")
	_, err = reads.ExecuteQueryToRecordsReader(context.Background(), dal.NewTextQuery("SELECT * FROM nope", nil))
	assert.EqualError(t, err, "ORA-00942: table or view does not exist")

	inner.queryErr = nil
	inner.txErr = errors.New("ORA-01456")
	_, err = reads.ExecuteQueryToRecordsReader(context.Background(), dal.NewTextQuery("SELECT 1 FROM DUAL", nil))
	assert.EqualError(t, err, "ORA-01456")
}

// An Oracle source is opened through oracleReads, and a failure of the open is OpenFailure's.
func TestOpenOracleWrapsTheAdapter(t *testing.T) {
	t.Setenv(OraclePreviewEnv, "1")
	ref, err := Parse("oracle://scott:" + serverSecret + "@db.example.com/FREEPDB1")
	require.NoError(t, err)
	t.Cleanup(SetServerOpenersForTest(ServerOpeners{Oracle: func(context.Context, string) (SchemaScanDB, error) { return &fakeServerDB{}, nil }}))
	db, err := ref.Open(context.Background())
	require.NoError(t, err)
	assert.IsType(t, oracleReads{}, db)

	t.Cleanup(SetServerOpenersForTest(ServerOpeners{Oracle: func(context.Context, string) (SchemaScanDB, error) {
		return nil, errors.New("dial " + serverSecret)
	}}))
	_, err = ref.Open(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), serverSecret)
}

func TestAsSchemaScanDB(t *testing.T) {
	db, err := asSchemaScanDB(&fakeServerDB{}, nil)
	require.NoError(t, err)
	assert.NotNil(t, db)
	db, err = asSchemaScanDB((*fakeServerDB)(nil), errors.New("refused"))
	assert.EqualError(t, err, "refused")
	assert.Nil(t, db, "no typed nil behind the interface")
}
