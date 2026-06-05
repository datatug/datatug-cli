package sqliteschema

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
)

var errTest = errors.New("injected test error")

// ─── collectionsReader.NextCollection ─────────────────────────────────────

// TestNextCollection_RowsErrAfterLoop covers lines 59-61 in collections.go:
// the rows.Err() != nil branch that fires after Next() returns false.
// RowError(0, err) only fires when a row at index 0 exists — without it Next()
// returns false via EOF and Err() stays nil (taking the else branch instead).
func TestNextCollection_RowsErrAfterLoop(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows := sqlmock.NewRows([]string{"type", "name", "sql"}).
		AddRow("table", "Country", "CREATE TABLE Country ()").
		RowError(0, errTest)
	mock.ExpectQuery("SELECT type, name, sql FROM sqlite_schema").WillReturnRows(rows)

	r, err := getCollections(db, collectionsFilter{CollectionType: datatug.CollectionTypeAny})
	if err != nil {
		t.Fatalf("getCollections: %v", err)
	}
	_, err = r.NextCollection()
	if err == nil {
		t.Error("expected error from rows.Err(), got nil")
	}
}

// TestNextCollection_ScanError covers lines 72-74 in collections.go:
// the scan error branch when the row has wrong column count.
func TestNextCollection_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// CollectionTypeAny uses the 3-column query and calls Scan(&dbType, &name, &sqlText).
	// Supply only 2 columns so Scan fails.
	rows := sqlmock.NewRows([]string{"type", "name"}).
		AddRow("table", "Country")
	mock.ExpectQuery("SELECT type, name, sql FROM sqlite_schema").WillReturnRows(rows)

	r, err := getCollections(db, collectionsFilter{CollectionType: datatug.CollectionTypeAny})
	if err != nil {
		t.Fatalf("getCollections: %v", err)
	}
	_, err = r.NextCollection()
	if err == nil {
		t.Error("expected scan error with 2-column row for 3-column Scan, got nil")
	}
}

// ─── columnsReader.NextColumn ─────────────────────────────────────────────

// TestColumnsReader_RowsErr covers columns.go:53-56.
func TestColumnsReader_RowsErr(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// A row must exist at index 0 for RowError(0,err) to fire (otherwise EOF path taken).
	rows := sqlmock.NewRows([]string{"cid", "name", "type", "notnull", "dflt_value", "pk"}).
		AddRow(int64(0), "id", "INTEGER", int64(1), nil, int64(1)).
		RowError(0, errTest)
	mock.ExpectQuery("PRAGMA table_info").WillReturnRows(rows)

	sqlRows, queryErr := db.Query("PRAGMA table_info('Country')")
	if queryErr != nil {
		t.Fatalf("db.Query: %v", queryErr)
	}

	r := &columnsReader{rows: sqlRows}
	_, err = r.NextColumn()
	if err == nil {
		t.Error("expected rows.Err() error, got nil")
	}
}

// TestColumnsReader_ScanError covers columns.go:73-75.
func TestColumnsReader_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// NextColumn scans 6 fields; supply only 3 to force a scan error.
	rows := sqlmock.NewRows([]string{"cid", "name", "type"}).
		AddRow(1, "col1", "TEXT")
	mock.ExpectQuery("PRAGMA table_info").WillReturnRows(rows)

	sqlRows, queryErr := db.Query("PRAGMA table_info('Country')")
	if queryErr != nil {
		t.Fatalf("db.Query: %v", queryErr)
	}

	r := &columnsReader{rows: sqlRows}
	_, err = r.NextColumn()
	if err == nil {
		t.Error("expected scan error with 3-column row for 6-column Scan, got nil")
	}
}

// ─── uniqueConstraintIndexes / indexColumnNames ───────────────────────────

// TestUniqueConstraintIndexes_QueryError covers constraints.go:82-84.
func TestUniqueConstraintIndexes_QueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("PRAGMA index_list").WillReturnError(errTest)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.uniqueConstraintIndexes(db, "Country")
	if err == nil {
		t.Error("expected error from PRAGMA index_list query, got nil")
	}
}

// TestUniqueConstraintIndexes_ScanError covers constraints.go:90-92.
func TestUniqueConstraintIndexes_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// index_list returns 5 cols; supply only 2 to force scan error.
	rows := sqlmock.NewRows([]string{"seq", "name"}).AddRow(0, "idx_name")
	mock.ExpectQuery("PRAGMA index_list").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.uniqueConstraintIndexes(db, "Country")
	if err == nil {
		t.Error("expected scan error for index_list with 2-column row, got nil")
	}
}

// TestIndexColumnNames_QueryError covers constraints.go:102-104.
func TestIndexColumnNames_QueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("PRAGMA index_info").WillReturnError(errTest)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.indexColumnNames(db, "some_index")
	if err == nil {
		t.Error("expected error from PRAGMA index_info query, got nil")
	}
}

// TestIndexColumnNames_ScanError covers constraints.go:110-112.
func TestIndexColumnNames_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// index_info returns 3 cols; supply only 1 to force scan error.
	rows := sqlmock.NewRows([]string{"seqno"}).AddRow(0)
	mock.ExpectQuery("PRAGMA index_info").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.indexColumnNames(db, "some_index")
	if err == nil {
		t.Error("expected scan error for index_info with 1-column row, got nil")
	}
}

// ─── GetConstraints error paths ───────────────────────────────────────────

// TestGetConstraints_FKScanError covers constraints.go:36-39.
func TestGetConstraints_FKScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// foreign_key_list returns 8 cols; supply only 2 to force scan error.
	rows := sqlmock.NewRows([]string{"id", "seq"}).AddRow(0, 0)
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetConstraints(context.Background(), "", "main", "User")
	if err == nil {
		t.Error("expected scan error for foreign_key_list with 2-column row, got nil")
	}
}

// TestGetConstraints_FKRowsErr covers constraints.go:52-55.
func TestGetConstraints_FKRowsErr(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Row must exist at index 0 so RowError(0,err) fires rather than taking the EOF path.
	rows := sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}).
		AddRow(int64(0), int64(0), "Country", "CountryID", "ID", "NO ACTION", "NO ACTION", "NONE").
		RowError(0, errTest)
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetConstraints(context.Background(), "", "main", "User")
	if err == nil {
		t.Error("expected rows.Err() error from foreign_key_list, got nil")
	}
}

// TestGetConstraints_UniqueIndexError covers constraints.go:60-62.
func TestGetConstraints_UniqueIndexError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// foreign_key_list succeeds (empty), then index_list fails.
	fkRows := sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"})
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnRows(fkRows)
	mock.ExpectQuery("PRAGMA index_list").WillReturnError(errTest)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetConstraints(context.Background(), "", "main", "User")
	if err == nil {
		t.Error("expected error from uniqueConstraintIndexes, got nil")
	}
}

// TestGetConstraints_IndexColumnNamesError covers constraints.go:65-67.
func TestGetConstraints_IndexColumnNamesError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// foreign_key_list succeeds (empty), index_list returns one "u" origin index,
	// then index_info fails.
	fkRows := sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"})
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnRows(fkRows)

	idxRows := sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}).
		AddRow(0, "idx_country_name", 1, "u", 0)
	mock.ExpectQuery("PRAGMA index_list").WillReturnRows(idxRows)
	mock.ExpectQuery("PRAGMA index_info").WillReturnError(errTest)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetConstraints(context.Background(), "", "main", "Country")
	if err == nil {
		t.Error("expected error from indexColumnNames, got nil")
	}
}

// ─── foreignKeysReader.NextForeignKey ─────────────────────────────────────

// TestForeignKeysReader_ScanError covers foreign_keys.go:65-67.
func TestForeignKeysReader_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// foreign_key_list returns 8 cols; supply only 2 to force scan error.
	rows := sqlmock.NewRows([]string{"id", "seq"}).AddRow(0, 0)
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnRows(rows)

	sqlRows, queryErr := db.Query("PRAGMA foreign_key_list('User')")
	if queryErr != nil {
		t.Fatalf("db.Query: %v", queryErr)
	}

	r := &foreignKeysReader{table: "User", rows: sqlRows}
	_, err = r.NextForeignKey()
	if err == nil {
		t.Error("expected scan error in NextForeignKey, got nil")
	}
}

// TestForeignKeysReader_RowsErrAfterLoop covers foreign_keys.go:89-91.
func TestForeignKeysReader_RowsErrAfterLoop(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Row must exist at index 0 so RowError(0,err) fires rather than taking the EOF path.
	rows := sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}).
		AddRow(int64(0), int64(0), "Country", "CountryID", "ID", "NO ACTION", "NO ACTION", "NONE").
		RowError(0, errTest)
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnRows(rows)

	sqlRows, queryErr := db.Query("PRAGMA foreign_key_list('User')")
	if queryErr != nil {
		t.Fatalf("db.Query: %v", queryErr)
	}

	r := &foreignKeysReader{table: "User", rows: sqlRows}
	_, err = r.NextForeignKey()
	if err == nil {
		t.Error("expected rows.Err() error in NextForeignKey, got nil")
	}
}

// ─── GetIndexColumns error paths ──────────────────────────────────────────

// TestGetIndexColumns_ScanError covers index_columns.go:29-31.
func TestGetIndexColumns_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// index_info returns 3 cols; supply only 1.
	rows := sqlmock.NewRows([]string{"seqno"}).AddRow(0)
	mock.ExpectQuery("PRAGMA index_info").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetIndexColumns(context.Background(), "", "main", "Country", "some_index")
	if err == nil {
		t.Error("expected scan error from GetIndexColumns, got nil")
	}
}

// TestGetIndexColumns_RowsErr covers index_columns.go:38-40.
func TestGetIndexColumns_RowsErr(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// RowError(0, err) fires when the driver reads row index 0; Next() returns
	// false and database/sql stores the error so rows.Err() returns it.
	// We must add a row so index 0 exists in the set.
	rows := sqlmock.NewRows([]string{"seqno", "cid", "name"}).
		AddRow(int64(0), int64(0), "col1").
		RowError(0, errTest)
	mock.ExpectQuery("PRAGMA index_info").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetIndexColumns(context.Background(), "", "main", "Country", "some_index")
	if err == nil {
		t.Error("expected rows.Err() error from GetIndexColumns, got nil")
	}
}

// ─── GetIndexes error paths ───────────────────────────────────────────────

// TestGetIndexes_ScanError covers indexes.go:29-31.
func TestGetIndexes_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// index_list returns 5 cols; supply only 2.
	rows := sqlmock.NewRows([]string{"seq", "name"}).AddRow(0, "idx_name")
	mock.ExpectQuery("PRAGMA index_list").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetIndexes(context.Background(), "", "main", "Country")
	if err == nil {
		t.Error("expected scan error from GetIndexes, got nil")
	}
}

// TestGetIndexes_RowsErr covers indexes.go:45-47.
func TestGetIndexes_RowsErr(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Add a row so index 0 exists; RowError(0, err) fires during Next() for
	// that row, causing Next() to return false with rows.Err() set.
	rows := sqlmock.NewRows([]string{"seq", "name", "unique", "origin", "partial"}).
		AddRow(int64(0), "idx_name", int64(1), "c", int64(0)).
		RowError(0, errTest)
	mock.ExpectQuery("PRAGMA index_list").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetIndexes(context.Background(), "", "main", "Country")
	if err == nil {
		t.Error("expected rows.Err() error from GetIndexes, got nil")
	}
}

// ─── GetReferrers error paths ─────────────────────────────────────────────

// TestGetReferrers_ScanError covers referrers.go:30-32.
func TestGetReferrers_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Supply 2 columns but Scan expects only 1 (*string), so scan fails.
	rows := sqlmock.NewRows([]string{"name", "extra"}).AddRow("Country", "unexpected")
	mock.ExpectQuery("SELECT name FROM sqlite_schema").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetReferrers(context.Background(), "", "Country")
	if err == nil {
		t.Error("expected scan error from GetReferrers, got nil")
	}
}

// TestGetReferrers_RowsErr covers referrers.go:35-37.
func TestGetReferrers_RowsErr(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Add a row so index 0 exists; RowError(0, err) fires during Next() for
	// that row, causing Next() to return false with rows.Err() set.
	rows := sqlmock.NewRows([]string{"name"}).
		AddRow("SomeTable").
		RowError(0, errTest)
	mock.ExpectQuery("SELECT name FROM sqlite_schema").WillReturnRows(rows)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetReferrers(context.Background(), "", "Country")
	if err == nil {
		t.Error("expected rows.Err() error from GetReferrers, got nil")
	}
}

// TestGetReferrers_GetFKError covers referrers.go:42-44.
func TestGetReferrers_GetFKError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Return one table name successfully, then make the FK query fail.
	tableRows := sqlmock.NewRows([]string{"name"}).AddRow("User")
	mock.ExpectQuery("SELECT name FROM sqlite_schema").WillReturnRows(tableRows)
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnError(errTest)

	sp := schemaProvider{getSqliteDB: func() (*sql.DB, error) { return db, nil }}
	_, err = sp.GetReferrers(context.Background(), "", "Country")
	if err == nil {
		t.Error("expected error from GetForeignKeys inside GetReferrers, got nil")
	}
}
