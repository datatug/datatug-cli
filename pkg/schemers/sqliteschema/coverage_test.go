package sqliteschema

import (
	"context"
	"database/sql"
	"io"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

// helpers

func newProviderWithTestDB(t *testing.T) (schemer.SchemaProvider, *sql.DB) {
	t.Helper()
	db, err := createTestDB()
	if err != nil {
		t.Fatalf("createTestDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSchemaProvider(func() (*sql.DB, error) { return db, nil }), db
}

func newProviderWithErrDB(t *testing.T) schemer.SchemaProvider {
	t.Helper()
	return NewSchemaProvider(func() (*sql.DB, error) { return nil, sql.ErrConnDone })
}

func newProviderWithClosedDB(t *testing.T) schemer.SchemaProvider {
	t.Helper()
	closed, _ := sql.Open("sqlite3", ":memory:")
	_ = closed.Close()
	return NewSchemaProvider(func() (*sql.DB, error) { return closed, nil })
}

// ---------- schema.go ----------

func TestNewSchemaProvider_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil getSqliteDB")
		}
	}()
	NewSchemaProvider(nil)
}

func TestGetCollections_Success(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	r, err := s.GetCollections(context.Background(), (*dal.Key)(nil))
	if err != nil {
		t.Fatalf("GetCollections: %v", err)
	}
	var names []string
	for {
		c, err := r.NextCollection()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextCollection: %v", err)
		}
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		t.Error("expected at least one collection")
	}
}

func TestIsBulkProvider(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	if s.(interface{ IsBulkProvider() bool }).IsBulkProvider() {
		t.Error("IsBulkProvider should return false for SQLite")
	}
}

// ---------- collections.go – getCollections branches ----------

func TestGetCollections_CollectionTypeTable(t *testing.T) {
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// The Table filter queries only 2 columns (name, sql), so NextCollection
	// hits the default switch branch (dbType=="") and returns an error.
	// The reader itself is returned without error — only NextCollection fails.
	r, err := getCollections(db, collectionsFilter{CollectionType: datatug.CollectionTypeTable})
	if err != nil {
		t.Fatalf("getCollections(Table): %v", err)
	}
	// Call NextCollection once to cover the scan+switch path; expect the
	// "unsupported DB type" default branch error (dbType is empty because
	// the Table-specific query omits the type column).
	_, err = r.NextCollection()
	if err == nil {
		t.Error("expected error from NextCollection when dbType column is absent")
	}
}

func TestGetCollections_CollectionTypeView(t *testing.T) {
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// The test DB has no views; the query still succeeds and returns EOF immediately.
	r, err := getCollections(db, collectionsFilter{CollectionType: datatug.CollectionTypeView})
	if err != nil {
		t.Fatalf("getCollections(View): %v", err)
	}
	_, err = r.NextCollection()
	if err != io.EOF {
		t.Errorf("expected io.EOF for empty view list, got %v", err)
	}
}

func TestGetCollections_CollectionTypeAny(t *testing.T) {
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	r, err := getCollections(db, collectionsFilter{CollectionType: datatug.CollectionTypeAny})
	if err != nil {
		t.Fatalf("getCollections(Any): %v", err)
	}
	var count int
	for {
		_, err := r.NextCollection()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextCollection: %v", err)
		}
		count++
	}
	if count == 0 {
		t.Error("expected at least one collection")
	}
}

func TestGetCollections_DefaultBranch(t *testing.T) {
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	_, err = getCollections(db, collectionsFilter{CollectionType: "bogus"})
	if err == nil {
		t.Error("expected error for unknown CollectionType")
	}
}

func TestGetCollections_QueryError(t *testing.T) {
	closed, _ := sql.Open("sqlite3", ":memory:")
	_ = closed.Close()

	// Table, View, and Any/Unknown branches should all fail on a closed DB.
	for _, ct := range []datatug.CollectionType{
		datatug.CollectionTypeTable,
		datatug.CollectionTypeView,
		datatug.CollectionTypeAny,
		datatug.CollectionTypeUnknown,
	} {
		_, err := getCollections(closed, collectionsFilter{CollectionType: ct})
		if err == nil {
			t.Errorf("expected error for closed DB, collectionType=%s", ct)
		}
	}
}

// ---------- collections.go – NextCollection branches ----------

func TestNextCollection_UnsupportedType(t *testing.T) {
	// Inject a row with an unexpected type by inserting directly into the reader.
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Use "trigger" type which is valid in sqlite_schema but not handled by NextCollection.
	_, err = db.Exec(`CREATE TRIGGER trig AFTER INSERT ON Country BEGIN SELECT 1; END`)
	if err != nil {
		t.Skipf("cannot create trigger for test: %v", err)
	}

	// Query rows that include the trigger type.
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_schema WHERE type = 'trigger'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	r := &collectionsReader{rows: rows}
	_, err = r.NextCollection()
	if err == nil {
		t.Error("expected error for unsupported DB type")
	}
}

// ---------- columns.go ----------

func TestGetColumnsReader_Success(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)
	ref := dal.NewRootCollectionRef("Country", "")
	r, err := sp.GetColumnsReader(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	if err != nil {
		t.Fatalf("GetColumnsReader: %v", err)
	}
	var cols []schemer.Column
	for {
		c, err := r.NextColumn()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextColumn: %v", err)
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		t.Error("expected columns for Country table")
	}
}

func TestGetColumnsReader_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	ref := dal.NewRootCollectionRef("Country", "")
	_, err := sp.GetColumnsReader(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

func TestGetColumnsReader_EmptyName(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)
	// Use zero-value CollectionRef whose Name() returns "" without panicking.
	var emptyRef dal.CollectionRef
	_, err := sp.GetColumnsReader(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &emptyRef})
	if err == nil {
		t.Error("expected error when collection name is empty")
	}
}

func TestGetColumnsReader_QueryError(t *testing.T) {
	sp := newProviderWithClosedDB(t).(schemaProvider)
	ref := dal.NewRootCollectionRef("Country", "")
	_, err := sp.GetColumnsReader(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	if err == nil {
		t.Error("expected error when db is closed")
	}
}

func TestGetColumns_Success(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)
	ref := dal.NewRootCollectionRef("Country", "")
	cols, err := sp.GetColumns(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	if err != nil {
		t.Fatalf("GetColumns: %v", err)
	}
	if len(cols) == 0 {
		t.Error("expected columns for Country table")
	}
}

func TestGetColumns_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	ref := dal.NewRootCollectionRef("Country", "")
	_, err := sp.GetColumns(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

// ---------- constraints.go ----------

func TestGetConstraints_Success(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)

	// Country has UNIQUE constraints.
	r, err := sp.GetConstraints(context.Background(), "", "main", "Country")
	if err != nil {
		t.Fatalf("GetConstraints Country: %v", err)
	}
	var constraints []*schemer.Constraint
	for {
		c, err := r.NextConstraint()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextConstraint: %v", err)
		}
		constraints = append(constraints, c)
	}
	if len(constraints) == 0 {
		t.Error("expected UNIQUE constraints for Country table")
	}

	// User has FOREIGN KEY constraints.
	r2, err := sp.GetConstraints(context.Background(), "", "main", "User")
	if err != nil {
		t.Fatalf("GetConstraints User: %v", err)
	}
	var fkConstraints []*schemer.Constraint
	for {
		c, err := r2.NextConstraint()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextConstraint User: %v", err)
		}
		fkConstraints = append(fkConstraints, c)
	}
	if len(fkConstraints) == 0 {
		t.Error("expected FK constraints for User table")
	}
}

func TestGetConstraints_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	_, err := sp.GetConstraints(context.Background(), "", "main", "Country")
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

func TestGetConstraints_QueryError(t *testing.T) {
	sp := newProviderWithClosedDB(t).(schemaProvider)
	_, err := sp.GetConstraints(context.Background(), "", "main", "Country")
	if err == nil {
		t.Error("expected error when db is closed")
	}
}

// ---------- indexes.go ----------

func TestGetIndexes_Success(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)

	r, err := sp.GetIndexes(context.Background(), "", "main", "Country")
	if err != nil {
		t.Fatalf("GetIndexes: %v", err)
	}
	var indexes []*schemer.Index
	for {
		idx, err := r.NextIndex()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextIndex: %v", err)
		}
		indexes = append(indexes, idx)
	}
	if len(indexes) == 0 {
		t.Error("expected at least one index for Country table")
	}
}

func TestGetIndexes_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	_, err := sp.GetIndexes(context.Background(), "", "main", "Country")
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

func TestGetIndexes_QueryError(t *testing.T) {
	sp := newProviderWithClosedDB(t).(schemaProvider)
	_, err := sp.GetIndexes(context.Background(), "", "main", "Country")
	if err == nil {
		t.Error("expected error when db is closed")
	}
}

// ---------- index_columns.go ----------

func TestGetIndexColumns_Success(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)

	// Get the index name for Country's unique constraint.
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query("PRAGMA index_list('Country')")
	if err != nil {
		t.Fatalf("PRAGMA index_list: %v", err)
	}
	var indexName string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if origin == "u" {
			indexName = name
			break
		}
	}
	_ = rows.Close()

	if indexName == "" {
		t.Skip("no unique index found on Country")
	}

	r, err := sp.GetIndexColumns(context.Background(), "", "main", "Country", indexName)
	if err != nil {
		t.Fatalf("GetIndexColumns: %v", err)
	}
	var cols []*schemer.IndexColumn
	for {
		c, err := r.NextIndexColumn()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextIndexColumn: %v", err)
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		t.Error("expected at least one index column")
	}
}

func TestGetIndexColumns_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	_, err := sp.GetIndexColumns(context.Background(), "", "main", "Country", "idx")
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

func TestGetIndexColumns_QueryError(t *testing.T) {
	sp := newProviderWithClosedDB(t).(schemaProvider)
	_, err := sp.GetIndexColumns(context.Background(), "", "main", "Country", "idx")
	if err == nil {
		t.Error("expected error when db is closed")
	}
}

// ---------- records_count.go ----------

func TestRecordsCount_Success(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)

	count, err := sp.RecordsCount(context.Background(), "", "", "Country")
	if err != nil {
		t.Fatalf("RecordsCount: %v", err)
	}
	if count == nil {
		t.Fatal("expected non-nil count")
	}
	if *count != 0 {
		t.Errorf("expected 0 records, got %d", *count)
	}
}

func TestRecordsCount_WithSchema(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)

	count, err := sp.RecordsCount(context.Background(), "", "main", "Country")
	if err != nil {
		t.Fatalf("RecordsCount with schema: %v", err)
	}
	if count == nil {
		t.Fatal("expected non-nil count")
	}
}

func TestRecordsCount_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	_, err := sp.RecordsCount(context.Background(), "", "", "Country")
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

func TestRecordsCount_QueryError(t *testing.T) {
	sp := newProviderWithClosedDB(t).(schemaProvider)
	_, err := sp.RecordsCount(context.Background(), "", "", "Country")
	if err == nil {
		t.Error("expected error when db is closed")
	}
}

// ---------- collections.go – view branch in NextCollection ----------

func TestNextCollection_ViewType(t *testing.T) {
	db, err := createTestDB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// Add a view so NextCollection hits the "view" branch.
	if _, err = db.Exec(`CREATE VIEW CountryView AS SELECT ID, Name FROM Country`); err != nil {
		t.Fatalf("create view: %v", err)
	}

	// Use CollectionTypeUnknown (Any) so the 3-column query is used and dbType is populated.
	r, err := getCollections(db, collectionsFilter{CollectionType: datatug.CollectionTypeUnknown})
	if err != nil {
		t.Fatalf("getCollections: %v", err)
	}
	var sawView bool
	for {
		c, err := r.NextCollection()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextCollection: %v", err)
		}
		if c.Type == datatug.CollectionTypeView {
			sawView = true
		}
	}
	if !sawView {
		t.Error("expected to see a view type collection")
	}
}

// ---------- schema.go – GetCollections error path ----------

func TestGetCollections_ErrDB(t *testing.T) {
	s := newProviderWithErrDB(t)
	_, err := s.GetCollections(context.Background(), (*dal.Key)(nil))
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

// ---------- foreign_keys.go error paths ----------

func TestGetForeignKeysReader_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	_, err := sp.GetForeignKeysReader(context.Background(), "", "User")
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

func TestGetForeignKeysReader_EmptyTable(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)
	_, err := sp.GetForeignKeysReader(context.Background(), "", "")
	if err == nil {
		t.Error("expected error when table name is empty")
	}
}

func TestGetForeignKeysReader_QueryError(t *testing.T) {
	sp := newProviderWithClosedDB(t).(schemaProvider)
	_, err := sp.GetForeignKeysReader(context.Background(), "", "User")
	if err == nil {
		t.Error("expected error when db is closed")
	}
}

func TestGetForeignKeys_ErrDB(t *testing.T) {
	sp := newProviderWithErrDB(t).(schemaProvider)
	_, err := sp.GetForeignKeys(context.Background(), "", "User")
	if err == nil {
		t.Error("expected error when getSqliteDB returns error")
	}
}

func TestGetForeignKeys_CancelledContext(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so ctx.Err() is set before the loop
	_, err := sp.GetForeignKeys(ctx, "", "User")
	// GetForeignKeysReader ignores context, so the query always succeeds.
	// After appending the first FK, GetForeignKeys checks ctx.Err() which
	// returns context.Canceled (User has 2 FKs so the loop runs at least once).
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// ---------- foreign_keys.go – Close ----------

func TestForeignKeysReader_Close(t *testing.T) {
	s, _ := newProviderWithTestDB(t)
	sp := s.(schemaProvider)

	r, err := sp.GetForeignKeysReader(context.Background(), "", "User")
	if err != nil {
		t.Fatalf("GetForeignKeysReader: %v", err)
	}
	fkr := r.(*foreignKeysReader)
	if err := fkr.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------- assertForeignKeys coverage ----------

func TestAssertForeignKeys_Mismatch(t *testing.T) {
	// Exercise the mismatch branch (len(actual) != len(expected)).
	// We use a sub-test with a fake *testing.T via t.Run so failures are captured.
	t.Run("count mismatch is reported", func(t *testing.T) {
		expected := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col1"}},
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id"}},
			},
		}
		actual := []schemer.ForeignKey{}
		// assertForeignKeys will call t.Errorf — use a nested fake test.
		inner := &testing.T{}
		assertForeignKeys(inner, "TestTable", expected, actual, false)
		if !inner.Failed() {
			t.Error("expected assertForeignKeys to report failure for count mismatch")
		}
	})

	t.Run("missing FK is reported", func(t *testing.T) {
		expected := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col1"}},
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id"}},
			},
		}
		actual := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col1"}},
				To:   schemer.FKAnchor{Name: "C", Columns: []string{"id"}}, // different target
			},
		}
		inner := &testing.T{}
		assertForeignKeys(inner, "TestTable", expected, actual, false)
		if !inner.Failed() {
			t.Error("expected assertForeignKeys to report missing FK")
		}
	})

	t.Run("column length mismatch continues search", func(t *testing.T) {
		expected := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col1", "col2"}},
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id1", "id2"}},
			},
		}
		actual := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col1"}}, // fewer columns
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id1"}},
			},
		}
		inner := &testing.T{}
		assertForeignKeys(inner, "TestTable", expected, actual, false)
		if !inner.Failed() {
			t.Error("expected assertForeignKeys to report column mismatch")
		}
	})

	t.Run("column value mismatch sets match=false", func(t *testing.T) {
		// Same name, same column count, but different column values — exercises line 26.
		expected := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col1"}},
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id1"}},
			},
		}
		actual := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col_DIFFERENT"}}, // same count, different value
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id1"}},
			},
		}
		inner := &testing.T{}
		assertForeignKeys(inner, "TestTable", expected, actual, false)
		if !inner.Failed() {
			t.Error("expected assertForeignKeys to report column value mismatch")
		}
	})

	t.Run("referrer mismatch is reported", func(t *testing.T) {
		expected := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "A", Columns: []string{"col1"}},
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id"}},
			},
		}
		actual := []schemer.ForeignKey{
			{
				From: schemer.FKAnchor{Name: "X", Columns: []string{"col1"}}, // different referrer
				To:   schemer.FKAnchor{Name: "B", Columns: []string{"id"}},
			},
		}
		inner := &testing.T{}
		assertForeignKeys(inner, "TestTable", expected, actual, true) // isReferrer=true
		if !inner.Failed() {
			t.Error("expected assertForeignKeys to report missing referrer")
		}
	})
}
