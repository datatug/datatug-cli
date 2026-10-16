package queryresults

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestOrderColumns(t *testing.T) {
	sorted := []string{"age", "breed", "id", "name", "zone", "extra"}
	table := []string{"id", "ZONE", "name", "age", "breed", "id"}
	got := OrderColumns(sorted, table)
	want := []string{"id", "zone", "name", "age", "breed", "extra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !reflect.DeepEqual(sorted, []string{"age", "breed", "id", "name", "zone", "extra"}) {
		t.Fatal("the argument was reordered")
	}
	// columns the source does not name keep their order, after the named ones
	if got := OrderColumns([]string{"b", "a", "n", "m"}, []string{"m", "n"}); !reflect.DeepEqual(got, []string{"m", "n", "b", "a"}) {
		t.Fatalf("got %v", got)
	}
	if got := OrderColumns(sorted, nil); !reflect.DeepEqual(got, sorted) {
		t.Fatalf("no source order: got %v", got)
	}
}

func TestColumnIsInternal(t *testing.T) {
	if !(Column{Name: KeyColumn}).IsInternal() || !(Column{Name: "x", Internal: true}).IsInternal() || (Column{Name: "x"}).IsInternal() {
		t.Fatal("only the key column and a column marked internal are internal")
	}
}

func TestBuildKeepsColumnOrderAndMarksTheKeyInternal(t *testing.T) {
	c := NewCollector(5)
	c.Add("__dalgo_record_id", map[string]any{"zone": "z", "id": int64(1), "age": int64(3)})
	data := c.Build("t1", []string{"id", "zone", "age"}, nil)
	var names []string
	for _, column := range data.Columns {
		names = append(names, column.Name)
	}
	if !reflect.DeepEqual(names, []string{KeyColumn, "id", "zone", "age"}) {
		t.Fatalf("meta columns %v", names)
	}
	for i, column := range data.Columns {
		if column.Internal != (i == 0) {
			t.Fatalf("column %q internal = %v", column.Name, column.Internal)
		}
	}
	encoded, _ := json.Marshal(data.Columns)
	var raw []map[string]any
	_ = json.Unmarshal(encoded, &raw)
	if raw[0]["internal"] != true {
		t.Fatalf("the key column is written with internal: true: %s", encoded)
	}
	if _, found := raw[1]["internal"]; found {
		t.Fatalf("a field column carries no internal member: %s", encoded)
	}
	var file struct {
		Recordset struct {
			Columns []map[string]any `json:"columns"`
			Rows    [][]any          `json:"rows"`
		} `json:"recordset"`
	}
	if err := json.Unmarshal(data.Bytes, &file); err != nil {
		t.Fatal(err)
	}
	// data.json stays as old readers know it: $key first and still a value of every row
	if file.Recordset.Columns[0]["name"] != KeyColumn || len(file.Recordset.Rows[0]) != 4 {
		t.Fatalf("data.json %s", data.Bytes)
	}
	if _, found := file.Recordset.Columns[0]["internal"]; found {
		t.Fatal("the recordset's column descriptors keep the contract's shape")
	}
}

// A result written before the marker and the table order existed: alphabetical
// columns, $key holding the placeholder, no internal member. It stays readable.
func TestLoadResultWrittenBeforeColumnOrder(t *testing.T) {
	store := newStore(t)
	result, err := store.Create("legacy")
	if err != nil {
		t.Fatal(err)
	}
	data := `{"format":"datatug.query-result.data","version":1,"id":"legacy","recordset":{"columns":[` +
		`{"name":"$key","type":"string"},{"name":"BillingCity","type":"string"},{"name":"InvoiceId","type":"integer"}],` +
		`"rows":[[{"type":"string","value":"__dalgo_record_id"},{"type":"string","value":"Oslo"},{"type":"integer","value":"1"}]]}}`
	now := time.Now().UTC()
	meta := `{"format":"datatug.query-result.meta","version":1,"id":"legacy","createdAt":"` + now.Format(time.RFC3339) + `","expiresAt":"` + now.Add(time.Hour).Format(time.RFC3339) +
		`","cliVersion":"v","dataBytes":` + strconv.Itoa(len(data)) + `,"rows":{"kept":1,"hasMore":false,"truncatedBy":"none"},` +
		`"columns":[{"name":"$key","type":"string","declaredType":null,"nullable":null,"pk":null,"source":null,"foreignKeys":null},` +
		`{"name":"BillingCity","type":"string","declaredType":null,"nullable":null,"pk":null,"source":null,"foreignKeys":null},` +
		`{"name":"InvoiceId","type":"integer","declaredType":null,"nullable":null,"pk":null,"source":null,"foreignKeys":null}]}`
	if err := result.Write([]byte(data), []byte(meta)); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Meta.Columns; len(got) != 3 || got[0].Internal || !got[0].IsInternal() || got[1].IsInternal() {
		t.Fatalf("a result without the marker still hides its key by name: %+v", got)
	}
	if loaded.Recordset.Columns[1].Name != "BillingCity" || loaded.Recordset.Rows[0][0].Str != "__dalgo_record_id" {
		t.Fatalf("recordset %+v", loaded.Recordset)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "legacy", DataFile)); err != nil {
		t.Fatal(err)
	}
}
