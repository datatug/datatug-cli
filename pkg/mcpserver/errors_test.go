package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMetadataAndInputRefusals(t *testing.T) {
	c, _ := newFixture(t)
	ctx := context.Background()
	for _, run := range []func() (any, error){
		func() (any, error) { return c.describeProject(ctx, projectInput{"../bad"}) },
		func() (any, error) { return c.listQueries(ctx, projectInput{"../bad"}) },
		func() (any, error) { return c.listTables(ctx, sourceInput{}) },
		func() (any, error) { return c.describeTable(ctx, tableInput{}) },
		func() (any, error) { return c.runQuery(ctx, runInput{}) },
		func() (any, error) {
			return c.runQuery(ctx, runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "q", Limit: 501})
		},
		func() (any, error) {
			return c.runQuery(ctx, runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "q", Parameters: map[string]string{"unknown": "1"}})
		},
	} {
		if _, err := run(); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if queryIdentifier(strings.Repeat("x", 1025)) {
		t.Fatal("unbounded query ID")
	}
	if safeText("password=supersecret") == "password=supersecret" {
		t.Fatal("credential metadata escaped screen")
	}
	if got := safeTexts([]string{"password=supersecret", "admin"}); got[1] != "admin" || strings.Contains(got[0], "supersecret") {
		t.Fatal(got)
	}
	for _, test := range []struct {
		q  *datatug.QueryDef
		id string
	}{
		{nil, "q"},
		{&datatug.QueryDef{Type: datatug.QueryTypeSQL}, "../q"},
		{&datatug.QueryDef{Type: datatug.QueryTypeSQL, Text: "password=supersecret"}, "q"},
		{&datatug.QueryDef{Type: datatug.QueryTypeSQL, Parameters: datatug.Parameters{{ID: "p", IsCredential: true}}}, "q"},
	} {
		if _, err := publicQuery(test.q, test.id, true); err == nil {
			t.Fatal("unsafe query exposed")
		}
	}
	base := *c.base
	c.base = &url.URL{Scheme: "http", Host: "\x00"}
	if err := c.request(ctx, "agent-info", nil, nil, &struct{}{}); err == nil {
		t.Fatal("invalid internal URL accepted")
	}
	c.base = &base
}

func TestEachReadPropagatesSafeAgentFailure(t *testing.T) {
	c, _ := newFixture(t)
	ctx := context.Background()
	c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"ACCESS_DENIED","message":"password=supersecret"}}`))}, nil
	})
	for _, run := range []func() (any, error){
		func() (any, error) { return c.agentInfo(ctx, noInput{}) },
		func() (any, error) { return c.listProjects(ctx, noInput{}) },
		func() (any, error) { return c.describeProject(ctx, projectInput{"demo"}) },
		func() (any, error) { return c.listQueries(ctx, projectInput{"demo"}) },
		func() (any, error) { return c.getQuery(ctx, queryInput{"demo", "q"}) },
		func() (any, error) { return c.listTables(ctx, sourceInput{"demo", "local", "chinook"}) },
		func() (any, error) {
			return c.describeTable(ctx, tableInput{"demo", "local", "chinook", "", "Invoice"})
		},
		func() (any, error) {
			return c.runQuery(ctx, runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "q"})
		},
	} {
		if out, err := run(); err == nil || !strings.Contains(err.Error(), "ACCESS_DENIED") || strings.Contains(err.Error(), "supersecret") {
			t.Fatalf("%v %v", out, err)
		}
	}
}

func reply(c *Client, route, body string, status int) func() {
	previous := c.http.Transport
	c.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/datatug/"+route {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		return previous.RoundTrip(r)
	})
	return func() { c.http.Transport = previous }
}

func TestUnexpectedMetadataDoesNotBecomeAuthority(t *testing.T) {
	c, _ := newFixture(t)
	ctx := context.Background()
	restore := reply(c, "agent-info", `{}`, 200)
	if _, err := c.info(ctx); err == nil {
		t.Fatal("accepted invalid session metadata")
	}
	restore()
	restore = reply(c, "queries/get_query", `{"id":"q","type":"TugQL"}`, 200)
	if _, err := c.loadQuery(ctx, queryInput{"demo", "q"}); err == nil {
		t.Fatal("accepted non-SQL")
	}
	restore()
	for _, route := range []string{"agent-info", "exec/run_query"} {
		restore = reply(c, route, `{"error":{"code":"ACCESS_DENIED"}}`, 403)
		if _, err := c.runQuery(ctx, runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "q"}); err == nil {
			t.Fatal("execution ignored failed scope or denial")
		}
		restore()
	}
	restore = reply(c, "exec/run_query", `{}`, 200)
	if _, err := c.runQuery(ctx, runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "q", Limit: 1}); err == nil {
		t.Fatal("accepted invalid result")
	}
	restore()
	root := &datatug.QueriesFolder{}
	folder := root
	for range 34 {
		child := &datatug.QueriesFolder{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "folder"}}}
		folder.Folders = datatug.QueryFolders{child}
		folder = child
	}
	body, _ := json.Marshal(root)
	restore = reply(c, "queries/all_queries", string(body), 200)
	if _, err := c.listQueries(ctx, projectInput{"demo"}); err == nil {
		t.Fatal("accepted unbounded nesting")
	}
	restore()
	restore = reply(c, "projects/project_full", `{"environments":[{"id":"prod"},{"id":"local"}]}`, 200)
	out, err := c.describeProject(ctx, projectInput{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(projectDescription).Environments[0].ID; got != "local" {
		t.Fatal("unstable environment order")
	}
	restore()
}

func TestSchemaAndResultMetadataAreRedacted(t *testing.T) {
	c, _ := newFixture(t)
	ctx := context.Background()
	const secret = "password=supersecret"
	// Use a raw shape to prove unknown row/default/profile fields are discarded too.
	body := `{"source":"` + secret + `","schema":"` + secret + `","table":"` + secret + `","columns":[{"name":"` + secret + `","type":"` + secret + `"}],"primaryKey":["` + secret + `"],"referencedBy":["` + secret + `"],"foreignKeys":[{"name":"` + secret + `","columns":["` + secret + `"],"refTable":{"name":"` + secret + `","schema":"` + secret + `"},"refColumns":["` + secret + `"]}],"rows":["supersecret"],"default":"supersecret"}`
	restore := reply(c, "records/schema", body, 200)
	out, err := c.describeTable(ctx, tableInput{"demo", "local", "chinook", "", "Invoice"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "supersecret") || strings.Contains(string(encoded), `"rows"`) {
		t.Fatal(string(encoded))
	}
	restore()
	result := apicontract.Result{Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: secret, Type: "string"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue("authorized value")}}}, Provenance: apicontract.Provenance{Source: secret, Collection: secret, QueryID: secret, SnapshotID: secret, Mode: "live", ObservedAt: "2026-10-23T00:00:00Z", ExecutionProfile: "opaque-privileged"}, Limitations: []apicontract.Limitation{{Policy: secret, HiddenColumns: []string{secret}}}, BindingsApplied: []apicontract.Binding{{ParameterID: secret, Value: apicontract.ScalarValue(apicontract.NewIntegerValue("1")), Origin: "manual", OriginEvidence: "client-reported"}}}
	encoded, _ = json.Marshal(result)
	restore = reply(c, "exec/run_query", string(encoded), 200)
	out, err = c.runQuery(ctx, runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "q"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(out)
	if strings.Contains(string(encoded), "supersecret") || !strings.Contains(string(encoded), "authorized value") {
		t.Fatal(string(encoded))
	}
	restore()
}

func TestSDKRunLifecycleAndOutputBounds(t *testing.T) {
	c, _ := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, "test", serverRead, serverWrite) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "lifecycle"}, nil).Connect(ctx, &mcp.IOTransport{Reader: clientRead, Writer: clientWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	s := mcp.NewServer(&mcp.Implementation{Name: "bounds"}, nil)
	add(s, "bounded", "test bounded output", func(context.Context, noInput) (any, error) { return strings.Repeat("x", maxBody+1), nil })
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err = mcp.NewClient(&mcp.Implementation{Name: "bounds-test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "bounded", Arguments: map[string]any{}})
	if err != nil || !out.IsError {
		t.Fatalf("unbounded output accepted: %v %v", out, err)
	}
}
