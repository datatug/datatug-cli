package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/mcpserver"
)

type meaningRequest struct {
	route string
	agent string
	body  map[string]any
}

var meaningAgentResponses = map[string]string{
	"meanings/describe": `{"source":"chinook","labelsRevision":"r1","notice":"Local masking is accident prevention for honest agents.","tables":[
		{"schema":"","name":"Customer","kind":"table","reviewed":false,"meanings":[{"entity":"Customer","title":"Buyer","description":"A buyer"}],"primaryKey":["CustomerId"],
		 "columns":[{"name":"CustomerId","type":"INTEGER","nullable":false},{"name":"SupportRepId","type":"INTEGER","nullable":true},
		  {"name":"Email","type":"NVARCHAR(60)","nullable":false,"meaning":{"entity":"Customer","field":"Email","provenance":"declared","description":"Contact"},
		   "label":{"class":"pii","kind":"email","origin":"suggested","state":"pending","rule":"pii-email","revealable":true}}],
		 "foreignKeys":[{"columns":["SupportRepId"],"refTable":{"schema":"","name":"Employee"},"refColumns":["EmployeeId"]}]},
		{"schema":"main","name":"Invoice","kind":"view","reviewed":true,"meanings":[],"primaryKey":[],"columns":[]}],
		"joins":[{"from":{"table":"Invoice","columns":["CustomerId"]},"to":{"table":"Customer","columns":["CustomerId"]},"provenance":"foreign-key"}]}`,
	"meanings/find": `{"matches":[{"table":"Customer","column":"Email","type":"NVARCHAR(60)","label":{"class":"pii","kind":"email","origin":"suggested","state":"pending"},"meaning":{"entity":"Customer","field":"Email"}},{"table":"Employee"}]}`,
	"records/read": `{"recordset":{"columns":[{"name":"CustomerId","type":"integer"},{"name":"Email","type":"string"},{"name":"Card","type":"string"},{"name":"Note","type":"string"}],
		"rows":[[{"type":"integer","value":"1"},null,null,{"type":"null","value":null}],[7,null,null,"plain"],[{"type":"string","value":null},null,null,null]]},
		"limitations":[],"provenance":{"source":"chinook","mode":"live","observedAt":"2026-11-04T00:00:00Z","executionProfile":"protected"},"truncated":true,
		"protection":{"Email":{"class":"pii","kind":"email","state":"masked","revealable":true,"origin":"suggested","ask":"Ask for an elevated approval naming Customer.Email"},
		 "Card":{"class":"secret","state":"masked","revealable":false}},"notice":"Local masking is accident prevention for honest agents."}`,
	"meanings/labels":        `{"revision":"r1","tables":[{"schema":"sales","name":"Customer","kind":"table","reviewed":true,"columns":[{"name":"CustomerId","type":"INTEGER"},{"name":"Email","type":"TEXT","label":{"class":"pii","kind":"email","origin":"suggested","state":"pending","rule":"pii-email"}},{"name":"Token","type":"TEXT","label":{"class":"secret","origin":"column","state":"confirmed","nonRevealable":true}},{"name":"Plain","type":"TEXT","label":{"class":"pii"}}]},{"schema":"","name":"Other","kind":"view","reviewed":false,"columns":[]}],"stored":{"format":"datatug-labels/v1"}}`,
	"meanings/labels/change": `{"revision":"r2","event":{"type":"meaning.label.confirmed","actor":{"id":"alex"},"target":{"source":"chinook","table":"Customer","column":"Email"},"at":"2026-11-04T00:00:00Z"}}`,
}

// fakeMeaningAgent emulates the local agent's meaning routes; override
// returns a status and body to replace the contract example for a route.
func fakeMeaningAgent(t *testing.T, override func(route string) (int, string)) (string, *[]meaningRequest) {
	t.Helper()
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	t.Setenv(agentSessionEnv, "")
	requests := &[]meaningRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimPrefix(r.URL.Path, "/datatug/")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*requests = append(*requests, meaningRequest{route, r.Header.Get(mcpserver.AgentHeader), body})
		status, text := 0, ""
		if override != nil {
			status, text = override(route)
		}
		if text == "" {
			status, text = http.StatusOK, meaningAgentResponses[route]
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, text)
	}))
	t.Cleanup(server.Close)
	return server.URL, requests
}

func runMeaningCommand(t *testing.T, url string, args ...string) (string, error) {
	t.Helper()
	cmd := meaningCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(append([]string{}, args...), "--agent-url", url, "--project", "demo", "--env", "local", "--source", "chinook"))
	err := cmd.Execute()
	return out.String(), err
}

func TestMeaningCommandRegisteredWithHonestScope(t *testing.T) {
	cmd, _, err := DatatugCommand().Find([]string{"meaning", "label", "propose"})
	if err != nil || cmd.Name() != "propose" {
		t.Fatalf("meaning label propose not registered: %v", err)
	}
	meaning := meaningCommand()
	if !strings.Contains(meaning.Long, "accident prevention for honest agents") || !strings.Contains(meaning.PersistentFlags().Lookup("agent").Usage, "accident prevention for honest agents") {
		t.Fatal("help does not state the accident-prevention scope")
	}
	if meaning.PersistentFlags().Lookup("reveal") != nil {
		t.Fatal("--reveal is not part of this command group")
	}
}

func TestMeaningDescribe(t *testing.T) {
	url, requests := fakeMeaningAgent(t, nil)
	out, err := runMeaningCommand(t, url, "describe", "--table", "Customer", "--table", "Invoice")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Customer (table) unreviewed\n  means Customer (Buyer): A buyer\n  CustomerId INTEGER NOT NULL PK\n  SupportRepId INTEGER FK→Employee\n",
		"  Email NVARCHAR(60) NOT NULL → Customer.Email [declared]: Contact [pii/email: suggested, pending, rule pii-email]\n",
		"\nmain.Invoice (view)\n",
		"Joins:\n  Invoice(CustomerId) → Customer(CustomerId) [foreign-key]\n",
		"\nLocal masking is accident prevention for honest agents.\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if (*requests)[0].agent != "" || len((*requests)[0].body["tables"].([]any)) != 2 {
		t.Fatalf("request: %+v", (*requests)[0])
	}
	out, err = runMeaningCommand(t, url, "describe", "--format", "json", "--agent")
	if err != nil || !strings.Contains(out, `"labelsRevision": "r1"`) || (*requests)[1].agent != "1" {
		t.Fatalf("%s %v %+v", out, err, (*requests)[1])
	}
	url, _ = fakeMeaningAgent(t, func(string) (int, string) {
		return 200, `{"source":"s","tables":[{"name":"T","kind":"table","reviewed":true}]}`
	})
	if out, err = runMeaningCommand(t, url, "describe"); err != nil || out != "T (table)\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestMeaningAgentContextFromSessionEnv(t *testing.T) {
	url, requests := fakeMeaningAgent(t, nil)
	t.Setenv(agentSessionEnv, "session-1")
	if _, err := runMeaningCommand(t, url, "find", "email"); err != nil || (*requests)[0].agent != "1" {
		t.Fatalf("%v %+v", err, *requests)
	}
}

func TestMeaningFind(t *testing.T) {
	url, requests := fakeMeaningAgent(t, nil)
	out, err := runMeaningCommand(t, url, "find", "email")
	if err != nil || out != "Customer.Email NVARCHAR(60) → Customer.Email [pii/email: suggested, pending]\nEmployee\n" || (*requests)[0].body["text"] != "email" {
		t.Fatalf("%q %v", out, err)
	}
	if out, err = runMeaningCommand(t, url, "find", "email", "--format", "json"); err != nil || !strings.Contains(out, `"matches"`) {
		t.Fatalf("%q %v", out, err)
	}
	url, _ = fakeMeaningAgent(t, func(string) (int, string) { return 200, `{}` })
	if out, err = runMeaningCommand(t, url, "find", "nothing"); err != nil || out != "No matches.\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestMeaningRead(t *testing.T) {
	url, requests := fakeMeaningAgent(t, nil)
	out, err := runMeaningCommand(t, url, "read", "--schema", "main", "--table", "Customer", "--column", "CustomerId", "--column", "Email", "--limit", "3")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	for i, want := range []string{
		"CustomerId  Email  Card  Note",
		"1           ••••   ••••  NULL",
		"7           ••••   ••••  plain",
		"NULL        ••••   ••••  NULL",
		"(truncated)",
		"Email: pii/email masked (revealable, suggested) — Ask for an elevated approval naming Customer.Email",
		"Card: secret masked (non-revealable)",
		"Local masking is accident prevention for honest agents.",
	} {
		if lines[i] != want {
			t.Fatalf("line %d = %q, want %q in:\n%s", i, lines[i], want, out)
		}
	}
	body := (*requests)[0].body
	if body["schema"] != "main" || body["table"] != "Customer" || body["limit"] != 3.0 || len(body["columns"].([]any)) != 2 {
		t.Fatalf("request: %v", body)
	}
	if out, err = runMeaningCommand(t, url, "read", "--table", "Customer", "--format", "json"); err != nil || !strings.Contains(out, `"ask"`) || !strings.Contains(out, "null") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err = runMeaningCommand(t, url, "read", "--table", "Customer", "--reveal", "Email", "--reveal", "Card"); err != nil {
		t.Fatal(err)
	}
	if got := (*requests)[len(*requests)-1].body["reveal"]; len(got.([]any)) != 2 {
		t.Fatalf("reveal: %v", got)
	}
	if _, err = runMeaningCommand(t, url, "read", "--table", "Customer", "--reveal-all"); err != nil {
		t.Fatal(err)
	}
	if got := (*requests)[len(*requests)-1].body["reveal"].([]any); len(got) != 1 || got[0] != "*" {
		t.Fatalf("reveal-all: %v", got)
	}
	url, _ = fakeMeaningAgent(t, func(string) (int, string) {
		return 200, `{"recordset":{"columns":[{"name":"A","type":"string"}],"rows":[["x"]]},"provenance":{"source":"s"}}`
	})
	if out, err = runMeaningCommand(t, url, "read", "--table", "T"); err != nil || out != "A\nx\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestMeaningLabelList(t *testing.T) {
	url, requests := fakeMeaningAgent(t, nil)
	out, err := runMeaningCommand(t, url, "label", "list", "--table", "Customer")
	want := "Labels revision: r1\nsales.Customer (table)\n  CustomerId INTEGER -\n  Email TEXT [pii/email: suggested, pending, rule pii-email]\n  Token TEXT [secret: column, confirmed, non-revealable]\n  Plain TEXT [pii]\nOther (view) unreviewed\n"
	if err != nil || out != want {
		t.Fatalf("%q %v", out, err)
	}
	if tables := (*requests)[0].body["tables"].([]any); tables[0] != "Customer" {
		t.Fatalf("request: %v", (*requests)[0].body)
	}
	if out, err = runMeaningCommand(t, url, "label", "list", "--format", "json"); err != nil || !strings.Contains(out, `"stored"`) || (*requests)[1].body["tables"] != nil {
		t.Fatalf("%q %v", out, err)
	}
}

func TestMeaningLabelChanges(t *testing.T) {
	url, requests := fakeMeaningAgent(t, nil)
	out, err := runMeaningCommand(t, url, "label", "confirm", "--table", "sales.Customer", "--column", "Email", "--reason", "it is an email")
	if err != nil || out != "meaning.label.confirmed chinook/sales.Customer.Email; labels revision r2\n" {
		t.Fatalf("%q %v", out, err)
	}
	change := (*requests)[1].body
	inner := change["change"].(map[string]any)
	if (*requests)[0].route != "meanings/labels" || change["ifRevision"] != "r1" || inner["action"] != "confirm" || inner["class"] != "pii" || inner["kind"] != "email" || inner["reason"] != "it is an email" {
		t.Fatalf("change: %v", change)
	}
	for _, test := range []struct {
		args  []string
		check func(map[string]any) bool
	}{
		{[]string{"label", "dismiss", "--table", "Customer", "--column", "Missing"}, func(c map[string]any) bool { return c["class"] == nil }},
		{[]string{"label", "dismiss", "--table", "Other", "--column", "Email"}, func(c map[string]any) bool { return c["class"] == nil }},
		{[]string{"label", "confirm", "--table", "Customer", "--column", "Email", "--class", "financial"}, func(c map[string]any) bool { return c["class"] == "financial" }},
		{[]string{"label", "set", "--table", "Customer", "--column", "Email", "--class", "pii", "--kind", "email", "--non-revealable", "--reason", "r"}, func(c map[string]any) bool { return c["nonRevealable"] == true }},
		{[]string{"label", "clear", "--table", "Customer"}, func(c map[string]any) bool { return c["target"].(map[string]any)["column"] == nil }},
		{[]string{"label", "propose", "--table", "Customer", "--column", "Phone", "--class", "pii", "--agent"}, func(c map[string]any) bool { return c["action"] == "propose" }},
		{[]string{"label", "class", "--class", "pii", "--non-revealable", "--reason", "policy"}, func(c map[string]any) bool {
			return c["action"] == "class" && c["target"].(map[string]any)["table"] == ""
		}},
	} {
		before := len(*requests)
		if _, err := runMeaningCommand(t, url, test.args...); err != nil {
			t.Fatalf("%v: %v", test.args, err)
		}
		if got := (*requests)[before+1].body["change"].(map[string]any); !test.check(got) {
			t.Fatalf("%v: %v", test.args, got)
		}
	}
	if out, err := runMeaningCommand(t, url, "label", "class", "--class", "pii", "--reason", "r"); err != nil || out != "meaning.label.confirmed chinook; labels revision r2\n" {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := runMeaningCommand(t, url, "label", "set", "--table", "T", "--class", "none", "--reason", "r", "--format", "json"); err != nil || !strings.Contains(out, `"revision": "r2"`) {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := runMeaningCommand(t, url, "label", "set", "--class", "pii"); err == nil || !strings.Contains(err.Error(), "--table is required") {
		t.Fatal(err)
	}
}

func TestMeaningCommandErrors(t *testing.T) {
	url, _ := fakeMeaningAgent(t, func(string) (int, string) {
		return 403, `{"code":"LABEL_CHANGE_NOT_ALLOWED_FOR_AGENT","message":"agents may only propose"}`
	})
	for _, args := range [][]string{
		{"describe"}, {"find", "x"}, {"read", "--table", "T"}, {"label", "list"}, {"label", "set", "--table", "T"},
	} {
		if _, err := runMeaningCommand(t, url, args...); err == nil || !strings.Contains(err.Error(), "LABEL_CHANGE_NOT_ALLOWED_FOR_AGENT: agents may only propose") {
			t.Errorf("%v: %v", args, err)
		}
	}
	url, _ = fakeMeaningAgent(t, func(route string) (int, string) {
		if route == "meanings/labels/change" {
			return 409, `{"code":"LABELS_REVISION_CONFLICT"}`
		}
		return 0, ""
	})
	if _, err := runMeaningCommand(t, url, "label", "clear", "--table", "T"); err == nil || !strings.Contains(err.Error(), "LABELS_REVISION_CONFLICT") {
		t.Fatal(err)
	}
	if _, err := runMeaningCommand(t, url, "describe", "--format", "yaml"); err == nil {
		t.Error("accepted --format yaml")
	}
	for args, want := range map[string]string{
		"describe": "--project, --env and --source are required",
		"describe --project p --env e --source s --agent-url http://evil.test:1": "--agent-url must use localhost",
	} {
		cmd := meaningCommand()
		cmd.SetArgs(strings.Fields(args))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", args, err)
		}
	}
}

func TestMeaningCellText(t *testing.T) {
	for raw, want := range map[string]string{`"x"`: "x", `null`: "NULL", `{"type":"null","value":null}`: "NULL", `{"type":"string","value":null}`: "NULL", `{"type":"boolean","value":true}`: "true", `{bad`: "NULL", `1.5`: "1.5"} {
		if got := meaningCellText(json.RawMessage(raw), false); got != want {
			t.Errorf("%s = %q, want %q", raw, got, want)
		}
	}
	if meaningCellText(json.RawMessage(`"secret"`), true) != maskedCell {
		t.Fatal("masked cell rendered")
	}
}
