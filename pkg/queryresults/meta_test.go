package queryresults

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

func TestMetaEncode(t *testing.T) {
	created := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	swap(t, &now, func() time.Time { return created })
	data := Data{Bytes: []byte("12345"), Rows: Rows{Kept: 1, TruncatedBy: TruncatedNone}, Columns: []Column{{Name: KeyColumn, Type: "string"}}}
	meta := NewMeta("t1", "v1.2.3", data)
	meta.Query = Query{Kind: QueryKindFrom, TugQL: "from:\n  name: Invoice\n", From: "Invoice", Policy: PolicyNone,
		Parameters: map[string]apicontract.TypedValueOrSet{"n": apicontract.ScalarValue(apicontract.NewIntegerValue("5"))}}
	meta.DB = DB{Driver: "sqlite"}
	var decoded map[string]any
	if err := json.Unmarshal(meta.Encode(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["format"] != MetaFormat || decoded["version"] != 1.0 || decoded["dataBytes"] != 5.0 || decoded["expiresAt"] != "2026-10-22T00:00:00Z" {
		t.Fatalf("meta %v", decoded)
	}
	db := decoded["db"].(map[string]any)
	if db["ref"] != nil || db["project"] != nil || db["driver"] != "sqlite" {
		t.Fatalf("db %v", db)
	}
	if decoded["provenance"] != nil || len(decoded["unavailable"].(map[string]any)) != 0 {
		t.Fatalf("provenance/unavailable %v", decoded)
	}

	// query text and parameters past the bound are dropped together
	meta.Query.TugQL = strings.Repeat("x", MaxQueryBytes)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	query := decoded["query"].(map[string]any)
	if query["tugql"] != nil || query["parameters"] != nil || decoded["unavailable"].(map[string]any)["query"] != UnavailableTooLarge {
		t.Fatalf("too-large %v %v", query, decoded["unavailable"])
	}

	// at most 400 columns are described
	meta.Query.TugQL = ""
	for i := 0; i < MaxMetaColumns+5; i++ {
		meta.Columns = append(meta.Columns, Column{Name: "c", Type: "string"})
	}
	_ = json.Unmarshal(meta.Encode(), &decoded)
	if len(decoded["columns"].([]any)) != MaxMetaColumns || decoded["unavailable"].(map[string]any)["columns"] != UnavailableTooMany {
		t.Fatalf("columns bound %d", len(decoded["columns"].([]any)))
	}

	// a meta.json past 1 MiB drops the column descriptions
	meta.Columns = nil
	for i := 0; i < 10; i++ {
		meta.Columns = append(meta.Columns, Column{Name: strings.Repeat("n", 120<<10), Type: "string"})
	}
	encoded := meta.Encode()
	_ = json.Unmarshal(encoded, &decoded)
	if len(encoded) > MaxMetaBytes || len(decoded["columns"].([]any)) != 0 {
		t.Fatalf("meta of %d bytes", len(encoded))
	}
}

func TestLines(t *testing.T) {
	data := Data{Rows: Rows{Kept: 2000, HasMore: true}, Columns: make([]Column, 7)}
	if got := string(LocatorLine(data)); got != `{"resultFiles":{"version":1},"rowsKept":2000,"hasMore":true,"columns":7}`+"\n" {
		t.Fatalf("locator line %q", got)
	}
	if got := string(FailureLine(ReasonUnwritable)); got != `{"resultFiles":null,"reason":"results-store-unwritable"}`+"\n" {
		t.Fatalf("failure line %q", got)
	}
	if got := string(LocateLine(nil, ReasonExpired)); got != `{"resultFiles":null,"reason":"expired"}`+"\n" {
		t.Fatalf("locate null %q", got)
	}
	locator := &Locator{Version: 1, ID: "t1", Root: "/r", Data: "/r/t1/data.json", Meta: "/r/t1/meta.json"}
	if got := string(LocateLine(locator, "")); got != `{"resultFiles":{"version":1,"id":"t1","root":"/r","data":"/r/t1/data.json","meta":"/r/t1/meta.json"}}`+"\n" {
		t.Fatalf("locate %q", got)
	}
}

func TestMetaDescribeAndListBounds(t *testing.T) {
	meta := NewMeta("t", "v", Data{Columns: []Column{{Name: KeyColumn, Type: "string"}, {Name: "id", Type: "integer"}, {Name: "n", Type: "string"}}})
	meta.Describe(map[string]ColumnFacts{
		"id": {DeclaredType: "int", PK: 1, Source: ColumnSource{Instance: "root", Schema: "main", Relation: "T", Column: "id"}, ForeignKeys: []ColumnFK{{RefSchema: "main", RefRelation: "U", RefColumn: "id"}}},
		"n":  {DeclaredType: "string", Nullable: true, Source: ColumnSource{Instance: "root", Relation: "T", Column: "n"}},
	})
	if meta.Columns[0].DeclaredType != nil || meta.Columns[0].ForeignKeys != nil {
		t.Fatal("an undescribed column stays null")
	}
	if *meta.Columns[1].PK != 1 || *meta.Columns[1].DeclaredType != "int" || len(meta.Columns[1].ForeignKeys) != 1 {
		t.Fatalf("described %+v", meta.Columns[1])
	}
	if meta.Columns[2].PK != nil || !*meta.Columns[2].Nullable || meta.Columns[2].ForeignKeys == nil {
		t.Fatalf("described %+v", meta.Columns[2])
	}
	var decoded map[string]any
	_ = json.Unmarshal(meta.Encode(), &decoded)
	if len(decoded["participants"].([]any)) != 0 || len(decoded["related"].([]any)) != 0 {
		t.Fatal("empty lists are written as []")
	}
	meta.Participants = make([]Participant, MaxParticipants+1)
	meta.Related = make([]Related, MaxRelated+1)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	unavailable := decoded["unavailable"].(map[string]any)
	if len(decoded["participants"].([]any)) != MaxParticipants || len(decoded["related"].([]any)) != MaxRelated || unavailable["participants"] != UnavailableTooMany || unavailable["related"] != UnavailableTooMany {
		t.Fatalf("bounds %v", unavailable)
	}
	// a query text that grows when escaped is measured as written
	meta.Participants, meta.Related = nil, nil
	meta.Query.TugQL = strings.Repeat("<", MaxQueryBytes/3)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	if decoded["unavailable"].(map[string]any)["query"] != UnavailableTooLarge {
		t.Fatal("escaped query text is not measured")
	}
}

func TestSetEditor(t *testing.T) {
	ref := "datatug://p/main?env=local"
	meta := Meta{DB: DB{Ref: &ref}, Query: Query{Kind: QueryKindTugQL, Parameters: map[string]apicontract.TypedValueOrSet{
		"b": apicontract.ScalarValue(apicontract.NewIntegerValue("2")),
		"a": apicontract.ScalarValue(apicontract.NewIntegerValue("1")),
	}}}
	meta.SetEditor("from:\n  name: T\n", LanguageTugQL)
	q := meta.Query
	if q.Text != "from:\n  name: T\n" || q.Language != "tugql" || q.DBRef != ref || len(q.Params) != 2 || q.Params[0].Name != "a" || q.Params[1].Name != "b" {
		t.Fatalf("editor fields %+v", q)
	}
	var decoded map[string]any
	if err := json.Unmarshal(meta.Encode(), &decoded); err != nil {
		t.Fatal(err)
	}
	query := decoded["query"].(map[string]any)
	params := query["params"].([]any)
	first := params[0].(map[string]any)
	if query["text"] == nil || query["language"] != "tugql" || query["dbRef"] != ref || first["name"] != "a" || first["value"].(map[string]any)["type"] != "integer" {
		t.Fatalf("encoded query %v", query)
	}

	// no project source, no parameters, no text: the fields are absent
	meta = Meta{Query: Query{Kind: QueryKindSaved}}
	meta.SetEditor("", LanguageHTTP)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	query = decoded["query"].(map[string]any)
	if _, has := query["dbRef"]; has || query["params"] != nil || query["text"] != nil || query["language"] != "http" {
		t.Fatalf("encoded query %v", query)
	}

	// the editor members have a bound of their own: past it only they go
	meta = Meta{}
	meta.SetEditor(strings.Repeat("x", MaxQueryBytes), LanguageSQL)
	meta.Query.TugQL = "from:\n  name: T\n"
	_ = json.Unmarshal(meta.Encode(), &decoded)
	query = decoded["query"].(map[string]any)
	if query["text"] != nil || query["tugql"] == nil || decoded["unavailable"].(map[string]any)["query"] != nil || decoded["unavailable"].(map[string]any)["editor"] != UnavailableTooLarge {
		t.Fatalf("editor members over their bound %v %v", query, decoded["unavailable"])
	}

	// params alone over the bound, and both, and neither
	meta = Meta{Query: Query{Parameters: map[string]apicontract.TypedValueOrSet{"p": apicontract.ScalarValue(apicontract.NewIntegerValue("1"))}}}
	meta.SetEditor("select 1", LanguageSQL)
	meta.Query.Params[0].Name = strings.Repeat("n", MaxQueryBytes)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	query = decoded["query"].(map[string]any)
	unavailable := decoded["unavailable"].(map[string]any)
	if query["text"] != nil || query["params"] != nil || query["parameters"] == nil || unavailable["editor"] != UnavailableTooLarge || unavailable["query"] != nil {
		t.Fatalf("params over their bound %v %v", query, unavailable)
	}
	meta.Query.Params = nil
	meta.Query.Text = strings.Repeat("x", MaxQueryBytes)
	meta.Query.TugQL = strings.Repeat("y", MaxQueryBytes)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	unavailable = decoded["unavailable"].(map[string]any)
	if unavailable["editor"] != UnavailableTooLarge || unavailable["query"] != UnavailableTooLarge {
		t.Fatalf("both over %v", unavailable)
	}
	meta = Meta{}
	meta.SetEditor("select 1", LanguageSQL)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	if _, has := decoded["unavailable"].(map[string]any)["editor"]; has {
		t.Fatal("editor flag set with nothing dropped")
	}

	// the legacy bound is not shared with them: 200 KiB of each is kept
	meta = Meta{}
	meta.SetEditor(strings.Repeat("x", 200<<10), LanguageTugQL)
	meta.Query.TugQL = strings.Repeat("y", 200<<10)
	encoded := meta.Encode()
	_ = json.Unmarshal(encoded, &decoded)
	query = decoded["query"].(map[string]any)
	if query["text"] == nil || query["tugql"] == nil || decoded["unavailable"].(map[string]any)["query"] != nil {
		t.Fatalf("both within their own bounds %v", decoded["unavailable"])
	}

	// the legacy members over theirs go alone, the editor members stay
	meta.Query.TugQL = strings.Repeat("y", MaxQueryBytes)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	query = decoded["query"].(map[string]any)
	if query["tugql"] != nil || query["text"] == nil || decoded["unavailable"].(map[string]any)["query"] != UnavailableTooLarge {
		t.Fatalf("legacy over its bound %v %v", query, decoded["unavailable"])
	}

	// both at their maximum still leave room for the prompt in 1 MiB
	meta.Query.TugQL = strings.Repeat("y", MaxQueryBytes-100)
	meta.Query.Text = strings.Repeat("x", MaxQueryBytes-100)
	for i := 0; i < MaxMetaColumns; i++ {
		meta.Columns = append(meta.Columns, Column{Name: strings.Repeat("c", 1000), Type: "string"})
	}
	if size := len(meta.Encode()); size > MaxMetaBytes-MaxPromptJSONBytes {
		t.Fatalf("meta.json %d leaves no room for the prompt", size)
	}
}
