package querybody

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestStructuredUsesExplicitRepresentation(t *testing.T) {
	yaml := "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\n"
	for _, query := range []datatug.QueryDef{
		{Type: datatug.QueryTypeTugQL, StructuredBody: true, Text: yaml},
		{Type: datatug.QueryTypeSQL, Text: "SELECT 1"},
	} {
		got, err := Structured(&query)
		if err != nil || got != query.Text {
			t.Fatalf("explicit body changed: %q / %v", got, err)
		}
	}
	// YAML in a text file is not silently reinterpreted as structured input.
	if _, err := Structured(&datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: yaml}); err == nil {
		t.Fatal("structured YAML accepted as text")
	}
	text := "from Invoice as i\nwhere i.InvoiceId = 7\nlimit 3\nselect i.InvoiceId"
	got, err := Structured(&datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tugql.Deserialize([]byte(got)); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got), &body); err != nil {
		t.Fatal(err)
	}
	if body["from"].(map[string]any)["name"] != "Invoice" || body["limit"] != float64(3) || !strings.Contains(got, "InvoiceId") || !strings.Contains(got, "7") {
		t.Fatalf("text semantics lost: %s", got)
	}
}

func TestStructuredRefusesAuthoringContextInsteadOfDroppingIt(t *testing.T) {
	for _, text := range []string{
		"parameters (\n  @id integer required\n)\nfrom Invoice\nwhere InvoiceId = @id\nselect InvoiceId",
		"with Items as (\n  from Invoice\n  select InvoiceId\n)\nfrom Items\nselect InvoiceId",
	} {
		_, err := Structured(&datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: text})
		if err == nil || !strings.Contains(err.Error(), "authorized resolver context") {
			t.Fatalf("authoring context discarded: %v", err)
		}
	}
}

func TestExtensionRecordsTheRepresentation(t *testing.T) {
	for _, sample := range []struct {
		typ        datatug.QueryType
		structured bool
		want       string
	}{
		{datatug.QueryTypeTugQL, false, "tugql"}, {datatug.QueryTypeTugQL, true, "tugql.yaml"}, {datatug.QueryTypeSQL, false, "sql"},
	} {
		if got := Extension(sample.typ, sample.structured); got != sample.want {
			t.Fatalf("extension=%q want%q", got, sample.want)
		}
	}
}

func TestParseTextKeepsDeclaredDatabases(t *testing.T) {
	text := "databases (\n  sales = 'datatug://chinook/main'\n  geo\n)\nfrom sales::Invoice as i\njoin geo::countries as c\n  on i.BillingCountry = c.name\nselect i.InvoiceId, c.code"
	document, databases, err := ParseText(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(databases) != 2 || databases[0] != (tugql.TugQLDatabase{Name: "sales", Target: "datatug://chinook/main"}) || databases[1] != (tugql.TugQLDatabase{Name: "geo"}) {
		t.Fatalf("databases=%v", databases)
	}
	if !strings.Contains(document, `"database":"sales"`) || !strings.Contains(document, `"database":"geo"`) {
		t.Fatalf("alias not lowered to database: %s", document)
	}
	got, err := Databases(&datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: text})
	if err != nil || len(got) != 2 {
		t.Fatalf("Databases=%v / %v", got, err)
	}
	for _, query := range []datatug.QueryDef{
		{Type: datatug.QueryTypeTugQL, StructuredBody: true, Text: "from: {database: sales, name: Invoice}\n"},
		{Type: datatug.QueryTypeSQL, Text: "SELECT 1"},
	} {
		if got, err := Databases(&query); err != nil || got != nil {
			t.Fatalf("a structured or SQL body declares no databases: %v / %v", got, err)
		}
	}
	if _, err := Databases(&datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: "from"}); err == nil {
		t.Fatal("invalid text accepted")
	}
}
