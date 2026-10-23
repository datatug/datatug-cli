package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectAppsClient(t *testing.T, server *mcp.Server, apps bool, version string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	caps := &mcp.ClientCapabilities{}
	if apps {
		caps.Extensions = map[string]any{appsExtension: map[string]any{"mimeTypes": []string{appsMIME}}}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "apps-test"}, &mcp.ClientOptions{Capabilities: caps}).Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestAppsSDKSessionProjectionAndPrivateEnvelope(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) { testAppsSDKProjection(t, version) })
	}
}

func testAppsSDKProjection(t *testing.T, version string) {
	client, _ := newFixture(t)
	const secret = "PRIVATE_CELL_6d5c40"
	transport := client.http.Transport
	client.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/datatug/exec/run_query" {
			body := `{"recordset":{"columns":[{"name":"secret","type":"string"}],"rows":[[{"type":"string","value":"` + secret + `"}]]},"limitations":[],"bindingsApplied":[],"provenance":{"source":"chinook","queryId":"customers/purchases","mode":"live","observedAt":"2026-10-23T00:00:00Z","executionProfile":"opaque-privileged"}}`
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		return transport.RoundTrip(r)
	})
	server := New(client, "test")
	plain := connectAppsClient(t, server, false, version)
	app := connectAppsClient(t, server, true, version)
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, test := range []struct {
		cs      *mcp.ClientSession
		private bool
	}{{plain, false}, {app, true}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				tools, err := test.cs.ListTools(ctx, nil)
				if err != nil {
					t.Error(err)
					return
				}
				for _, tool := range tools.Tools {
					if tool.Name == "datatug_run_query" {
						if (tool.Meta["ui"] != nil) != test.private {
							t.Errorf("cross-session tool metadata: %+v", tool)
						}
					}
				}
				// Official client preserves explicitly supplied per-request protocol meta.
				// Spoof the opposite capability after connection negotiation.
				caps := map[string]any{"extensions": map[string]any{}}
				if !test.private {
					caps["extensions"] = map[string]any{appsExtension: map[string]any{"mimeTypes": []string{appsMIME}}}
				}
				result, err := test.cs.CallTool(ctx, &mcp.CallToolParams{Name: "datatug_run_query", Meta: mcp.Meta{mcp.MetaKeyProtocolVersion: "2026-07-28", mcp.MetaKeyClientCapabilities: caps}, Arguments: map[string]any{"project": "demo", "environment": "local", "source": "chinook", "queryId": "customers/purchases", "parameters": map[string]string{"CustomerId": "1"}}})
				if err != nil || result.IsError {
					t.Errorf("query: %+v %v", result, err)
					return
				}
				visible, _ := json.Marshal(struct {
					Content    []mcp.Content
					Structured any
				}{result.Content, result.StructuredContent})
				private, _ := json.Marshal(result.Meta)
				if test.private {
					if strings.Contains(string(visible), secret) || !strings.Contains(string(private), secret) {
						t.Errorf("private envelope visible=%s meta=%s", visible, private)
					}
				} else if !strings.Contains(string(visible), secret) || result.Meta["datatug"] != nil {
					t.Errorf("legacy result changed: %s", visible)
				}
			}
		}()
	}
	wg.Wait()
	resources, err := app.ReadResource(ctx, &mcp.ReadResourceParams{URI: appsResourceURI})
	if err != nil || len(resources.Contents) != 1 || resources.Contents[0].MIMEType != appsMIME {
		t.Fatalf("resource: %v %v", resources, err)
	}
	if strings.Contains(resources.Contents[0].Text, secret) {
		t.Fatal("private data embedded in resource")
	}
	if _, err := plain.ReadResource(ctx, &mcp.ReadResourceParams{URI: appsResourceURI}); err == nil {
		t.Fatal("plain client read UI resource")
	}
	list, err := plain.ListResources(ctx, nil)
	if err != nil || len(list.Resources) != 0 {
		t.Fatalf("plain UI discovery: %v %v", list, err)
	}
}

func TestAppsCapabilityRequiresExactMIME(t *testing.T) {
	for _, settings := range []any{nil, "bad", map[string]any{"mimeTypes": []string{"text/html"}}, map[string]any{"mimeTypes": true}} {
		if supportsApps(&mcp.InitializeParams{Capabilities: &mcp.ClientCapabilities{Extensions: map[string]any{appsExtension: settings}}}) {
			t.Errorf("accepted malformed capability %v", settings)
		}
	}
}
