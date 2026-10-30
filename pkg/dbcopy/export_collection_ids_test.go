package dbcopy

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo2sqlite"
	"github.com/ingitdb/ingitdb-go/ingitdb/datavalidator"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	_ "modernc.org/sqlite"
)

func TestNativeCollectionID_InjectiveAndValid(t *testing.T) {
	inputs := map[string]string{
		"Album":                         "Album",
		"Order Details":                 "dt_4f726465722044657461696c73",
		"dt_4f726465722044657461696c73": "dt_64745f3466373236343635373232303434363537343631363936633733",
		"DT_4f726465722044657461696c73": "dt_44545f3466373236343635373232303434363537343631363936633733",
		"café":                          "dt_636166c3a9",
		".hidden":                       "dt_2e68696464656e",
		"trailing-":                     "dt_747261696c696e672d",
		"a/b":                           "dt_612f62",
		"CON":                           "dt_434f4e",
		"nul.txt":                       "dt_6e756c2e747874",
		"COM1":                          "dt_434f4d31",
	}
	seen := map[string]string{}
	for source, want := range inputs {
		got, err := nativeCollectionID(source)
		if err != nil || got != want {
			t.Fatalf("nativeCollectionID(%q) = %q, %v; want %q", source, got, err, want)
		}
		if prior, ok := seen[got]; ok {
			t.Fatalf("%q and %q map to the same ID %q", prior, source, got)
		}
		seen[got] = source
	}
	for _, source := range []string{"", "bad\x00name", "bad\nname", strings.Repeat("x", 256)} {
		if _, err := nativeCollectionID(source); err == nil {
			t.Fatalf("accepted unrepresentable source name %q", source)
		}
	}
}

func TestExportInGitDB_EncodedNamesPreserveSourceAndRelationships(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	for _, statement := range []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE "Order Headers" (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE "Order Details" (id INTEGER PRIMARY KEY, order_id INTEGER REFERENCES "Order Headers"(id))`,
		`INSERT INTO "Order Headers" VALUES (1)`,
		`INSERT INTO "Order Details" VALUES (7, 1)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := dalgo2sqlite.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "ingr"} {
		t.Run(format, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "native")
			counts, err := ExportInGitDB(context.Background(), source, destination, ExportOptions{RecordsFormat: format})
			if err != nil {
				t.Fatal(err)
			}
			if counts["Order Headers"] != 1 || counts["Order Details"] != 1 {
				t.Fatalf("source-name counts: %v", counts)
			}
			b, err := os.ReadFile(filepath.Join(destination, ".ingitdb", "source-collections.json"))
			if err != nil {
				t.Fatal(err)
			}
			var mapping struct {
				Format      string            `json:"format"`
				Collections map[string]string `json:"collections"`
			}
			if err := json.Unmarshal(b, &mapping); err != nil {
				t.Fatal(err)
			}
			childID, _ := nativeCollectionID("Order Details")
			parentID, _ := nativeCollectionID("Order Headers")
			if mapping.Format != "datatug-source-collections/v1" || len(mapping.Collections) != 2 ||
				mapping.Collections[childID] != "Order Details" || mapping.Collections[parentID] != "Order Headers" {
				t.Fatalf("source-name mapping: %+v", mapping)
			}
			def, err := validator.ReadDefinition(destination)
			if err != nil {
				t.Fatal(err)
			}
			child := def.Collections[childID]
			if child == nil || child.SourceSchema == nil || len(child.SourceSchema.ForeignKeys) != 1 ||
				child.SourceSchema.ForeignKeys[0].ReferencedCollection != parentID ||
				!strings.Contains(child.SourceSchema.SourceDefinitionJSON, `Order Details`) {
				t.Fatalf("encoded source schema: %+v", child)
			}
			result, err := datavalidator.NewValidator().Validate(context.Background(), destination, def)
			if err != nil || len(result.Errors()) != 0 {
				t.Fatalf("native validation: %v, %v", result, err)
			}
		})
	}
}
