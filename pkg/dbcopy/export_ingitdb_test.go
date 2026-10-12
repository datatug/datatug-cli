package dbcopy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo2sqlite"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/datavalidator"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	_ "modernc.org/sqlite"
)

func TestExportInGitDB_Chinook(t *testing.T) {
	sourcePath, err := filepath.Abs("testdata/chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	source, err := dalgo2sqlite.NewDatabase(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	counts, err := ExportInGitDB(context.Background(), source, destination)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, count := range counts {
		total += count
	}
	if len(counts) != 11 || total != 15607 || counts["PlaylistTrack"] != 8715 {
		t.Fatalf("counts = %#v", counts)
	}
	def, err := validator.ReadDefinition(destination)
	if err != nil {
		t.Fatal(err)
	}
	result, err := datavalidator.NewValidator().Validate(context.Background(), destination, def)
	if err != nil || len(result.Errors()) != 0 {
		t.Fatalf("native validation = %v, %v", result, err)
	}
	playlist := def.Collections["PlaylistTrack"]
	if playlist.SourceSchema == nil || len(playlist.SourceSchema.ForeignKeys) != 2 || playlist.SourceSchema.ConstraintValidation != "provider-preflight-passed" {
		t.Fatalf("source relationships missing: %+v", playlist.SourceSchema)
	}
	if playlist.RecordFile.RecordType != ingitdb.MapOfRecords {
		t.Fatalf("record type = %q", playlist.RecordFile.RecordType)
	}
	files, err := filepath.Glob(filepath.Join(destination, "PlaylistTrack", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("record file count = %v, %v", files, err)
	}
}

func TestExportInGitDB_KeylessDecimalBlobAndViews(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
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
		`CREATE TABLE events (amount DECIMAL(30,8), payload BLOB, big INTEGER)`,
		`CREATE VIEW event_amounts AS SELECT amount FROM events`,
		`INSERT INTO events VALUES (1.25, x'00ff', 9223372036854775807)`,
		`INSERT INTO events VALUES (2.5, x'0102', 2)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	counts, err := ExportInGitDB(context.Background(), source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if counts["events"] != 2 {
		t.Fatalf("counts = %+v", counts)
	}
	def, err := validator.ReadDefinition(destination)
	if err != nil {
		t.Fatal(err)
	}
	events := def.Collections["events"]
	if events.SourceSchema.KeyMode != "export-ordinal" || len(events.PrimaryKey) != 0 {
		t.Fatalf("keyless source misrepresented: %+v", events)
	}
	if events.Columns["amount"].Type != ingitdb.ColumnTypeString || events.Columns["payload"].Type != ingitdb.ColumnTypeString {
		t.Fatalf("transport types = %+v", events.Columns)
	}
	data, err := os.ReadFile(filepath.Join(destination, "events", "records.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ingitdb.ParseMapOfRecordsContent(data, ingitdb.RecordFormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	first := parsed["row-000000000001"]
	if first["amount"] != "1.25" || first["payload"] != "AP8=" || first["big"] != int64(9223372036854775807) {
		t.Fatalf("transport loss: %#v", first)
	}
	views, err := os.ReadFile(filepath.Join(destination, ".ingitdb", "source-views.yaml"))
	if err != nil || !strings.Contains(string(views), "event_amounts") {
		t.Fatalf("view metadata missing: %s, %v", views, err)
	}
}

func TestExportInGitDB_NumericAndCollatedPrimaryKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
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
		`CREATE TABLE numeric_keys (v DECIMAL(20,2) PRIMARY KEY)`,
		`INSERT INTO numeric_keys VALUES (2), (10)`,
		`CREATE TABLE text_keys (v TEXT COLLATE NOCASE PRIMARY KEY)`,
		`INSERT INTO text_keys VALUES ('a'), ('B')`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := ExportInGitDB(context.Background(), source, filepath.Join(t.TempDir(), "native"))
	if err != nil {
		t.Fatal(err)
	}
	if counts["numeric_keys"] != 2 || counts["text_keys"] != 2 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestExportInGitDB_PreservesMixedDecimalStorageClasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
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
		`CREATE TABLE amounts (id INTEGER PRIMARY KEY, amount DECIMAL(10,2))`,
		`INSERT INTO amounts VALUES (1, 1.5), (2, 2)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	if _, err := ExportInGitDB(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	def, err := validator.ReadDefinition(destination)
	if err != nil {
		t.Fatal(err)
	}
	files := def.Collections["amounts"].SourceSchema.StorageClassFiles
	if len(files) != 1 {
		t.Fatalf("sidecar files = %v", files)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "amounts", files[0]))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		var entry struct {
			ID      string            `json:"id"`
			Classes map[string]string `json:"classes"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		seen[entry.Classes["amount"]] = true
	}
	if !seen["real"] || !seen["integer"] {
		t.Fatalf("mixed physical classes lost: %v", seen)
	}
}

func TestExportInGitDB_ConstraintFailureLeavesNoDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
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
		`CREATE TABLE parents (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id))`,
		`INSERT INTO children VALUES (1, 99)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	_, err = ExportInGitDB(context.Background(), source, destination)
	if err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("expected source constraint failure, got %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial destination published: %v", statErr)
	}
}

func TestExportInGitDB_RejectsUnsafeCollectionName(t *testing.T) {
	for _, name := range []string{"", "../other", "a/b", `.hidden`, `a\b`} {
		if err := safeExportName(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestExportInGitDB_RejectsUnrepresentableSQLiteAffinity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
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
		`CREATE TABLE strange (id INTEGER PRIMARY KEY, value INTEGER)`,
		`INSERT INTO strange VALUES (1, 'not-an-integer')`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	_, err = ExportInGitDB(context.Background(), source, destination)
	if err == nil || !strings.Contains(err.Error(), "mixed-affinity") {
		t.Fatalf("expected explicit mixed-affinity failure, got %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial destination published: %v", statErr)
	}
}

func TestExportInGitDB_RejectsInvalidSQLiteBooleanBeforePublishing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close source: %v", err)
		}
	})
	if _, err := raw.Exec(`CREATE TABLE flags (id INTEGER PRIMARY KEY, enabled BOOLEAN)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO flags VALUES (1, 2)`); err != nil {
		t.Fatal(err)
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "native")
	_, err = ExportInGitDB(context.Background(), source, destination)
	if err == nil || !strings.Contains(err.Error(), "BOOLEAN") {
		t.Fatalf("invalid BOOLEAN value was accepted or poorly diagnosed: %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial destination published: %v", err)
	}
}
