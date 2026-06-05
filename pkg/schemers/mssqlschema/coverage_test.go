package mssqlschema

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
	"github.com/datatug/datatug-cli/pkg/schemers/sqlinfoschema"
)

// ---- columnsProvider ----

func TestColumnsProvider_GetColumnsReader(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	cols := sqlmock.NewRows([]string{
		"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "ORDINAL_POSITION",
		"COLUMN_DEFAULT", "IS_NULLABLE", "DATA_TYPE",
		"CHARACTER_MAXIMUM_LENGTH", "CHARACTER_OCTET_LENGTH",
		"CHARACTER_SET_CATALOG", "CHARACTER_SET_SCHEMA", "CHARACTER_SET_NAME",
		"COLLATION_CATALOG", "COLLATION_SCHEMA", "COLLATION_NAME",
	}).AddRow("dbo", "Users", "ID", 1, nil, "NO", "int", nil, nil, nil, nil, nil, nil, nil, nil)

	mock.ExpectQuery(".*").WillReturnRows(cols)

	p := columnsProvider{ColumnsProvider: sqlinfoschema.ColumnsProvider{DB: db}}
	reader, err := p.GetColumnsReader(context.Background(), "testdb", schemer.ColumnsFilter{})
	if err != nil {
		t.Fatalf("GetColumnsReader error: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestColumnsProvider_GetColumnsReader_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(".*").WillReturnError(errors.New("db error"))

	p := columnsProvider{ColumnsProvider: sqlinfoschema.ColumnsProvider{DB: db}}
	_, err = p.GetColumnsReader(context.Background(), "testdb", schemer.ColumnsFilter{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestColumnsProvider_GetColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	cols := sqlmock.NewRows([]string{
		"TABLE_SCHEMA", "TABLE_NAME", "COLUMN_NAME", "ORDINAL_POSITION",
		"COLUMN_DEFAULT", "IS_NULLABLE", "DATA_TYPE",
		"CHARACTER_MAXIMUM_LENGTH", "CHARACTER_OCTET_LENGTH",
		"CHARACTER_SET_CATALOG", "CHARACTER_SET_SCHEMA", "CHARACTER_SET_NAME",
		"COLLATION_CATALOG", "COLLATION_SCHEMA", "COLLATION_NAME",
	}).AddRow("dbo", "Users", "ID", 1, nil, "NO", "int", nil, nil, nil, nil, nil, nil, nil, nil)

	mock.ExpectQuery(".*").WillReturnRows(cols)

	p := columnsProvider{ColumnsProvider: sqlinfoschema.ColumnsProvider{DB: db}}
	columns, err := p.GetColumns(context.Background(), "testdb", schemer.ColumnsFilter{})
	if err != nil {
		t.Fatalf("GetColumns error: %v", err)
	}
	if len(columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(columns))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestColumnsProvider_GetColumns_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(".*").WillReturnError(errors.New("db error"))

	p := columnsProvider{ColumnsProvider: sqlinfoschema.ColumnsProvider{DB: db}}
	_, err = p.GetColumns(context.Background(), "testdb", schemer.ColumnsFilter{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- constraintsProvider ----

func TestConstraintsProvider_GetConstraints(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{
		"TABLE_SCHEMA", "TABLE_NAME",
		"CONSTRAINT_TYPE", "CONSTRAINT_NAME",
		"COLUMN_NAME",
		"UNIQUE_CONSTRAINT_CATALOG", "UNIQUE_CONSTRAINT_SCHEMA", "UNIQUE_CONSTRAINT_NAME",
		"MATCH_OPTION", "UPDATE_RULE", "DELETE_RULE",
		"REF_TABLE_CATALOG", "REF_TABLE_SCHEMA", "REF_TABLE_NAME", "REF_COL_NAME",
	}).AddRow("dbo", "Users", "PRIMARY KEY", "PK_Users", "ID",
		nil, nil, nil,
		nil, nil, nil,
		nil, nil, nil, nil)

	mock.ExpectQuery(".*").WillReturnRows(rows)

	p := constraintsProvider{ConstraintsProvider: sqlinfoschema.ConstraintsProvider{DB: db}}
	reader, err := p.GetConstraints(context.Background(), "testdb", "dbo", "Users")
	if err != nil {
		t.Fatalf("GetConstraints error: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestConstraintsProvider_GetConstraints_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(".*").WillReturnError(errors.New("db error"))

	p := constraintsProvider{ConstraintsProvider: sqlinfoschema.ConstraintsProvider{DB: db}}
	_, err = p.GetConstraints(context.Background(), "testdb", "dbo", "Users")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- indexColumnsProvider ----

func TestIndexColumnsProvider_GetIndexColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{
		"schema_name", "object_name", "index_name", "column_name",
		"key_ordinal", "partition_ordinal", "is_descending_key",
		"is_included_column", "column_store_order_ordinal",
	}).AddRow("dbo", "Users", "PK_Users", "ID", 1, 0, false, false, 0)

	mock.ExpectQuery(".*").WillReturnRows(rows)

	p := indexColumnsProvider{IndexColumnsProvider: sqlinfoschema.IndexColumnsProvider{DB: db}}
	reader, err := p.GetIndexColumns(context.Background(), "testdb", "dbo", "Users", "PK_Users")
	if err != nil {
		t.Fatalf("GetIndexColumns error: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestIndexColumnsProvider_GetIndexColumns_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(".*").WillReturnError(errors.New("db error"))

	p := indexColumnsProvider{IndexColumnsProvider: sqlinfoschema.IndexColumnsProvider{DB: db}}
	_, err = p.GetIndexColumns(context.Background(), "testdb", "dbo", "Users", "PK_Users")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- indexesProvider ----

func TestIndexesProvider_GetIndexes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{
		"schema_name", "object_name", "object_type",
		"name", "type", "type_desc",
		"is_unique", "is_primary_key", "is_unique_constraint",
	}).AddRow("dbo", "Users", "Table", "PK_Users", 1, "CLUSTERED", true, true, false)

	mock.ExpectQuery(".*").WillReturnRows(rows)

	p := indexesProvider{IndexesProvider: sqlinfoschema.IndexesProvider{DB: db}}
	reader, err := p.GetIndexes(context.Background(), "testdb", "dbo", "Users")
	if err != nil {
		t.Fatalf("GetIndexes error: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestIndexesProvider_GetIndexes_Error(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(".*").WillReturnError(errors.New("db error"))

	p := indexesProvider{IndexesProvider: sqlinfoschema.IndexesProvider{DB: db}}
	_, err = p.GetIndexes(context.Background(), "testdb", "dbo", "Users")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- schemaProvider panic stubs ----

func TestSchemaProvider_GetForeignKeysReader_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from GetForeignKeysReader")
		}
	}()
	s := schemaProvider{}
	_, _ = s.GetForeignKeysReader(context.Background(), "dbo", "Users")
}

func TestSchemaProvider_GetForeignKeys_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from GetForeignKeys")
		}
	}()
	s := schemaProvider{}
	_, _ = s.GetForeignKeys(context.Background(), "dbo", "Users")
}

func TestSchemaProvider_GetReferrers_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from GetReferrers")
		}
	}()
	s := schemaProvider{}
	_, _ = s.GetReferrers(context.Background(), "dbo", "Users")
}

func TestSchemaProvider_IsBulkProvider(t *testing.T) {
	s := schemaProvider{}
	if !s.IsBulkProvider() {
		t.Error("expected IsBulkProvider to return true")
	}
}

// ---- schemaProvider RecordsCount ----

func TestSchemaProvider_RecordsCount_HappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	expectedCount := 42
	rows := sqlmock.NewRows([]string{"count"}).AddRow(expectedCount)
	mock.ExpectQuery(`SELECT COUNT\(1\) FROM \[dbo\]\.\[Users\]`).WillReturnRows(rows)

	s := schemaProvider{db: db}
	count, err := s.RecordsCount(context.Background(), "testdb", "dbo", "Users")
	if err != nil {
		t.Fatalf("RecordsCount error: %v", err)
	}
	if count == nil {
		t.Fatal("expected non-nil count")
	}
	if *count != expectedCount {
		t.Errorf("expected count %d, got %d", expectedCount, *count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestSchemaProvider_RecordsCount_QueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(".*").WillReturnError(errors.New("connection refused"))

	s := schemaProvider{db: db}
	count, err := s.RecordsCount(context.Background(), "testdb", "dbo", "Users")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if count != nil {
		t.Error("expected nil count on error")
	}
}

func TestSchemaProvider_RecordsCount_NoRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"count"}) // no rows added
	mock.ExpectQuery(".*").WillReturnRows(rows)

	s := schemaProvider{db: db}
	count, err := s.RecordsCount(context.Background(), "testdb", "dbo", "Users")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != nil {
		t.Errorf("expected nil count for empty result, got %d", *count)
	}
}

// ---- collectionsProvider (GetCollections) ----

func TestGetCollections_NilParentKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"TABLE_SCHEMA", "TABLE_NAME", "TABLE_TYPE"}).
		AddRow("dbo", "Users", "table")
	mock.ExpectQuery(".*").WillReturnRows(rows)

	v := collectionsProvider{db: db}
	reader, err := v.GetCollections(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetCollections error: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetCollections_WithParentKey_NoParent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"TABLE_NAME", "TABLE_TYPE"}).
		AddRow("Users", "table")
	mock.ExpectQuery(".*").WillReturnRows(rows)

	parentKey := dal.NewKeyWithID("schemas", "dbo")
	v := collectionsProvider{db: db}
	reader, err := v.GetCollections(context.Background(), parentKey)
	if err != nil {
		t.Fatalf("GetCollections error: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetCollections_WithParentKey_WithParent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"TABLE_NAME", "TABLE_TYPE"}).
		AddRow("Users", "table")
	mock.ExpectQuery(".*").WillReturnRows(rows)

	catalogKey := dal.NewKeyWithID("catalogs", "mydb")
	schemaKey := dal.NewKeyWithParentAndID(catalogKey, "schemas", "dbo")
	v := collectionsProvider{db: db}
	reader, err := v.GetCollections(context.Background(), schemaKey)
	if err != nil {
		t.Fatalf("GetCollections error: %v", err)
	}
	if reader == nil {
		t.Fatal("expected non-nil reader")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetCollections_QueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(".*").WillReturnError(errors.New("query failed"))

	v := collectionsProvider{db: db}
	_, err = v.GetCollections(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---- tablesReader (NextCollection) ----

func TestNextCollection_SchemaEmpty_TableAndView(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"TABLE_SCHEMA", "TABLE_NAME", "TABLE_TYPE"}).
		AddRow("dbo", "Users", "table").
		AddRow("dbo", "UserView", "VIEW")
	mock.ExpectQuery(".*").WillReturnRows(rows)

	v := collectionsProvider{db: db}
	reader, err := v.GetCollections(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetCollections error: %v", err)
	}

	// Read table row
	ci, err := reader.NextCollection()
	if err != nil {
		t.Fatalf("NextCollection (table) error: %v", err)
	}
	if ci == nil {
		t.Fatal("expected non-nil CollectionInfo for table")
	}

	// Read view row
	ci, err = reader.NextCollection()
	if err != nil {
		t.Fatalf("NextCollection (view) error: %v", err)
	}
	if ci == nil {
		t.Fatal("expected non-nil CollectionInfo for view")
	}

	// End of rows
	ci, err = reader.NextCollection()
	if err != nil {
		t.Fatalf("NextCollection (EOF) unexpected error: %v", err)
	}
	if ci != nil {
		t.Fatal("expected nil CollectionInfo at end of rows")
	}
}

func TestNextCollection_SchemaSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"TABLE_NAME", "TABLE_TYPE"}).
		AddRow("Users", "table")
	mock.ExpectQuery(".*").WillReturnRows(rows)

	parentKey := dal.NewKeyWithID("schemas", "dbo")
	v := collectionsProvider{db: db}
	reader, err := v.GetCollections(context.Background(), parentKey)
	if err != nil {
		t.Fatalf("GetCollections error: %v", err)
	}

	ci, err := reader.NextCollection()
	if err != nil {
		t.Fatalf("NextCollection error: %v", err)
	}
	if ci == nil {
		t.Fatal("expected non-nil CollectionInfo")
	}
}

func TestNextCollection_RowsErr(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// RowError(0, ...) returns the error after row 0 is read (pos-1 == 0).
	// We must add a row so row 0 exists; the error is returned by Next() for that row.
	rows := sqlmock.NewRows([]string{"TABLE_SCHEMA", "TABLE_NAME", "TABLE_TYPE"}).
		AddRow("dbo", "Users", "table").
		RowError(0, errors.New("rows iteration error"))
	mock.ExpectQuery(".*").WillReturnRows(rows)

	v := collectionsProvider{db: db}
	reader, err := v.GetCollections(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetCollections error: %v", err)
	}

	_, err = reader.NextCollection()
	if err == nil {
		t.Fatal("expected error from row iteration, got nil")
	}
}
