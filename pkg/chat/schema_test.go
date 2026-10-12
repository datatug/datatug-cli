package chat

import (
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
)

func TestFormatSchemaContext(t *testing.T) {
	schema := &api.CatalogSchema{Relations: []api.CatalogRelation{{
		Schema: "main", Name: "Customer", DbType: "BASE TABLE",
		Columns: []api.CatalogColumn{{Name: "CustomerId", DbType: "INTEGER"}, {Name: "City", DbType: "TEXT"}},
	}}}
	got := FormatSchemaContext(schema)
	if got != "- Customer (schema: main; BASE TABLE): CustomerId [INTEGER], City [TEXT]" {
		t.Fatalf("schema context = %q", got)
	}
	if !strings.Contains(buildInstruction(got), "Never\nwrite SQL") {
		t.Fatal("instruction must forbid SQL output")
	}
	if FormatSchemaContext(nil) != "(no scanned tables or views)" {
		t.Fatal("nil schema fallback changed")
	}
}
