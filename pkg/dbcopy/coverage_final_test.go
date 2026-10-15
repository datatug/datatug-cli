package dbcopy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	bqwriter "github.com/dal-go/dalgo2bigquery"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Not parallel: tests table mapping error and EncodeRow newline termination.
func TestBigQueryTarget_PreflightAndEncodeRow_FinalCov(t *testing.T) {
	// Table mapping error: invalid UTF-8 table name in table.Ref.Name()
	sink := BigQueryCopySink{Writer: &bqwriter.LoadWriter{}}
	badTable := CopyTable{
		Ref: dal.NewRootCollectionRef("bad\xffname", ""),
		Definition: &dbschema.CollectionDef{
			Name: "bad\xffname",
			Fields: []dbschema.FieldDef{
				{Name: "id", Type: dbschema.Int},
			},
		},
	}
	_, err := sink.Preflight(context.Background(), []CopyTable{badTable})
	require.ErrorContains(t, err, "map source collection")

	// EncodeRow valid row returning newline-terminated JSON
	plan := &bigQueryCopyPlan{
		tables: map[string]*bigQueryMappedTable{
			"users": {
				targetName: "users",
				fields: []bigQueryMappedField{
					{
						sourceName: "id",
						target:     bqwriter.Field{Name: "id", Type: "INT64"},
						typeCode:   dbschema.Int,
					},
				},
			},
		},
	}
	tbl := CopyTable{
		Ref: dal.NewRootCollectionRef("users", ""),
		Definition: &dbschema.CollectionDef{
			Name: "users",
			Fields: []dbschema.FieldDef{
				{Name: "id", Type: dbschema.Int},
			},
		},
	}
	encoded, err := plan.EncodeRow(tbl, dbschema.SourceRow{
		Values: map[string]any{"id": int64(42)},
	})
	require.NoError(t, err)
	require.True(t, len(encoded) > 0 && encoded[len(encoded)-1] == '\n')
}

// Not parallel: mutates exportRecordsWriterNewBuffered.
func TestExportRecordsWriter_WriteByteFail_FinalCov(t *testing.T) {
	row := map[string]any{"key": "val"}
	withID := map[string]any{"key": "val", "$ID": "1"}
	data, err := json.Marshal(withID)
	require.NoError(t, err)

	bw := bufio.NewWriterSize(failingWriter{err: errors.New("underlying fail")}, len(data))

	w := &exportRecordsWriter{
		format:   ingitdb.RecordFormatJSONL,
		buffered: bw,
	}
	err = w.Write("1", row)
	require.ErrorContains(t, err, "underlying fail")
}

// Not parallel: mutates storageClassFileSync and storageClassFileClose.
func TestExportStorageClasses_FinalCov(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. finishFile buffer Flush error: write data into buffer, then close file descriptor.
	w1 := newStorageClassWriter(tmpDir)
	f1, err := os.Create(filepath.Join(tmpDir, "flush_fail.jsonl"))
	require.NoError(t, err)
	w1.file = f1
	w1.buffer = bufio.NewWriter(f1)
	_, _ = w1.buffer.WriteString("test line")
	_ = f1.Close() // close underlying file
	err = w1.finishFile()
	require.Error(t, err)

	// 2. finishFile Sync error via storageClassFileSync seam
	origSync := storageClassFileSync
	defer func() { storageClassFileSync = origSync }()
	storageClassFileSync = func(f *os.File) error { return errors.New("sync failed") }

	w2 := newStorageClassWriter(tmpDir)
	f2, err := os.Create(filepath.Join(tmpDir, "sync_fail.jsonl"))
	require.NoError(t, err)
	defer func() { _ = f2.Close() }()
	w2.file = f2
	w2.buffer = bufio.NewWriter(f2)
	err = w2.finishFile()
	require.ErrorContains(t, err, "sync failed")

	// 3. finishFile Close error via storageClassFileClose seam
	storageClassFileSync = origSync
	origClose := storageClassFileClose
	defer func() { storageClassFileClose = origClose }()
	storageClassFileClose = func(f *os.File) error { return errors.New("close failed") }

	w3 := newStorageClassWriter(tmpDir)
	f3, err := os.Create(filepath.Join(tmpDir, "close_fail.jsonl"))
	require.NoError(t, err)
	defer func() { _ = f3.Close() }()
	w3.file = f3
	w3.buffer = bufio.NewWriter(f3)
	err = w3.finishFile()
	require.ErrorContains(t, err, "close failed")

	// 4. rotate error when finishFile fails
	w4 := newStorageClassWriter(tmpDir)
	f4, err := os.Create(filepath.Join(tmpDir, "rotate_fail.jsonl"))
	require.NoError(t, err)
	defer func() { _ = f4.Close() }()
	w4.file = f4
	w4.buffer = bufio.NewWriter(f4)
	err = w4.rotate()
	require.ErrorContains(t, err, "close failed")

	// 5. Close error when finishFile fails
	w5 := newStorageClassWriter(tmpDir)
	f5, err := os.Create(filepath.Join(tmpDir, "close_finish_fail.jsonl"))
	require.NoError(t, err)
	defer func() { _ = f5.Close() }()
	w5.file = f5
	w5.buffer = bufio.NewWriter(f5)
	_, err = w5.Close()
	require.ErrorContains(t, err, "close failed")
	storageClassFileClose = origClose
}

type mockFailingDBReader struct {
	dal.DB
	mockBaseSchemaReader
	queryErr error
}

func (m mockFailingDBReader) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }
func (m mockFailingDBReader) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("t", "")}, nil
}
func (m mockFailingDBReader) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name:   "t",
		Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}, nil
}
func (m mockFailingDBReader) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return nil, errors.New("query failed")
}

type mockCloseFailRecordsReader struct {
	rec record.Record
}

func (m *mockCloseFailRecordsReader) Cursor() (string, error) { return "", nil }
func (m *mockCloseFailRecordsReader) Next() (record.Record, error) {
	if m.rec != nil {
		r := m.rec
		m.rec = nil
		return r, nil
	}
	return nil, io.EOF
}
func (m *mockCloseFailRecordsReader) Close() error { return errors.New("reader close fail") }

type mockCloseFailDB struct {
	dal.DB
	mockBaseSchemaReader
}

func (m mockCloseFailDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }
func (m mockCloseFailDB) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("t", "")}, nil
}
func (m mockCloseFailDB) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name:   "t",
		Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}, nil
}
func (m mockCloseFailDB) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	k := record.NewKeyWithID("t", "1")
	rec := record.NewRecordWithData(k, map[string]any{"id": int64(1)})
	return &mockCloseFailRecordsReader{rec: rec}, nil
}

type mockQueryOnlyNonMapDB struct {
	dal.DB
	mockBaseSchemaReader
}

func (m mockQueryOnlyNonMapDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }
func (m mockQueryOnlyNonMapDB) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")}, nil
}
func (m mockQueryOnlyNonMapDB) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name:   "tbl",
		Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}, nil
}
func (m mockQueryOnlyNonMapDB) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	k := record.NewKeyWithID("tbl", "1")
	rec := record.NewRecordWithData(k, "string_not_map")
	return &singleRecordReader{rec: rec}, nil
}

// Not parallel: mutates seams in transfer.go.
func TestTransfer_FinalCov(t *testing.T) {
	// 1. removeCopyStagingDir error joined with existing retErr (transfer.go line 107)
	origRemove := removeCopyStagingDir
	defer func() { removeCopyStagingDir = origRemove }()
	removeCopyStagingDir = func(string) error { return errors.New("rm staging fail") }

	validSource := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
	}

	planPrepFail := mockSinkPlan{prepErr: errors.New("prepare failed")}
	sinkPrepFail := mockSink{plan: planPrepFail}
	_, err := CopyToSink(context.Background(), validSource, sinkPrepFail, CopyOpts{})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrStagingCleanup))
	removeCopyStagingDir = origRemove

	// 2. copyWriterFlush error in staging loop (transfer.go line 132)
	origFlush := copyWriterFlush
	defer func() { copyWriterFlush = origFlush }()
	copyWriterFlush = func(w *bufio.Writer) error { return errors.New("flush staged error") }

	sinkOK := mockSink{plan: mockSinkPlan{}}
	_, err = CopyToSink(context.Background(), validSource, sinkOK, CopyOpts{})
	require.ErrorContains(t, err, "flush staged collection")
	copyWriterFlush = origFlush

	// 3. copyCloseFile error in staging loop (transfer.go line 135)
	origClose := copyCloseFile
	defer func() { copyCloseFile = origClose }()
	copyCloseFile = func(f *os.File) error { return errors.New("close staged error") }

	_, err = CopyToSink(context.Background(), validSource, sinkOK, CopyOpts{})
	require.ErrorContains(t, err, "close staged collection")

	// 4. copyCloseFile error in loading loop (transfer.go line 163)
	closeCalls := 0
	copyCloseFile = func(f *os.File) error {
		closeCalls++
		if closeCalls > 1 { // second close happens in loading loop
			return errors.New("close staged in load error")
		}
		return f.Close()
	}
	_, err = CopyToSink(context.Background(), validSource, sinkOK, CopyOpts{})
	require.ErrorContains(t, err, "close staged collection")
	copyCloseFile = origClose

	// 5. EncodeRow error in stageCopyRows (transfer.go line 182)
	sourceWithRow := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
		rowsReader: mockSourceRowsFailDB{
			cursor: &mockCursor{
				rows: []dbschema.SourceRow{
					{Values: map[string]any{"id": 1}},
				},
			},
		},
	}
	sinkEncodeFail := mockSink{plan: mockSinkPlan{encodeErr: errors.New("encode row fail")}}
	_, err = CopyToSink(context.Background(), sourceWithRow, sinkEncodeFail, CopyOpts{})
	require.ErrorContains(t, err, "encode source row from")

	// 6. ExecuteQueryToRecordsReader error in stageCopyRows with row filters (transfer.go line 244)
	optsWithFilter := CopyOpts{
		Filters: &filter.Directives{
			LimitsByTable: map[string]int{"t": 10},
		},
	}
	sinkPlanOK := mockSinkPlan{encodeLine: []byte("{\"id\":1}\n")}
	_, err = CopyToSink(context.Background(), mockFailingDBReader{queryErr: errors.New("exec query fail")}, mockSink{plan: sinkPlanOK}, optsWithFilter)
	require.ErrorContains(t, err, "query source collection")

	// 7. reader.Close() error in stageCopyRows with row filters (transfer.go line 248)
	_, err = CopyToSink(context.Background(), mockCloseFailDB{}, mockSink{plan: sinkPlanOK}, optsWithFilter)
	require.ErrorContains(t, err, "close recordset reader for")
}

type mockExportCheckDB struct {
	dal.DB
	mockBaseSchemaReader
	refs        []dal.CollectionRef
	defs        map[string]*dbschema.CollectionDef
	describeErr error
	views       []dbschema.SourceViewDef
	viewsErr    error
	rowsReader  dbschema.SourceRowsReader
	openRowsErr error
}

func (m mockExportCheckDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }
func (m mockExportCheckDB) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return m.refs, nil
}
func (m mockExportCheckDB) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if m.describeErr != nil {
		return nil, m.describeErr
	}
	if def, ok := m.defs[ref.Name()]; ok {
		return def, nil
	}
	return nil, nil
}
func (m mockExportCheckDB) ListSourceViews(ctx context.Context) ([]dbschema.SourceViewDef, error) {
	if m.viewsErr != nil {
		return nil, m.viewsErr
	}
	return m.views, nil
}
func (m mockExportCheckDB) OpenSourceRows(ctx context.Context, ref *dal.CollectionRef) (dbschema.SourceRowCursor, error) {
	if m.openRowsErr != nil {
		return nil, m.openRowsErr
	}
	if m.rowsReader != nil {
		return m.rowsReader.OpenSourceRows(ctx, ref)
	}
	return &mockCursor{}, nil
}
func (m mockExportCheckDB) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	k := record.NewKeyWithID("tbl", "1")
	rec := record.NewRecordWithData(k, map[string]any{"id": int64(1)})
	return &singleRecordReader{rec: rec}, nil
}

// Not parallel: tests ExportInGitDB validation and error branches.
func TestExportInGitDB_ValidationErrors_FinalCov(t *testing.T) {
	tmpDir := t.TempDir()

	// Line 61: len(refs) == 0 -> ErrSourceHasNoTables
	_, err := ExportInGitDB(context.Background(), mockEmptyCollectionsDB{}, filepath.Join(tmpDir, "empty"), ExportOptions{})
	require.ErrorIs(t, err, ErrSourceHasNoTables)

	// Line 70: ref.Schema() != ""
	dbWithSchema := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewQualifiedRootCollectionRef("myschema", "tbl", "")},
	}
	_, err = ExportInGitDB(context.Background(), dbWithSchema, filepath.Join(tmpDir, "schema"), ExportOptions{})
	require.ErrorContains(t, err, "has a namespace, database, or parent")

	// Line 74: nativeCollectionID error (control char in name)
	dbEmptyName := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("bad\x01name", "")},
	}
	_, err = ExportInGitDB(context.Background(), dbEmptyName, filepath.Join(tmpDir, "empty_name"), ExportOptions{})
	require.ErrorContains(t, err, "unsafe collection name")

	// Line 78: INGR format safeINGRHeaderName error
	dbBadINGRName := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("bad\nname", "")},
	}
	_, err = ExportInGitDB(context.Background(), dbBadINGRName, filepath.Join(tmpDir, "bad_ingr"), ExportOptions{RecordsFormat: "ingr"})
	require.Error(t, err)

	// Line 82: duplicate source collection name
	dbDupName := mockExportCheckDB{
		refs: []dal.CollectionRef{
			dal.NewRootCollectionRef("users", ""),
			dal.NewRootCollectionRef("users", ""),
		},
		defs: map[string]*dbschema.CollectionDef{
			"users": {
				Name: "users",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbDupName, filepath.Join(tmpDir, "dup_name"), ExportOptions{})
	require.ErrorContains(t, err, "duplicate source collection name")

	// Line 86: nativeIDs collision via exportNativeCollectionID seam
	origNativeID := exportNativeCollectionID
	defer func() { exportNativeCollectionID = origNativeID }()
	exportNativeCollectionID = func(name string) (string, error) { return "collided_id", nil }
	dbCollision := mockExportCheckDB{
		refs: []dal.CollectionRef{
			dal.NewRootCollectionRef("table1", ""),
			dal.NewRootCollectionRef("table2", ""),
		},
		defs: map[string]*dbschema.CollectionDef{
			"table1": {
				Name: "table1",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
			"table2": {
				Name: "table2",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbCollision, filepath.Join(tmpDir, "collision"), ExportOptions{})
	require.ErrorContains(t, err, "map to the same native ID")
	exportNativeCollectionID = origNativeID

	// Line 90: caseFoldedIDs collision (e.g. "Users" and "users")
	dbCaseFold := mockExportCheckDB{
		refs: []dal.CollectionRef{
			dal.NewRootCollectionRef("Users", ""),
			dal.NewRootCollectionRef("users", ""),
		},
		defs: map[string]*dbschema.CollectionDef{
			"Users": {
				Name: "Users",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
			"users": {
				Name: "users",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbCaseFold, filepath.Join(tmpDir, "casefold"), ExportOptions{})
	require.ErrorContains(t, err, "alias on case-insensitive filesystems")

	// Line 96: DescribeCollection error
	dbDescribeFail := mockExportCheckDB{
		refs:        []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		describeErr: errors.New("describe failed"),
	}
	_, err = ExportInGitDB(context.Background(), dbDescribeFail, filepath.Join(tmpDir, "desc_fail"), ExportOptions{})
	require.ErrorContains(t, err, "describe source collection")

	// Line 99: DescribeCollection inconsistent schema
	_, err = ExportInGitDB(context.Background(), mockInconsistentSchemaDB{}, filepath.Join(tmpDir, "inconsistent"), ExportOptions{})
	require.ErrorContains(t, err, "returned inconsistent schema")

	// Line 103: field name with control character
	dbBadField := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "bad\x01field", Type: dbschema.String},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbBadField, filepath.Join(tmpDir, "bad_field"), ExportOptions{})
	require.ErrorContains(t, err, "unsafe source field name")

	// Line 107: INGR format field name safeINGRHeaderName error
	dbBadINGRField := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "field:with:colon", Type: dbschema.String},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbBadINGRField, filepath.Join(tmpDir, "bad_ingr_field"), ExportOptions{RecordsFormat: "ingr"})
	require.Error(t, err)

	// Line 114: reserved field $ID in JSONL
	dbReservedID := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "$ID", Type: dbschema.String},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbReservedID, filepath.Join(tmpDir, "reserved_id"), ExportOptions{RecordsFormat: "jsonl"})
	require.ErrorContains(t, err, "contains reserved transport field $ID")

	// Line 119: ExportCollectionDefinition error (e.g. duplicate field name)
	dbDupField := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "col", Type: dbschema.Type(120)},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbDupField, filepath.Join(tmpDir, "dup_field"), ExportOptions{})
	require.ErrorContains(t, err, "map source collection")

	// Line 124: validateExportRelationships error (foreign key references unknown table)
	dbBadFK := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("orders", "")},
		defs: map[string]*dbschema.CollectionDef{
			"orders": {
				Name: "orders",
				Fields: []dbschema.FieldDef{
					{Name: "user_id", Type: dbschema.Int},
				},
				ForeignKeys: []dbschema.ForeignKeyDef{
					{
						Fields:               []dal.FieldName{"user_id"},
						ReferencedCollection: "nonexistent_users",
						ReferencedFields:     []dal.FieldName{"id"},
					},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbBadFK, filepath.Join(tmpDir, "bad_fk"), ExportOptions{})
	require.ErrorContains(t, err, "missing collection")

	// Line 138: destination already exists
	existingDest := filepath.Join(tmpDir, "exists")
	require.NoError(t, os.Mkdir(existingDest, 0o755))
	validDB := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), validDB, existingDest, ExportOptions{})
	require.ErrorContains(t, err, "destination already exists")

	// Line 144: destination parent does not exist
	nonexistentParent := filepath.Join(tmpDir, "no_parent", "sub", "dest")
	_, err = ExportInGitDB(context.Background(), validDB, nonexistentParent, ExportOptions{})
	require.ErrorContains(t, err, "destination parent")
}

// Not parallel: mutates seams in export_ingitdb.go.
func TestExportInGitDB_SeamsAndPublish_FinalCov(t *testing.T) {
	tmpDir := t.TempDir()

	validDB := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
		views: []dbschema.SourceViewDef{
			{Name: "v_tbl"},
		},
	}

	// Line 162: ctx cancelled before exportCollection loop
	cancCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ExportInGitDB(cancCtx, validDB, filepath.Join(tmpDir, "canc1"), ExportOptions{})
	require.ErrorIs(t, err, context.Canceled)

	// Line 199: exportYAMLMarshal fails on views
	origYAMLMarshal := exportYAMLMarshal
	defer func() { exportYAMLMarshal = origYAMLMarshal }()
	exportYAMLMarshal = func(in any) ([]byte, error) {
		if _, ok := in.([]dbschema.SourceViewDef); ok {
			return nil, errors.New("views yaml fail")
		}
		return yaml.Marshal(in)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "views_yaml_fail"), ExportOptions{})
	require.ErrorContains(t, err, "views yaml fail")
	exportYAMLMarshal = origYAMLMarshal

	// Line 214: exportReadDefinition fails
	origReadDef := exportReadDefinition
	defer func() { exportReadDefinition = origReadDef }()
	exportReadDefinition = func(dir string, opts ...ingitdb.ReadOption) (*ingitdb.Definition, error) {
		return nil, errors.New("read def fail")
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "read_def_fail"), ExportOptions{})
	require.ErrorContains(t, err, "staged native definition invalid")
	exportReadDefinition = origReadDef

	// Line 220: exportPublishExclusive fails
	origPublish := exportPublishExclusive
	defer func() { exportPublishExclusive = origPublish }()
	exportPublishExclusive = func(stage, dest string) error {
		return errors.New("publish exclusive fail")
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "publish_fail"), ExportOptions{})
	require.ErrorContains(t, err, "publish export")
	exportPublishExclusive = origPublish

	// Line 306: exportValidateCollectionID fails in nativeCollectionID
	origValColID := exportValidateCollectionID
	defer func() { exportValidateCollectionID = origValColID }()
	exportValidateCollectionID = func(id string) error {
		return errors.New("validate col id fail")
	}
	_, err = nativeCollectionID("valid_name")
	require.ErrorContains(t, err, "validate col id fail")
	exportValidateCollectionID = origValColID

	// Line 373: exportValidateColDef fails
	origValColDef := exportValidateColDef
	defer func() { exportValidateColDef = origValColDef }()
	exportValidateColDef = func(c *ingitdb.CollectionDef) error {
		return errors.New("validate col def fail")
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "col_def_fail"), ExportOptions{})
	require.ErrorContains(t, err, "validate col def fail")
	exportValidateColDef = origValColDef

	// Line 442: newExportIDIndex fails via exportCreateTemp error
	origCreateTemp := exportCreateTemp
	defer func() { exportCreateTemp = origCreateTemp }()
	exportCreateTemp = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("create temp index fail")
	}
	dbWithPK := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name:       "tbl",
				PrimaryKey: []dal.FieldName{"id"},
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbWithPK, filepath.Join(tmpDir, "id_index_fail"), ExportOptions{})
	require.ErrorContains(t, err, "create temp index fail")
	exportCreateTemp = origCreateTemp

	// Line 487: exportFileSync fails in exportCollection
	origFileSync := exportFileSync
	defer func() { exportFileSync = origFileSync }()
	exportFileSync = func(f *os.File) error {
		return errors.New("file sync fail")
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "sync_fail"), ExportOptions{})
	require.ErrorContains(t, err, "file sync fail")
	exportFileSync = origFileSync

	// Unit tests for encodeExportValue (lines 680, 691, 715)
	vInt, err := encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, json.Number("123"))
	require.NoError(t, err)
	require.Equal(t, int64(123), vInt)

	vBool, err := encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, true)
	require.NoError(t, err)
	require.Equal(t, true, vBool)

	vFloat, err := encodeExportValue(dbschema.FieldDef{Type: dbschema.Float}, float64(3.14))
	require.NoError(t, err)
	require.Equal(t, float64(3.14), vFloat)
}

// Not parallel: mutates seams in export_ingitdb.go.
func TestExportInGitDB_RemainingBranches_Cov100(t *testing.T) {
	tmpDir := t.TempDir()

	validDB := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
	}

	// Line 78: safeINGRHeaderName error in ExportInGitDB
	origNativeID := exportNativeCollectionID
	defer func() { exportNativeCollectionID = origNativeID }()
	exportNativeCollectionID = func(string) (string, error) { return "bad:id", nil }
	_, err := ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "bad_ingr_header"), ExportOptions{RecordsFormat: "ingr"})
	require.Error(t, err)
	exportNativeCollectionID = origNativeID

	// Line 135: exportFilepathAbs error
	origAbs := exportFilepathAbs
	defer func() { exportFilepathAbs = origAbs }()
	exportFilepathAbs = func(string) (string, error) { return "", errors.New("abs fail") }
	_, err = ExportInGitDB(context.Background(), validDB, "dest", ExportOptions{})
	require.ErrorContains(t, err, "abs fail")
	exportFilepathAbs = origAbs

	// Line 140: exportOSLstat non-ErrNotExist error
	origLstat := exportOSLstat
	defer func() { exportOSLstat = origLstat }()
	exportOSLstat = func(string) (os.FileInfo, error) { return nil, errors.New("lstat fail") }
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "lstat_fail"), ExportOptions{})
	require.ErrorContains(t, err, "lstat fail")
	exportOSLstat = origLstat

	// Line 148: exportOSMkdirTemp error
	origMkdirTemp := exportOSMkdirTemp
	defer func() { exportOSMkdirTemp = origMkdirTemp }()
	exportOSMkdirTemp = func(string, string) (string, error) { return "", errors.New("mkdir temp fail") }
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "mkdirtemp_fail"), ExportOptions{})
	require.ErrorContains(t, err, "create export staging directory")
	exportOSMkdirTemp = origMkdirTemp

	// Line 155: exportReserveCollectionDirs error
	origReserve := exportReserveCollectionDirs
	defer func() { exportReserveCollectionDirs = origReserve }()
	exportReserveCollectionDirs = func(string, []exportTable, func(string, os.FileMode) error) error {
		return errors.New("reserve fail")
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "reserve_fail"), ExportOptions{})
	require.ErrorContains(t, err, "reserve fail")
	exportReserveCollectionDirs = origReserve

	// Line 172: exportOSMkdirAll (.ingitdb dir) error
	origMkdirAll := exportOSMkdirAll
	defer func() { exportOSMkdirAll = origMkdirAll }()
	exportOSMkdirAll = func(path string, perm os.FileMode) error {
		if strings.HasSuffix(path, ".ingitdb") {
			return errors.New("ingitdb dir fail")
		}
		return os.MkdirAll(path, perm)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "mkdir_ingitdb_fail"), ExportOptions{})
	require.ErrorContains(t, err, "ingitdb dir fail")
	exportOSMkdirAll = origMkdirAll

	// Line 184: exportOSWriteFile (source-collections.json) error
	origWriteFile := exportOSWriteFile
	defer func() { exportOSWriteFile = origWriteFile }()
	exportOSWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, "source-collections.json") {
			return errors.New("write source-collections fail")
		}
		return os.WriteFile(path, data, perm)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "write_colls_fail"), ExportOptions{})
	require.ErrorContains(t, err, "write source-collections fail")
	exportOSWriteFile = origWriteFile

	// Line 202: exportOSWriteFile (source-views.yaml) error
	dbWithViews := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name:   "tbl",
				Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
			},
		},
		views: []dbschema.SourceViewDef{{Name: "v_tbl"}},
	}
	exportOSWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, "source-views.yaml") {
			return errors.New("write views fail")
		}
		return os.WriteFile(path, data, perm)
	}
	_, err = ExportInGitDB(context.Background(), dbWithViews, filepath.Join(tmpDir, "write_views_fail"), ExportOptions{})
	require.ErrorContains(t, err, "write views fail")

	// Line 208: exportYAMLMarshal (root-collections.yaml) error
	origRegYAML := exportYAMLMarshal
	exportYAMLMarshal = func(in any) ([]byte, error) {
		if _, ok := in.(map[string]string); ok {
			return nil, errors.New("registry yaml fail")
		}
		return origRegYAML(in)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "reg_fail"), ExportOptions{})
	require.ErrorContains(t, err, "registry yaml fail")
	exportYAMLMarshal = origRegYAML

	// Line 211: exportOSWriteFile (root-collections.yaml) error
	exportOSWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, "root-collections.yaml") {
			return errors.New("write root fail")
		}
		return os.WriteFile(path, data, perm)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "write_root_fail"), ExportOptions{})
	require.ErrorContains(t, err, "write root fail")
	exportOSWriteFile = origWriteFile

	// Line 377: exportOSMkdirAll (col schema dir) error
	exportOSMkdirAll = func(path string, perm os.FileMode) error {
		if strings.HasSuffix(path, ingitdb.SchemaDir) {
			return errors.New("schema dir fail")
		}
		return os.MkdirAll(path, perm)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "col_schema_dir_fail"), ExportOptions{})
	require.ErrorContains(t, err, "schema dir fail")
	exportOSMkdirAll = origMkdirAll

	// Line 381: exportYAMLMarshal (colDef) error
	origYAML := exportYAMLMarshal
	defer func() { exportYAMLMarshal = origYAML }()
	exportYAMLMarshal = func(v any) ([]byte, error) {
		if _, ok := v.(*ingitdb.CollectionDef); ok {
			return nil, errors.New("coldef yaml fail")
		}
		return yaml.Marshal(v)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "coldef_yaml_fail"), ExportOptions{})
	require.ErrorContains(t, err, "coldef yaml fail")
	exportYAMLMarshal = origYAML

	// Line 384: exportOSWriteFile (colDef) error
	exportOSWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, ingitdb.CollectionDefFileName) {
			return errors.New("write coldef file fail")
		}
		return os.WriteFile(path, data, perm)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "write_coldef_fail"), ExportOptions{})
	require.ErrorContains(t, err, "write coldef file fail")
	exportOSWriteFile = origWriteFile

	// Line 388: exportOSCreate (record file) error
	origCreate := exportOSCreate
	defer func() { exportOSCreate = origCreate }()
	exportOSCreate = func(path string) (*os.File, error) {
		return nil, errors.New("create record file fail")
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "create_record_file_fail"), ExportOptions{})
	require.ErrorContains(t, err, "create record file fail")
	exportOSCreate = origCreate

	// Line 393: exportNewRecordsWriter error
	origNewWriter := exportNewRecordsWriter
	defer func() { exportNewRecordsWriter = origNewWriter }()
	exportNewRecordsWriter = func(io.Writer, ingitdb.RecordFormat, string, []dbschema.FieldDef) (*exportRecordsWriter, error) {
		return nil, errors.New("new records writer fail")
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "new_writer_fail"), ExportOptions{})
	require.ErrorContains(t, err, "new records writer fail")
	exportNewRecordsWriter = origNewWriter

	// Line 409: OpenSourceRows error
	dbOpenFail := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name:   "tbl",
				Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
			},
		},
		openRowsErr: errors.New("open rows fail"),
	}
	_, err = ExportInGitDB(context.Background(), dbOpenFail, filepath.Join(tmpDir, "open_rows_fail"), ExportOptions{})
	require.ErrorContains(t, err, "open rows fail")

	// Line 419: ExecuteQueryToRecordsReader error in exportCollection (source without SourceRowsReader)
	dbQueryFail := mockFailingDBReader{queryErr: errors.New("query source rows fail")}
	_, err = ExportInGitDB(context.Background(), dbQueryFail, filepath.Join(tmpDir, "query_fail"), ExportOptions{})
	require.ErrorContains(t, err, "read source rows")

	// Line 429: rec.Data() not map[string]any
	dbNonMap := mockQueryOnlyNonMapDB{}
	_, err = ExportInGitDB(context.Background(), dbNonMap, filepath.Join(tmpDir, "non_map"), ExportOptions{})
	require.ErrorContains(t, err, "unsupported shape")

	// Line 448: ctx cancelled during collection export loop
	cancCtx, cancel := context.WithCancel(context.Background())
	dbCanc := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name:   "tbl",
				Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
			},
		},
		rowsReader: mockSourceRowsFailDB{
			cursor: &cancelingCursor{cancel: cancel},
		},
	}
	_, err = ExportInGitDB(cancCtx, dbCanc, filepath.Join(tmpDir, "canc_loop"), ExportOptions{})
	require.ErrorIs(t, err, context.Canceled)

	// Line 458: classAvailability.Check error (storage class availability changed for decimal field)
	dbBadStorage := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
					{Name: "dec", Type: dbschema.Decimal},
				},
			},
		},
		rowsReader: mockSourceRowsFailDB{
			cursor: &mockCursor{
				rows: []dbschema.SourceRow{
					{Values: map[string]any{"id": int64(1), "dec": "12.34"}, StorageClasses: map[string]string{"dec": "text"}},
					{Values: map[string]any{"id": int64(2), "dec": "56.78"}, StorageClasses: nil},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbBadStorage, filepath.Join(tmpDir, "bad_storage"), ExportOptions{})
	require.ErrorContains(t, err, "physical storage-class metadata availability changed")

	// Line 465: encodeExportRow error (invalid type for int)
	dbEncodeFail := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name:   "tbl",
				Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
			},
		},
		rowsReader: mockSourceRowsFailDB{
			cursor: &mockCursor{
				rows: []dbschema.SourceRow{
					{Values: map[string]any{"id": "not_an_int"}},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbEncodeFail, filepath.Join(tmpDir, "encode_fail"), ExportOptions{})
	require.ErrorContains(t, err, "integer arrived as string")

	// Line 468: datavalidator.ValidateRecordData returns findings
	dbValidatorFail := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name: "tbl",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int, Nullable: false},
				},
			},
		},
		rowsReader: mockSourceRowsFailDB{
			cursor: &mockCursor{
				rows: []dbschema.SourceRow{
					{Values: map[string]any{"id": nil}},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbValidatorFail, filepath.Join(tmpDir, "validator_fail"), ExportOptions{})
	require.ErrorContains(t, err, "is invalid in native schema")

	// Line 472: idIndex.Add duplicate PK
	dbDupPK := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name:       "tbl",
				PrimaryKey: []dal.FieldName{"id"},
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
				},
			},
		},
		rowsReader: mockSourceRowsFailDB{
			cursor: &mockCursor{
				rows: []dbschema.SourceRow{
					{Values: map[string]any{"id": int64(1)}},
					{Values: map[string]any{"id": int64(1)}},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbDupPK, filepath.Join(tmpDir, "dup_pk"), ExportOptions{})
	require.ErrorContains(t, err, "duplicate or invalid source primary key")

	// Line 476: storage.Append fails via storageJSONMarshal error
	origStorageJSON := storageJSONMarshal
	defer func() { storageJSONMarshal = origStorageJSON }()
	storageJSONMarshal = func(any) ([]byte, error) { return nil, errors.New("storage json fail") }
	makeDecDB := func() mockExportCheckDB {
		return mockExportCheckDB{
			refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
			defs: map[string]*dbschema.CollectionDef{
				"tbl": {
					Name: "tbl",
					Fields: []dbschema.FieldDef{
						{Name: "dec", Type: dbschema.Decimal},
					},
				},
			},
			rowsReader: mockSourceRowsFailDB{
				cursor: &mockCursor{
					rows: []dbschema.SourceRow{
						{
							Values:         map[string]any{"dec": "12.34"},
							StorageClasses: map[string]string{"dec": "text"},
						},
					},
				},
			},
		}
	}
	_, err = ExportInGitDB(context.Background(), makeDecDB(), filepath.Join(tmpDir, "storage_append_fail"), ExportOptions{})
	require.ErrorContains(t, err, "storage json fail")
	storageJSONMarshal = origStorageJSON

	// Line 479: writer.Write fails
	origWriterBuffered := exportRecordsWriterNewBuffered
	defer func() { exportRecordsWriterNewBuffered = origWriterBuffered }()
	exportRecordsWriterNewBuffered = func(io.Writer) *bufio.Writer {
		return bufio.NewWriterSize(failingWriter{err: errors.New("writer fail")}, 1)
	}
	dbWriteFail := mockExportCheckDB{
		refs: []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")},
		defs: map[string]*dbschema.CollectionDef{
			"tbl": {
				Name:   "tbl",
				Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
			},
		},
		rowsReader: mockSourceRowsFailDB{
			cursor: &mockCursor{
				rows: []dbschema.SourceRow{
					{Values: map[string]any{"id": int64(1)}},
				},
			},
		},
	}
	_, err = ExportInGitDB(context.Background(), dbWriteFail, filepath.Join(tmpDir, "writer_write_fail"), ExportOptions{RecordsFormat: "jsonl"})
	require.ErrorContains(t, err, "writer fail")
	exportRecordsWriterNewBuffered = origWriterBuffered

	// Line 484: writer.Close fails
	exportRecordsWriterNewBuffered = func(out io.Writer) *bufio.Writer {
		return bufio.NewWriterSize(failingWriter{err: errors.New("flush fail")}, 256*1024)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "writer_close_fail"), ExportOptions{})
	require.ErrorContains(t, err, "flush fail")
	exportRecordsWriterNewBuffered = origWriterBuffered

	// Line 491: storage.Close fails via storageClassFileClose
	origStorageClose := storageClassFileClose
	defer func() { storageClassFileClose = origStorageClose }()
	storageClassFileClose = func(*os.File) error { return errors.New("storage close fail") }
	_, err = ExportInGitDB(context.Background(), makeDecDB(), filepath.Join(tmpDir, "storage_close_fail"), ExportOptions{})
	require.ErrorContains(t, err, "storage close fail")
	storageClassFileClose = origStorageClose

	// Line 496: yaml.Marshal(colDef) error in finish of exportCollection
	callCount := 0
	exportYAMLMarshal = func(v any) ([]byte, error) {
		if _, ok := v.(*ingitdb.CollectionDef); ok {
			callCount++
			if callCount > 1 {
				return nil, errors.New("finish coldef yaml fail")
			}
		}
		return yaml.Marshal(v)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "finish_yaml_fail"), ExportOptions{})
	require.ErrorContains(t, err, "finish coldef yaml fail")
	exportYAMLMarshal = origYAML

	// Line 499: exportOSWriteFile error in finish of exportCollection
	callWriteCount := 0
	exportOSWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, ingitdb.CollectionDefFileName) {
			callWriteCount++
			if callWriteCount > 1 {
				return errors.New("finish write coldef fail")
			}
		}
		return os.WriteFile(path, data, perm)
	}
	_, err = ExportInGitDB(context.Background(), validDB, filepath.Join(tmpDir, "finish_write_coldef_fail"), ExportOptions{})
	require.ErrorContains(t, err, "finish write coldef fail")
	exportOSWriteFile = origWriteFile
}

type cancelingCursor struct {
	cancel context.CancelFunc
	called bool
}

func (c *cancelingCursor) Next() (dbschema.SourceRow, error) {
	if !c.called {
		c.called = true
		c.cancel()
		return dbschema.SourceRow{Values: map[string]any{"id": int64(1)}}, nil
	}
	return dbschema.SourceRow{}, io.EOF
}
func (c *cancelingCursor) Close() error { return nil }
