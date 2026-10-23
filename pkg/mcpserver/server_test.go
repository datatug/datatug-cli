package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/internal/hermetictest"
	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) { os.Exit(hermetictest.Main(m)) }

func newFixture(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/datatug/agent-info":
			_, _ = io.WriteString(w, `{"version":"test","principal":{"id":"admin","roles":[],"groups":[]},"securityContextId":"fresh","projects":[{"id":"demo"},{"id":"https://bad"}],"capabilities":{"opaqueReadOnly":true}}`)
		case "/datatug/projects/project_full":
			_, _ = io.WriteString(w, `{"environments":[{"id":"local","dbServers":[{"host":"secret-host","password":"secret-password","catalogs":["chinook","postgres://bad"]}]},{"id":"../bad"}],"dbModels":[{"environments":[{"id":"local","DbCatalogs":[{"id":"model-source"}]},{"id":"../bad"}]}],"entities":[{"id":"Customer","title":"Customers","password":"hidden"},{"id":"../bad"}]}`)
		case "/datatug/queries/all_queries":
			_, _ = io.WriteString(w, `{"id":"~","items":[{"id":"other","type":"HTTP"}],"folders":[{"id":"customers","items":[{"id":"purchases","type":"SQL","title":"Purchases","parameters":[{"id":"CustomerId","type":"integer","isRequired":true,"defaultValue":"do-not-expose"}]}]}]}`)
		case "/datatug/queries/get_query":
			_, _ = io.WriteString(w, `{"id":"purchases","folderPath":"customers","type":"SQL","text":"SELECT Total FROM Invoice WHERE CustomerId = @CustomerId","parameters":[{"id":"CustomerId","type":"integer","isRequired":true}],"targets":[{"password":"do-not-expose"}]}`)
		case "/datatug/catalog-tables":
			_, _ = io.WriteString(w, `{"tables":[{"schema":"","name":"Invoice","password":"hidden"}],"views":[{"schema":"","name":"Customers"}]}`)
		case "/datatug/records/schema":
			if r.Method != http.MethodPost {
				t.Error("schema must POST")
			}
			_, _ = io.WriteString(w, `{"source":"chinook","schema":"","table":"Invoice","columns":[{"name":"InvoiceId","type":"integer"}],"primaryKey":["InvoiceId"],"referencedBy":[],"foreignKeys":[],"rows":["do-not-expose"]}`)
		case "/datatug/exec/run_query":
			var req apicontract.ExecutionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.SecurityContextID != "fresh" || req.Record || req.Snapshot || req.TugQL != "" {
				t.Errorf("unexpected execution authority: %+v", req)
			}
			result := apicontract.Result{Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "Total", Type: "integer"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("14")}}}, Limitations: []apicontract.Limitation{{Policy: "native-sql"}}, BindingsApplied: []apicontract.Binding{}, Provenance: apicontract.Provenance{Source: "chinook", QueryID: req.QueryID, Mode: "live", ObservedAt: "2026-10-23T00:00:00Z", ExecutionProfile: "opaque-privileged"}}
			_ = json.NewEncoder(w).Encode(result)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, server
}

func TestSDKDiscoveryAndSafeReadTools(t *testing.T) {
	client, _ := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := New(client, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("tools %v: %v", tools, err)
	}
	calls := []struct {
		name string
		args map[string]any
		want string
	}{
		{"datatug_agent_info", map[string]any{}, "opaqueReadOnly"},
		{"datatug_list_projects", map[string]any{}, "demo"},
		{"datatug_describe_project", map[string]any{"project": "demo"}, "model-source"},
		{"datatug_list_queries", map[string]any{"project": "demo"}, "customers/purchases"},
		{"datatug_get_query", map[string]any{"project": "demo", "queryId": "customers/purchases"}, "SELECT Total"},
		{"datatug_list_tables", map[string]any{"project": "demo", "environment": "local", "source": "chinook"}, "Invoice"},
		{"datatug_describe_table", map[string]any{"project": "demo", "environment": "local", "source": "chinook", "schema": "", "table": "Invoice"}, "InvoiceId"},
		{"datatug_run_query", map[string]any{"project": "demo", "environment": "local", "source": "chinook", "queryId": "customers/purchases", "parameters": map[string]string{"CustomerId": "1"}}, "14"},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
			if err != nil || result.IsError {
				t.Fatalf("%v: %v", result, err)
			}
			body, err := json.Marshal(result.StructuredContent)
			if err != nil || !strings.Contains(string(body), call.want) {
				t.Fatalf("missing %s: %s (%v)", call.want, body, err)
			}
			if len(result.Content) == 0 {
				t.Fatal("missing compatible text content")
			}
			for _, secret := range []string{"secret-password", "secret-host", "do-not-expose", "password", "securityContextId"} {
				if strings.Contains(string(body), secret) {
					t.Fatalf("leaked %s: %s", secret, body)
				}
			}
		})
	}
	for _, call := range []mcp.CallToolParams{{Name: "delete_project", Arguments: map[string]any{}}, {Name: "datatug_run_query", Arguments: map[string]any{"project": "demo", "allowWrites": true}}, {Name: "datatug_get_query", Arguments: map[string]any{"project": "demo", "queryId": "../bad"}}} {
		result, err := cs.CallTool(ctx, &call)
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("invalid tool call accepted: %s", call.Name)
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("secret read failure") }
func (brokenReader) Close() error             { return nil }

func TestTransportFailsClosed(t *testing.T) {
	for _, origin := range []string{"%", "https://127.0.0.1:8989", "http://user:secret@127.0.0.1:8989", "http://evil.test:8989", "http://127.0.0.1", "http://localhost:0", "http://localhost:65536", "http://localhost:80/path", "http://localhost:80?token=secret", "http://localhost:80#token"} {
		if _, err := NewClient(origin); err == nil {
			t.Errorf("accepted %s", origin)
		}
	}
	for _, origin := range []string{"http://localhost:8989/", "http://[::1]:8989"} {
		c, err := NewClient(origin)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	c, _ := newFixture(t)
	if err := c.request(context.Background(), "projects/delete_project", nil, nil, &struct{}{}); err == nil {
		t.Fatal("write route allowed")
	}
	if err := c.request(context.Background(), "agent-info", nil, struct{}{}, &struct{}{}); err == nil {
		t.Fatal("unexpected body allowed")
	}
	for _, body := range []any{func() {}, strings.Repeat("x", maxRequest)} {
		if err := c.request(context.Background(), "exec/run_query", nil, body, &struct{}{}); err == nil {
			t.Fatal("invalid body allowed")
		}
	}
	for _, test := range []struct {
		status int
		body   string
		want   string
	}{{401, `{"token":"secret"}`, "AUTHENTICATION_REQUIRED"}, {403, `{"error":{"code":"ACCESS_DENIED","message":"password=secret"}}`, "ACCESS_DENIED"}, {400, `{"code":"INVALID_REQUEST"}`, "INVALID_REQUEST"}, {500, `{"error":{"code":"secret-password"}}`, "AGENT_ERROR"}, {200, `not JSON secret`, "invalid response"}, {200, strings.Repeat("x", maxBody+1), "exceeds"}} {
		c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
		})
		err := c.request(context.Background(), "agent-info", nil, nil, &struct{}{})
		if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: brokenReader{}}, nil
	})
	if err := c.request(context.Background(), "agent-info", nil, nil, &struct{}{}); err == nil {
		t.Fatal("read error ignored")
	}
	c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("dsn=secret") })
	if err := c.request(context.Background(), "agent-info", nil, nil, &struct{}{}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.request(ctx, "agent-info", nil, nil, &struct{}{}); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal(err)
	}
}

func TestRedirectCannotLeakAgentKey(t *testing.T) {
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	seen := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = true; t.Error("followed redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing local authentication")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	port, _ := strconv.Atoi(c.base.Port())
	secret, err := agentaccess.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := agentaccess.WriteKey(agentaccess.StateDir(), port, secret); err != nil {
		t.Fatal(err)
	}
	if err := c.request(context.Background(), "agent-info", nil, nil, &struct{}{}); err == nil || seen {
		t.Fatalf("redirect succeeded: %v", err)
	}
}

func TestParameterBindingsAreTyped(t *testing.T) {
	def := datatug.Parameters{}
	values := map[string]string{"string": "'; DELETE FROM Invoice; --", "integer": "9007199254740993", "decimal": "1.25", "number": "2.5", "boolean": "false", "date": "2026-10-23", "datetime": "2026-10-23T00:00:00Z"}
	for kind := range values {
		def = append(def, datatug.ParameterDef{ID: kind, Type: kind})
	}
	bound, origins, err := bindParameters(def, values)
	if err != nil || len(bound) != 7 || len(origins) != 7 {
		t.Fatalf("%v %v %v", bound, origins, err)
	}
	if bound["integer"].Scalar.Str != "9007199254740993" || bound["string"].Scalar.Str != values["string"] {
		t.Fatal("lossy binding")
	}
	for _, test := range []struct {
		p     datatug.ParameterDef
		value string
	}{{datatug.ParameterDef{ID: "p", Type: "number"}, "not-number"}, {datatug.ParameterDef{ID: "p", Type: "number"}, "NaN"}, {datatug.ParameterDef{ID: "p", Type: "integer"}, "1.2"}, {datatug.ParameterDef{ID: "p", Type: "boolean"}, "yes"}, {datatug.ParameterDef{ID: "p", Type: "unknown"}, "x"}, {datatug.ParameterDef{ID: "p", Type: "string", IsCredential: true}, "x"}, {datatug.ParameterDef{ID: "p", Type: "string", IsMultiValue: true}, "x"}, {datatug.ParameterDef{ID: "other", Type: "string"}, "x"}} {
		if _, _, err := bindParameters(datatug.Parameters{test.p}, map[string]string{"p": test.value}); err == nil {
			t.Errorf("accepted %+v", test)
		}
	}
}
