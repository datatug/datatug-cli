package dbcopy

import (
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
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
}

func (p *recordingCopyPlan) EncodeRow(_ CopyTable, _ dbschema.SourceRow) ([]byte, error) {
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

func TestCopyToSinkPreservesPhysicalRowsFromKeylessSQLiteTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyless.db")
	raw, err := sqlOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE discounts (code TEXT, rate INTEGER)`,
		`INSERT INTO discounts VALUES ('A', 10), ('A', 10), ('B', 25)`,
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
	plan := &recordingCopyPlan{}
	summary, err := CopyToSink(context.Background(), source, recordingCopySink{plan: plan}, CopyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.tables) != 1 || len(plan.tables[0].Definition.PrimaryKey) != 0 {
		t.Fatalf("preflight tables = %#v, expected one keyless table", plan.tables)
	}
	var tableRows int64
	for _, count := range summary.RowsByTable {
		tableRows += count
	}
	if !plan.prepared || plan.loadedRows != 3 || summary.RowsCopied != 3 || tableRows != 3 {
		t.Fatalf("copy summary=%#v plan=%#v", summary, plan)
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
