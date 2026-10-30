package dbcopy

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
)

type recordingCopySink struct{ plan *recordingCopyPlan }

func (s recordingCopySink) Preflight(_ context.Context, tables []CopyTable) (CopySinkPlan, error) {
	s.plan.tables = tables
	return s.plan, nil
}

type recordingCopyPlan struct {
	tables       []CopyTable
	prepared     bool
	loadedRows   int64
	loadedTables []string
	encodedRows  []dbschema.SourceRow
}

func (p *recordingCopyPlan) EncodeRow(_ CopyTable, row dbschema.SourceRow) ([]byte, error) {
	p.encodedRows = append(p.encodedRows, row)
	return []byte("{}\n"), nil
}
func (p *recordingCopyPlan) TargetName(table CopyTable) string { return table.Ref.Name() }
func (p *recordingCopyPlan) Prepare(context.Context) error     { p.prepared = true; return nil }
func (p *recordingCopyPlan) LoadTable(_ context.Context, table CopyTable, rows io.Reader) (string, int64, error) {
	data, err := io.ReadAll(rows)
	if err != nil {
		return "", 0, err
	}
	count := int64(strings.Count(string(data), "{}\n"))
	p.loadedRows += count
	p.loadedTables = append(p.loadedTables, table.Ref.Name())
	return table.Ref.Name(), count, nil
}

func TestCopyToSinkPreservesPhysicalRowsFromKeylessSQLiteTableWithTableSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyless.db")
	raw, err := sqlOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE discounts (code TEXT, rate INTEGER)`,
		`INSERT INTO discounts VALUES ('A', 10), ('A', 10), ('B', 25)`,
		`CREATE TABLE ignored (code TEXT, rate INTEGER)`,
		`INSERT INTO ignored VALUES ('X', 99)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		filters       *filter.Directives
		wantTables    int
		wantTotalRows int64
	}{
		{name: "no table filter", wantTables: 2, wantTotalRows: 4},
		{name: "include", filters: &filter.Directives{IncludeTables: []string{"discounts"}}, wantTables: 1, wantTotalRows: 3},
		{name: "exclude", filters: &filter.Directives{ExcludeTables: []string{"ignored"}}, wantTables: 1, wantTotalRows: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &recordingCopyPlan{}
			summary, err := CopyToSink(context.Background(), source, recordingCopySink{plan: plan}, CopyOpts{Filters: tc.filters})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.tables) != tc.wantTables || plan.tables[0].Ref.Name() != "discounts" || len(plan.tables[0].Definition.PrimaryKey) != 0 {
				t.Fatalf("preflight tables = %#v, expected %d tables with keyless discounts first", plan.tables, tc.wantTables)
			}
			if !plan.prepared || plan.loadedRows != tc.wantTotalRows || summary.RowsCopied != tc.wantTotalRows || summary.RowsByTable["discounts"] != 3 {
				t.Fatalf("copy summary=%#v plan=%#v", summary, plan)
			}
			if got := len(plan.encodedRows); got != int(tc.wantTotalRows) {
				t.Fatalf("encoded rows = %d, want %d physical rows including duplicates", got, tc.wantTotalRows)
			}
			for i, row := range plan.encodedRows {
				if row.StorageClasses["code"] != "text" || row.StorageClasses["rate"] != "integer" {
					t.Fatalf("row %d lacks native SQLite storage metadata: %#v", i, row.StorageClasses)
				}
			}
		})
	}
}

func TestCopyToSinkNormalizesFilteredSQLiteBooleanAndRejectsInvalidValuesBeforePrepare(t *testing.T) {
	newSource := func(t *testing.T, enabledValues string) dalgo2sqlite.Database {
		t.Helper()
		path := filepath.Join(t.TempDir(), "booleans.db")
		raw, err := sqlOpen(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range []string{
			`CREATE TABLE flags (id INTEGER PRIMARY KEY, enabled BOOLEAN)`,
			`INSERT INTO flags VALUES ` + enabledValues,
		} {
			if _, err := raw.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		source, err := dalgo2sqlite.NewDatabase(path)
		if err != nil {
			t.Fatal(err)
		}
		return *source
	}
	filters := &filter.Directives{Where: map[string]*filter.PredicateGroup{
		"flags": {Conditions: []filter.Predicate{{Field: "id", Operator: filter.OpGreaterOrEqual, Value: "1"}}},
	}}

	t.Run("physical SQLite boolean values stay bool", func(t *testing.T) {
		source := newSource(t, "(1, 1), (2, 0), (3, NULL)")
		plan := &recordingCopyPlan{}
		summary, err := CopyToSink(context.Background(), &source, recordingCopySink{plan: plan}, CopyOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if summary.RowsCopied != 3 || len(plan.encodedRows) != 3 || plan.encodedRows[0].Values["enabled"] != true || plan.encodedRows[1].Values["enabled"] != false || plan.encodedRows[2].Values["enabled"] != nil {
			t.Fatalf("physical boolean summary=%#v rows=%#v", summary, plan.encodedRows)
		}
	})

	t.Run("valid integer boolean values become bool", func(t *testing.T) {
		source := newSource(t, "(1, 1), (2, 0), (3, NULL)")
		plan := &recordingCopyPlan{}
		summary, err := CopyToSink(context.Background(), &source, recordingCopySink{plan: plan}, CopyOpts{Filters: filters})
		if err != nil {
			t.Fatal(err)
		}
		if summary.RowsCopied != 3 || !plan.prepared {
			t.Fatalf("summary=%#v plan=%#v", summary, plan)
		}
		if len(plan.encodedRows) != 3 || plan.encodedRows[0].Values["enabled"] != true || plan.encodedRows[1].Values["enabled"] != false || plan.encodedRows[2].Values["enabled"] != nil {
			t.Fatalf("filtered boolean rows = %#v, want true, false, and NULL", plan.encodedRows)
		}
		if value, ok := plan.encodedRows[1].Values["id"].(int64); !ok || value != 2 {
			t.Fatalf("ordinary INTEGER id was reinterpreted: %T(%v)", plan.encodedRows[1].Values["id"], plan.encodedRows[1].Values["id"])
		}
	})

	t.Run("boolean row filter uses typed boolean constant", func(t *testing.T) {
		source := newSource(t, "(1, 1), (2, 0)")
		booleanFilters := &filter.Directives{Where: map[string]*filter.PredicateGroup{
			"flags": {Conditions: []filter.Predicate{{Field: "enabled", Operator: filter.OpEqual, Value: "true"}}},
		}}
		plan := &recordingCopyPlan{}
		summary, err := CopyToSink(context.Background(), &source, recordingCopySink{plan: plan}, CopyOpts{Filters: booleanFilters})
		if err != nil {
			t.Fatal(err)
		}
		if summary.RowsCopied != 1 || len(plan.encodedRows) != 1 || plan.encodedRows[0].Values["enabled"] != true {
			t.Fatalf("boolean-filtered summary=%#v rows=%#v", summary, plan.encodedRows)
		}
	})

	for _, tc := range []struct {
		name   string
		values string
	}{
		{name: "integer outside boolean domain", values: "(1, 1), (2, 2)"},
		{name: "text storage class", values: "(1, 1), (2, 'true')"},
		{name: "real storage class", values: "(1, 1), (2, 1.5)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := newSource(t, tc.values)
			plan := &recordingCopyPlan{}
			_, err := CopyToSink(context.Background(), &source, recordingCopySink{plan: plan}, CopyOpts{Filters: filters})
			if err == nil {
				t.Fatal("invalid SQLite BOOLEAN value was accepted")
			}
			if plan.prepared {
				t.Fatalf("target Prepare ran before source value validation: %v", err)
			}
		})
	}
}

func TestCopyToSinkReportsStagingCleanupFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.db")
	raw, err := sqlOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`CREATE TABLE items (id INTEGER PRIMARY KEY)`, `INSERT INTO items VALUES (1)`} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	removeStagingDir := removeCopyStagingDir
	removeCopyStagingDir = func(dir string) error {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return errors.New("simulated staging cleanup failure")
	}
	t.Cleanup(func() { removeCopyStagingDir = removeStagingDir })
	_, err = CopyToSink(context.Background(), source, recordingCopySink{plan: &recordingCopyPlan{}}, CopyOpts{})
	if !errors.Is(err, ErrStagingCleanup) {
		t.Fatalf("CopyToSink cleanup error = %v, want it surfaced", err)
	}
}

func TestCopyToSinkReadsDirectInGitDBSource(t *testing.T) {
	sourcePath, err := filepath.Abs("testdata/chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	sqliteSource, err := dalgo2sqlite.NewDatabase(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	ingitPath := filepath.Join(t.TempDir(), "native-project")
	if _, err := ExportInGitDB(context.Background(), sqliteSource, ingitPath); err != nil {
		t.Fatal(err)
	}
	source, err := dalgo2ingitdb.NewDatabase(ingitPath, validator.NewCollectionsReader())
	if err != nil {
		t.Fatal(err)
	}
	plan := &recordingCopyPlan{}
	summary, err := CopyToSink(context.Background(), source, recordingCopySink{plan: plan}, CopyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Tables != 11 || summary.RowsCopied != 15607 || plan.loadedRows != 15607 || len(summary.RowSkips) != 0 {
		t.Fatalf("inGitDB copy summary=%#v plan=%#v", summary, plan)
	}
}

func sqlOpen(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }
