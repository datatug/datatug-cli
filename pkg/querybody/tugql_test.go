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
