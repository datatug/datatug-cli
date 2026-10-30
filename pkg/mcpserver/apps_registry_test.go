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

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func appsArgs() map[string]any {
	return map[string]any{"project": "demo", "environment": "local", "source": "chinook", "queryId": "customers/purchases", "parameters": map[string]string{"CustomerId": "1"}}
}
func appsCall(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func appsPrivate(t *testing.T, result *mcp.CallToolResult) appsPayload {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected result error: %+v", result)
	}
	raw, _ := json.Marshal(result.Meta["datatug"])
	var payload appsPayload
	if json.Unmarshal(raw, &payload) != nil || len(payload.Handle) != 64 || payload.Version != 1 {
		t.Fatalf("invalid payload: %s", raw)
	}
	return payload
}
func requireAppsUnavailable(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	raw, _ := json.Marshal(result)
	if !result.IsError || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != errAppsUnavailable.Error() || result.Meta["datatug"] != nil {
		t.Fatalf("unsafe/unexpected error: %s", raw)
	}
}
func TestAppsContinuationAuthorizationAndValidation(t *testing.T) {
	client, _ := newFixture(t)
	var mu sync.Mutex
	mutation := ""
	transport := client.http.Transport
	client.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		current := mutation
		mu.Unlock()
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return nil, err
		}
		text := string(raw)
		if req.URL.Path == "/datatug/agent-info" {
			if current == "principal" {
				text = strings.Replace(text, `"id":"admin"`, `"id":"other"`, 1)
			}
			if current == "context" {
				text = strings.Replace(text, `"fresh"`, `"revoked"`, 1)
			}
		}
		if req.URL.Path == "/datatug/queries/get_query" && current == "parameter" {
			text = strings.Replace(text, `"type":"integer"`, `"type":"decimal"`, 1)
		}
		if req.URL.Path == "/datatug/queries/get_query" && current == "query" {
			text = strings.Replace(text, "SELECT Total", "SELECT Total + 1", 1)
		}
		response.Body = io.NopCloser(strings.NewReader(text))
		return response, nil
	})
	server := New(client, "test")
	app := connectAppsClient(t, server, true, "2026-07-28")
	other := connectAppsClient(t, server, true, "2026-07-28")
	plain := connectAppsClient(t, server, false, "2026-07-28")
	initial := appsPrivate(t, appsCall(t, app, "datatug_run_query", appsArgs()))
	if initial.Values["CustomerId"] != "1" || len(initial.Parameters) != 1 {
		t.Fatalf("private input missing: %+v", initial)
	}
	read := appsPrivate(t, appsCall(t, app, "datatug_apps_result", map[string]any{"handle": initial.Handle}))
	if string(read.Result) != string(initial.Result) {
		t.Fatal("retained result changed")
	}
	rerun := appsPrivate(t, appsCall(t, app, "datatug_apps_rerun", map[string]any{"handle": initial.Handle, "parameters": map[string]string{"CustomerId": "2"}}))
	if rerun.Handle == initial.Handle || rerun.Values["CustomerId"] != "2" {
		t.Fatal("rerun did not create fresh snapshot")
	}
	requireAppsUnavailable(t, appsCall(t, other, "datatug_apps_result", map[string]any{"handle": initial.Handle}))
	requireAppsUnavailable(t, appsCall(t, app, "datatug_apps_result", map[string]any{"handle": "guessed"}))
	requireAppsUnavailable(t, appsCall(t, plain, "datatug_apps_result", map[string]any{"handle": initial.Handle}))
	const sentinel = "UI_ONLY_SECRET_8024"
	for _, test := range []struct {
		name string
		args any
	}{
		{"datatug_apps_rerun", map[string]any{"handle": initial.Handle, "parameters": sentinel}},
		{"datatug_apps_rerun", map[string]any{"handle": initial.Handle, "parameters": map[string]string{"CustomerId": sentinel}}},
		{"datatug_apps_rerun", map[string]any{"handle": initial.Handle, "parameters": map[string]string{sentinel: sentinel}}},
		{"datatug_apps_rerun", map[string]any{"handle": initial.Handle, "parameters": map[string]string{"CustomerId": sentinel}, sentinel: sentinel}},
		{"datatug_apps_rerun", map[string]any{"handle": initial.Handle, "parameters": map[string]string{"CustomerId": sentinel}, "queryId": sentinel}},
		{"datatug_run_query", map[string]any{"project": "demo", "parameters": sentinel}},
	} {
		result := appsCall(t, app, test.name, test.args)
		requireAppsUnavailable(t, result)
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), sentinel) {
			t.Fatal("private validation leaked input")
		}
	}
	for _, kind := range []string{"principal", "context", "query", "parameter"} {
		mu.Lock()
		mutation = ""
		mu.Unlock()
		payload := appsPrivate(t, appsCall(t, app, "datatug_run_query", appsArgs()))
		mu.Lock()
		mutation = kind
		mu.Unlock()
		requireAppsUnavailable(t, appsCall(t, app, "datatug_apps_result", map[string]any{"handle": payload.Handle}))
		mu.Lock()
		mutation = ""
		mu.Unlock()
		requireAppsUnavailable(t, appsCall(t, app, "datatug_apps_result", map[string]any{"handle": payload.Handle}))
	}
}

func registryResult(size int) any {
	return map[string]any{"recordset": map[string]any{"columns": []any{map[string]any{"name": "x", "type": "string"}}, "rows": []any{[]any{map[string]any{"type": "string", "value": strings.Repeat("x", size)}}}}, "truncated": false}
}
func registryQuery() *datatug.QueryDefWithFolderPath {
	return &datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{Type: datatug.QueryTypeSQL}}
}
func retainTest(t *testing.T, r *appsRegistry, size int) appsPayload {
	t.Helper()
	result, err := r.retain(context.Background(), runInput{QueryID: "q"}, "fingerprint", registryQuery(), registryResult(size), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return appsPrivate(t, result)
}
func TestAppsRegistryClockBoundsAndRevocation(t *testing.T) {
	r := newAppsRegistry()
	defer r.close()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	first := retainTest(t, r, 10)
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		retainTest(t, r, 10)
	}
	if _, err := r.acquire(first.Handle, false); err == nil || len(r.entries) != appsMaxResults {
		t.Fatal("oldest count bound failed")
	}
	latest := retainTest(t, r, 10)
	now = now.Add(appsIdleTTL)
	if _, err := r.acquire(latest.Handle, false); err == nil || r.bytes != 0 {
		t.Fatal("idle expiry failed")
	}
	absolute := retainTest(t, r, 10)
	for i := 0; i < 3; i++ {
		now = now.Add(9 * time.Minute)
		e, err := r.acquire(absolute.Handle, false)
		if err != nil {
			t.Fatal(err)
		}
		r.release(absolute.Handle, e, true)
	}
	now = now.Add(3 * time.Minute)
	if _, err := r.acquire(absolute.Handle, false); err == nil {
		t.Fatal("absolute expiry failed")
	}
	if _, err := r.retain(context.Background(), runInput{QueryID: "q"}, "fingerprint", registryQuery(), registryResult(maxBody), "", nil); err == nil {
		t.Fatal("oversize envelope retained")
	}
	for i := 0; i < 10; i++ {
		retainTest(t, r, maxBody-2000)
	}
	if r.bytes > appsMaxRetained || len(r.entries) >= 10 {
		t.Fatalf("aggregate bytes not bounded: %d %d", r.bytes, len(r.entries))
	}
	active := retainTest(t, r, 10)
	e, err := r.acquire(active.Handle, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.acquire(active.Handle, true); err == nil {
		t.Fatal("concurrent rerun accepted")
	}
	r.revoke(active.Handle)
	if _, err = r.retain(context.Background(), e.input, e.fingerprint, registryQuery(), registryResult(10), active.Handle, e); err == nil {
		t.Fatal("revoked late completion revived result")
	}
	ctx, done := r.operation(context.Background())
	defer done()
	r.close()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel operation")
	}
	if _, err = r.retain(context.Background(), runInput{}, "fingerprint", registryQuery(), registryResult(1), "", nil); err == nil {
		t.Fatal("closed registry retained result")
	}
}

func TestAppsBlockedRerunCancellation(t *testing.T) {
	client, _ := newFixture(t)
	r := newAppsRegistry()
	defer r.close()
	initial, err := client.appsInitial(context.Background(), r, runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "customers/purchases", Parameters: map[string]string{"CustomerId": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	payload := appsPrivate(t, initial)
	transport := client.http.Transport
	started := make(chan struct{})
	cancelled := make(chan struct{})
	client.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/datatug/exec/run_query" {
			close(started)
			<-req.Context().Done()
			close(cancelled)
			return nil, req.Context().Err()
		}
		return transport.RoundTrip(req)
	})
	done := make(chan error, 1)
	go func() {
		_, err := client.appsContinue(context.Background(), r, payload.Handle, map[string]string{"CustomerId": "2"}, true)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("rerun did not start")
	}
	if _, err := client.appsContinue(context.Background(), r, payload.Handle, map[string]string{"CustomerId": "3"}, true); err != errAppsUnavailable {
		t.Fatalf("duplicate rerun: %v", err)
	}
	r.close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("close left HTTP work alive")
	}
	select {
	case err := <-done:
		if err != errAppsUnavailable {
			t.Fatalf("canceled rerun: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close left rerun alive")
	}
	if len(r.entries) != 0 || r.bytes != 0 {
		t.Fatal("close retained results")
	}
}

func TestAppsStdinEOFCancelsInFlightRerun(t *testing.T) {
	client, _ := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, client, "test", serverRead, serverWrite) }()
	caps := &mcp.ClientCapabilities{Extensions: map[string]any{appsExtension: map[string]any{"mimeTypes": []string{appsMIME}}}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "eof-test"}, &mcp.ClientOptions{Capabilities: caps}).Connect(ctx, &mcp.IOTransport{Reader: clientRead, Writer: clientWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	payload := appsPrivate(t, appsCall(t, cs, "datatug_run_query", appsArgs()))
	transport := client.http.Transport
	started := make(chan struct{})
	cancelled := make(chan struct{})
	client.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/datatug/exec/run_query" {
			close(started)
			<-req.Context().Done()
			close(cancelled)
			return nil, req.Context().Err()
		}
		return transport.RoundTrip(req)
	})
	callDone := make(chan struct{})
	go func() {
		_, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "datatug_apps_rerun", Arguments: map[string]any{"handle": payload.Handle, "parameters": map[string]string{"CustomerId": "2"}}})
		close(callDone)
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("rerun did not start")
	}
	_ = clientWrite.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("stdin EOF did not cancel owned HTTP work")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run EOF: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run leaked on EOF")
	}
	select {
	case <-callDone:
	case <-time.After(time.Second):
		t.Fatal("client call leaked on EOF")
	}
}

func TestAppsRequestCancellationAtRegistryCommit(t *testing.T) {
	for _, rerun := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "rerun"}[rerun], func(t *testing.T) {
			client, _ := newFixture(t)
			r := newAppsRegistry()
			defer r.close()
			input := runInput{Project: "demo", Environment: "local", Source: "chinook", QueryID: "customers/purchases", Parameters: map[string]string{"CustomerId": "1"}}
			handle := ""
			if rerun {
				result, err := client.appsInitial(context.Background(), r, input)
				if err != nil {
					t.Fatal(err)
				}
				handle = appsPrivate(t, result).Handle
			}
			// The injected clock is reached under the registry lock after execution,
			// reauthorization and payload serialization, immediately before commit.
			reached := make(chan struct{})
			resume := make(chan struct{})
			calls := 0
			r.now = func() time.Time {
				calls++
				if (!rerun && calls == 1) || (rerun && calls == 2) {
					close(reached)
					<-resume
				}
				return time.Unix(1000, 0)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if rerun {
					_, err = client.appsContinue(ctx, r, handle, map[string]string{"CustomerId": "2"}, true)
				} else {
					_, err = client.appsInitial(ctx, r, input)
				}
				done <- err
			}()
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatal("did not reach registry commit")
			}
			cancel()
			close(resume)
			select {
			case err := <-done:
				if err != errAppsUnavailable {
					t.Fatalf("canceled commit result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled commit leaked")
			}
			want := 0
			if rerun {
				want = 1
			}
			if len(r.entries) != want {
				t.Fatalf("canceled request published fresh handle: %d", len(r.entries))
			}
		})
	}
}
