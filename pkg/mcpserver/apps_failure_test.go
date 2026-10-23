package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAppsCapabilityAndMiddlewareFailClosed(t *testing.T) {
	if supportsApps(nil) || supportsApps(&mcp.InitializeParams{}) || supportsApps(&mcp.InitializeParams{Capabilities: &mcp.ClientCapabilities{Extensions: map[string]any{appsExtension: func() {}}}}) {
		t.Fatal("accepted absent/unserializable capability")
	}
	a := newAppsProtocol()
	defer a.close()
	called := 0
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		called++
		return &mcp.ListToolsResult{}, nil
	}
	if _, err := a.middleware(next)(context.Background(), "tools/list", &mcp.ClientRequest[*mcp.ListToolsParams]{}); err != nil || called != 1 {
		t.Fatal("nonserver passthrough changed")
	}
	if _, err := a.middleware(next)(context.Background(), "ping", &mcp.ServerRequest[*mcp.PingParams]{Session: &mcp.ServerSession{}}); err != nil || called != 2 {
		t.Fatal("preinit ping passthrough changed")
	}
	client, _ := newFixture(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "private-failures"}, nil)
	addRunQuery(server, client, "test")
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				return nil, errors.New("PRIVATE_INTERNAL_ERROR")
			}
			return next(ctx, method, req)
		}
	})
	server.AddReceivingMiddleware(a.middleware)
	app := connectAppsClient(t, server, true, "2026-07-28")
	requireAppsUnavailable(t, appsCall(t, app, "datatug_run_query", appsArgs()))
	requireAppsUnavailable(t, appsCall(t, app, "datatug_run_query", map[string]any{"parameters": strings.Repeat("x", maxRequest+1)}))
	// Filtering UI resources must preserve other server resources.
	server.AddResource(&mcp.Resource{URI: "data://safe", Name: "safe"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})
	a.addResource(server)
	plain := connectAppsClient(t, server, false, "2026-07-28")
	resources, err := plain.ListResources(context.Background(), nil)
	if err != nil || len(resources.Resources) != 1 || resources.Resources[0].URI != "data://safe" {
		t.Fatalf("non-UI resources lost: %+v %v", resources, err)
	}
}

func TestAppsHandlersRequireRegistryAndLegacyBounds(t *testing.T) {
	client, _ := newFixture(t)
	server := mcp.NewServer(&mcp.Implementation{Name: "missing-registry"}, nil)
	addRunQuery(server, client, "test")
	addAppsTools(server, client)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(context.WithValue(ctx, appsModeKey{}, true), method, req)
		}
	})
	app := connectAppsClient(t, server, true, "2026-07-28")
	requireAppsUnavailable(t, appsCall(t, app, "datatug_run_query", appsArgs()))
	requireAppsUnavailable(t, appsCall(t, app, "datatug_apps_result", map[string]any{"handle": "x"}))
	requireAppsUnavailable(t, appsCall(t, app, "datatug_apps_rerun", map[string]any{"handle": "x", "parameters": map[string]string{}}))
	plain := connectAppsClient(t, New(client, "test"), false, "2026-07-28")
	args := appsArgs()
	args["parameters"] = map[string]string{"CustomerId": "bad-integer"}
	if !appsCall(t, plain, "datatug_run_query", args).IsError {
		t.Fatal("invalid legacy binding accepted")
	}
	// A compact wire response can expand beyond the MCP output cap when JSON
	// escapes HTML-sensitive cells. Test the final envelope, not just HTTP bytes.
	transport := client.http.Transport
	client.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/datatug/exec/run_query" {
			result := apicontract.Result{Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "x", Type: "string"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue(strings.Repeat("<", maxBody/3))}}}, Provenance: apicontract.Provenance{Source: "chinook", QueryID: "customers/purchases", Mode: "live", ObservedAt: "2026-10-23T00:00:00Z", ExecutionProfile: "opaque-privileged"}}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(result); err != nil {
				t.Fatal(err)
			}
			wire := bytes.ReplaceAll(buf.Bytes(), []byte(`\u003c`), []byte("<"))
			if len(wire) > maxBody {
				t.Fatal("fixture exceeds HTTP cap before MCP encoding")
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(wire))}, nil
		}
		return transport.RoundTrip(req)
	})
	bounded := appsCall(t, plain, "datatug_run_query", appsArgs())
	if !bounded.IsError || !strings.Contains(bounded.Content[0].(*mcp.TextContent).Text, "MCP output exceeds 2 MiB") {
		t.Fatalf("wrong final-envelope guard: %+v", bounded)
	}
}

func TestAppsPrivateDefaultsAndInvalidPayloads(t *testing.T) {
	r := newAppsRegistry()
	defer r.close()
	ctx := context.Background()
	q := registryQuery()
	if _, _, err := privateEnvelope(appsPayload{Result: json.RawMessage("bad")}); err != errAppsUnavailable {
		t.Fatal("invalid payload accepted")
	}
	for _, test := range []struct {
		q   *datatug.QueryDefWithFolderPath
		out any
	}{
		{q, func() {}},
		{&datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{Type: "HTTP"}}, registryResult(1)},
		{q, map[string]any{"bindingsApplied": "bad"}},
	} {
		if _, err := r.retain(ctx, runInput{QueryID: "q"}, "fp", test.q, test.out, "", nil); err != errAppsUnavailable {
			t.Fatalf("invalid retention accepted: %v", err)
		}
	}
	q.Parameters = datatug.Parameters{{ID: "exact", Type: "decimal"}, {ID: "enabled", Type: "boolean"}, {ID: "count", Type: "number"}, {ID: "set", Type: "string"}}
	raw := json.RawMessage(`{"recordset":{"columns":[],"rows":[]},"bindingsApplied":[{"parameterId":"exact","value":{"type":"decimal","value":"9007199254740992.123456789"}},{"parameterId":"enabled","value":{"type":"boolean","value":true}},{"parameterId":"count","value":{"type":"number","value":1.5}},{"parameterId":"unknown","value":{"type":"string","value":"ignored"}},{"parameterId":"set","value":{"type":"set","values":[{"type":"string","value":"ignored"}]}}]}`)
	result, err := r.retain(ctx, runInput{QueryID: "q"}, "fp", q, raw, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := appsPrivate(t, result)
	if payload.Values["exact"] != "9007199254740992.123456789" || payload.Values["enabled"] != "true" || payload.Values["count"] != "1.5" || len(payload.Values) != 3 {
		t.Fatalf("applied defaults changed: %+v", payload.Values)
	}
	// All slots active: no active query may be evicted to admit a fresh result.
	for len(r.entries) < appsMaxResults {
		retainTest(t, r, 1)
	}
	for _, e := range r.entries {
		e.busy = true
	}
	if _, err := r.retain(ctx, runInput{QueryID: "q"}, "fp", registryQuery(), registryResult(1), "", nil); err != errAppsUnavailable {
		t.Fatal("evicted busy result")
	}
	// Cancellation can occur while eviction cancels an older in-flight operation.
	for _, e := range r.entries {
		e.busy = false
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, e := range r.entries {
		e.cancel = cancel
	}
	if _, err := r.retain(canceled, runInput{QueryID: "q"}, "fp", registryQuery(), registryResult(1), "", nil); err != errAppsUnavailable {
		t.Fatal("published after eviction cancellation")
	}
}

func testAppsInput() runInput {
	return runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "customers/purchases", Parameters: map[string]string{"CustomerId": "1"}}
}

func TestAppsInitialFailureStages(t *testing.T) {
	for _, stage := range []string{"prepare", "execute", "revalidate"} {
		t.Run(stage, func(t *testing.T) {
			client, _ := newFixture(t)
			transport := client.http.Transport
			queries := 0
			client.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/datatug/queries/get_query" {
					queries++
					if stage == "prepare" || (stage == "revalidate" && queries == 2) {
						return nil, errors.New("private failure")
					}
				}
				if stage == "execute" && req.URL.Path == "/datatug/exec/run_query" {
					return nil, errors.New("private failure")
				}
				return transport.RoundTrip(req)
			})
			r := newAppsRegistry()
			defer r.close()
			if _, err := client.appsInitial(context.Background(), r, testAppsInput()); err != errAppsUnavailable || len(r.entries) != 0 {
				t.Fatalf("failure stage published result: %v", err)
			}
		})
	}
}

func TestAppsContinuationRevocationWindows(t *testing.T) {
	for _, stage := range []string{"read-cancel", "rerun-revoke", "rerun-definition"} {
		t.Run(stage, func(t *testing.T) {
			client, _ := newFixture(t)
			r := newAppsRegistry()
			defer r.close()
			initial, err := client.appsInitial(context.Background(), r, testAppsInput())
			if err != nil {
				t.Fatal(err)
			}
			handle := appsPrivate(t, initial).Handle
			transport := client.http.Transport
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			queries := 0
			if stage == "read-cancel" {
				clockCalls := 0
				r.now = func() time.Time {
					clockCalls++
					if clockCalls == 2 {
						cancel()
					}
					return time.Now()
				}
			}
			client.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(req)
				if err != nil {
					return nil, err
				}
				if req.URL.Path == "/datatug/queries/get_query" {
					queries++
					if stage == "rerun-revoke" && queries == 1 {
						r.revoke(handle)
					}

					if stage == "rerun-definition" && queries == 2 {
						raw, _ := io.ReadAll(response.Body)
						_ = response.Body.Close()
						response.Body = io.NopCloser(strings.NewReader(strings.Replace(string(raw), "SELECT Total", "SELECT Total+1", 1)))
					}
				}
				return response, nil
			})
			rerun := stage != "read-cancel"
			if _, err := client.appsContinue(ctx, r, handle, map[string]string{"CustomerId": "2"}, rerun); err != errAppsUnavailable {
				t.Fatalf("revocation window published result: %v", err)
			}
		})
	}
}

func TestAppsRunParentCancellationRemainsError(t *testing.T) {
	client, _ := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input, writer := io.Pipe()
	defer func() { _ = input.Close(); _ = writer.Close() }()
	var output bytes.Buffer
	err := Run(ctx, client, "test", input, nopWriteCloser{&output})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation lost: %v", err)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
