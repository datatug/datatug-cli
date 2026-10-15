package dbcompare

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"math"
	"math/big"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/recordops"
	"github.com/dal-go/record"
	"github.com/stretchr/testify/require"
)

type mockAdapterDB struct {
	dal.DB
	adapter dal.Adapter
}

func (m mockAdapterDB) Adapter() dal.Adapter {
	return m.adapter
}

type mockSourceViewsDB struct {
	dal.DB
	views []dbschema.SourceViewDef
	err   error
}

func (m mockSourceViewsDB) ListSourceViews(ctx context.Context) ([]dbschema.SourceViewDef, error) {
	return m.views, m.err
}

type mockPlainDB struct {
	dal.DB
}

type mockSourceRowsDB struct {
	dal.DB
	cursor dbschema.SourceRowCursor
	err    error
}

func (m mockSourceRowsDB) OpenSourceRows(ctx context.Context, ref *dal.CollectionRef) (dbschema.SourceRowCursor, error) {
	return m.cursor, m.err
}

type mockCursor struct {
	rows     []dbschema.SourceRow
	index    int
	nextErr  error
	closeErr error
}

func (m *mockCursor) Next() (dbschema.SourceRow, error) {
	if m.nextErr != nil {
		return dbschema.SourceRow{}, m.nextErr
	}
	if m.index >= len(m.rows) {
		return dbschema.SourceRow{}, io.EOF
	}
	row := m.rows[m.index]
	m.index++
	return row, nil
}

func (m *mockCursor) Close() error {
	return m.closeErr
}

type mockRecordsReader struct {
	dal.RecordsReader
	nextErr  error
	closeErr error
}

func (m *mockRecordsReader) Cursor() (string, error) {
	if m.RecordsReader != nil {
		return m.RecordsReader.Cursor()
	}
	return "", nil
}

func (m *mockRecordsReader) Next() (record.Record, error) {
	if m.nextErr != nil {
		return nil, m.nextErr
	}
	if m.RecordsReader != nil {
		return m.RecordsReader.Next()
	}
	return nil, io.EOF
}

func (m *mockRecordsReader) Close() error {
	if m.closeErr != nil {
		return m.closeErr
	}
	if m.RecordsReader != nil {
		return m.RecordsReader.Close()
	}
	return nil
}

// Not parallel: mutates package seams.
func TestCloseAndNewRowStoreSeams(t *testing.T) {
	// rowCount nil check
	require.Equal(t, int64(0), rowCount(nil))

	// Close on nil
	var nilStore *rowStore
	require.NoError(t, nilStore.Close())

	// Close with db == nil and remove error
	origRemoveAll := osRemoveAll
	defer func() { osRemoveAll = origRemoveAll }()
	osRemoveAll = func(path string) error { return errors.New("cannot remove") }
	dummyStore := &rowStore{tempDir: "/not/existing"}
	err := dummyStore.Close()
	require.ErrorContains(t, err, "comparison temporary row files could not be removed")

	// Close with db close error
	origDBClose := dbClose
	defer func() { dbClose = origDBClose }()
	osRemoveAll = origRemoveAll
	dbClose = func(db *sql.DB) error { return errors.New("db close err") }
	realStore, err := newRowStore(false)
	require.NoError(t, err)
	err = realStore.Close()
	require.ErrorContains(t, err, "db close err")
	dbClose = origDBClose

	// newRowStore errors:
	// 1. MkdirTemp error
	origMkdirTemp := osMkdirTemp
	defer func() { osMkdirTemp = origMkdirTemp }()
	osMkdirTemp = func(dir, pattern string) (string, error) { return "", errors.New("mkdir fail") }
	_, err = newRowStore(false)
	require.ErrorContains(t, err, "could not create comparison row staging")
	osMkdirTemp = origMkdirTemp

	// 2. OpenFile error
	origOpenFile := osOpenFile
	defer func() { osOpenFile = origOpenFile }()
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) { return nil, errors.New("open fail") }
	_, err = newRowStore(false)
	require.ErrorContains(t, err, "could not create comparison row spool")
	osOpenFile = origOpenFile

	// 3. fileClose error
	origFileClose := fileClose
	defer func() { fileClose = origFileClose }()
	fileClose = func(f *os.File) error {
		_ = f.Close()
		return errors.New("file close fail")
	}
	_, err = newRowStore(false)
	require.ErrorContains(t, err, "could not initialize comparison row spool")
	fileClose = origFileClose

	// 4. sqlOpen error
	origSQLOpen := sqlOpen
	defer func() { sqlOpen = origSQLOpen }()
	sqlOpen = func(driverName, dataSourceName string) (*sql.DB, error) { return nil, errors.New("sql open fail") }
	_, err = newRowStore(false)
	require.ErrorContains(t, err, "could not open comparison row spool")
	sqlOpen = origSQLOpen

	// 5. dbExec error
	origDBExec := dbExec
	defer func() { dbExec = origDBExec }()
	dbExec = func(db *sql.DB, query string) (sql.Result, error) { return nil, errors.New("pragma fail") }
	_, err = newRowStore(false)
	require.ErrorContains(t, err, "could not initialize comparison row spool")
	dbExec = origDBExec
}

// Not parallel: mutates package seams.
func TestCompareValidationAndPostgresSchema(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY)`)
	right := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY)`)

	// left or right nil
	_, err := Compare(context.Background(), "left", nil, "right", right, Options{})
	require.ErrorContains(t, err, "compare requires two open DALgo databases")
	_, err = Compare(context.Background(), "left", left, "right", nil, Options{})
	require.ErrorContains(t, err, "compare requires two open DALgo databases")

	// negative detail limit
	_, err = Compare(context.Background(), "left", left, "right", right, Options{DetailLimit: -1})
	require.ErrorContains(t, err, "detail limit cannot be negative")

	// dalgo2postgres left missing LeftSchema
	pgAdapter := dal.NewAdapter("dalgo2postgres", "1.0")
	leftPG := mockAdapterDB{DB: left, adapter: pgAdapter}
	_, err = Compare(context.Background(), "left", leftPG, "right", right, Options{})
	require.ErrorContains(t, err, "left PostgreSQL schema must be explicit")

	// dalgo2postgres right missing RightSchema
	rightPG := mockAdapterDB{DB: right, adapter: pgAdapter}
	_, err = Compare(context.Background(), "left", left, "right", rightPG, Options{})
	require.ErrorContains(t, err, "right PostgreSQL schema must be explicit")

	// unknown key mapping relation name
	_, err = Compare(context.Background(), "left", left, "right", right, Options{
		KeyMaps: map[string][]string{"unknown_relation": {"id"}},
	})
	require.ErrorContains(t, err, "comparison key mapping names unknown relation")

	// incompatible shared columns
	leftIncompat := openTestDatabase(t, `CREATE TABLE diff (val TEXT)`)
	rightIncompat := openTestDatabase(t, `CREATE TABLE diff (val INTEGER)`)
	_, err = Compare(context.Background(), "left", leftIncompat, "right", rightIncompat, Options{})
	require.ErrorContains(t, err, "shared columns have incompatible DALgo types")
}

// Not parallel: mutates package seams.
func TestInspectBranches(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t, `CREATE TABLE t1 (id INTEGER PRIMARY KEY, v TEXT)`)

	origListCols := dbschemaListCollections
	defer func() { dbschemaListCollections = origListCols }()
	origDescribe := dbschemaDescribeCollection
	defer func() { dbschemaDescribeCollection = origDescribe }()
	origListIdx := dbschemaListIndexes
	defer func() { dbschemaListIndexes = origListIdx }()

	defaultDescribe := func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{
			Name: ref.Name(),
			Fields: []dbschema.FieldDef{
				{Name: "id", Type: dbschema.Int},
			},
		}, nil
	}
	dbschemaDescribeCollection = defaultDescribe
	dbschemaListIndexes = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
		return nil, nil
	}

	// 1. dbschemaListCollections error
	dbschemaListCollections = func(ctx context.Context, db dal.DB, parent *record.Key) ([]dal.CollectionRef, error) {
		return nil, errors.New("list fail")
	}
	_, err := inspect(ctx, db, "")
	require.ErrorContains(t, err, "list collections: list fail")
	dbschemaListCollections = func(ctx context.Context, db dal.DB, parent *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("t1", "")}, nil
	}

	// 2. SourceViewReader ListSourceViews error
	viewDBErr := mockSourceViewsDB{DB: db, err: errors.New("view err")}
	_, err = inspect(ctx, viewDBErr, "")
	require.ErrorContains(t, err, "list views: view err")

	// 3. SourceViewReader empty view name or duplicate
	viewDBEmpty := mockSourceViewsDB{DB: db, views: []dbschema.SourceViewDef{{Name: ""}}}
	_, err = inspect(ctx, viewDBEmpty, "")
	require.ErrorContains(t, err, "source returned an empty or duplicate view name")

	viewDBDup := mockSourceViewsDB{DB: db, views: []dbschema.SourceViewDef{{Name: "v1"}, {Name: "v1"}}}
	_, err = inspect(ctx, viewDBDup, "")
	require.ErrorContains(t, err, "source returned an empty or duplicate view name")

	// 4. SourceViewReader valid views, matching a ref name (viewNames[ref.Name()])
	viewDBValid := mockSourceViewsDB{DB: db, views: []dbschema.SourceViewDef{{Name: "t1"}}}
	inv, err := inspect(ctx, viewDBValid, "")
	require.NoError(t, err)
	require.Contains(t, inv.views, "t1")
	require.Empty(t, inv.collections)

	// 5. Adapter is nil (fallback to "database")
	origAdapterDB := mockAdapterDB{DB: db, adapter: nil}
	inv, err = inspect(ctx, origAdapterDB, "")
	require.NoError(t, err)
	require.Contains(t, inv.warnings[0], "by the database adapter")

	// 6. Ref with empty name
	dbschemaListCollections = func(ctx context.Context, db dal.DB, parent *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{{}}, nil
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "source returned an empty collection name")

	// 7. Duplicate collection name
	dbschemaListCollections = func(ctx context.Context, db dal.DB, parent *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("dup", ""), dal.NewRootCollectionRef("dup", "")}, nil
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "source returned duplicate collection")

	dbschemaListCollections = func(ctx context.Context, db dal.DB, parent *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("t1", "")}, nil
	}

	// 8. dbschemaDescribeCollection error
	dbschemaDescribeCollection = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return nil, errors.New("desc fail")
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "describe \"t1\": desc fail")

	// 9. inconsistent metadata: definition == nil or definition.Name != ref.Name()
	dbschemaDescribeCollection = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return nil, nil
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "inconsistent schema metadata")

	dbschemaDescribeCollection = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{Name: "different"}, nil
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "inconsistent schema metadata")

	// 10. Empty field name or duplicate field name
	dbschemaDescribeCollection = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{
			Name: "t1",
			Fields: []dbschema.FieldDef{
				{Name: ""},
			},
		}, nil
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "returned an empty or duplicate field name")

	dbschemaDescribeCollection = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{
			Name: "t1",
			Fields: []dbschema.FieldDef{
				{Name: "col1"},
				{Name: "col1"},
			},
		}, nil
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "returned an empty or duplicate field name")
	dbschemaDescribeCollection = defaultDescribe

	// 11. Qualified postgres schema
	pgDB := mockAdapterDB{DB: db, adapter: dal.NewAdapter("dalgo2postgres", "1.0")}
	inv, err = inspect(ctx, pgDB, "custom_schema")
	require.NoError(t, err)
	require.Equal(t, "custom_schema.t1", inv.collections["t1"].ref.Path())

	// 12. dbschemaListIndexes error (unsupported vs other error)
	dbschemaListIndexes = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
		return nil, errors.New("list idx fail")
	}
	_, err = inspect(ctx, db, "")
	require.ErrorContains(t, err, "list indexes for \"t1\": list idx fail")

	dbschemaListIndexes = func(ctx context.Context, db dal.DB, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
		return nil, dal.ErrNotSupported
	}
	inv, err = inspect(ctx, db, "")
	require.NoError(t, err)
	require.False(t, inv.collections["t1"].indexesKnown)
	require.Contains(t, inv.warnings[0], "index metadata for t1 is unsupported")
}

// Not parallel: mutates package seams.
func TestReadRowsBranches(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t, `CREATE TABLE test_tab (id INTEGER PRIMARY KEY, note TEXT)`, `INSERT INTO test_tab VALUES (1, 'hello')`)
	ref := dal.NewRootCollectionRef("test_tab", "")
	ownDef := dbschema.CollectionDef{
		Name: "test_tab",
		Fields: []dbschema.FieldDef{
			{Name: "id", Type: dbschema.Int},
			{Name: "note", Type: dbschema.String},
		},
	}
	otherDef := ownDef

	// Incompatible source types between own and other
	incompatOther := dbschema.CollectionDef{
		Name: "test_tab",
		Fields: []dbschema.FieldDef{
			{Name: "id", Type: dbschema.String},
		},
	}
	_, err := readRows(ctx, db, ref, ownDef, incompatOther, []string{"id"})
	require.ErrorContains(t, err, "has incompatible source types")

	// newRowStore fails
	origMkdirTemp := osMkdirTemp
	defer func() { osMkdirTemp = origMkdirTemp }()
	osMkdirTemp = func(dir, pattern string) (string, error) { return "", errors.New("mkdir fail") }
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not create comparison row staging")
	osMkdirTemp = origMkdirTemp

	// txBegin fails
	origTxBegin := txBegin
	defer func() { txBegin = origTxBegin }()
	txBegin = func(ctx context.Context, db *sql.DB) (*sql.Tx, error) { return nil, errors.New("tx fail") }
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not begin comparison row staging")
	txBegin = origTxBegin

	// txPrepare fails
	origTxPrepare := txPrepare
	defer func() { txPrepare = origTxPrepare }()
	txPrepare = func(ctx context.Context, tx *sql.Tx, query string) (*sql.Stmt, error) {
		return nil, errors.New("prepare fail")
	}
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not prepare comparison row staging")
	txPrepare = origTxPrepare

	// stmtExecContext fails
	origStmtExec := stmtExecContext
	defer func() { stmtExecContext = origStmtExec }()
	stmtExecContext = func(ctx context.Context, stmt *sql.Stmt, args ...any) (sql.Result, error) {
		return nil, errors.New("exec fail")
	}
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not stage normalized comparison row")
	stmtExecContext = origStmtExec

	// stmtClose fails
	origStmtClose := stmtClose
	defer func() { stmtClose = origStmtClose }()
	stmtClose = func(stmt *sql.Stmt) error { return errors.New("stmt close fail") }
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not finish comparison row staging")
	stmtClose = origStmtClose

	// txCommit fails
	origTxCommit := txCommit
	defer func() { txCommit = origTxCommit }()
	txCommit = func(tx *sql.Tx) error { return errors.New("commit fail") }
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not commit comparison row staging")
	txCommit = origTxCommit

	// dbExecContext (indexing) fails
	origDbExecContext := dbExecContext
	defer func() { dbExecContext = origDbExecContext }()
	dbExecContext = func(ctx context.Context, db *sql.DB, query string) (sql.Result, error) {
		return nil, errors.New("index fail")
	}
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not index comparison row staging")
	dbExecContext = origDbExecContext

	// dbExecuteQueryToRecordsReader fails
	plainDB := mockPlainDB{DB: db}
	origRecordsReader := dbExecuteQueryToRecordsReader
	defer func() { dbExecuteQueryToRecordsReader = origRecordsReader }()
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return nil, errors.New("open reader fail")
	}
	_, err = readRows(ctx, plainDB, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "open row reader: open reader fail")

	// RecordsReader Next error
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return &mockRecordsReader{nextErr: errors.New("next fail")}, nil
	}
	_, err = readRows(ctx, plainDB, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "read row: next fail")

	// RecordsReader non-map[string]any data
	recWithSlice := record.NewRecordWithoutKey([]string{"not a map"})
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return &mockRecordsReader{RecordsReader: dal.NewRecordsReader([]record.Record{recWithSlice})}, nil
	}
	_, err = readRows(ctx, plainDB, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "row data has type []string; want map[string]any")

	// RecordsReader appendRow error: missing declared column
	recMissingCol := record.NewRecordWithoutKey(map[string]any{"id": int64(1)})
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return &mockRecordsReader{RecordsReader: dal.NewRecordsReader([]record.Record{recMissingCol})}, nil
	}
	_, err = readRows(ctx, plainDB, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "row is missing declared column \"note\"")

	// RecordsReader appendRow error: normalizeValueForSource error (unsupported bool)
	defWithBadCol := dbschema.CollectionDef{
		Name: "test_tab",
		Fields: []dbschema.FieldDef{
			{Name: "id", Type: dbschema.Bool},
		},
	}
	recBadVal := record.NewRecordWithoutKey(map[string]any{"id": "invalid_bool"})
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return &mockRecordsReader{RecordsReader: dal.NewRecordsReader([]record.Record{recBadVal})}, nil
	}
	_, err = readRows(ctx, plainDB, ref, defWithBadCol, defWithBadCol, []string{"id"})
	require.ErrorContains(t, err, "unsupported boolean value")

	// RecordsReader appendRow error: comparison key column absent
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return &mockRecordsReader{RecordsReader: dal.NewRecordsReader([]record.Record{record.NewRecordWithoutKey(map[string]any{"id": int64(1)})})}, nil
	}
	defSingle := dbschema.CollectionDef{
		Name: "test_tab",
		Fields: []dbschema.FieldDef{
			{Name: "id", Type: dbschema.Int},
		},
	}
	_, err = readRows(ctx, plainDB, ref, defSingle, defSingle, []string{"absent_key"})
	require.ErrorContains(t, err, "comparison key column \"absent_key\" is absent")

	// RecordsReader appendRow error: comparison key column contains NULL
	recNullKey := record.NewRecordWithoutKey(map[string]any{"id": nil})
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return &mockRecordsReader{RecordsReader: dal.NewRecordsReader([]record.Record{recNullKey})}, nil
	}
	_, err = readRows(ctx, plainDB, ref, defSingle, defSingle, []string{"id"})
	require.ErrorContains(t, err, "comparison key column \"id\" contains NULL or is absent")

	// RecordsReader Close error
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return &mockRecordsReader{closeErr: errors.New("reader close fail")}, nil
	}
	_, err = readRows(ctx, plainDB, ref, defSingle, defSingle, []string{"id"})
	require.ErrorContains(t, err, "close row reader: reader close fail")
	dbExecuteQueryToRecordsReader = origRecordsReader

	// SourceRowsReader branches:
	// OpenSourceRows error
	rowsDBOpenErr := mockSourceRowsDB{DB: db, err: errors.New("open rows fail")}
	_, err = readRows(ctx, rowsDBOpenErr, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "open physical rows: open rows fail")

	// Next error
	rowsDBNextErr := mockSourceRowsDB{DB: db, cursor: &mockCursor{nextErr: errors.New("cursor next fail")}}
	_, err = readRows(ctx, rowsDBNextErr, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "read physical row: cursor next fail")

	// appendRow error in SourceRowsReader
	rowsDBAppendErr := mockSourceRowsDB{DB: db, cursor: &mockCursor{rows: []dbschema.SourceRow{
		{Values: map[string]any{"id": int64(1)}},
	}}}
	_, err = readRows(ctx, rowsDBAppendErr, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "row is missing declared column \"note\"")

	// cursor.Close error
	rowsDBCloseErr := mockSourceRowsDB{DB: db, cursor: &mockCursor{
		rows:     []dbschema.SourceRow{{Values: map[string]any{"id": int64(1), "note": "ok"}}},
		closeErr: errors.New("cursor close fail"),
	}}
	_, err = readRows(ctx, rowsDBCloseErr, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "close physical rows: cursor close fail")

	// absent column in ownFields (exists only in otherFields)
	ownSub := dbschema.CollectionDef{Name: "test_tab", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}}
	otherSup := dbschema.CollectionDef{Name: "test_tab", Fields: []dbschema.FieldDef{
		{Name: "id", Type: dbschema.Int},
		{Name: "extra", Type: dbschema.String},
	}}
	st, err := readRows(ctx, db, ref, ownSub, otherSup, []string{"id"})
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	require.Equal(t, int64(1), st.count)

	// jsonMarshal error on sortKey
	origJSONMarshal := jsonMarshal
	defer func() { jsonMarshal = origJSONMarshal }()
	jsonMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("marshal sortKey error")
	}
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not encode normalized comparison key")

	// jsonMarshal error on payload
	callCount := 0
	jsonMarshal = func(v any) ([]byte, error) {
		callCount++
		if callCount == 2 {
			return nil, errors.New("marshal payload error")
		}
		return json.Marshal(v)
	}
	_, err = readRows(ctx, db, ref, ownDef, otherDef, []string{"id"})
	require.ErrorContains(t, err, "could not encode normalized comparison row")
	jsonMarshal = origJSONMarshal
}

// Not parallel: mutates package seams.
func TestCompareRemainingErrors(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE only_left (id INTEGER PRIMARY KEY)`)
	right := openTestDatabase(t, `CREATE TABLE only_right (id INTEGER PRIMARY KEY)`)

	// Key mapping error for only_left
	_, err := Compare(context.Background(), "left", left, "right", right, Options{
		KeyMaps: map[string][]string{"only_left": {}},
	})
	require.ErrorContains(t, err, "comparison key mapping for \"only_left\" is empty")

	// Key mapping error for only_right
	_, err = Compare(context.Background(), "left", left, "right", right, Options{
		KeyMaps: map[string][]string{"only_right": {}},
	})
	require.ErrorContains(t, err, "comparison key mapping for \"only_right\" is empty")

	// Key mapping error for shared table
	commonLeft := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY)`)
	commonRight := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY)`)
	_, err = Compare(context.Background(), "left", commonLeft, "right", commonRight, Options{
		KeyMaps: map[string][]string{"item": {}},
	})
	require.ErrorContains(t, err, "comparison key mapping for \"item\" is empty")

	// readRows error in Compare
	origTxBegin := txBegin
	defer func() { txBegin = origTxBegin }()
	txBegin = func(ctx context.Context, db *sql.DB) (*sql.Tx, error) { return nil, errors.New("read rows error") }
	_, err = Compare(context.Background(), "left", commonLeft, "right", commonRight, Options{})
	require.ErrorContains(t, err, "read collection \"item\": could not begin comparison row staging")
	txBegin = origTxBegin

	// diffRows error in Compare
	origDiff := recordopsDiff
	defer func() { recordopsDiff = origDiff }()
	recordopsDiff = func(left recordops.RecordSeq[string], right []recordops.RecordSeq[string], opts ...recordops.Option) iter.Seq2[recordops.IDDiff[string], error] {
		return func(yield func(recordops.IDDiff[string], error) bool) {
			yield(recordops.IDDiff[string]{}, errors.New("diff error"))
		}
	}
	_, err = Compare(context.Background(), "left", commonLeft, "right", commonRight, Options{})
	require.ErrorContains(t, err, "compare rows in collection \"item\": diff error")
	recordopsDiff = origDiff

	// closeErr error in Compare
	origDBClose := dbClose
	defer func() { dbClose = origDBClose }()
	dbClose = func(db *sql.DB) error { return errors.New("spool close err") }
	_, err = Compare(context.Background(), "left", commonLeft, "right", commonRight, Options{})
	require.ErrorContains(t, err, "close comparison spool for collection \"item\": spool close err")
	dbClose = origDBClose

	// inspect left error
	origListCols := dbschemaListCollections
	defer func() { dbschemaListCollections = origListCols }()
	dbschemaListCollections = func(ctx context.Context, db dal.DB, parent *record.Key) ([]dal.CollectionRef, error) {
		return nil, errors.New("left inspect failed")
	}
	_, err = Compare(context.Background(), "left", commonLeft, "right", commonRight, Options{})
	require.ErrorContains(t, err, "inspect left database \"left\": list collections: left inspect failed")

	// inspect right error
	calledLeft := false
	dbschemaListCollections = func(ctx context.Context, db dal.DB, parent *record.Key) ([]dal.CollectionRef, error) {
		if !calledLeft {
			calledLeft = true
			return origListCols(ctx, db, parent)
		}
		return nil, errors.New("right inspect failed")
	}
	_, err = Compare(context.Background(), "left", commonLeft, "right", commonRight, Options{})
	require.ErrorContains(t, err, "inspect right database \"right\": list collections: right inspect failed")
	dbschemaListCollections = origListCols

	// remainingDetails < 0 branch
	origDiffRows := diffRowsFn
	defer func() { diffRowsFn = origDiffRows }()
	diffRowsFn = func(ctx context.Context, left, right *rowStore, columns, key []string, detailLimit int) (DifferenceCounts, []RecordDiff, bool, error) {
		return DifferenceCounts{Changed: 5}, make([]RecordDiff, 5), true, nil
	}
	report, err := Compare(context.Background(), "left", commonLeft, "right", commonRight, Options{
		Details:     true,
		DetailLimit: 1,
	})
	require.NoError(t, err)
	require.Equal(t, 5, len(report.Relations[0].Details))
	diffRowsFn = origDiffRows

	// duplicate right mapping in matchRelations
	_, err = matchRelations(
		map[string]relation{"t1": {}, "t2": {}},
		map[string]relation{"target": {}},
		map[string]string{"t1": "target", "t2": "target"},
	)
	require.ErrorContains(t, err, `both target right relation "target"`)
}

// Not parallel: mutates package seams.
func TestDiffRowsAndRowSequence(t *testing.T) {
	ctx := context.Background()

	// 1. rowSequence with store == nil
	seqNil := rowSequence(ctx, nil, []string{"id"})
	called := false
	seqNil(func(r record.WithID[string], err error) bool {
		called = true
		return true
	})
	require.False(t, called)

	// 2. queryContext error
	origQueryContext := queryContext
	defer func() { queryContext = origQueryContext }()
	queryContext = func(ctx context.Context, db *sql.DB, query string, args ...any) (*sql.Rows, error) {
		return nil, errors.New("query error")
	}
	store, err := newRowStore(false)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	seqErr := rowSequence(ctx, store, []string{"id"})
	var gotErr error
	seqErr(func(r record.WithID[string], err error) bool {
		gotErr = err
		return false
	})
	require.ErrorContains(t, gotErr, "could not read comparison row spool")
	queryContext = origQueryContext

	// 3. Scan error: query returns 3 columns instead of 2
	queryContext = func(ctx context.Context, db *sql.DB, query string, args ...any) (*sql.Rows, error) {
		return db.QueryContext(ctx, "SELECT 1, 2, 3")
	}
	seqScanErr := rowSequence(ctx, store, []string{"id"})
	seqScanErr(func(r record.WithID[string], err error) bool {
		gotErr = err
		return false
	})
	require.ErrorContains(t, gotErr, "could not scan comparison row spool")
	queryContext = origQueryContext

	// 4. json.Unmarshal error: payload is not valid JSON
	_, err = store.db.Exec("INSERT INTO staged_rows(sort_key, payload) VALUES (X'01', X'FF')")
	require.NoError(t, err)
	seqJSONErr := rowSequence(ctx, store, []string{"id"})
	seqJSONErr(func(r record.WithID[string], err error) bool {
		gotErr = err
		return false
	})
	require.ErrorContains(t, gotErr, "could not decode comparison row spool")

	// Clear table
	_, _ = store.db.Exec("DELETE FROM staged_rows")

	// 5. Unkeyed store with multiple identical sort keys (occurrence increment)
	sortKey := []byte{0x01, 0x02}
	val1, _ := json.Marshal(stagedRow{Values: []normalizedValue{{Kind: "int", Text: "1"}}})
	val2, _ := json.Marshal(stagedRow{Values: []normalizedValue{{Kind: "int", Text: "2"}}})
	_, err = store.db.Exec("INSERT INTO staged_rows(sort_key, payload) VALUES (?, ?)", sortKey, val1)
	require.NoError(t, err)
	_, err = store.db.Exec("INSERT INTO staged_rows(sort_key, payload) VALUES (?, ?)", sortKey, val2)
	require.NoError(t, err)

	var ids []string
	seqUnkeyed := rowSequence(ctx, store, []string{"id", "extra_col"})
	seqUnkeyed(func(r record.WithID[string], err error) bool {
		require.NoError(t, err)
		ids = append(ids, r.ID)
		return true // continue
	})
	require.Len(t, ids, 2)
	require.Contains(t, ids[0], "!00000000000000000001")
	require.Contains(t, ids[1], "!00000000000000000002")

	// Early break in yield (yield returns false)
	earlyBreakCount := 0
	seqUnkeyed(func(r record.WithID[string], err error) bool {
		earlyBreakCount++
		return false
	})
	require.Equal(t, 1, earlyBreakCount)

	// 6. rows.Err() coverage
	cancCtx, cancel := context.WithCancel(ctx)
	seqCancel := rowSequence(cancCtx, store, []string{"id"})
	var cancelErr error
	seqCancel(func(r record.WithID[string], err error) bool {
		if err != nil {
			cancelErr = err
			return false
		}
		cancel()
		time.Sleep(50 * time.Millisecond)
		return true
	})
	require.ErrorContains(t, cancelErr, "comparison row spool read failed")

	// 7. diffRows unknown candidate status
	origDiff := recordopsDiff
	defer func() { recordopsDiff = origDiff }()
	recordopsDiff = func(left recordops.RecordSeq[string], right []recordops.RecordSeq[string], opts ...recordops.Option) iter.Seq2[recordops.IDDiff[string], error] {
		return func(yield func(recordops.IDDiff[string], error) bool) {
			diff := recordops.IDDiff[string]{
				ID: "dummy",
				Candidates: []recordops.CandidateState{
					{Status: recordops.RecordStatus(120)},
				},
			}
			yield(diff, nil)
		}
	}
	_, _, _, err = diffRows(ctx, store, store, []string{"id"}, []string{"id"}, 10)
	require.ErrorContains(t, err, "unknown recordops status 120")
	recordopsDiff = origDiff
}

// Not parallel: pure unit tests covering schema, normalization, and display helpers.
func TestPureHelpersAndEdgeCases(t *testing.T) {
	// relationName
	require.Equal(t, "myschema.mytable", relationName(dal.NewQualifiedRootCollectionRef("myschema", "mytable", "")))
	require.Equal(t, "mytable", relationName(dal.NewRootCollectionRef("mytable", "")))

	// indexSignature & mergeIndexes
	idx1 := dbschema.IndexDef{Fields: []dal.FieldName{"a", "b"}, Unique: true}
	idx2 := dbschema.IndexDef{Fields: []dal.FieldName{"c"}, Unique: false}
	merged := mergeIndexes([]dbschema.IndexDef{idx1}, []dbschema.IndexDef{idx1, idx2})
	require.Len(t, merged, 2)
	require.Equal(t, "true:a\x00b", indexSignature(idx1))

	// foreignKeySignatures & indexSignatures
	fk := dbschema.ForeignKeyDef{
		ReferencedNamespace:  "ns",
		ReferencedCollection: "parent",
		Fields:               []dal.FieldName{"child_id"},
		ReferencedFields:     []dal.FieldName{"id"},
		Enforcement:          "ENFORCED",
		OnUpdate:             "CASCADE",
		OnDelete:             "RESTRICT",
	}
	fkSigs := foreignKeySignatures([]dbschema.ForeignKeyDef{fk})
	require.Len(t, fkSigs, 1)
	require.Contains(t, fkSigs[0], "child_id")
	idxSigs := indexSignatures([]dbschema.IndexDef{idx1})
	require.Len(t, idxSigs, 1)

	// equalOptionalInt
	val1, val2 := 10, 20
	require.True(t, equalOptionalInt(nil, nil))
	require.False(t, equalOptionalInt(&val1, nil))
	require.False(t, equalOptionalInt(nil, &val1))
	require.True(t, equalOptionalInt(&val1, &val1))
	require.False(t, equalOptionalInt(&val1, &val2))

	// equalPrecision
	p1 := dbschema.Precision{Total: 10, Scale: 2}
	p2 := dbschema.Precision{Total: 10, Scale: 3}
	require.True(t, equalPrecision(nil, nil))
	require.False(t, equalPrecision(&p1, nil))
	require.False(t, equalPrecision(nil, &p1))
	require.True(t, equalPrecision(&p1, &p1))
	require.False(t, equalPrecision(&p1, &p2))

	// appendInventoryChanges view differences
	rep := &Report{LeftName: "L", RightName: "R"}
	leftInv := relationInventory{
		adapterName: "adapterX",
		views: map[string]dbschema.SourceViewDef{
			"v_left_only": {Name: "v_left_only"},
			"v_diff_cols": {Name: "v_diff_cols", Columns: []string{"a"}},
			"v_diff_sql":  {Name: "v_diff_sql", CreateSQL: "SELECT 1"},
		},
	}
	rightInv := relationInventory{
		adapterName: "adapterX",
		views: map[string]dbschema.SourceViewDef{
			"v_right_only": {Name: "v_right_only"},
			"v_diff_cols":  {Name: "v_diff_cols", Columns: []string{"b"}},
			"v_diff_sql":   {Name: "v_diff_sql", CreateSQL: "SELECT 2"},
		},
	}
	appendInventoryChanges(rep, leftInv, rightInv, []relationPair{
		{name: "only_l", hasLeft: true, hasRight: false},
		{name: "only_r", hasLeft: false, hasRight: true},
	})
	require.Contains(t, rep.SchemaChanges, "relation only in L: only_l")
	require.Contains(t, rep.SchemaChanges, "relation only in R: only_r")
	require.Contains(t, rep.SchemaChanges, "view only in L: v_left_only")
	require.Contains(t, rep.SchemaChanges, "view only in R: v_right_only")
	require.Contains(t, rep.SchemaChanges, "view v_diff_cols has different ordered columns")
	require.Contains(t, rep.SchemaChanges, "view v_diff_sql definition differs")

	// compareRelationSchema field differences
	defL := dbschema.CollectionDef{
		Name: "test_tab",
		Fields: []dbschema.FieldDef{
			{Name: "only_l", Type: dbschema.String},
			{Name: "diff_null", Type: dbschema.String, Nullable: true},
			{Name: "diff_len", Type: dbschema.String, Length: &val1},
			{Name: "diff_prec", Type: dbschema.Decimal, Precision: &p1},
			{Name: "diff_def", Type: dbschema.String, Default: dbschema.DefaultLiteral{Value: "def1"}},
			{Name: "diff_auto", Type: dbschema.Int, AutoIncrement: true},
		},
		PrimaryKey: []dal.FieldName{"diff_auto"},
		ForeignKeys: []dbschema.ForeignKeyDef{
			{ReferencedCollection: "other1"},
		},
		Indexes: []dbschema.IndexDef{
			{Fields: []dal.FieldName{"diff_auto"}, Unique: true},
		},
	}
	defR := dbschema.CollectionDef{
		Name: "test_tab",
		Fields: []dbschema.FieldDef{
			{Name: "only_r", Type: dbschema.String},
			{Name: "diff_null", Type: dbschema.String, Nullable: false},
			{Name: "diff_len", Type: dbschema.String, Length: &val2},
			{Name: "diff_prec", Type: dbschema.Decimal, Precision: &p2},
			{Name: "diff_def", Type: dbschema.String, Default: dbschema.DefaultLiteral{Value: "def2"}},
			{Name: "diff_auto", Type: dbschema.Int, AutoIncrement: false},
		},
		PrimaryKey: []dal.FieldName{"diff_null"},
		ForeignKeys: []dbschema.ForeignKeyDef{
			{ReferencedCollection: "other2"},
		},
		Indexes: []dbschema.IndexDef{
			{Fields: []dal.FieldName{"diff_null"}, Unique: false},
		},
	}
	changes, comp := compareRelationSchema(defL, defR, "test_tab", true)
	require.True(t, comp)
	require.Contains(t, changes, "test_tab.only_l exists only in left")
	require.Contains(t, changes, "test_tab.only_r exists only in right")
	require.Contains(t, changes, "test_tab.diff_null nullability differs")
	require.Contains(t, changes, "test_tab.diff_len length differs")
	require.Contains(t, changes, "test_tab.diff_prec decimal precision differs")
	require.Contains(t, changes, "test_tab.diff_def default differs")
	require.Contains(t, changes, "test_tab.diff_auto auto-increment differs")
	require.Contains(t, changes, "test_tab column order differs")
	require.Contains(t, changes, "test_tab primary key differs")
	require.Contains(t, changes, "test_tab foreign keys differ")
	require.Contains(t, changes, "test_tab indexes differ")

	// declaredTypeChanges nil check
	require.Nil(t, declaredTypeChanges(nil, nil, "tab"))
	require.Nil(t, declaredTypeChanges(&dbschema.SourceDefinition{}, nil, "tab"))
	require.Nil(t, declaredTypeChanges(nil, &dbschema.SourceDefinition{}, "tab"))

	// declaredType & sourceDialect nil check
	require.Empty(t, declaredType(nil, "col"))
	require.Empty(t, sourceDialect(nil))
	sourceDef := &dbschema.SourceDefinition{
		Dialect: " Postgres ",
		Columns: []dbschema.SourceColumnDef{{Name: "c1", DeclaredType: "integer"}},
	}
	require.Equal(t, "postgres", sourceDialect(sourceDef))
	require.Equal(t, "integer", declaredType(sourceDef, "c1"))
	require.Empty(t, declaredType(sourceDef, "unknown"))

	// sameUsablePrimaryKey edge cases
	require.Nil(t, sameUsablePrimaryKey(dbschema.CollectionDef{}, dbschema.CollectionDef{})) // empty pk
	pkTabL := dbschema.CollectionDef{PrimaryKey: []dal.FieldName{"a"}}
	pkTabR := dbschema.CollectionDef{PrimaryKey: []dal.FieldName{"b"}}
	require.Nil(t, sameUsablePrimaryKey(pkTabL, pkTabR)) // different pk
	pkTabL.Fields = []dbschema.FieldDef{{Name: "a", Type: dbschema.Int, Nullable: true}}
	pkTabR = dbschema.CollectionDef{PrimaryKey: []dal.FieldName{"a"}, Fields: []dbschema.FieldDef{{Name: "a", Type: dbschema.Int, Nullable: false}}}
	require.Nil(t, sameUsablePrimaryKey(pkTabL, pkTabR)) // nullable left
	pkTabL.Fields[0].Nullable = false
	pkTabR.Fields[0].Nullable = true
	require.Nil(t, sameUsablePrimaryKey(pkTabL, pkTabR)) // nullable right
	pkTabR.Fields[0].Nullable = false
	pkTabR.Fields[0].Type = dbschema.String
	require.Nil(t, sameUsablePrimaryKey(pkTabL, pkTabR)) // type mismatch
	pkTabR.Fields = nil
	require.Nil(t, sameUsablePrimaryKey(pkTabL, pkTabR)) // missing field in right

	// comparisonKey edge cases
	_, err := comparisonKey("t", dbschema.CollectionDef{}, dbschema.CollectionDef{}, map[string][]string{"t": {""}})
	require.ErrorContains(t, err, "empty or duplicate column")
	_, err = comparisonKey("t", dbschema.CollectionDef{}, dbschema.CollectionDef{}, map[string][]string{"t": {"a", "a"}})
	require.ErrorContains(t, err, "empty or duplicate column")
	defHasField := dbschema.CollectionDef{Fields: []dbschema.FieldDef{{Name: "a", Type: dbschema.Int}}}
	_, err = comparisonKey("t", defHasField, dbschema.CollectionDef{}, map[string][]string{"t": {"missing"}})
	require.ErrorContains(t, err, "names missing column")
	defHasFieldR := dbschema.CollectionDef{Fields: []dbschema.FieldDef{{Name: "missing", Type: dbschema.Int}}}
	_, err = comparisonKey("t", defHasField, defHasFieldR, map[string][]string{"t": {"missing"}})
	require.ErrorContains(t, err, "names missing column")
	defHasFieldIncompatR := dbschema.CollectionDef{Fields: []dbschema.FieldDef{{Name: "a", Type: dbschema.String}}}
	_, err = comparisonKey("t", defHasField, defHasFieldIncompatR, map[string][]string{"t": {"a"}})
	require.ErrorContains(t, err, "has incompatible types")

	// canonicalDecimal edge cases
	_, ok := canonicalDecimal(nil)
	require.False(t, ok)
	_, ok = canonicalDecimal(big.NewRat(1, 3))
	require.False(t, ok)
	zeroRat, ok := canonicalDecimal(new(big.Rat).SetInt64(0))
	require.True(t, ok)
	require.Equal(t, "0", zeroRat)
	scaleRat, ok := canonicalDecimal(big.NewRat(1, 40)) // 1 / (2^3 * 5^1)
	require.True(t, ok)
	require.Equal(t, "0.025", scaleRat)

	// integerString edge cases
	for _, num := range []any{
		int(1), int8(2), int16(3), int32(4), int64(5),
		uint(6), uint8(7), uint16(8), uint32(9), uint64(10),
		*big.NewInt(11), big.NewInt(12),
		"13", []byte("14"), json.Number("15"), 42.0,
	} {
		str, ok := integerString(num)
		require.True(t, ok, "failed for %T: %v", num, num)
		require.NotEmpty(t, str)
	}
	var nilBigInt *big.Int
	_, ok = integerString(nilBigInt)
	require.False(t, ok)
	_, ok = integerString("not_a_num")
	require.False(t, ok)
	_, ok = integerString([]byte("not_a_num"))
	require.False(t, ok)
	_, ok = integerString(json.Number("not_a_num"))
	require.False(t, ok)
	_, ok = integerString(math.NaN())
	require.False(t, ok)
	_, ok = integerString(math.Inf(1))
	require.False(t, ok)
	_, ok = integerString(float64(1 << 54))
	require.False(t, ok)
	_, ok = integerString(float64(-1 << 54))
	require.False(t, ok)
	_, ok = integerString(1.5)
	require.False(t, ok)
	_, ok = integerString(struct{}{})
	require.False(t, ok)

	// decimalString edge cases
	for _, dec := range []any{
		"1.23", []byte("4.56"), json.Number("7.89"), *big.NewInt(10), big.NewInt(11), 12,
	} {
		str, ok := decimalString(dec)
		require.True(t, ok)
		require.NotEmpty(t, str)
	}
	_, ok = decimalString("")
	require.False(t, ok)
	_, ok = decimalString([]byte{})
	require.False(t, ok)
	_, ok = decimalString(json.Number(""))
	require.False(t, ok)
	_, ok = decimalString(nilBigInt)
	require.False(t, ok)
	_, ok = decimalString(struct{}{})
	require.False(t, ok)

	// floatValue edge cases
	f32, ok := floatValue(float32(1.25))
	require.True(t, ok)
	require.Equal(t, 1.25, f32)
	fInt, ok := floatValue(int64(100))
	require.True(t, ok)
	require.Equal(t, float64(100), fInt)
	_, ok = floatValue(uint64(math.MaxUint64)) // precision lost
	require.False(t, ok)
	_, ok = floatValue(json.Number("invalid"))
	require.False(t, ok)
	_, ok = floatValue(json.Number("0.10000000000000001")) // imprecise
	require.False(t, ok)
	fExact, ok := floatValue(json.Number("0.25"))
	require.True(t, ok)
	require.Equal(t, 0.25, fExact)
	_, ok = floatValue("not_a_float")
	require.False(t, ok)

	// timeValueForDeclaredType edge cases
	// 1. date
	tVal, ok := timeValueForDeclaredType("2026-10-14", "", "date")
	require.True(t, ok)
	require.Equal(t, "2026-10-14", tVal)
	_, ok = timeValueForDeclaredType("invalid-date", "", "date")
	require.False(t, ok)

	// 2. time
	now := time.Now()
	tVal, ok = timeValueForDeclaredType(now, "", "time")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType(now, "", "time with time zone")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("12:34:56", "", "time")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("12:34:56.123456789", "", "time")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("12:34:56Z", "", "time with time zone")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("12:34:56.123456789+01:00", "", "time with time zone")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	_, ok = timeValueForDeclaredType("invalid-time", "", "time")
	require.False(t, ok)

	// 3. instant
	tVal, ok = timeValueForDeclaredType(now, "", "timestamp with time zone")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType(now, "", "timestamptz")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType(now, "", "timestamp_tz")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType(now, "bigquery", "timestamp")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("2026-10-14 12:34:56Z", "", "timestamptz")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("2026-10-14 12:34:56.123456789+02:00", "", "timestamptz")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	_, ok = timeValueForDeclaredType("invalid-instant", "", "timestamptz")
	require.False(t, ok)

	// 4. wall timestamp
	tVal, ok = timeValueForDeclaredType(now, "", "datetime")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType(now, "", "timestamp without time zone")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType(now, "", "timestamp_ntz")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("2026-10-14 12:34:56", "", "timestamp")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("2026-10-14T12:34:56.123456789", "", "timestamp")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	_, ok = timeValueForDeclaredType("invalid-wall", "", "timestamp")
	require.False(t, ok)

	// 5. inferred
	tVal, ok = timeValueForDeclaredType(now, "", "")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("2026-10-14T12:34:56Z", "", "")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("2026-10-14", "", "")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	tVal, ok = timeValueForDeclaredType("12:34:56", "", "")
	require.True(t, ok)
	require.NotEmpty(t, tVal)
	_, ok = timeValueForDeclaredType("invalid_fallback", "", "")
	require.False(t, ok)
	_, ok = timeValueForDeclaredType("not-temporal", "", "")
	require.False(t, ok)
	_, ok = timeValueForDeclaredType(12345, "", "")
	require.False(t, ok)

	// 6. unknown declared
	_, ok = timeValueForDeclaredType("val", "", "unsupported_type")
	require.False(t, ok)

	// normalizeValueForSource error paths
	// unsupported type
	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Type(120)}, "val")
	require.ErrorContains(t, err, "unsupported schema type")

	// unsupported string type
	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.String}, 123)
	require.ErrorContains(t, err, "unsupported text value")

	// unsupported bytes type
	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Bytes}, "string")
	require.ErrorContains(t, err, "unsupported binary value")

	// unsupported float type
	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Float}, "not_float")
	require.ErrorContains(t, err, "unsupported floating-point value")
	// float 0 and -0 normalization
	v0, err := normalizeValue(dbschema.FieldDef{Type: dbschema.Float}, float64(0))
	require.NoError(t, err)
	require.Equal(t, "0", v0.Text)

	// unsupported time value
	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Time}, "not_time")
	require.ErrorContains(t, err, "unsupported temporal value")

	// fieldDiffs branches
	// Missing with baseline == nil
	require.Nil(t, fieldDiffs(nil, recordops.CandidateState{Status: recordops.Missing}))
	// Missing with baseline != nil
	base := &recordops.RecordSnapshot{Fields: []recordops.FieldValue{{Name: "f1", Value: normalizedValue{Kind: "int", Text: "10"}}}}
	mDiffs := fieldDiffs(base, recordops.CandidateState{Status: recordops.Missing})
	require.Len(t, mDiffs, 1)
	require.True(t, mDiffs[0].AfterAbsent)
	require.Equal(t, "10", mDiffs[0].Before)

	// Extra
	candExtra := recordops.CandidateState{Status: recordops.Extra, Fields: []recordops.FieldValue{{Name: "f2", Value: normalizedValue{Kind: "int", Text: "20"}}}}
	eDiffs := fieldDiffs(nil, candExtra)
	require.Len(t, eDiffs, 1)
	require.True(t, eDiffs[0].BeforeAbsent)
	require.Equal(t, "20", eDiffs[0].After)

	// Changed with baseline == nil
	require.Nil(t, fieldDiffs(nil, recordops.CandidateState{Status: recordops.Changed}))
	// Matched
	require.Nil(t, fieldDiffs(base, recordops.CandidateState{Status: recordops.Matched}))

	// Changed with before absent and after absent
	candChanged := recordops.CandidateState{
		Status: recordops.Changed,
		Fields: []recordops.FieldValue{
			{Name: "f1", Value: normalizedValue{Kind: "int", Text: "11"}, Absent: true},
			{Name: "new_f", Value: normalizedValue{Kind: "string", Text: "new"}},
		},
	}
	cDiffs := fieldDiffs(base, candChanged)
	require.Len(t, cDiffs, 2)
	require.True(t, cDiffs[0].AfterAbsent)
	require.True(t, cDiffs[1].BeforeAbsent)

	// publicValue branches
	require.Equal(t, "raw", publicValue("raw"))
	require.Nil(t, publicValue(normalizedValue{Null: true}))
	require.Nil(t, publicValue(normalizedValue{Absent: true}))
	require.Equal(t, true, publicValue(normalizedValue{Kind: "bool", Text: "1"}))
	require.Equal(t, false, publicValue(normalizedValue{Kind: "bool", Text: "0"}))
	require.Equal(t, "0x00ff", publicValue(normalizedValue{Kind: "bytes", Text: "00ff"}))
	require.Equal(t, "123", publicValue(normalizedValue{Kind: "int", Text: "123"}))
	fBits := strconv.FormatUint(math.Float64bits(12.5), 16)
	require.Equal(t, 12.5, publicValue(normalizedValue{Kind: "float", Text: fBits}))
	require.Equal(t, "invalid_hex", publicValue(normalizedValue{Kind: "float", Text: "invalid_hex"}))
	require.Equal(t, "other", publicValue(normalizedValue{Kind: "custom", Text: "other"}))

	// displayValue branches
	require.Equal(t, "<absent>", displayValue(normalizedValue{Absent: true}))
	require.Equal(t, "null", displayValue(normalizedValue{Null: true}))
	require.Equal(t, `"hello"`, displayValue(normalizedValue{Kind: "string", Text: "hello"}))
	require.Equal(t, "0x00ff", displayValue(normalizedValue{Kind: "bytes", Text: "00ff"}))
	require.Equal(t, "1", displayValue(normalizedValue{Kind: "bool", Text: "1"}))
	require.Equal(t, "123", displayValue(normalizedValue{Kind: "int", Text: "123"}))
	require.Equal(t, "12.5", displayValue(normalizedValue{Kind: "float", Text: fBits}))
	require.Equal(t, "<invalid-float>", displayValue(normalizedValue{Kind: "float", Text: "invalid_hex"}))
	require.Equal(t, "custom_text", displayValue(normalizedValue{Kind: "custom", Text: "custom_text"}))

	// displayID branches
	// len(key) == 0 with "!"
	unkeyedID := hex.EncodeToString([]byte("test")) + "!00000000000000000001"
	disp1 := displayID(unkeyedID, nil)
	require.Contains(t, disp1, "keyless row sha256:")
	require.Contains(t, disp1, "occurrence:1")

	// len(key) == 0 without "!"
	disp2 := displayID(hex.EncodeToString([]byte("test")), nil)
	require.Contains(t, disp2, "keyless row sha256:")

	// hex.DecodeString error
	require.Equal(t, "invalid_hex", displayID("invalid_hex", nil))

	// len(key) > 0 with invalid json
	require.Equal(t, hex.EncodeToString([]byte("not json")), displayID(hex.EncodeToString([]byte("not json")), []string{"id"}))

	// len(key) > 0 with wrong length
	singleValJSON, _ := json.Marshal([]normalizedValue{{Kind: "int", Text: "1"}})
	require.Equal(t, hex.EncodeToString(singleValJSON), displayID(hex.EncodeToString(singleValJSON), []string{"id", "other"}))

	// len(key) > 0 valid
	validKeyJSON, _ := json.Marshal([]normalizedValue{
		{Kind: "int", Text: "1"},
		{Kind: "string", Text: "abc"},
	})
	dispKey := displayID(hex.EncodeToString(validKeyJSON), []string{"id", "name"})
	require.Equal(t, `id=1, name="abc"`, dispKey)

	// ratSetString error in normalizeValueForSource
	origRatSetString := ratSetString
	defer func() { ratSetString = origRatSetString }()
	ratSetString = func(r *big.Rat, s string) (*big.Rat, bool) { return nil, false }
	_, err = normalizeValueForSource(dbschema.FieldDef{Type: dbschema.Decimal}, "sqlite", "decimal", "123.45")
	require.ErrorContains(t, err, "invalid decimal value")
	ratSetString = origRatSetString

	// canonicalDecimalFn error in normalizeValueForSource
	origCanonical := canonicalDecimalFn
	defer func() { canonicalDecimalFn = origCanonical }()
	canonicalDecimalFn = func(r *big.Rat) (string, bool) { return "", false }
	_, err = normalizeValueForSource(dbschema.FieldDef{Type: dbschema.Decimal}, "sqlite", "decimal", "123.45")
	require.ErrorContains(t, err, "decimal value has no finite decimal representation")
	canonicalDecimalFn = origCanonical

	// floatString returning "-0" or "" in canonicalDecimal
	origFloatString := floatString
	defer func() { floatString = origFloatString }()
	floatString = func(r *big.Rat, prec int) string { return "-0" }
	str, ok := canonicalDecimal(big.NewRat(1, 1))
	require.True(t, ok)
	require.Equal(t, "0", str)
	floatString = func(r *big.Rat, prec int) string { return "" }
	str, ok = canonicalDecimal(big.NewRat(1, 1))
	require.True(t, ok)
	require.Equal(t, "0", str)
	floatString = origFloatString

	// bigIntSetString error in floatValue
	origBigIntSetString := bigIntSetString
	defer func() { bigIntSetString = origBigIntSetString }()
	bigIntSetString = func(z *big.Int, s string, base int) (*big.Int, bool) { return nil, false }
	_, ok = floatValue(int64(42))
	require.False(t, ok)
	bigIntSetString = origBigIntSetString

	// temporalText []byte branch
	text, _, ok, asTime := temporalText([]byte("2026-01-01"))
	require.True(t, ok)
	require.False(t, asTime)
	require.Equal(t, "2026-01-01", text)

	// dateValue !ok branch
	_, ok = dateValue(12345)
	require.False(t, ok)

	// timeOfDayValue !ok branch
	_, ok = timeOfDayValue(12345, false)
	require.False(t, ok)

	// wallTimestampValue !ok branch
	_, ok = wallTimestampValue(12345)
	require.False(t, ok)

	// instantValue !ok branch
	_, ok = instantValue(12345)
	require.False(t, ok)
}
