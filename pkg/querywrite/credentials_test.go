package querywrite

import (
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// credentialCases pairs values with whether the screen must refuse them:
// the review's cases (the four quoted-value regressions first), the
// capture endpoint's own cases and the core screen's documented shapes.
var credentialCases = []struct {
	value string
	found bool
}{
	{`Server=db;User Id=sa;Password="hunter2";`, true},
	{`Server=db;User Id=sa;Password='hunter2';`, true},
	{`user:"hunter2"@tcp(db:3306)/app`, true},
	{`postgres://admin:"hunter2"@db/app`, true},
	{`Password=hunter2`, true},
	{`postgres://u:p@h`, true},
	{`(Password=hunter2)`, true},
	{`{"password":"hunter2"}`, true},
	{`api_key=sk-live-123456`, true},
	{`DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=abc123def456==`, true},
	{"Authorization: Bearer abc123", true},
	{"postgres://user:secret@host/db", true},
	{"jdbc:postgresql://user:secret@host/db", true},
	{"postgres://user:%73ecret@host/db", true},
	{"postgres://user:@host/db", true},
	{"copy postgres://u:p@h/db into the tool", true},
	{"user:secret@tcp(host:3306)/db", true},
	{"uid=u;pwd=secret", true},
	{"postgres://u@h/db?password=secret", true},
	{"where:\n  right:\n    value: \"Password=secret\"\n", true},

	{"postgres://user@host/db", false},
	{"https://example.com/a?b=c", false},
	{"ask ops@example.com", false},
	{"Server=h;Password=;", false},
	{"password_hash is never selected", false},
	{"Which invoices does this customer have?", false},
	{"from:\n  name: Invoice\nwhere:\n  op: ==\n  left:\n    field: CustomerId\n  right:\n    param: Customer.ID\n", false},
	{"UPDATE users SET password = :new_password", false},
	{"token={{token}}", false},
	{"", false},
}

func TestCredentialReason(t *testing.T) {
	for _, tt := range credentialCases {
		reason, found := CredentialReason(tt.value)
		if found != tt.found {
			t.Errorf("CredentialReason(%q) found = %v (%q), want %v", tt.value, found, reason, tt.found)
		}
		if found && reason == "" {
			t.Errorf("CredentialReason(%q): a refusal must say why", tt.value)
		}
	}
}

func TestQueryCredentialReason(t *testing.T) {
	clean := func() datatug.QueryDef {
		return datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q", Title: "Invoices"}},
			Type:        datatug.QueryTypeSQL,
			Text:        "SELECT * FROM Invoice WHERE CustomerId = :id",
			Parameters:  datatug.Parameters{{ID: "id", Type: "integer", Title: "Customer", DefaultValue: "42"}},
			Targets:     []datatug.QueryDefTarget{{Driver: "sqlite3", Catalog: "chinook", Credentials: datatug.Credentials{Username: "reader"}}},
		}
	}
	q := clean()
	if field, reason, found := QueryCredentialReason(&q); found {
		t.Fatalf("a clean query was refused: %s %s", field, reason)
	}
	tests := []struct {
		field  string
		mutate func(q *datatug.QueryDef)
	}{
		{"title", func(q *datatug.QueryDef) { q.Title = "Server=db;Password=hunter2" }},
		{"text", func(q *datatug.QueryDef) { q.Text = "SELECT 1 -- postgres://admin:hunter2@db/app" }},
		{"parameters[0].title", func(q *datatug.QueryDef) { q.Parameters[0].Title = "uid=a;pwd=b" }},
		{"targets[0].host", func(q *datatug.QueryDef) { q.Targets[0].Host = "u:p@db" }},
		{"targets[0].username", func(q *datatug.QueryDef) { q.Targets[0].Username = "Password=hunter2" }},
		{"parameters[0].defaultValue", func(q *datatug.QueryDef) { q.Parameters[0].DefaultValue = "postgres://u:p@h/db" }},
		{"parameters[0].defaultValue", func(q *datatug.QueryDef) {
			q.Parameters[0].DefaultValue = map[string]any{"password": "hunter2"}
		}},
		{"parameters[0].defaultValue", func(q *datatug.QueryDef) {
			q.Parameters[0].DefaultValue = []any{"ok", map[string]any{"dsn": "user:secret@tcp(db)/app"}}
		}},
		{"parameters[0].defaultValue", func(q *datatug.QueryDef) {
			q.Parameters[0].DefaultValue = map[string]any{"Password=hunter2": 1}
		}},
	}
	for _, tt := range tests {
		q := clean()
		tt.mutate(&q)
		field, reason, found := QueryCredentialReason(&q)
		if !found || field != tt.field || reason == "" {
			t.Errorf("QueryCredentialReason = (%q, %q, %v), want a refusal of %s", field, reason, found, tt.field)
		}
	}
	for _, v := range []any{nil, 42, []any{"a", 1.5}, map[string]any{"page_token": "x"}, func() {}} {
		if reason, found := defaultValueCredentialReason(v); found {
			t.Errorf("defaultValueCredentialReason(%#v) refused a clean default: %s", v, reason)
		}
	}
}
