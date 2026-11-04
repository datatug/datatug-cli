package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// meaningCall records one request the emulated local agent received.
type meaningCall struct {
	route string
	agent string
	body  map[string]any
}

// meaningAgent emulates the mg-sensitivity wire contract. respond returns the
// status and body for a route; nil uses the contract examples below.
func meaningAgent(t *testing.T, agent bool, respond func(route string) (int, string)) (*Client, *[]meaningCall) {
	t.Helper()
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	calls := &[]meaningCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimPrefix(r.URL.Path, "/datatug/")
		if r.Method != http.MethodPost {
			t.Errorf("%s must POST", route)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*calls = append(*calls, meaningCall{route, r.Header.Get(AgentHeader), body})
		status, text := 0, ""
		if respond != nil {
			status, text = respond(route)
		}
		if text == "" {
			status, text = http.StatusOK, contractResponses[route]
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, text)
	}))
	t.Cleanup(server.Close)
	client, err := NewCommandClient(server.URL, agent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, calls
}

var contractResponses = map[string]string{
	"meanings/describe": `{"source":"chinook","labelsRevision":"r1","notice":"Local masking is accident prevention for honest agents","tables":[
		{"schema":"","name":"Customer","kind":"table","reviewed":false,"meanings":[{"entity":"Customer","title":"Customer","description":"A buyer"}],"primaryKey":["CustomerId"],
		 "columns":[{"name":"CustomerId","type":"INTEGER","nullable":false},{"name":"Email","type":"NVARCHAR(60)","nullable":false,"meaning":{"entity":"Customer","field":"Email","provenance":"declared","description":"password=hunter2"},
		  "label":{"class":"pii","kind":"email","origin":"suggested","state":"pending","rule":"pii-email","revealable":true,"nonRevealable":false}}],
		 "foreignKeys":[{"columns":["SupportRepId"],"refTable":{"schema":"","name":"Employee"},"refColumns":["EmployeeId"]}]},
		{"schema":"main","name":"Invoice","kind":"view","reviewed":true,"primaryKey":[],"columns":[]}],
		"joins":[{"from":{"table":"Invoice","columns":["CustomerId"]},"to":{"table":"Customer","columns":["CustomerId"]},"provenance":"foreign-key"}]}`,
	"meanings/find": `{"matches":[{"table":"Customer","column":"Email","type":"NVARCHAR(60)","label":{"class":"pii","kind":"email","origin":"suggested","state":"pending","revealable":true},"meaning":{"entity":"Customer","field":"Email","title":"E-mail"}},{"table":"Employee"}]}`,
	"records/read": `{"recordset":{"columns":[{"name":"CustomerId","type":"integer"},{"name":"Email","type":"string"}],"rows":[[{"type":"integer","value":"1"},null],[{"type":"integer","value":"2"},null]]},
		"limitations":[{"policy":"meaning-labels","rowsFiltered":false,"hiddenColumns":[]}],"provenance":{"source":"chinook","collection":"Customer","mode":"live","observedAt":"2026-11-04T00:00:00Z","executionProfile":"protected"},"truncated":false,
		"protection":{"Email":{"class":"pii","kind":"email","state":"masked","revealable":true,"origin":"suggested","ask":"Ask your user for an elevated approval naming Customer.Email"}},"notice":"Local masking is accident prevention for honest agents"}`,
	"meanings/labels":        `{"revision":"r1","tables":[{"schema":"","name":"Customer","kind":"table","reviewed":false,"columns":[{"name":"CustomerId","type":"INTEGER"},{"name":"Email","type":"NVARCHAR(60)","label":{"class":"pii","kind":"email","origin":"suggested","state":"pending","rule":"pii-email","revealable":true}}]}],"stored":{"format":"datatug-labels/v1"}}`,
	"meanings/labels/change": `{"revision":"r2","event":{"type":"meaning.label.proposed","actor":{"id":"agent","agent":true},"target":{"source":"chinook","table":"Customer","column":"Phone"},"at":"2026-11-04T00:00:00Z"}}`,
	"records/schema":         `{"source":"chinook","schema":"","table":"Customer","columns":[{"name":"Email","type":"text","label":{"class":"pii","kind":"email","origin":"suggested","state":"pending","revealable":true},"protection":{"class":"pii","state":"masked","revealable":true,"ask":"ask"}},{"name":"CustomerId","type":"integer"}],"primaryKey":["CustomerId"],"referencedBy":[],"foreignKeys":[]}`,
}

var chinook = MeaningScope{Project: "demo", Environment: "local", Source: "chinook"}

func TestMeaningClientMethodsFollowTheWireContract(t *testing.T) {
	ctx := context.Background()
	c, calls := meaningAgent(t, false, nil)
	d, err := c.DescribeMeaning(ctx, MeaningScope{"demo", "local", "chinook", []string{"customer"}})
	if err != nil || len(d.Tables) != 2 || d.Tables[0].Columns[1].Label.Kind != "email" || d.Joins[0].Provenance != "foreign-key" || d.Tables[1].Meanings == nil {
		t.Fatalf("describe: %+v %v", d, err)
	}
	if strings.Contains(d.Tables[0].Columns[1].Meaning.Description, "hunter2") {
		t.Fatal("metadata was not redacted")
	}
	f, err := c.FindMeaning(ctx, chinook, "email")
	if err != nil || len(f.Matches) != 2 || f.Matches[0].Meaning.Title != "E-mail" || f.Matches[1].Label != nil {
		t.Fatalf("find: %+v %v", f, err)
	}
	r, err := c.ReadTable(ctx, TableReadRequest{Project: "demo", Environment: "local", Source: "chinook", Table: "Customer", Columns: []string{"CustomerId", "Email"},
		Where: []Condition{{"CustomerId", ">", 0.0}, {"Country", "=", "UK"}, {"Active", "!=", false}, {"Id", "<=", 3}, {"Id", "<", int64(9)}, {"Id", ">=", json.Number("1")}}, OrderBy: []Order{{Column: "CustomerId", Desc: true}}})
	if err != nil || len(r.Recordset.Rows) != 2 || string(r.Recordset.Rows[0][1]) != "null" || r.Protection["Email"].Ask == "" || r.Notice == "" {
		t.Fatalf("read: %+v %v", r, err)
	}
	l, err := c.MeaningLabels(ctx, chinook)
	if err != nil || l.Revision != "r1" || l.Tables[0].Columns[1].Label.Rule != "pii-email" || l.Stored.Format != meaninglabels.Format {
		t.Fatalf("labels: %+v %v", l, err)
	}
	ch, err := c.ChangeLabel(ctx, chinook, "r1", meaninglabels.Change{Action: meaninglabels.ActionClassMark, Class: meaninglabels.ClassPII, NonRevealable: true, Reason: "policy"})
	if err != nil || ch.Revision != "r2" || ch.Event.Type != "meaning.label.proposed" {
		t.Fatalf("change: %+v %v", ch, err)
	}
	want := []string{"meanings/describe", "meanings/find", "records/read", "meanings/labels", "meanings/labels/change"}
	for i, call := range *calls {
		if call.route != want[i] || call.agent != "" {
			t.Fatalf("call %d: %+v", i, call)
		}
	}
	read := (*calls)[2].body
	if read["limit"] != 100.0 || read["schema"] != "" || len(read["where"].([]any)) != 6 || read["env"] != "local" {
		t.Fatalf("read request: %v", read)
	}
	change := (*calls)[4].body
	if change["ifRevision"] != "r1" || change["change"].(map[string]any)["target"].(map[string]any)["source"] != "chinook" {
		t.Fatalf("change request: %v", change)
	}
	if tables := (*calls)[0].body["tables"].([]any); tables[0] != "customer" {
		t.Fatalf("describe request: %v", (*calls)[0].body)
	}
}

func TestMeaningClientNormalizesEmptyAnswers(t *testing.T) {
	ctx := context.Background()
	c, _ := meaningAgent(t, true, func(route string) (int, string) {
		switch route {
		case "records/read":
			return 200, `{"recordset":{"columns":[{"name":"a","type":"string"}]},"provenance":{"source":"s"}}`
		case "meanings/labels":
			return 200, `{"revision":"r0"}`
		}
		return 200, `{}`
	})
	d, err := c.DescribeMeaning(ctx, chinook)
	if err != nil || d.Tables == nil || d.Joins == nil {
		t.Fatalf("%+v %v", d, err)
	}
	f, err := c.FindMeaning(ctx, chinook, "x")
	if err != nil || f.Matches == nil {
		t.Fatalf("%+v %v", f, err)
	}
	r, err := c.ReadTable(ctx, TableReadRequest{Project: "demo", Environment: "local", Source: "chinook", Table: "t", Limit: 5})
	if err != nil || r.Recordset.Rows == nil || r.Limitations == nil || r.Protection != nil {
		t.Fatalf("%+v %v", r, err)
	}
	l, err := c.MeaningLabels(ctx, chinook)
	if err != nil || l.Tables == nil {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestMeaningClientRefusesInvalidInputBeforeSending(t *testing.T) {
	ctx := context.Background()
	c, calls := meaningAgent(t, true, nil)
	long := strings.Repeat("x", 257)
	badScopes := []MeaningScope{{Project: "../x", Environment: "local", Source: "chinook"}, {"demo", "local", "chinook", []string{""}}, {"demo", "local", "chinook", []string{long}}, {"demo", "local", "chinook", []string{"bad\x00name"}}, {"demo", "local", "chinook", []string{"\xff"}}, {"demo", "local", "chinook", make([]string, 257)}}
	for _, scope := range badScopes {
		if _, err := c.DescribeMeaning(ctx, scope); err == nil {
			t.Errorf("describe accepted %v", scope)
		}
	}
	if _, err := c.FindMeaning(ctx, badScopes[0], "x"); err == nil {
		t.Error("find accepted bad scope")
	}
	for _, text := range []string{"", strings.Repeat("é", 201), "a\nb"} {
		if _, err := c.FindMeaning(ctx, chinook, text); err == nil {
			t.Errorf("find accepted %q", text)
		}
	}
	if _, err := c.FindMeaning(ctx, chinook, strings.Repeat("é", 200)); err != nil {
		t.Errorf("find refused 200 characters: %v", err)
	}
	base := TableReadRequest{Project: "demo", Environment: "local", Source: "chinook", Table: "t"}
	bad := []func(*TableReadRequest){
		func(r *TableReadRequest) { r.Source = "a/b" },
		func(r *TableReadRequest) { r.Table = "" },
		func(r *TableReadRequest) { r.Schema = long },
		func(r *TableReadRequest) { r.Columns = []string{""} },
		func(r *TableReadRequest) { r.Where = make([]Condition, 33) },
		func(r *TableReadRequest) { r.OrderBy = make([]Order, 33) },
		func(r *TableReadRequest) { r.Limit = -1 },
		func(r *TableReadRequest) { r.Limit = 501 },
		func(r *TableReadRequest) { r.Where = []Condition{{"", "=", "x"}} },
		func(r *TableReadRequest) { r.Where = []Condition{{"a", "LIKE", "x"}} },
		func(r *TableReadRequest) { r.Where = []Condition{{"a", "=", nil}} },
		func(r *TableReadRequest) { r.Where = []Condition{{"a", "=", []any{1}}} },
		func(r *TableReadRequest) { r.OrderBy = []Order{{Column: ""}} },
		func(r *TableReadRequest) { r.Reveal = []string{"*", "a"} },
		func(r *TableReadRequest) { r.Reveal = []string{""} },
	}
	for i, mutate := range bad {
		r := base
		mutate(&r)
		if _, err := c.ReadTable(ctx, r); err == nil {
			t.Errorf("read case %d accepted", i)
		}
	}
	if _, err := c.MeaningLabels(ctx, badScopes[0]); err == nil {
		t.Error("labels accepted bad scope")
	}
	if _, err := c.ChangeLabel(ctx, badScopes[0], "", meaninglabels.Change{}); err == nil {
		t.Error("change accepted bad scope")
	}
	for _, change := range []meaninglabels.Change{
		{Action: meaninglabels.ActionSet},
		{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Table: "t", Column: "\n"}},
		{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Table: "t"}, Reason: strings.Repeat("r", 1001)},
	} {
		if _, err := c.ChangeLabel(ctx, chinook, "", change); err == nil {
			t.Errorf("change accepted %+v", change)
		}
	}
	if _, err := c.ChangeLabel(ctx, chinook, long, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Table: "t"}}); err == nil {
		t.Error("change accepted a long revision")
	}
	if len(*calls) != 1 {
		t.Fatalf("invalid input reached the agent: %+v", *calls)
	}
}

func TestMeaningClientSurfacesAgentRefusals(t *testing.T) {
	ctx := context.Background()
	refuse := func(string) (int, string) {
		return 403, `{"code":"FIELD_PROTECTED","message":"Customer.Email is pii;\n ask for an elevated approval","field":"Email"}`
	}
	c, _ := meaningAgent(t, true, refuse)
	checks := []func() error{
		func() error { _, err := c.DescribeMeaning(ctx, chinook); return err },
		func() error { _, err := c.FindMeaning(ctx, chinook, "x"); return err },
		func() error {
			_, err := c.ReadTable(ctx, TableReadRequest{Project: "demo", Environment: "local", Source: "chinook", Table: "t"})
			return err
		},
		func() error { _, err := c.MeaningLabels(ctx, chinook); return err },
		func() error {
			_, err := c.ChangeLabel(ctx, chinook, "r", meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Table: "t"}})
			return err
		},
	}
	for i, check := range checks {
		err := check()
		if err == nil || !strings.HasPrefix(err.Error(), "FIELD_PROTECTED: Customer.Email is pii; ask for") {
			t.Errorf("case %d: %v", i, err)
		}
	}
	for _, body := range []string{
		`{"recordset":{"columns":[{"name":"a","type":"string"}],"rows":[[null],[null]]}}`,
		`{"recordset":{"columns":[{"name":"a","type":"string"}],"rows":[[null,null]]}}`,
	} {
		c, _ := meaningAgent(t, true, func(string) (int, string) { return 200, body })
		if _, err := c.ReadTable(ctx, TableReadRequest{Project: "demo", Environment: "local", Source: "chinook", Table: "t", Limit: 1}); err == nil || !strings.Contains(err.Error(), "invalid table read") {
			t.Errorf("accepted %s: %v", body, err)
		}
	}
}

func TestAgentErrorCodesPassThrough(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		status int
		body   string
		want   string
	}{
		{403, `{"code":"FIELD_PROTECTION_RAW_REFUSED","message":"saved SQL on a labelled source"}`, "FIELD_PROTECTION_RAW_REFUSED: saved SQL on a labelled source (HTTP 403)"},
		{403, `{"error":{"code":"LABEL_CHANGE_NOT_ALLOWED_FOR_AGENT","message":"agents may only propose"}}`, "LABEL_CHANGE_NOT_ALLOWED_FOR_AGENT: agents may only propose"},
		{403, `{"code":"REVEAL_NOT_ALLOWED_FOR_AGENT"}`, "REVEAL_NOT_ALLOWED_FOR_AGENT: local agent refused the request (HTTP 403)"},
		{403, `{"code":"FIELD_NON_REVEALABLE"}`, "FIELD_NON_REVEALABLE:"},
		{409, `{"code":"LABELS_REVISION_CONFLICT","message":"` + strings.Repeat("é", 400) + `"}`, "…"},
		{400, `{"code":"LABEL_TARGET_AMBIGUOUS","message":"customers could mean public.customers"}`, "LABEL_TARGET_AMBIGUOUS: customers could mean"},
		{400, `{"code":"LABEL_KIND_CLASS_MISMATCH"}`, "LABEL_KIND_CLASS_MISMATCH:"},
		{400, `{"code":"LABEL_lower","message":"secret"}`, "AGENT_ERROR: local agent refused"},
		{400, `{"code":"LABEL_` + strings.Repeat("X", 64) + `"}`, "AGENT_ERROR"},
		{400, `{"code":"LABELS_DOCUMENT_INVALID"}`, "LABELS_DOCUMENT_INVALID"},
		{403, `{"code":"FIELD_PROTECTED","message":"password=hunter2"}`, "FIELD_PROTECTED: [redacted metadata]"},
	} {
		c, _ := meaningAgent(t, true, func(string) (int, string) { return test.status, test.body })
		_, err := c.MeaningLabels(ctx, chinook)
		if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v", test.body, err)
		}
	}
}

func TestClientDeclaresAgentContext(t *testing.T) {
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get(AgentHeader))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	mcpClient, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpClient.Close()
	person, err := NewCommandClient(server.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	defer person.Close()
	if _, err := NewCommandClient("http://evil.test:1", true); err == nil {
		t.Fatal("accepted a non-loopback origin")
	}
	_ = mcpClient.request(context.Background(), "agent-info", nil, nil, &struct{}{})
	_ = mcpClient.request(context.Background(), "records/schema", nil, struct{}{}, &struct{}{})
	_ = person.request(context.Background(), "agent-info", nil, nil, &struct{}{})
	if strings.Join(seen, ",") != "1,1," {
		t.Fatalf("agent header: %q", seen)
	}
}

func TestMeaningMCPTools(t *testing.T) {
	c, calls := meaningAgent(t, true, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := New(c, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	if !strings.Contains(cs.InitializeResult().Instructions, "datatug_describe_meaning") || !strings.Contains(cs.InitializeResult().Instructions, "protection.ask") {
		t.Fatal("instructions miss the agent guidance")
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		readOnly := tool.Name != "datatug_propose_label"
		if tool.Annotations.ReadOnlyHint != readOnly || (!readOnly && (tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint)) {
			t.Errorf("%s annotations: %+v", tool.Name, tool.Annotations)
		}
	}
	source := map[string]any{"project": "demo", "environment": "local", "source": "chinook"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range source {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for _, call := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"datatug_describe_meaning", with(map[string]any{"tables": []string{"Customer"}}), `"foreign-key"`},
		{"datatug_find_meaning", with(map[string]any{"text": "email"}), `"pii"`},
		{"datatug_read_table", with(map[string]any{"table": "Customer", "columns": []string{"Email"}, "where": []map[string]any{{"column": "CustomerId", "op": "=", "value": 1}}, "limit": 5}), `"ask"`},
		{"datatug_propose_label", with(map[string]any{"table": "Customer", "column": "Phone", "class": "pii", "kind": "phone", "reason": "phone numbers"}), `"r2"`},
		{"datatug_describe_table", with(map[string]any{"schema": "", "table": "Customer"}), `"protection"`},
	} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %+v %v", call.name, result, err)
		}
		body, _ := json.Marshal(result.StructuredContent)
		if !strings.Contains(string(body), call.want) {
			t.Fatalf("%s missing %s: %s", call.name, call.want, body)
		}
	}
	routes := []string{}
	for _, call := range *calls {
		if call.agent != "1" {
			t.Fatalf("%s without agent header", call.route)
		}
		routes = append(routes, call.route)
	}
	if strings.Join(routes, " ") != "meanings/describe meanings/find records/read meanings/labels meanings/labels/change records/schema" {
		t.Fatalf("routes: %v", routes)
	}
	proposal := (*calls)[4].body
	if proposal["ifRevision"] != "r1" || proposal["change"].(map[string]any)["action"] != "propose" {
		t.Fatalf("proposal: %v", proposal)
	}
	if tables := (*calls)[3].body["tables"].([]any); tables[0] != "Customer" {
		t.Fatalf("labels narrowing: %v", (*calls)[3].body)
	}
	for _, args := range []map[string]any{
		with(map[string]any{"table": "Customer", "class": "none", "reason": "r"}),
		with(map[string]any{"table": "Customer", "class": "pii", "kind": "iban", "reason": "r"}),
		with(map[string]any{"table": "Customer", "class": "pii", "reason": " "}),
		with(map[string]any{"table": "", "class": "pii", "reason": "r"}),
	} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "datatug_propose_label", Arguments: args})
		if err == nil && !result.IsError {
			t.Errorf("accepted %v", args)
		}
	}
}
