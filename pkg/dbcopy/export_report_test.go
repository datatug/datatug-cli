package dbcopy

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
)

// reportSource is a SQLite database with a parent table, a child table that references it, a unique and
// a plain index, and a table with no key.
func reportSource(t *testing.T) dal.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE p (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id), n TEXT)`,
		`CREATE UNIQUE INDEX c_n ON c(n)`,
		`CREATE INDEX c_p ON c(p)`,
		`INSERT INTO p VALUES (1,'a'),(2,'b'),(3,'c')`,
		`INSERT INTO c VALUES (1,1,'x'),(2,2,'y'),(3,3,'z'),(4,3,'w')`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func rowsOf(result *ExportResult) map[string]int64 {
	rows := map[string]int64{}
	for _, table := range result.Tables {
		rows[table.Name] = table.Rows
	}
	return rows
}

func TestExportReportDescribesTables(t *testing.T) {
	result, err := ExportInGitDBReport(context.Background(), reportSource(t), filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tables) != 2 || len(result.Warnings) != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, table := range result.Tables {
		if table.Name != "c" {
			continue
		}
		if table.Rows != 4 || table.Columns != 3 || len(table.PrimaryKey) != 1 || table.PrimaryKey[0] != "id" || table.ForeignKeys != 1 {
			t.Fatalf("table c = %+v", table)
		}
		if table.UniqueConstraints != 1 || table.Indexes != 1 {
			t.Fatalf("indexes of c = %+v", table)
		}
	}
}

func TestExportIncludeDropsForeignKeysToLeftOutTables(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "out")
	result, err := ExportInGitDBReport(context.Background(), reportSource(t), destination,
		ExportOptions{Filters: &filter.Directives{IncludeTables: []string{"c"}}})
	if err != nil {
		t.Fatal(err)
	}
	if rows := rowsOf(result); len(rows) != 1 || rows["c"] != 4 {
		t.Fatalf("rows = %v", rows)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Kind != "foreign-key-dropped" || !strings.HasPrefix(result.Warnings[0].Object, "c.") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	if result.Tables[0].ForeignKeys != 0 {
		t.Fatalf("foreign keys = %d", result.Tables[0].ForeignKeys)
	}
	if _, err := os.Stat(filepath.Join(destination, "p")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("p was written: %v", err)
	}
}

func TestExportExcludeAndUnknownTable(t *testing.T) {
	source := reportSource(t)
	result, err := ExportInGitDB(context.Background(), source, filepath.Join(t.TempDir(), "out"),
		ExportOptions{Filters: &filter.Directives{ExcludeTables: []string{"c"}}})
	if err != nil || len(result) != 1 || result["p"] != 3 {
		t.Fatalf("result = %v, %v", result, err)
	}
	_, err = ExportInGitDB(context.Background(), source, filepath.Join(t.TempDir(), "out"),
		ExportOptions{Filters: &filter.Directives{IncludeTables: []string{"nope"}}})
	if err == nil || !strings.Contains(err.Error(), "not present on source") {
		t.Fatalf("err = %v", err)
	}
}

func TestExportNothingSelected(t *testing.T) {
	_, err := ExportInGitDBReport(context.Background(), reportSource(t), filepath.Join(t.TempDir(), "out"),
		ExportOptions{Filters: &filter.Directives{ExcludeTables: []string{"p", "c"}}})
	if !errors.Is(err, ErrNoCollectionsSelected) {
		t.Fatalf("err = %v", err)
	}
}

func TestExportWhereAndLimitNarrowRows(t *testing.T) {
	filters := &filter.Directives{
		Where:         map[string]*filter.PredicateGroup{"p": {Conditions: []filter.Predicate{{Field: "id", Operator: ">", Value: "1"}}}},
		LimitsByTable: map[string]int{"c": 2},
	}
	destination := filepath.Join(t.TempDir(), "out")
	result, err := ExportInGitDBReport(context.Background(), reportSource(t), destination, ExportOptions{Filters: filters, RecordsFormat: "jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if rows := rowsOf(result); rows["p"] != 2 || rows["c"] != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Kind != "filtered-parent" || !strings.HasPrefix(result.Warnings[0].Object, "c.") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	data, err := os.ReadFile(filepath.Join(destination, "c", "records.jsonl"))
	if err != nil || strings.Count(string(data), "\n") != 2 {
		t.Fatalf("records = %q, %v", data, err)
	}
}

func TestExportWhereAndLimitErrors(t *testing.T) {
	source := reportSource(t)
	destination := filepath.Join(t.TempDir(), "out")
	// A predicate on a table the export leaves out.
	_, err := ExportInGitDBReport(context.Background(), source, destination, ExportOptions{Filters: &filter.Directives{
		IncludeTables: []string{"c"},
		Where:         map[string]*filter.PredicateGroup{"p": {Conditions: []filter.Predicate{{Field: "id", Operator: "=", Value: "1"}}}},
		LimitsByTable: map[string]int{"zzz": 1},
	}})
	if err == nil || !strings.Contains(err.Error(), "--limit zzz") || !strings.Contains(err.Error(), "--where p") {
		t.Fatalf("err = %v", err)
	}
	// A predicate on a column the table does not have.
	_, err = ExportInGitDBReport(context.Background(), source, destination, ExportOptions{Filters: &filter.Directives{
		Where: map[string]*filter.PredicateGroup{"p": {Conditions: []filter.Predicate{{Field: "nope", Operator: "=", Value: "1"}}}},
	}})
	if err == nil || !strings.Contains(err.Error(), "compile where") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a failed export left %s: %v", destination, statErr)
	}
}

func TestExportRowFiltersOnAnEmptyPredicateGroup(t *testing.T) {
	filters := &filter.Directives{Where: map[string]*filter.PredicateGroup{"p": {}}}
	result, err := ExportInGitDBReport(context.Background(), reportSource(t), filepath.Join(t.TempDir(), "out"), ExportOptions{Filters: filters})
	if err != nil || rowsOf(result)["p"] != 3 {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestExportDryRunWritesNothing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	parent := t.TempDir()
	destination := filepath.Join(parent, "out")
	result, err := ExportInGitDBReport(context.Background(), reportSource(t), destination, ExportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rows := rowsOf(result); rows["p"] != 3 || rows["c"] != 4 {
		t.Fatalf("rows = %v", rows)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("a dry run touched the destination folder: %v", entries)
	}
	entries, _ := os.ReadDir(tmp)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".datatug-export-") {
			t.Fatalf("a dry run left its staging folder %s", entry.Name())
		}
	}
	// A dry run still refuses an existing destination.
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportInGitDBReport(context.Background(), reportSource(t), destination, ExportOptions{DryRun: true}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
}

func TestExportTableReportAndHelpersWithNoFilters(t *testing.T) {
	if err := checkFilterTables(nil, nil); err != nil {
		t.Fatal(err)
	}
	result := &ExportResult{}
	warnFilteredParents(nil, nil, result)
	builder := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("p", "")))
	if _, narrowed, err := applyExportRowFilters(builder, nil, dbschema.CollectionDef{Name: "p"}, nil); err != nil || narrowed {
		t.Fatalf("narrowed = %v, %v", narrowed, err)
	}
	def := &dbschema.CollectionDef{Name: "t"}
	if kept := dropUnselectedForeignKeys(def, nil, nil, result); len(kept) != 0 {
		t.Fatalf("kept = %v", kept)
	}
	// A key to a collection the source does not have is kept, for the relationship check to refuse.
	def.ForeignKeys = []dbschema.ForeignKeyDef{{Name: "fk", ReferencedCollection: "ghost"}}
	if kept := dropUnselectedForeignKeys(def, map[string]bool{"t": true}, map[string]bool{"t": true}, result); len(kept) != 1 || len(result.Warnings) != 0 {
		t.Fatalf("kept = %v, warnings = %v", kept, result.Warnings)
	}
}
