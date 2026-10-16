package queryresults

import (
	"encoding/json"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	store := newStore(t)
	c := NewCollector(5)
	c.Add("1", map[string]any{"n": int64(7)})
	data := c.Build("t1", []string{"n"}, nil)
	result, err := store.Create("t1")
	if err != nil {
		t.Fatal(err)
	}
	meta := NewMeta("t1", "v", data)
	if err := result.Write(data.Bytes, meta.Encode()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("t1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.ID != "t1" || len(loaded.Recordset.Rows) != 1 || loaded.Recordset.Rows[0][1].Str != "7" || len(loaded.DataJSON) != len(data.Bytes) {
		t.Fatalf("loaded %+v", loaded)
	}
	_, err = store.Load("nope")
	reasonIs(t, err, ReasonNotFound)

	dir := filepath.Join(store.Root(), "t1")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original, _ := os.ReadFile(filepath.Join(dir, MetaFile))
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	write(MetaFile, `{"format":"datatug.query-result.meta","version":2,"expiresAt":"`+expires+`","dataBytes":`+strconv.Itoa(len(data.Bytes))+`}`)
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonVersion)
	write(MetaFile, `{"version":[],"expiresAt":"`+expires+`","dataBytes":`+strconv.Itoa(len(data.Bytes))+`}`)
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
	write(MetaFile, string(original))

	write(DataFile, strings.Repeat(" ", len(data.Bytes)))
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
	padded := strings.Replace(string(data.Bytes), `"version":1`, `"version":3`, 1)
	write(DataFile, padded)
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonVersion)

	calls := 0
	swap(t, &osOpen, func(name string) (*os.File, error) {
		calls++
		if calls == 2 {
			return nil, errBoom
		}
		return os.Open(name)
	})
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
	calls = 10
	osOpen = func(name string) (*os.File, error) {
		calls++
		if calls == 13 {
			return nil, errBoom
		}
		return os.Open(name)
	}
	write(DataFile, string(data.Bytes))
	_, err = store.Load("t1")
	reasonIs(t, err, ReasonIncomplete)
}

// A data.json or meta.json of another shape than this package writes is an
// incomplete result, not a panic of its reader: no key column, a row shorter
// than the columns, a parameter that is neither a scalar nor a set.
func TestLoadRefusesMalformedShapes(t *testing.T) {
	store := newStore(t)
	c := NewCollector(5)
	c.Add("1", map[string]any{"n": int64(7)})
	data := c.Build("t1", []string{"n"}, nil)
	result, err := store.Create("t1")
	if err != nil {
		t.Fatal(err)
	}
	meta := NewMeta("t1", "v", data)
	if err := result.Write(data.Bytes, meta.Encode()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.Root(), "t1")
	var doc map[string]any
	original, _ := os.ReadFile(filepath.Join(dir, MetaFile))
	if err := json.Unmarshal(original, &doc); err != nil {
		t.Fatal(err)
	}
	write := func(dataJSON string, parameters any) {
		t.Helper()
		doc["dataBytes"] = len(dataJSON)
		query, _ := doc["query"].(map[string]any)
		if query == nil {
			query = map[string]any{}
			doc["query"] = query
		}
		if parameters == nil {
			delete(query, "parameters")
		} else {
			query["parameters"] = parameters
		}
		metaJSON, _ := json.Marshal(doc)
		if err := os.WriteFile(filepath.Join(dir, MetaFile), metaJSON, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, DataFile), []byte(dataJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var good map[string]any
	if err := json.Unmarshal(data.Bytes, &good); err != nil {
		t.Fatal(err)
	}
	recordset := good["recordset"].(map[string]any)
	columns, rows := recordset["columns"].([]any), recordset["rows"].([]any)
	with := func(columns, rows any) string {
		t.Helper()
		doc := map[string]any{"format": good["format"], "version": good["version"], "recordset": map[string]any{"columns": columns, "rows": rows}}
		content, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	var scalar any
	encoded, _ := json.Marshal(map[string]apicontract.TypedValueOrSet{"p": apicontract.ScalarValue(apicontract.TypedValue{Type: apicontract.ValueTypeInteger, Str: "1"})})
	if err := json.Unmarshal(encoded, &scalar); err != nil {
		t.Fatal(err)
	}
	write(with(columns, rows), scalar)
	if _, err := store.Load("t1"); err != nil {
		t.Fatalf("a well-formed result: %v", err)
	}
	firstRow := rows[0].([]any)
	for name, tc := range map[string]struct {
		data       string
		parameters any
	}{
		"empty columns":   {with([]any{}, []any{}), nil},
		"no key column":   {with(columns[1:], []any{}), nil},
		"short row":       {with(columns, []any{firstRow[:1]}), nil},
		"empty parameter": {with(columns, rows), map[string]any{"p": map[string]any{}}},
		"null parameter":  {with(columns, rows), map[string]any{"p": nil}},
	} {
		write(tc.data, tc.parameters)
		_, err := store.Load("t1")
		if ReasonOf(err) != ReasonIncomplete {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// JSON cannot decode a parameter to neither a scalar nor a set, but the check
// stands for any other way a Meta is made.
func TestWellFormedRefusesAnEmptyParameter(t *testing.T) {
	keyed := apicontract.Recordset{Columns: []apicontract.Column{{Name: KeyColumn}}}
	if wellFormed(keyed, map[string]apicontract.TypedValueOrSet{"p": {}}) {
		t.Fatal("an empty parameter is well formed")
	}
}

// A result written before query.text/language/dbRef/params and prompt existed
// still loads, and an annotated one loads with its prompt.
func TestLoadOldAndAnnotatedMeta(t *testing.T) {
	store := newStore(t)
	c := NewCollector(5)
	c.Add("1", map[string]any{"n": int64(7)})
	data := c.Build("o2", []string{"n"}, nil)
	result, err := store.Create("o2")
	if err != nil {
		t.Fatal(err)
	}
	old := `{"format":"datatug.query-result.meta","version":1,"id":"o2","createdAt":"2026-10-15T00:00:00Z","expiresAt":"` +
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `","dataBytes":` + strconv.Itoa(len(data.Bytes)) +
		`,"query":{"kind":"dtql","dtql":"x","policy":"none"}}`
	if err := result.Write(data.Bytes, []byte(old)); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("o2")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Query.DTQL != "x" || loaded.Meta.Query.Text != "" || loaded.Meta.Query.Params != nil || loaded.Meta.Prompt != nil {
		t.Fatalf("old meta %+v", loaded.Meta)
	}
	if err := store.Annotate("o2", "the prompt", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load("o2")
	if err != nil || loaded.Meta.Prompt == nil || loaded.Meta.Prompt.Text != "the prompt" || loaded.Meta.Query.DTQL != "x" {
		t.Fatalf("annotated %+v %v", loaded.Meta, err)
	}
}
