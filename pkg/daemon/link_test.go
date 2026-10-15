package daemon

import (
	"errors"
	"strings"
	"testing"
)

func linkStatus() Status {
	return Status{Running: true, UIBase: "https://ui.test/store/http-127.0.0.1:8989"}
}

func TestBuildLink(t *testing.T) {
	base := LinkRequest{Project: "p 1", Env: "dev", DB: "Chinook", Table: "Album"}
	const prefix = "https://ui.test/store/http-127.0.0.1:8989/project/p%201/env/dev/db/Chinook/"
	cases := []struct {
		name      string
		mod       func(*LinkRequest)
		url       string
		kind      string
		uiSupport string
	}{
		{"metadata", func(*LinkRequest) {}, prefix + "table/Album", KindTableMetadata, UISupportAvailable},
		{"view with schema", func(r *LinkRequest) { r.View, r.Schema = true, "public" }, prefix + "view/Album?schema=public", KindTableMetadata, UISupportAvailable},
		{"records default limit", func(r *LinkRequest) { r.Records = true }, prefix + "table/Album/records?limit=50", KindTableRecords, UISupportPending},
		{"records limit", func(r *LinkRequest) { r.Records, r.Limit = true, 5 }, prefix + "table/Album/records?limit=5", KindTableRecords, UISupportPending},
		{"record sorted keys", func(r *LinkRequest) { r.Record = `{"b":2,"a":"x"}` }, prefix + "table/Album/record?pk=%7B%22a%22%3A%22x%22%2C%22b%22%3A2%7D", KindRecord, UISupportPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mod(&req)
			got, err := BuildLink(linkStatus(), req)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != c.url || got.Kind != c.kind || got.UISupport != c.uiSupport || !got.Running {
				t.Fatalf("got %+v, want url %s kind %s support %s", got, c.url, c.kind, c.uiSupport)
			}
		})
	}
}

func TestBuildLinkUsageErrors(t *testing.T) {
	ok := LinkRequest{Project: "p", Env: "e", DB: "d", Table: "t"}
	cases := map[string]func(*LinkRequest){
		"--project is required":   func(r *LinkRequest) { r.Project = "" },
		"--env is required":       func(r *LinkRequest) { r.Env = "" },
		"--db is required":        func(r *LinkRequest) { r.DB = "" },
		"--table is required":     func(r *LinkRequest) { r.Table = "" },
		"mutually exclusive":      func(r *LinkRequest) { r.Records, r.Record = true, `{"a":1}` },
		"never connection URLs":   func(r *LinkRequest) { r.DB = "postgres://u:pw@h/db" },
		"never connection URLs ":  func(r *LinkRequest) { r.Schema = "a@b" },
		"--limit must be":         func(r *LinkRequest) { r.Records, r.Limit = true, MaxLimit+1 },
		"--limit must be ":        func(r *LinkRequest) { r.Records, r.Limit = true, -1 },
		"non-empty JSON object":   func(r *LinkRequest) { r.Record = `[1]` },
		"non-empty JSON object ":  func(r *LinkRequest) { r.Record = `{}` },
		"non-empty JSON object  ": func(r *LinkRequest) { r.Record = `{"a":1} {"b":2}` },
	}
	for want, mod := range cases {
		req := ok
		mod(&req)
		_, err := BuildLink(linkStatus(), req)
		var ue UsageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("%q: err = %v", want, err)
		}
	}
}

func TestUIBase(t *testing.T) {
	t.Setenv(UIBaseEnv, "")
	if got := UIBase(8990); got != "https://datatug.app/store/http-127.0.0.1:8990" {
		t.Fatal(got)
	}
	t.Setenv(UIBaseEnv, "http://localhost:4200/")
	if got := UIBase(8989); got != "http://localhost:4200/store/http-127.0.0.1:8989" {
		t.Fatal(got)
	}
}
