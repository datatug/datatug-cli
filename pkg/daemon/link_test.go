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
		name string
		mod  func(*LinkRequest)
		url  string
		kind string
	}{
		{"metadata has an empty schema", func(*LinkRequest) {}, prefix + "table/Album?schema=", KindTableMetadata},
		{"view with schema", func(r *LinkRequest) { r.View, r.Schema = true, "public" }, prefix + "view/Album?schema=public", KindTableMetadata},
		{"records default limit", func(r *LinkRequest) { r.Records = true }, prefix + "table/Album/records?limit=50&schema=", KindTableRecords},
		{"records limit and schema", func(r *LinkRequest) { r.Records, r.Limit, r.Schema = true, 5, "dbo" }, prefix + "table/Album/records?limit=5&schema=dbo", KindTableRecords},
		{"record key is a sorted fragment", func(r *LinkRequest) { r.Record = `{"b":2,"a":"x"}` }, prefix + "table/Album/record?schema=#pk=%7B%22a%22%3A%22x%22%2C%22b%22%3A2%7D", KindRecord},
		{"record composite key", func(r *LinkRequest) { r.Record = `{"TrackId":3402,"PlaylistId":1}` }, prefix + "table/Album/record?schema=#pk=%7B%22PlaylistId%22%3A1%2C%22TrackId%22%3A3402%7D", KindRecord},
		{"integer above 2^53-1 is a string", func(r *LinkRequest) { r.Record = `{"id":9007199254740993}` }, prefix + "table/Album/record?schema=#pk=%7B%22id%22%3A%229007199254740993%22%7D", KindRecord},
		{"negative integer below -(2^53-1) is a string", func(r *LinkRequest) { r.Record = `{"id":-9007199254740993}` }, prefix + "table/Album/record?schema=#pk=%7B%22id%22%3A%22-9007199254740993%22%7D", KindRecord},
		{"integer at 2^53-1 stays a number", func(r *LinkRequest) { r.Record = `{"id":9007199254740991}` }, prefix + "table/Album/record?schema=#pk=%7B%22id%22%3A9007199254740991%7D", KindRecord},
		{"exponent integer is plain digits", func(r *LinkRequest) { r.Record = `{"a":1E3,"b":1.0,"c":-0}` }, prefix + "table/Album/record?schema=#pk=%7B%22a%22%3A1000%2C%22b%22%3A1%2C%22c%22%3A0%7D", KindRecord},
		{"big exponent integer is a string", func(r *LinkRequest) { r.Record = `{"a":1e20,"b":9007199254740993.0}` }, prefix + "table/Album/record?schema=#pk=%7B%22a%22%3A%22100000000000000000000%22%2C%22b%22%3A%229007199254740993%22%7D", KindRecord},
		{"float and bool stay typed", func(r *LinkRequest) { r.Record = `{"f":1.5,"b":true}` }, prefix + "table/Album/record?schema=#pk=%7B%22b%22%3Atrue%2C%22f%22%3A1.5%7D", KindRecord},
		{"email-like key is percent-encoded and html is not escaped", func(r *LinkRequest) { r.Record = `{"e":"a@b.c <x> é"}` }, prefix + "table/Album/record?schema=#pk=%7B%22e%22%3A%22a%40b.c%20%3Cx%3E%20%C3%A9%22%7D", KindRecord},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.mod(&req)
			got, err := BuildLink(linkStatus(), req)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != c.url || got.Kind != c.kind || got.UISupport != UISupportPending || !got.Running {
				t.Fatalf("got %+v, want url %s kind %s", got, c.url, c.kind)
			}
		})
	}
}

func TestBuildLinkUsageErrors(t *testing.T) {
	ok := LinkRequest{Project: "p", Env: "e", DB: "d", Table: "t"}
	cases := map[string]func(*LinkRequest){
		"--project is required":          func(r *LinkRequest) { r.Project = "" },
		"--env is required":              func(r *LinkRequest) { r.Env = "" },
		"--db is required":               func(r *LinkRequest) { r.DB = "" },
		"--table is required":            func(r *LinkRequest) { r.Table = "" },
		"mutually exclusive":             func(r *LinkRequest) { r.Records, r.Record = true, `{"a":1}` },
		"never connection URLs":          func(r *LinkRequest) { r.DB = "postgres://u:pw@h/db" },
		"never connection URLs ":         func(r *LinkRequest) { r.Schema = "a@b" },
		"--limit must be":                func(r *LinkRequest) { r.Records, r.Limit = true, MaxLimit+1 },
		"--limit must be ":               func(r *LinkRequest) { r.Records, r.Limit = true, -1 },
		"non-empty JSON object":          func(r *LinkRequest) { r.Record = `[1]` },
		"non-empty JSON object ":         func(r *LinkRequest) { r.Record = `{}` },
		"non-empty JSON object  ":        func(r *LinkRequest) { r.Record = `{"a":1} {"b":2}` },
		"non-empty JSON object     ":     func(r *LinkRequest) { r.Record = `{"a":1}}` },
		"non-empty JSON object        ":  func(r *LinkRequest) { r.Record = `{"a":1e99999999}` },
		"non-empty JSON object         ": func(r *LinkRequest) { r.Record = `{"a":1e-99999999}` },
		"non-empty JSON object      ":    func(r *LinkRequest) { r.Record = `{"a":1e-999}` },
		"non-empty JSON object       ":   func(r *LinkRequest) { r.Record = `{"a":1e` + strings.Repeat("9", 60) + `}` },
		"non-empty JSON object   ":       func(r *LinkRequest) { r.Record = `{"a":null}` },
		"non-empty JSON object    ":      func(r *LinkRequest) { r.Record = `{"a":{"b":1}}` },
		"at most 256 characters":         func(r *LinkRequest) { r.Record = `{"a":"` + strings.Repeat("x", 257) + `"}` },
		"must not be . or ..":            func(r *LinkRequest) { r.Project = ".." },
		"must not be . or .. ":           func(r *LinkRequest) { r.Env = "." },
		"must not be . or ..  ":          func(r *LinkRequest) { r.DB = ".." },
		"must not be . or ..   ":         func(r *LinkRequest) { r.Table = ".." },
		"longer than 2048":               func(r *LinkRequest) { r.Table = strings.Repeat("t", MaxLinkLength) },
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

func TestEncodeComponent(t *testing.T) {
	if got := encodeComponent("a b+c/é'()*!~-_.:"); got != "a%20b%2Bc%2F%C3%A9'()*!~-_.%3A" {
		t.Fatal(got)
	}
}

func TestStatusUISupportIsPending(t *testing.T) {
	for kind, v := range uiSupport {
		if v != UISupportPending {
			t.Errorf("%s: %s", kind, v)
		}
	}
}

func TestBuildLinkCodeIsFirstInTheFragment(t *testing.T) {
	const prefix = "https://ui.test/store/http-127.0.0.1:8989/project/p/env/e/db/d/table/t"
	for name, c := range map[string]struct {
		mod func(*LinkRequest)
		url string
	}{
		"metadata": {func(*LinkRequest) {}, prefix + "?schema=#code=v1.1.2.3"},
		"records":  {func(r *LinkRequest) { r.Records = true }, prefix + "/records?limit=50&schema=#code=v1.1.2.3"},
		"record":   {func(r *LinkRequest) { r.Record = `{"id":5}` }, prefix + "/record?schema=#code=v1.1.2.3&pk=%7B%22id%22%3A5%7D"},
	} {
		req := LinkRequest{Project: "p", Env: "e", DB: "d", Table: "t", Code: "v1.1.2.3"}
		c.mod(&req)
		got, err := BuildLink(linkStatus(), req)
		if err != nil || got.URL != c.url {
			t.Errorf("%s: %q %v", name, got.URL, err)
		}
	}
}
