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
	meta.Query = Query{Kind: QueryKindFrom, DTQL: "from:\n  name: Invoice\n", From: "Invoice", Policy: PolicyNone,
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
	meta.Query.DTQL = strings.Repeat("x", MaxQueryBytes)
	_ = json.Unmarshal(meta.Encode(), &decoded)
	query := decoded["query"].(map[string]any)
	if query["dtql"] != nil || query["parameters"] != nil || decoded["unavailable"].(map[string]any)["query"] != UnavailableTooLarge {
		t.Fatalf("too-large %v %v", query, decoded["unavailable"])
	}

	// at most 400 columns are described
	meta.Query.DTQL = ""
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
