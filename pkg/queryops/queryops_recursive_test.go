package queryops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
)

const recursiveSalesQuery = `from: {name: Sales, alias: s, joins: [{from: {name: Parents, alias: p}, on: [{left: {field: parentId, source: s}, op: '==', right: {field: id, source: p}}]}]}
where: {and: [{op: '==', left: {field: region, source: s}, right: {value: keep}}, {op: '==', left: {field: active, source: p}, right: {value: 1}}, {op: '<=', left: {field: id, source: s}, right: {value: 6}}]}
orderBy: [{field: total, source: s}]
limit: 2
columns: [{field: category, source: s, as: category}, {field: total, source: s, as: total}]
`

func newRecursiveSQLite(t *testing.T) *dalgo2sqlite.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queryops.sqlite")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(`
CREATE TABLE Sales(id INTEGER PRIMARY KEY, category TEXT, total INTEGER, region TEXT, parentId INTEGER);
CREATE TABLE Parents(id INTEGER PRIMARY KEY, active INTEGER);
INSERT INTO Parents VALUES(1,1),(2,0);
INSERT INTO Sales VALUES
 (1,'A',10,'keep',1),(2,'A',20,'keep',1),(3,'A',30,'keep',1),
 (4,'B',40,'keep',1),(5,'B',50,'keep',1),(6,'B',60,'keep',1),
 (7,'Z',1000,'keep',2),(8,'Z',2000,'excluded',1),(9,'C',25,'keep',1);
`)
	if err != nil {
		_ = seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := dalgo2sqlite.NewDatabaseWithOptions(path, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func executeRecursivePreview(t *testing.T, db *dalgo2sqlite.Database, preview Preview) []map[string]any {
	t.Helper()
	query, err := tugql.Deserialize([]byte(preview.TugQL))
	if err != nil {
		t.Fatalf("deserialize preview: %v\n%s", err, preview.TugQL)
	}
	reader, err := dal.ExecuteRecursiveQuery(context.Background(), db, query)
	if err != nil {
		t.Fatalf("execute preview (scope %d): %v\n%s", preview.Scope, err, preview.TugQL)
	}
	defer func() { _ = reader.Close() }()
	var result []map[string]any
	for {
		record, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			t.Fatalf("read recursive result: %v", err)
		}
		data, ok := record.Data().(map[string]any)
		if !ok {
			t.Fatalf("record data type = %T, want map[string]any", record.Data())
		}
		result = append(result, data)
	}
}

func fieldStrings(rows []map[string]any, field string) []string {
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		values = append(values, fmt.Sprint(row[field]))
	}
	return values
}

func TestRecursiveSQLiteGroupingAndSortingRespectScopePredicatesAndOffset(t *testing.T) {
	t.Parallel()
	db := newRecursiveSQLite(t)
	allSix := strings.Replace(recursiveSalesQuery, "limit: 2", "limit: 6", 1)
	matchedRows := executeRecursivePreview(t, db, Preview{TugQL: allSix, Scope: MatchingSource})
	if len(matchedRows) != 6 {
		t.Fatalf("joined/filtered source returned %d rows, want six before captured-output bound", len(matchedRows))
	}
	for _, row := range matchedRows {
		if row["category"] == "Z" {
			t.Fatalf("join/WHERE-excluded Z row escaped into matching source: %+v", row)
		}
	}

	grouped, err := DeriveGroup(recursiveSalesQuery, []string{"category"}, true, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	groupRows := executeRecursivePreview(t, db, grouped)
	if got := fieldStrings(groupRows, "category"); fmt.Sprint(got) != "[A B]" {
		t.Fatalf("matching-source groups = %v, want A and B only (the joined/filtered Z rows must be excluded)", got)
	}
	if got := fieldStrings(groupRows, "count"); fmt.Sprint(got) != "[3 3]" {
		t.Fatalf("matching-source counts = %v, want three rows in each source group", got)
	}
	if len(groupRows) != 2 {
		t.Fatalf("group preview returned %d rows, want the original two-row bound", len(groupRows))
	}

	sorted, err := DeriveSort(recursiveSalesQuery, []SortTerm{{Column: "total", Descending: true}}, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	sortedRows := executeRecursivePreview(t, db, sorted)
	if got := fieldStrings(sortedRows, "total"); fmt.Sprint(got) != "[60 50]" {
		t.Fatalf("matching-source top rows = %v, want 60 and 50 outside original 10/20 limit", got)
	}
	for _, row := range sortedRows {
		if row["category"] == "Z" {
			t.Fatalf("join/WHERE-excluded Z row escaped into sort preview: %+v", row)
		}
	}

	// The source has six matching rows, but its captured output is only 10/20.
	// OriginalOutput sorting may reorder that two-row subset, never discover 60/50.
	originalOutput, err := DeriveSort(recursiveSalesQuery, []SortTerm{{Column: "total", Descending: true}}, OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	outputRows := executeRecursivePreview(t, db, originalOutput)
	if got := fieldStrings(outputRows, "total"); fmt.Sprint(got) != "[20 10]" {
		t.Fatalf("original-output sort = %v, want only captured values 20/10", got)
	}

	withOffset := strings.Replace(recursiveSalesQuery, "limit: 2\n", "limit: 2\noffset: 1\n", 1)
	matchingOffset, err := DeriveSort(withOffset, []SortTerm{{Column: "total", Descending: true}}, MatchingSource)
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldStrings(executeRecursivePreview(t, db, matchingOffset), "total"); fmt.Sprint(got) != "[50 40]" {
		t.Fatalf("matching-source offset after sort = %v, want 50/40", got)
	}
	outputOffset, err := DeriveSort(withOffset, []SortTerm{{Column: "total", Descending: true}}, OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldStrings(executeRecursivePreview(t, db, outputOffset), "total"); fmt.Sprint(got) != "[30 20]" {
		t.Fatalf("original-output offset scope = %v, want captured rows 20/30 reordered to 30/20", got)
	}
}

func TestRecursiveSQLiteOriginalOutputGroupingKeepsHavingAndAggregation(t *testing.T) {
	t.Parallel()
	db := newRecursiveSQLite(t)
	aggregated := `from: {name: Sales, alias: s, joins: [{from: {name: Parents, alias: p}, on: [{left: {field: parentId, source: s}, op: '==', right: {field: id, source: p}}]}]}
where: {and: [{op: '<=', left: {field: id, source: s}, right: {value: 9}}, {op: '==', left: {field: active, source: p}, right: {value: 1}}]}
groupBy: [{field: category, source: s}]
having: {op: '>', left: {aggregate: {function: count, args: [{star: true}]}}, right: {value: 1}}
orderBy: [{field: category, source: s, desc: true}]
limit: 1
columns: [{field: category, source: s, as: category}, {aggregate: {function: count, args: [{star: true}]}, as: count}]
`
	preview, err := DeriveGroup(aggregated, []string{"category"}, true, OriginalOutput)
	if err != nil {
		t.Fatal(err)
	}
	rows := executeRecursivePreview(t, db, preview)
	if len(rows) != 1 || rows[0]["category"] != "B" || fmt.Sprint(rows[0]["count"]) != "1" {
		t.Fatalf("original-output aggregate/HAVING result = %+v, want B with outer count 1", rows)
	}
	if !strings.Contains(preview.TugQL, "having:") {
		t.Fatalf("recursive preview lost inner HAVING:\n%s", preview.TugQL)
	}
}
