package dbcopy

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/datavalidator"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
)

// queryOnlySource exposes the standard DALgo schema/query interfaces but no
// optional source-row cursor. This exercises the generic fallback path.
type queryOnlySource struct {
	dal.DB
	dbschema.SchemaReader
}

type aliasingSource struct {
	dal.DB
	dbschema.SchemaReader
}

func (aliasingSource) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("Foo", ""), dal.NewRootCollectionRef("foo", "")}, nil
}

func (aliasingSource) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name: ref.Name(), Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
		PrimaryKey: []dal.FieldName{"id"},
	}, nil
}

func TestExportInGitDB_FilesystemAliasCannotOverwriteCollection(t *testing.T) {
	probe := t.TempDir()
	if err := os.Mkdir(filepath.Join(probe, "Foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(probe, "foo")); os.IsNotExist(err) {
		t.Skip("filesystem treats case variants as distinct; injected reservation test covers the error path")
	} else if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "native")
	_, err := ExportInGitDB(context.Background(), aliasingSource{}, dest)
	if err == nil || !strings.Contains(err.Error(), "reserve source collection") {
		t.Fatalf("case-aliasing source collections accepted: %v", err)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("case-aliasing export published destination: %v", err)
	}
}

func TestReserveExportCollectionDirs_MkdirFailure(t *testing.T) {
	stage := t.TempDir()
	tables := []exportTable{{def: dbschema.CollectionDef{Name: "first"}}, {def: dbschema.CollectionDef{Name: "second"}}}
	calls := 0
	err := reserveExportCollectionDirs(stage, tables, func(path string, mode os.FileMode) error {
		calls++
		if calls == 2 {
			return os.ErrExist
		}
		return os.Mkdir(path, mode)
	})
	if err == nil || !strings.Contains(err.Error(), "second") || calls != 2 {
		t.Fatalf("reservation failure not reported: calls=%d err=%v", calls, err)
	}
}

type cancelOnViewsSource struct {
	queryOnlySource
	cancel context.CancelFunc
}

func (s cancelOnViewsSource) ListSourceViews(context.Context) ([]dbschema.SourceViewDef, error) {
	s.cancel()
	return nil, nil
}

func TestExportInGitDB_CanceledAfterRowsDoesNotPublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close source: %v", err)
		}
	})
	if _, err := raw.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY); INSERT INTO items VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	wrapped, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, ok := dal.As[dbschema.SchemaReader](wrapped)
	if !ok {
		t.Fatal("SQLite schema reader unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	dest := filepath.Join(t.TempDir(), "native")
	_, err = ExportInGitDB(ctx, cancelOnViewsSource{queryOnlySource{DB: wrapped, SchemaReader: reader}, cancel}, dest)
	if err != context.Canceled {
		t.Fatalf("export cancellation = %v", err)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("canceled export published destination: %v", err)
	}
}

func TestExportInGitDB_GenericDALgoQueryFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close source: %v", err)
		}
	})
	for _, stmt := range []string{`CREATE TABLE items (id INTEGER PRIMARY KEY, note TEXT)`, `INSERT INTO items VALUES (1,'one'),(2,'two')`} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	wrapped, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, ok := dal.As[dbschema.SchemaReader](wrapped)
	if !ok {
		t.Fatal("SQLite schema reader unavailable")
	}
	source := queryOnlySource{DB: wrapped, SchemaReader: reader}
	if _, ok := dal.As[dbschema.SourceRowsReader](source); ok {
		t.Fatal("query fallback still exposes native cursor")
	}
	dest := filepath.Join(t.TempDir(), "native")
	counts, err := ExportInGitDB(context.Background(), source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if counts["items"] != 2 {
		t.Fatalf("counts: %v", counts)
	}
	def, err := validator.ReadDefinition(dest, ingitdb.Validate())
	if err != nil {
		t.Fatal(err)
	}
	rows := readFormatRows(t, dest, def.Collections["items"])
	if len(rows) != 2 {
		t.Fatalf("rows: %v", rows)
	}
}

func TestExportInGitDB_RecordsFormatsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close source: %v", err)
		}
	})
	for _, statement := range []string{
		`CREATE TABLE sample (amount DECIMAL(30,8), payload BLOB, big INTEGER, note TEXT)`,
		`INSERT INTO sample VALUES (1.25, x'00ff', 9223372036854775807, NULL)`,
		`INSERT INTO sample VALUES (2, x'0102', 2, '')`,
		`INSERT INTO sample VALUES (3.5, x'0304', 3, 'comma, "quoted"' || char(10) || 'line' || char(0) || 'zero')`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "JSONL", "ingr", "CSV", "yaml"} {
		t.Run(format, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "native")
			counts, err := ExportInGitDB(context.Background(), source, dest, ExportOptions{RecordsFormat: format})
			if err != nil {
				t.Fatal(err)
			}
			if counts["sample"] != 3 {
				t.Fatalf("counts: %v", counts)
			}
			def, err := validator.ReadDefinition(dest, ingitdb.Validate())
			if err != nil {
				t.Fatal(err)
			}
			findings, err := datavalidator.NewValidator().Validate(context.Background(), dest, def)
			if err != nil || len(findings.Errors()) != 0 {
				t.Fatalf("native validation: %v, %v", findings, err)
			}
			collection := def.Collections["sample"]
			rows := readFormatRows(t, dest, collection)
			if len(rows) != 3 {
				t.Fatalf("rows: %d", len(rows))
			}
			first := rows["row-000000000001"]
			second := rows["row-000000000002"]
			third := rows["row-000000000003"]
			if first["amount"] != "1.25" || first["payload"] != "AP8=" || fmt.Sprint(first["big"]) != "9223372036854775807" || first["note"] != nil {
				t.Fatalf("first row lost values: %#v", first)
			}
			if second["amount"] != "2" || second["payload"] != "AQI=" || second["note"] != "" {
				t.Fatalf("second row lost values: %#v", second)
			}
			if third["amount"] != "3.5" || third["note"] != "comma, \"quoted\"\nline\x00zero" {
				t.Fatalf("third row lost values: %#v", third)
			}
			if len(collection.SourceSchema.Fields) != 4 || collection.SourceSchema.KeyMode != "export-ordinal" {
				t.Fatalf("source metadata: %+v", collection.SourceSchema)
			}
			if strings.EqualFold(format, "csv") {
				if collection.RecordFile.CSVCellEncoding != "json-v1" || collection.ColumnsOrder[0] != "$ID" {
					t.Fatalf("typed CSV metadata: %+v", collection)
				}
			}
			adapter, err := dalgo2ingitdb.NewDatabase(dest, validator.NewCollectionsReader())
			if err != nil {
				t.Fatal(err)
			}
			reexport := filepath.Join(t.TempDir(), "reexport")
			recounts, err := ExportInGitDB(context.Background(), adapter, reexport)
			if err != nil {
				t.Fatalf("adapter re-export: %v", err)
			}
			if recounts["sample"] != 3 {
				t.Fatalf("re-export counts: %v", recounts)
			}
			redef, err := validator.ReadDefinition(reexport, ingitdb.Validate())
			if err != nil {
				t.Fatal(err)
			}
			results, err := datavalidator.NewValidator().Validate(context.Background(), reexport, redef)
			if err != nil || len(results.Errors()) != 0 {
				t.Fatalf("re-export validation: %v, %v", results, err)
			}
			reRows := readFormatRows(t, reexport, redef.Collections["sample"])
			if reRows["row-000000000001"]["payload"] != "AP8=" || fmt.Sprint(reRows["row-000000000001"]["big"]) != "9223372036854775807" || reRows["row-000000000002"]["note"] != "" {
				t.Fatalf("adapter re-export lost values: %#v", reRows)
			}
			if redef.Collections["sample"].SourceSchema.ConstraintValidation != "unverified" || len(redef.Collections["sample"].SourceSchema.StorageClassFiles) != 0 {
				t.Fatalf("second-hop physical provenance falsely asserted: %+v", redef.Collections["sample"].SourceSchema)
			}
		})
	}
}

func readFormatRows(t *testing.T, root string, def *ingitdb.CollectionDef) map[string]map[string]any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, def.ID, def.RecordFile.Name))
	if err != nil {
		t.Fatal(err)
	}
	if def.RecordFile.RecordType == ingitdb.MapOfRecords {
		rows, err := ingitdb.ParseMapOfRecordsContent(content, def.RecordFile.Format)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	var list []map[string]any
	if def.RecordFile.Format == ingitdb.RecordFormatCSV {
		parsed, err := ingitdb.ParseRecordContentForCollection(content, def)
		if err != nil {
			t.Fatal(err)
		}
		list = parsed["$records"].([]map[string]any)
	} else {
		list, err = ingitdb.ParseListOfRecordsContent(content, def.RecordFile.Format)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows := make(map[string]map[string]any, len(list))
	for _, row := range list {
		id, ok := ingitdb.ResolveListRecordKey(row, def)
		if !ok {
			t.Fatalf("unkeyed list row: %#v", row)
		}
		rows[id] = row
	}
	return rows
}

func TestExportInGitDB_UnknownRecordsFormatLeavesNoDestination(t *testing.T) {
	source, err := dalgo2sqlite.NewDatabase(filepath.Join(t.TempDir(), "unused.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "native")
	_, err = ExportInGitDB(context.Background(), source, dest, ExportOptions{RecordsFormat: "parquet"})
	if err == nil || !strings.Contains(err.Error(), "unsupported records format") {
		t.Fatalf("error: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination unexpectedly exists: %v", err)
	}
}
