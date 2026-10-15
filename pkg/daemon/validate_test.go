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
	}}
}

func TestBuildLinkWarnsWhenNotScanned(t *testing.T) {
	got, err := BuildLink(linkStatus(), LinkRequest{Project: "p", Env: "e", DB: "d", Table: "t"})
	if err != nil || len(got.Warnings) != 1 || got.Warnings[0] != WarningNotScanned {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestBuildLinkValidatesAgainstMetadata(t *testing.T) {
	base := LinkRequest{Project: "p", Env: "e", DB: "d", Metadata: testMetadata()}
	cases := []struct {
		name     string
		mod      func(*LinkRequest)
		notFound bool
		msg      string
	}{
		{"table ok", func(r *LinkRequest) { r.Table = "order_line" }, false, ""},
		{"view ok", func(r *LinkRequest) { r.Table, r.View = "names", true }, false, ""},
		{"table with schema ok", func(r *LinkRequest) { r.Table, r.Schema = "names", "audit" }, false, ""},
		{"records ok", func(r *LinkRequest) { r.Table, r.Records = "order_line", true }, false, ""},
		{"record ok", func(r *LinkRequest) { r.Table, r.Record = "order_line", `{"line_no":1,"order_id":2}` }, false, ""},
		{"missing table", func(r *LinkRequest) { r.Table = "ghost-secret" }, true, "not in the project's scanned metadata"},
		{"wrong schema", func(r *LinkRequest) { r.Table, r.Schema = "order_line", "other" }, true, "not in the project's scanned metadata"},
		{"view flag on a table", func(r *LinkRequest) { r.Table, r.View = "order_line", true }, false, "the object is a table"},
		{"table flag on a view", func(r *LinkRequest) { r.Table = "names"; r.Schema = "public" }, false, "the object is a view"},
		{"missing pk column", func(r *LinkRequest) { r.Table, r.Record = "order_line", `{"order_id":2}` }, false, "missing primary-key column"},
		{"not a pk column", func(r *LinkRequest) { r.Table, r.Record = "order_line", `{"order_id":2,"line_no":1,"sku":"s3cret"}` }, false, "not a primary-key column"},
		{"record on a view", func(r *LinkRequest) { r.Table, r.View, r.Record = "names", true, `{"id":1}` }, false, "not a primary-key column"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mod(&req)
			got, err := BuildLink(linkStatus(), req)
			if c.msg == "" {
				if err != nil || len(got.Warnings) != 0 || got.Warnings == nil {
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

func TestBuildLinkEmptyMetadataRefusesTheTable(t *testing.T) {
	_, err := BuildLink(linkStatus(), LinkRequest{Project: "p", Env: "e", DB: "d", Table: "t", Metadata: &Metadata{}})
	if !errors.As(err, new(NotFoundError)) {
		t.Fatal(err)
	}
}
