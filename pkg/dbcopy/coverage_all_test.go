package dbcopy

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRecordsReader struct {
	err error
}

func (m mockRecordsReader) Close() error            { return nil }
func (m mockRecordsReader) Cursor() (string, error) { return "", nil }
func (m mockRecordsReader) Next() (record.Record, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, errors.New("read error")
}

type mockQueryDB struct {
	dal.DB
	dbschema.SchemaReader
	readerErr error
	execErr   error
}

func (m mockQueryDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	if m.execErr != nil {
		return nil, m.execErr
	}
	return mockRecordsReader{err: m.readerErr}, nil
}

func TestURL_CoverageBranches(t *testing.T) {
	// 1. SupportedSchemes
	schemes := SupportedSchemes()
	assert.Contains(t, schemes, "sqlite")
	assert.Contains(t, schemes, "openvaultdb")

	// 2. OpenVaultDB parsing
	_, err := Parse("openvaultdb://")
	assert.ErrorContains(t, err, "path is required")

	ovRef, err := Parse("openvaultdb://path/to/desc.json")
	require.NoError(t, err)
	assert.Equal(t, "openvaultdb", ovRef.Scheme)
	assert.Equal(t, "path/to/desc.json", ovRef.Path)

	// 3. Postgres URL parsing error
	_, err = Parse("postgres://%invalid-url")
	assert.Error(t, err)

	// 4. Sqlite URL parsing error
	_, err = Parse("sqlite://%invalid-url")
	assert.Error(t, err)

	// 5. Ingitdb URL missing path
	_, err = Parse("ingitdb://")
	assert.ErrorContains(t, err, "missing local path")

	// 7. OpenProtectedForTest
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	makeSQLiteFile(t, dbPath, "CREATE TABLE t (id INT)")
	sqlRef := BackendRef{Scheme: "sqlite", Path: dbPath}
	db, err := sqlRef.OpenProtectedForTest(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, db)

	// 8. Open openvaultdb
	ovRefBogus := BackendRef{Scheme: "openvaultdb", Path: "/nonexistent/desc.json"}
	_, err = ovRefBogus.Open(context.Background())
	assert.Error(t, err)

	// 9. Open default unsupported scheme
	badRef := BackendRef{Scheme: "unsupported", Path: "foo"}
	_, err = badRef.Open(context.Background())
	assert.ErrorContains(t, err, "unsupported scheme")

	// 10. SQLite open error via seam
	origSQLite := newSQLiteDatabaseWithOptions
	defer func() { newSQLiteDatabaseWithOptions = origSQLite }()
	sqliteCause := errors.New("mock sqlite open fail")
	newSQLiteDatabaseWithOptions = func(dbPath string, schema dal.Schema, opts dalgo2sql.DbOptions) (*dalgo2sqlite.Database, error) {
		return nil, sqliteCause
	}
	_, err = sqlRef.Open(context.Background())
	// A driver's message is never shown (see BackendRef.OpenFailure); the cause is kept.
	assert.ErrorContains(t, err, "open sqlite source")
	assert.NotContains(t, err.Error(), "mock sqlite open fail")
	assert.ErrorIs(t, err, sqliteCause)
	newSQLiteDatabaseWithOptions = origSQLite

	// 11. InGitDB open error via seam
	ingitRef := BackendRef{Scheme: "ingitdb", Path: dbPath}
	origInGitDB := newInGitDBDatabase
	defer func() { newInGitDBDatabase = origInGitDB }()
	ingitCause := errors.New("mock ingitdb open fail")
	newInGitDBDatabase = func(dir string, r ingitdb.CollectionsReader, opts ...dalgo2ingitdb.DatabaseOption) (dal.DB, error) {
		return nil, ingitCause
	}
	_, err = ingitRef.Open(context.Background())
	assert.ErrorContains(t, err, "open ingitdb source")
	assert.NotContains(t, err.Error(), "mock ingitdb open fail")
	assert.ErrorIs(t, err, ingitCause)
	newInGitDBDatabase = origInGitDB
}

func TestEmptyTarget_CoverageBranches(t *testing.T) {
	ctx := context.Background()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	makeSQLiteFile(t, dbPath, "CREATE TABLE users (id INT PRIMARY KEY)")

	db, err := dalgo2sqlite.NewDatabase(dbPath)
	require.NoError(t, err)

	// 1. checkEmptyTarget fails on ListCollections with canceled context
	err = checkEmptyTarget(canceledCtx, db, []string{"users"})
	assert.Error(t, err)

	// 2. countRows fails on ExecuteQueryToRecordsReader with canceled context
	_, err = countRows(canceledCtx, db, "users")
	assert.Error(t, err)

	// 3. countRows reader.Next() returns non-EOF error
	mDB := mockQueryDB{DB: db, SchemaReader: db, readerErr: errors.New("corrupted stream")}
	_, err = countRows(ctx, mDB, "users")
	assert.ErrorContains(t, err, "corrupted stream")

	// 4. checkEmptyTarget propagates countRows error
	err = checkEmptyTarget(ctx, mDB, []string{"users"})
	assert.Error(t, err)
}

func TestEngine_CoverageBranches(t *testing.T) {
	ctx := context.Background()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src.db")
	tgtPath := filepath.Join(tmpDir, "tgt.db")
	makeSQLiteFile(t, srcPath, "CREATE TABLE t1 (id INT PRIMARY KEY)", "CREATE TABLE nopk (val TEXT)")
	makeSQLiteFile(t, tgtPath)

	src, err := dalgo2sqlite.NewDatabase(srcPath)
	require.NoError(t, err)
	tgt, err := dalgo2sqlite.NewDatabase(tgtPath)
	require.NoError(t, err)

	// 1. ListCollections fails on source with canceled context
	_, err = Copy(canceledCtx, src, tgt, CopyOpts{})
	assert.ErrorContains(t, err, "list source collections")

	// 2. All tables filtered out returns empty summary with no error
	summary, err := Copy(ctx, src, tgt, CopyOpts{
		Filters: &filter.Directives{ExcludeTables: []string{"t1", "nopk"}},
	})
	assert.NoError(t, err)
	assert.Equal(t, 0, summary.Tables)

	// 3. DropCollection fails in recreate mode
	_, err = Copy(ctx, src, mockQueryDB{DB: tgt, SchemaReader: tgt}, CopyOpts{
		Overwrite: "recreate",
		Filters:   &filter.Directives{IncludeTables: []string{"t1"}},
	})
	assert.ErrorContains(t, err, "drop target collection")

	// 4. Table without PK is skipped with warning
	var stderr bytes.Buffer
	summary, err = Copy(ctx, src, tgt, CopyOpts{
		Filters: &filter.Directives{IncludeTables: []string{"nopk"}},
		Stderr:  &stderr,
	})
	assert.NoError(t, err)
	assert.Contains(t, summary.RowSkips, "nopk")
	assert.Contains(t, stderr.String(), "row copy skipped")

	// 5. ProgressTracker StartTable and FinishTable calls
	tracker := NewProgressWriter(&stderr, true)
	_, err = Copy(ctx, src, tgt, CopyOpts{
		Filters:  &filter.Directives{IncludeTables: []string{"t1"}},
		Progress: tracker,
	})
	assert.NoError(t, err)

	// 6. resolveParallelism branches:
	// requested < 1
	assert.Equal(t, 1, resolveParallelism(-1, src, tgt, &stderr))

	// Source not concurrent, target concurrent
	mockSrcNonConc := mockNonConcurrentDB{DB: src}
	mockTgtConc := mockConcurrentDB{DB: tgt}
	p := resolveParallelism(4, mockSrcNonConc, mockTgtConc, &stderr)
	assert.Equal(t, 1, p)
	assert.Contains(t, stderr.String(), "requires serial writes")

	// 7. backendOf fallback
	assert.Equal(t, "unknown", backendOf(nil))
}

type mockNonConcurrentDB struct {
	dal.DB
}

func (m mockNonConcurrentDB) SupportsConcurrentConnections() bool { return false }
func (m mockNonConcurrentDB) Adapter() dal.Adapter                { return mockAdapter{name: "nonconc"} }

type mockConcurrentDB struct {
	dal.DB
}

func (m mockConcurrentDB) SupportsConcurrentConnections() bool { return true }
func (m mockConcurrentDB) Adapter() dal.Adapter                { return mockAdapter{name: "conc"} }

type mockAdapter struct {
	name string
}

func (m mockAdapter) Name() string    { return m.name }
func (m mockAdapter) Version() string { return "1.0" }

func TestEngineRows_CoverageBranches(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	makeSQLiteFile(t, dbPath, "CREATE TABLE t (id INT PRIMARY KEY)")
	db, err := dalgo2sqlite.NewDatabase(dbPath)
	require.NoError(t, err)

	// 1. copyRows with nil def
	_, err = copyRows(ctx, db, db, nil, CopyOpts{})
	assert.ErrorContains(t, err, "nil CollectionDef")

	// 2. copyRows with empty PrimaryKey
	_, err = copyRows(ctx, db, db, &dbschema.CollectionDef{Name: "t", PrimaryKey: nil}, CopyOpts{})
	assert.ErrorIs(t, err, ErrNoPrimaryKey)

	// 3. copyRows with invalid where filter
	invalidWhereFilter := &filter.Directives{
		Where: map[string]*filter.PredicateGroup{
			"t": {
				Conditions: []filter.Predicate{
					{Field: "nonexistent", Operator: filter.OpEqual, Value: "1"},
				},
			},
		},
	}
	_, err = copyRows(ctx, db, db, &dbschema.CollectionDef{
		Name:       "t",
		PrimaryKey: []dal.FieldName{"id"},
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}, CopyOpts{Filters: invalidWhereFilter})
	assert.Error(t, err)

	// 4. encodeRecordID error cases (single PK)
	_, err = encodeRecordID("t", []string{"id"}, map[string]any{"other": 1})
	assert.ErrorContains(t, err, "missing PK column")

	_, err = encodeRecordID("t", []string{"id"}, map[string]any{"id": nil})
	assert.ErrorContains(t, err, "has nil PK")
}

func TestReload_CoverageBranches(t *testing.T) {
	ctx := context.Background()

	// 1. ReloadSchemaMismatchError.Error() with empty values
	errMismatch := &ReloadSchemaMismatchError{Table: "t", Column: "c", Reason: "bad"}
	assert.Equal(t, `reload: schema mismatch on table "t" column "c": bad`, errMismatch.Error())

	// 2. typesCompatibleForReload incompatible
	assert.False(t, typesCompatibleForReload(dbschema.String, dbschema.Int, "sqlite"))

	// 3. sameFieldNameSet false branches
	assert.False(t, sameFieldNameSet([]dal.FieldName{"a"}, []dal.FieldName{"a", "b"}))
	assert.False(t, sameFieldNameSet([]dal.FieldName{"a"}, []dal.FieldName{"b"}))

	// 4. fieldNameList rendering
	assert.Equal(t, "()", fieldNameList(nil))
	assert.Equal(t, "(id)", fieldNameList([]dal.FieldName{"id"}))
	assert.Equal(t, "(id,name)", fieldNameList([]dal.FieldName{"id", "name"}))

	// 5. targetRecordKey with synthetic PK ($key)
	recNoKey := record.NewRecordWithoutKey(map[string]any{"id": 1}).SetError(nil)
	_, err := targetRecordKey("col", []string{"$key"}, recNoKey)
	assert.ErrorContains(t, err, "has no key")

	k := record.NewKeyWithID("col", "123")
	recWithKey := record.NewRecordWithData(k, map[string]any{"id": "123"})
	gotKey, err := targetRecordKey("col", []string{"$key"}, recWithKey)
	assert.NoError(t, err)
	assert.Equal(t, k, gotKey)

	// 6. targetRecordKey with no PK fields
	recNil := record.NewRecordWithoutKey([]string{"not-map"}).SetError(nil)
	_, err = targetRecordKey("col", nil, recNil)
	assert.ErrorContains(t, err, "record has no key and no map data")

	recWithKeySlice := record.NewRecordWithData(k, []string{"not-map"})
	gotKey2, err := targetRecordKey("col", nil, recWithKeySlice)
	assert.NoError(t, err)
	assert.Equal(t, k, gotKey2)

	// 7. targetRecordKey with missing PK in map data
	recData := record.NewRecordWithoutKey(map[string]any{"other": 1}).SetError(nil)
	_, err = targetRecordKey("col", []string{"id"}, recData)
	assert.ErrorContains(t, err, "missing PK column")

	// 8. truncateTargetCollection with 0 keys (empty table)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	makeSQLiteFile(t, dbPath, "CREATE TABLE empty_t (id INT PRIMARY KEY)")
	tgt, err := dalgo2sqlite.NewDatabase(dbPath)
	require.NoError(t, err)

	err = truncateTargetCollection(ctx, tgt, "empty_t")
	assert.NoError(t, err)

	// 9. collectKeysToTruncate describe error
	_, err = collectKeysToTruncate(ctx, tgt, "nonexistent_table")
	assert.Error(t, err)

	// 10. validateReloadSchema describe target error
	srcDBPath := filepath.Join(tmpDir, "src.db")
	makeSQLiteFile(t, srcDBPath, "CREATE TABLE users (id INT PRIMARY KEY)")
	src, err := dalgo2sqlite.NewDatabase(srcDBPath)
	require.NoError(t, err)

	ref := dal.NewCollectionRef("users", "", nil)
	// Target does not have "users" table
	err = validateReloadSchema(ctx, src, tgt, &ref, BackendSQLite, BackendSQLite)
	assert.Error(t, err)
	var mismatch *ReloadSchemaMismatchError
	assert.True(t, errors.As(err, &mismatch))
	assert.Contains(t, mismatch.Reason, "target table not describable")

	// 11. validateReloadSchema PK mismatch
	tgtMismatchPath := filepath.Join(tmpDir, "tgt_mismatch.db")
	makeSQLiteFile(t, tgtMismatchPath, "CREATE TABLE users (id INT, email TEXT, PRIMARY KEY (id, email))")
	tgtMismatch, err := dalgo2sqlite.NewDatabase(tgtMismatchPath)
	require.NoError(t, err)

	err = validateReloadSchema(ctx, src, tgtMismatch, &ref, BackendSQLite, BackendSQLite)
	assert.Error(t, err)
	assert.True(t, errors.As(err, &mismatch))
	assert.Equal(t, "primary key column set mismatch", mismatch.Reason)

	// 12. validateReloadSchema type mismatch
	tgtTypeMismatchPath := filepath.Join(tmpDir, "tgt_type.db")
	makeSQLiteFile(t, tgtTypeMismatchPath, "CREATE TABLE users (id TEXT PRIMARY KEY)")
	tgtTypeMismatch, err := dalgo2sqlite.NewDatabase(tgtTypeMismatchPath)
	require.NoError(t, err)

	err = validateReloadSchema(ctx, src, tgtTypeMismatch, &ref, BackendSQLite, BackendSQLite)
	assert.Error(t, err)
	assert.True(t, errors.As(err, &mismatch))
	assert.Equal(t, "type mismatch", mismatch.Reason)
}

func TestEngine_MoreCoverage(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. numCPU = 1 in resolveParallelism (line 305)
	origNumCPU := numCPU
	defer func() { numCPU = origNumCPU }()
	numCPU = func() int { return 1 }
	var stderr bytes.Buffer
	srcDBPath := filepath.Join(tmpDir, "src.db")
	makeSQLiteFile(t, srcDBPath, "CREATE TABLE t (id INT PRIMARY KEY)")
	src, _ := dalgo2sqlite.NewDatabase(srcDBPath)
	assert.Equal(t, 1, resolveParallelism(0, src, src, &stderr))
	numCPU = origNumCPU

	// 2. copyOneTable skips when DescribeCollection fails (lines 236-243)
	mockSrc := mockDescribeFailDB{
		DB:           src,
		SchemaReader: src,
	}
	tgtDBPath := filepath.Join(tmpDir, "tgt.db")
	makeSQLiteFile(t, tgtDBPath)
	tgt, _ := dalgo2sqlite.NewDatabase(tgtDBPath)
	summary, err := Copy(ctx, mockSrc, tgt, CopyOpts{Stderr: &stderr})
	assert.NoError(t, err)
	assert.Contains(t, summary.Skipped, "bad_tbl")
	assert.Contains(t, stderr.String(), "skipping \"bad_tbl\"")

	// 3. ddl.CreateCollection error in copyOneTable (lines 260-261)
	// Target already has "t", but empty, so emptyTargetCheck passes, but CREATE TABLE fails!
	tgtExistingPath := filepath.Join(tmpDir, "tgt_existing.db")
	makeSQLiteFile(t, tgtExistingPath, "CREATE TABLE t (id INT PRIMARY KEY)")
	tgtExisting, _ := dalgo2sqlite.NewDatabase(tgtExistingPath)
	_, err = Copy(ctx, src, tgtExisting, CopyOpts{})
	assert.ErrorContains(t, err, "create target collection")

	// 4. truncateTargetCollection error in reload mode (lines 256-257)
	mockTgtTruncFail := mockDeleteFailDB{
		DB:           tgtExisting,
		SchemaReader: tgtExisting,
	}
	_, err = Copy(ctx, src, mockTgtTruncFail, CopyOpts{Overwrite: "reload"})
	assert.ErrorContains(t, err, "truncate target")

	// 5. Worker loop returns on workerCtx.Err() != nil (lines 202-204)
	mockParSrc := mockCancelSource{DB: src, SchemaReader: src}
	mockParTgt := mockCancelTarget{DB: tgtExisting, SchemaReader: tgtExisting, SchemaModifier: tgtExisting}
	_, err = Copy(ctx, mockParSrc, mockParTgt, CopyOpts{
		ParallelStreams: 2,
		Overwrite:       "recreate",
	})
	assert.ErrorContains(t, err, "t1 fail")
}

type mockCancelSource struct {
	dal.DB
	dbschema.SchemaReader
}

func (m mockCancelSource) SupportsConcurrentConnections() bool { return true }
func (m mockCancelSource) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockCancelSource) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{
		dal.NewRootCollectionRef("t1", ""),
		dal.NewRootCollectionRef("t2", ""),
		dal.NewRootCollectionRef("t3", ""),
	}, nil
}

func (m mockCancelSource) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if ref.Name() == "t2" {
		<-ctx.Done()
		return nil, errors.New("skip t2")
	}
	return &dbschema.CollectionDef{
		Name:       ref.Name(),
		PrimaryKey: []dal.FieldName{"id"},
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}, nil
}

type mockCancelTarget struct {
	dal.DB
	dbschema.SchemaReader
	ddl.SchemaModifier
}

func (m mockCancelTarget) SupportsConcurrentConnections() bool { return true }
func (m mockCancelTarget) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockCancelTarget) CreateCollection(ctx context.Context, c dbschema.CollectionDef, opts ...ddl.Option) error {
	if c.Name == "t1" {
		return errors.New("t1 fail")
	}
	return nil
}

type mockDescribeFailDB struct {
	dal.DB
	dbschema.SchemaReader
}

func (m mockDescribeFailDB) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("bad_tbl", "")}, nil
}

func (m mockDescribeFailDB) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return nil, errors.New("cannot describe")
}

func (m mockDescribeFailDB) SupportsConcurrentConnections() bool { return true }
func (m mockDescribeFailDB) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2sqlite"} }

func TestEngineRows_And_Reload_MoreCoverage(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	makeSQLiteFile(t, dbPath, "CREATE TABLE t (id INT PRIMARY KEY)")
	db, _ := dalgo2sqlite.NewDatabase(dbPath)

	def := &dbschema.CollectionDef{
		Name:       "t",
		PrimaryKey: []dal.FieldName{"id"},
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}

	// 1. copyRows reader.Next() returns non-EOF error (line 146)
	mockSrcReadFail := mockQueryDB{DB: db, SchemaReader: db, readerErr: errors.New("read stream error")}
	_, err := copyRows(ctx, mockSrcReadFail, db, def, CopyOpts{})
	assert.ErrorContains(t, err, "read stream error")

	// 2. copyRows record.Data() is not map[string]any (lines 151-154)
	mockSrcBadData := mockSingleRecordDB{DB: db, SchemaReader: db, rec: record.NewRecordWithoutKey([]string{"slice"}).SetError(nil)}
	_, err = copyRows(ctx, mockSrcBadData, db, def, CopyOpts{})
	assert.ErrorContains(t, err, "unexpected Record data shape")

	// 3. ReloadSchemaMismatchError with SourceValue and TargetValue (lines 51-53)
	errMismatch := &ReloadSchemaMismatchError{Table: "t", Column: "c", Reason: "bad", SourceValue: "int", TargetValue: "text"}
	assert.Contains(t, errMismatch.Error(), "(source=int, target=text)")

	// 4. validateReloadSchema with DescribeCollection source error (lines 78-79)
	mockSrcDescFail := mockDescribeFailDB{DB: db, SchemaReader: db}
	ref := dal.NewCollectionRef("t", "", nil)
	err = validateReloadSchema(ctx, mockSrcDescFail, db, &ref, BackendSQLite, BackendSQLite)
	assert.ErrorContains(t, err, "reload: describe source")

	// 5. validateReloadSchema with unmappable type (lines 119-121)
	mockUnmappableSrc := mockUnmappableTypeDB{DB: db, SchemaReader: db}
	err = validateReloadSchema(ctx, mockUnmappableSrc, db, &ref, BackendSQLite, BackendSQLite)
	assert.ErrorContains(t, err, "cannot map source type")

	// 6. readAllRecordsForTruncate fallback to text query (lines 196-197)
	mockFallbackDB := mockFallbackQueryDB{DB: db, SchemaReader: db}
	reader, err := readAllRecordsForTruncate(ctx, mockFallbackDB, "t")
	assert.NoError(t, err)
	assert.NotNil(t, reader)
	_ = reader.Close()

	// 7. collectKeysToTruncate readAllRecordsForTruncate error (lines 295-296)
	mockReadFail := mockQueryDB{DB: db, SchemaReader: db, execErr: errors.New("exec fail")}
	_, err = collectKeysToTruncate(ctx, mockReadFail, "t")
	assert.ErrorContains(t, err, "read target \"t\" for truncate")

	// 8. collectKeysToTruncate reader.Next error (lines 306-307)
	mockNextFail := mockQueryDB{DB: db, SchemaReader: db, readerErr: errors.New("next fail")}
	_, err = collectKeysToTruncate(ctx, mockNextFail, "t")
	assert.ErrorContains(t, err, "read record from target \"t\"")

	// 9. collectKeysToTruncate targetRecordKey error (lines 310-311)
	mockKeyFail := mockSingleRecordDB{DB: db, SchemaReader: db, rec: record.NewRecordWithoutKey(map[string]any{"other": 1}).SetError(nil)}
	_, err = collectKeysToTruncate(ctx, mockKeyFail, "t")
	assert.ErrorContains(t, err, "missing PK column")

	// 10. truncateTargetCollection tx.Delete error (lines 264-265)
	mockTxDeleteFail := mockDeleteFailDB{DB: db, SchemaReader: db}
	err = truncateTargetCollection(ctx, mockTxDeleteFail, "t")
	assert.ErrorContains(t, err, "delete record")

	// 11. copyRows buildTargetKey error for dalgo2ingitdb when PK missing (engine_rows.go:158, 191)
	mockIngitTgt := mockIngitTargetDB{DB: db, SchemaReader: db}
	mockSrcMissingPK := mockSingleRecordDB{
		DB:           db,
		SchemaReader: db,
		rec:          record.NewRecordWithoutKey(map[string]any{"other": 1}).SetError(nil),
	}
	_, err = copyRows(ctx, mockSrcMissingPK, mockIngitTgt, def, CopyOpts{})
	assert.ErrorContains(t, err, "missing PK column")

	// 12. truncateTargetCollection collectKeysToTruncate error (reload.go:255)
	err = truncateTargetCollection(ctx, mockReadFail, "t")
	assert.ErrorContains(t, err, "read target \"t\" for truncate")
}

type mockIngitTargetDB struct {
	dal.DB
	dbschema.SchemaReader
}

func (m mockIngitTargetDB) SupportsConcurrentConnections() bool { return true }
func (m mockIngitTargetDB) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2ingitdb"} }

type mockSingleRecordDB struct {
	dal.DB
	dbschema.SchemaReader
	rec record.Record
}

func (m mockSingleRecordDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	return &singleRecordReader{rec: m.rec}, nil
}
func (m mockSingleRecordDB) SupportsConcurrentConnections() bool { return true }
func (m mockSingleRecordDB) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2sqlite"} }

type singleRecordReader struct {
	rec  record.Record
	done bool
}

func (s *singleRecordReader) Close() error            { return nil }
func (s *singleRecordReader) Cursor() (string, error) { return "", nil }
func (s *singleRecordReader) Next() (record.Record, error) {
	if s.done {
		return nil, dal.ErrNoMoreRecords
	}
	s.done = true
	return s.rec, nil
}

type mockUnmappableTypeDB struct {
	dal.DB
	dbschema.SchemaReader
}

func (m mockUnmappableTypeDB) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name:       "t",
		PrimaryKey: []dal.FieldName{"id"},
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Null}},
	}, nil
}
func (m mockUnmappableTypeDB) SupportsConcurrentConnections() bool { return true }
func (m mockUnmappableTypeDB) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2sqlite"} }

type mockFallbackQueryDB struct {
	dal.DB
	dbschema.SchemaReader
}

func (m mockFallbackQueryDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	if _, ok := query.(dal.TextQuery); ok {
		return &singleRecordReader{done: true}, nil
	}
	return nil, errors.New("structured query not supported")
}
func (m mockFallbackQueryDB) SupportsConcurrentConnections() bool { return true }
func (m mockFallbackQueryDB) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2sqlite"} }

type mockDeleteFailDB struct {
	dal.DB
	dbschema.SchemaReader
}

func (m mockDeleteFailDB) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name:       "t",
		PrimaryKey: []dal.FieldName{"id"},
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}, nil
}

func (m mockDeleteFailDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	k := record.NewKeyWithID("t", "1")
	rec := record.NewRecordWithData(k, map[string]any{"id": 1})
	return &singleRecordReader{rec: rec}, nil
}

func (m mockDeleteFailDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, opts ...dal.TransactionOption) error {
	return worker(ctx, mockFailingTx{})
}
func (m mockDeleteFailDB) SupportsConcurrentConnections() bool { return true }
func (m mockDeleteFailDB) Adapter() dal.Adapter                { return mockAdapter{name: "dalgo2sqlite"} }

type mockFailingTx struct {
	dal.ReadwriteTransaction
}

func (m mockFailingTx) Delete(ctx context.Context, key *record.Key) error {
	return errors.New("injected delete fail")
}
