package daemon

import (
	"errors"
	"strings"
	"testing"
)

func testMetadata() *Metadata {
	return &Metadata{Relations: []Relation{
		{Schema: "public", Name: "order_line", PrimaryKey: []string{"order_id", "line_no"}},
		{Schema: "public", Name: "names", View: true},
		{Schema: "audit", Name: "names"},
		{Schema: "main", Name: "nokey"},
		{Schema: "main", Name: "kv", PrimaryKey: []string{"id"}},
		{Schema: "main", Name: "kv", View: true},
	}}
}

func loaded(m *Metadata) func() *Metadata { return func() *Metadata { return m } }

func TestBuildLinkWarnsWhenNotScanned(t *testing.T) {
	for name, load := range map[string]func() *Metadata{"no loader": nil, "loader finds none": loaded(nil)} {
		got, err := BuildLink(linkStatus(), LinkRequest{Project: "p", Env: "e", DB: "d", Table: "t", Metadata: load})
		if err != nil || len(got.Warnings) != 1 || got.Warnings[0] != WarningNotScanned {
			t.Fatalf("%s: %+v %v", name, got, err)
		}
	}
}

func TestBuildLinkValidatesAgainstMetadata(t *testing.T) {
	base := LinkRequest{Project: "p", Env: "e", DB: "d", Metadata: loaded(testMetadata())}
	cases := []struct {
		name     string
		mod      func(*LinkRequest)
		notFound bool
		msg      string
		warning  string
	}{
		{"table ok", func(r *LinkRequest) { r.Table = "order_line" }, false, "", ""},
		{"view ok", func(r *LinkRequest) { r.Table, r.View, r.Schema = "names", true, "public" }, false, "", ""},
		{"table with schema ok", func(r *LinkRequest) { r.Table, r.Schema = "names", "audit" }, false, "", ""},
		{"records ok", func(r *LinkRequest) { r.Table, r.Records = "order_line", true }, false, "", ""},
		{"record ok", func(r *LinkRequest) { r.Table, r.Record = "order_line", `{"line_no":1,"order_id":2}` }, false, "", ""},
		{"record on a table with no key info warns", func(r *LinkRequest) { r.Table, r.Record = "nokey", `{"x":1}` }, false, "", WarningKeyNotScanned},
		{"missing table", func(r *LinkRequest) { r.Table = "ghost-secret" }, true, "not in the project's scanned metadata", ""},
		{"wrong schema", func(r *LinkRequest) { r.Table, r.Schema = "order_line", "other" }, true, "not in the project's scanned metadata", ""},
		{"schema is case-sensitive", func(r *LinkRequest) { r.Table, r.Schema = "order_line", "PUBLIC" }, true, "not in the project's scanned metadata", ""},
		{"name is case-sensitive", func(r *LinkRequest) { r.Table = "ORDER_LINE" }, true, "not in the project's scanned metadata", ""},
		{"view flag on a table", func(r *LinkRequest) { r.Table, r.View = "order_line", true }, false, "the object is a table", ""},
		{"table flag on a view", func(r *LinkRequest) { r.Table, r.Schema = "names", "public" }, false, "the object is a view", ""},
		{"same name in two schemas needs a schema", func(r *LinkRequest) { r.Table = "names" }, false, "ambiguous: several schemas hold that name, give --schema", ""},
		{"table and view of one schema with the same name: the flag picks", func(r *LinkRequest) { r.Table, r.View = "kv", true }, false, "", ""},
		{"missing pk column", func(r *LinkRequest) { r.Table, r.Record = "order_line", `{"order_id":2}` }, false, "missing primary-key column", ""},
		{"not a pk column", func(r *LinkRequest) { r.Table, r.Record = "order_line", `{"order_id":2,"line_no":1,"sku":"s3cret"}` }, false, "not a primary-key column", ""},
		{"key names are case-sensitive", func(r *LinkRequest) { r.Table, r.Record = "kv", `{"ID":1}` }, false, "not a primary-key column", ""},
		{"record on a view", func(r *LinkRequest) { r.Table, r.View, r.Schema, r.Record = "names", true, "public", `{"id":1}` }, false, "a view has no primary key", ""},
		{"too long link with an unknown table is a usage error", func(r *LinkRequest) { r.Table = strings.Repeat("x", 3000) }, false, "longer than", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mod(&req)
			got, err := BuildLink(linkStatus(), req)
			if c.msg == "" {
				if err != nil || got.Warnings == nil || (c.warning == "") != (len(got.Warnings) == 0) || (c.warning != "" && got.Warnings[0] != c.warning) {
					t.Fatalf("%+v %v", got, err)
				}
				return
			}
			var nf NotFoundError
			var ue UsageError
			if c.notFound != errors.As(err, &nf) || c.notFound == errors.As(err, &ue) {
				t.Fatalf("wrong error kind: %v", err)
			}
			if !strings.Contains(err.Error(), c.msg) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "ghost") {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestBuildLinkChecksTheRequestBeforeReadingMetadata(t *testing.T) {
	read := false
	load := func() *Metadata { read = true; return nil }
	for _, req := range []LinkRequest{
		{Project: "p", Env: "e", DB: "d"},
		{Project: "p", Env: "e", DB: "d", Table: "t", Record: "{"},
		{Project: "p", Env: "e", DB: "d", Table: "t", Records: true, Limit: -1},
	} {
		req.Metadata = load
		if _, err := BuildLink(linkStatus(), req); err == nil || read {
			t.Fatalf("%v %v", err, read)
		}
	}
}

func TestBuildLinkScannedSourceWithoutTheNameRefusesIt(t *testing.T) {
	_, err := BuildLink(linkStatus(), LinkRequest{Project: "p", Env: "e", DB: "d", Table: "t", Metadata: loaded(&Metadata{})})
	if !errors.As(err, new(NotFoundError)) {
		t.Fatal(err)
	}
}
