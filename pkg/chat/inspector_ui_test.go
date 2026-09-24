package chat

import "testing"

func metaIsZero(meta inspectorColumnMeta) bool {
	return meta.qualified == "" && meta.dbType == "" && len(meta.objects) == 0
}

// columnMetaForTestCatalog is a small catalog shared by columnMetaFor's
// direct unit tests: one "main.invoice" table (also present twice under
// "second.invoice" for the ambiguous-source case) plus a non-table object
// that must never be matched.
func columnMetaForTestCatalog() ProjectCatalog {
	return ProjectCatalog{Objects: []ProjectObject{
		{
			Reference:   ContextReference{Kind: "table", ObjectID: "main.invoice", SourceID: "db1"},
			Columns:     []string{"InvoiceId", "CustomerId"},
			ColumnTypes: map[string]string{"InvoiceId": "INTEGER", "CustomerId": "INTEGER"},
		},
		{
			Reference:   ContextReference{Kind: "table", ObjectID: "second.invoice", SourceID: "db2"},
			Columns:     []string{"InvoiceId"},
			ColumnTypes: map[string]string{"InvoiceId": "INTEGER"},
		},
		{
			// Not a table/project_view: must be skipped even though its
			// column name matches.
			Reference: ContextReference{Kind: "query", ObjectID: "some.query", SourceID: "db1"},
			Columns:   []string{"InvoiceId"},
		},
	}}
}

// TestColumnMetaForNilRecordScansWholeCatalog covers the record==nil path
// (e.g. currentRecordsetDetails without a focused RecordSet's DTQL parsed):
// every table/project_view object is scanned directly by column name.
func TestColumnMetaForNilRecordScansWholeCatalog(t *testing.T) {
	catalog := ProjectCatalog{Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "table", ObjectID: "main.invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}},
	}}
	meta := columnMetaFor(catalog, nil, "InvoiceId")
	if meta.qualified != "main.invoice.InvoiceId" || meta.dbType != "INTEGER" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestColumnMetaForEmptyDTQLReturnsEmptyMeta(t *testing.T) {
	record := &RecordSet{DTQL: ""}
	meta := columnMetaFor(columnMetaForTestCatalog(), record, "InvoiceId")
	if !metaIsZero(meta) {
		t.Fatalf("meta = %+v, want zero value for an empty DTQL", meta)
	}
}

func TestColumnMetaForInvalidDTQLReturnsEmptyMeta(t *testing.T) {
	record := &RecordSet{DTQL: "not: [valid, dtql"}
	meta := columnMetaFor(columnMetaForTestCatalog(), record, "InvoiceId")
	if !metaIsZero(meta) {
		t.Fatalf("meta = %+v, want zero value for invalid DTQL", meta)
	}
}

func TestColumnMetaForWildcardExcludedColumnIsUnattributed(t *testing.T) {
	record := &RecordSet{DTQL: "from: {name: Invoice}\ncolumns:\n  - wildcard: {exclude: [InvoiceId]}\n"}
	meta := columnMetaFor(columnMetaForTestCatalog(), record, "InvoiceId")
	if !metaIsZero(meta) {
		t.Fatalf("meta = %+v, want zero value for an excluded wildcard column", meta)
	}
}

// Note: dtql's own validateWildcardYAML rejects any wildcard.Source that
// isn't exactly the FROM clause's own name/alias (or empty) before
// columnMetaFor ever runs, and DTQL has no multi-relation FROM at all (a
// literal "joins are not supported by DTQL", dtql/serialize.go) -- so
// allowed()'s "wildcard source resolves to zero or multiple relations"
// branch is unreachable defensive code via any real DTQL document, not
// exercised here. The same holds for relationInstances returning zero
// instances (its walk always appends at least the root FROM relation, and
// dtql.Deserialize requires a FROM clause to succeed at all) -- both
// defensive branches stay uncovered by design.

// TestColumnMetaForNameWithSchemaPrefixUsesShortNameForCatalogLookup covers
// the nil-record path's own name normalisation: a caller-qualified name
// (e.g. "t.InvoiceId", as currentRecordsetDetails may pass when echoing a
// query's own output alias) is matched against the catalog by its short
// (unqualified) column name.
func TestColumnMetaForNameWithSchemaPrefixUsesShortNameForCatalogLookup(t *testing.T) {
	catalog := ProjectCatalog{Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "table", ObjectID: "main.invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}},
	}}
	meta := columnMetaFor(catalog, nil, "t.InvoiceId")
	if meta.qualified != "main.invoice.InvoiceId" || meta.dbType != "INTEGER" {
		t.Fatalf("meta = %+v, want the short name (after the last '.') matched", meta)
	}
}

// TestColumnMetaForNonMatchingRelationCatalogObjectIsSkipped covers the
// sourceRelations guard in the final catalog loop: a table that isn't the
// DTQL's own FROM relation (here "main.other", column-named the same as the
// queried column) must be skipped even though record.Database doesn't rule
// it out on its own.
func TestColumnMetaForNonMatchingRelationCatalogObjectIsSkipped(t *testing.T) {
	catalog := ProjectCatalog{Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "table", ObjectID: "main.invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}},
		{Reference: ContextReference{Kind: "table", ObjectID: "main.other"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "TEXT"}},
	}}
	record := &RecordSet{DTQL: "from: {name: invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n"}
	meta := columnMetaFor(catalog, record, "InvoiceId")
	if meta.qualified != "main.invoice.InvoiceId" || meta.dbType != "INTEGER" || len(meta.objects) != 1 {
		t.Fatalf("meta = %+v, want only the FROM-relation table matched", meta)
	}
}

func TestColumnMetaForWildcardResolvesCatalogColumn(t *testing.T) {
	record := &RecordSet{DTQL: "from: {name: invoice}\ncolumns:\n  - wildcard: {source: invoice, exclude: [Nonexistent]}\n", Database: "db1"}
	meta := columnMetaFor(columnMetaForTestCatalog(), record, "InvoiceId")
	if meta.qualified != "main.invoice.InvoiceId" || meta.dbType != "INTEGER" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestColumnMetaForDerivedValueSameNameIsUnattributed(t *testing.T) {
	// A non-FieldRef projection (an aggregate) aliased to the exact queried
	// name is a derived value: columnMetaFor must not attribute it to a
	// same-named physical column.
	record := &RecordSet{DTQL: "from: {name: Invoice}\ncolumns:\n  - aggregate: {function: COUNT, args: [{star: true}]}\n    as: InvoiceId\nlimit: 10\n"}
	meta := columnMetaFor(columnMetaForTestCatalog(), record, "InvoiceId")
	if !metaIsZero(meta) {
		t.Fatalf("meta = %+v, want zero value for a derived same-named column", meta)
	}
}

func TestColumnMetaForNoColumnMatchesNameIsUnattributed(t *testing.T) {
	record := &RecordSet{DTQL: "from: {name: Invoice}\ncolumns: [{field: CustomerId}]\nlimit: 5\n"}
	meta := columnMetaFor(columnMetaForTestCatalog(), record, "InvoiceId")
	if !metaIsZero(meta) {
		t.Fatalf("meta = %+v, want zero value when no projected column matches the queried name", meta)
	}
}

func TestColumnMetaForDuplicateOutputNamesIsUnattributed(t *testing.T) {
	// Two FieldRef projections both aliased to "X": columnMetaFor must
	// refuse to attribute rather than guess.
	record := &RecordSet{DTQL: "from: {name: Invoice}\ncolumns:\n  - field: InvoiceId\n    as: X\n  - field: CustomerId\n    as: X\nlimit: 5\n"}
	meta := columnMetaFor(columnMetaForTestCatalog(), record, "X")
	if !metaIsZero(meta) {
		t.Fatalf("meta = %+v, want zero value for duplicate output names", meta)
	}
}

// TestColumnMetaForRecordDatabaseFiltersCatalogObjects covers the
// record.Database guard specifically: two catalog objects share the exact
// same relation ObjectID (so sourceRelations alone can't disambiguate
// them) but live under different sources -- only record.Database narrows
// it to one.
func TestColumnMetaForRecordDatabaseFiltersCatalogObjects(t *testing.T) {
	catalog := ProjectCatalog{Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "table", ObjectID: "main.invoice", SourceID: "db1"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}},
		{Reference: ContextReference{Kind: "table", ObjectID: "main.invoice", SourceID: "db2"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "TEXT"}},
	}}
	record := &RecordSet{DTQL: "from: {name: invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n", Database: "db2"}
	meta := columnMetaFor(catalog, record, "InvoiceId")
	if meta.qualified != "main.invoice.InvoiceId" || meta.dbType != "TEXT" || len(meta.objects) != 1 {
		t.Fatalf("meta = %+v, want only the db2-scoped table matched", meta)
	}
}

func TestColumnMetaForAmbiguousMultipleMatchesReportsAmbiguousSource(t *testing.T) {
	// Both "main.invoice" and "second.invoice" carry an InvoiceId column;
	// a nil record (no DTQL-derived sourceRelations filter) scans the whole
	// catalog and must find both.
	catalog := ProjectCatalog{Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "table", ObjectID: "main.invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}},
		{Reference: ContextReference{Kind: "table", ObjectID: "second.invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "TEXT"}},
	}}
	meta := columnMetaFor(catalog, nil, "InvoiceId")
	if meta.qualified != "ambiguous source" || meta.dbType != "" || len(meta.objects) != 2 {
		t.Fatalf("meta = %+v, want an ambiguous-source result naming both objects", meta)
	}
}
