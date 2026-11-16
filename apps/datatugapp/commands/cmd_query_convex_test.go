package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const convexFixtureToken = "owned-convex-user-token"
const convexFixtureDoc = `{"_id":"$ID","_creationTime":1.5,"wide":{"$integer":"/////////38="},"binary":{"$bytes":"AP+A"},"negativeZero":{"$float":"AAAAAAAAAIA="},"infinity":{"$float":"AAAAAAAA8H8="},"nan":{"$float":"AQAAAAAA+H8="},"constructor":"own","prototype":true,"__proto__":{"constructor":"nested"},"":null,"_data":"wire","nested":[true,null,"<native>&","😀"]}`

func convexTestMapping(server *httptest.Server) convexNativeConfig {
	return convexNativeConfig{Endpoint: server.URL, TokenEnv: "DATATUG_CONVEX_READ_FIXTURE", Collections: map[string]convexCollectionConfig{"tasks": {GetFunction: "dalgo:getTask", StaticArgs: json.RawMessage(`{"tenant":"owned"}`), Fields: []string{"_id", "wide", "binary"}}}, Queries: map[string]convexQueryConfig{"list": {Function: "dalgo:listTasks", Collection: "tasks", ArgumentNames: []string{"filter"}, StaticArgs: json.RawMessage(`{"tenant":"owned"}`)}}}
}
func convexTestConfig(t *testing.T, server *httptest.Server) string {
	t.Helper()
	c := convexTestMapping(server)
	c.CAFile = dynamoTestFile(t, "convex-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	return dynamoTestJSON(t, "convex-config.json", c)
}
func convexTestCommand(t *testing.T, ctx context.Context, config, request, timeout string) (string, string, error) {
	t.Helper()
	cmd := queryCommand()
	var out, diag bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diag)
	cmd.SetIn(strings.NewReader(request))
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"convex", "--config", config, "--request", "-", "--timeout", timeout})
	err := cmd.Execute()
	return out.String(), diag.String(), err
}
func convexTestArgs(t *testing.T, w http.ResponseWriter, r *http.Request) (string, map[string]json.RawMessage, bool) {
	t.Helper()
	if r.Method != "POST" || r.URL.Path != "/api/query" || r.URL.RawQuery != "" || r.TLS == nil || r.Header.Get("Authorization") != "Bearer "+convexFixtureToken || r.Header.Get("Cookie") != "" {
		t.Error("trusted public-query HTTP scope changed")
		w.WriteHeader(400)
		return "", nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	if err != nil || len(body) > 65536 {
		t.Error("bounded request failed")
		return "", nil, false
	}
	var p struct {
		Path   string            `json:"path"`
		Format string            `json:"format"`
		Args   []json.RawMessage `json:"args"`
	}
	if decodeDynamoDocument(body, &p) != nil || p.Format != "convex_encoded_json" || len(p.Args) != 1 {
		t.Error("native encoded query envelope changed")
		return "", nil, false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(p.Args[0], &args) != nil || string(args["tenant"]) != `"owned"` {
		t.Error("fixed scope changed")
		return "", nil, false
	}
	return p.Path, args, true
}
func convexTestResponse(t *testing.T, w http.ResponseWriter, value string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, `{"status":"success","value":`+value+`}`); err != nil {
		t.Error("owned buffered response failed")
	}
}
func convexTestRow(id string) string { return strings.ReplaceAll(convexFixtureDoc, "$ID", id) }

func TestQueryConvexOwnedTLSNativeReadsAndOpaquePages(t *testing.T) {
	t.Setenv("DATATUG_CONVEX_READ_FIXTURE", convexFixtureToken)
	var calls, pages atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, args, ok := convexTestArgs(t, w, r)
		if !ok {
			return
		}
		calls.Add(1)
		if path == "dalgo:getTask" {
			var id string
			if json.Unmarshal(args["id"], &id) != nil {
				t.Error("native ID changed")
				return
			}
			if id == "missing" {
				convexTestResponse(t, w, "null")
			} else {
				convexTestResponse(t, w, convexTestRow(id))
			}
			return
		}
		if path != "dalgo:listTasks" {
			t.Error("unconfigured function called")
			return
		}
		if string(args["filter"]) != `{"$integer":"/////////38="}` {
			t.Error("native int64 argument changed")
		}
		var pagination struct {
			Size   int     `json:"numItems"`
			Cursor *string `json:"cursor"`
			Rows   int     `json:"maximumRowsRead"`
			Bytes  int     `json:"maximumBytesRead"`
		}
		if json.Unmarshal(args["paginationOpts"], &pagination) != nil || pagination.Size != 2 || pagination.Rows != 2 || pagination.Bytes < 1 {
			t.Error("server work budgets lost")
		}
		switch pages.Add(1) {
		case 1:
			if pagination.Cursor != nil {
				t.Error("first cursor not null")
			}
			convexTestResponse(t, w, `{"page":[],"isDone":false,"continueCursor":"private-page-one"}`)
		case 2:
			if pagination.Cursor == nil || *pagination.Cursor != "private-page-one" {
				t.Error("server position lost")
			}
			convexTestResponse(t, w, `{"page":[`+convexTestRow("a")+`,`+convexTestRow("b")+`],"isDone":false,"continueCursor":"private-page-two"}`)
		default:
			t.Error("extra query retry/page")
		}
	}))
	t.Cleanup(server.Close)
	config := convexTestConfig(t, server)
	for _, request := range []string{`{"operation":"get","collection":"tasks","id":"a"}`, `{"operation":"get","collection":"tasks","id":"missing"}`, `{"operation":"exists","collection":"tasks","id":"a"}`, `{"operation":"testConnection","collection":"tasks","id":"missing"}`, `{"operation":"getMany","collection":"tasks","ids":["b","missing","a","a"]}`, `{"operation":"getMany","collection":"tasks","ids":[]}`, `{"operation":"query","query":"list","args":{"filter":{"$integer":"/////////38="}},"pageSize":2,"pages":2}`} {
		out, diag, err := convexTestCommand(t, context.Background(), config, request, "2s")
		if err != nil || !strings.Contains(out, `"format":"convex_encoded_json"`) || !strings.Contains(diag, "DataTug application policies are not applied") {
			t.Fatal("native command failed", err)
		}
		for _, private := range []string{convexFixtureToken, server.URL, "private-page-one", "private-page-two"} {
			if strings.Contains(out+diag, private) {
				t.Fatal("token/endpoint/cursor leaked")
			}
		}
		if strings.Contains(request, `"operation":"query"`) {
			if !strings.Contains(out, `"hasMore":true`) || !strings.Contains(out, `"pages":2`) {
				t.Fatal("whole-page hasMore contract lost")
			}
		}
		if strings.Contains(request, `"operation":"get"`) && strings.Contains(request, `"id":"a"`) {
			for _, native := range []string{`"$integer":"/////////38="`, `"$bytes":"AP+A"`, `"$float":"AAAAAAAAAIA="`, `"$float":"AAAAAAAA8H8="`, `"$float":"AQAAAAAA+H8="`, `"__proto__"`, `"constructor":"own"`, `"prototype":true`, `"":null`, `"_data":"wire"`, `\u003cnative\u003e\u0026`} {
				if !strings.Contains(out, native) {
					t.Fatal("native value/field changed")
				}
			}
		}
		if strings.Contains(request, `"ids":["b"`) {
			var result struct {
				Documents []*struct {
					ID string `json:"id"`
				} `json:"documents"`
			}
			if json.Unmarshal([]byte(out), &result) != nil || len(result.Documents) != 4 || result.Documents[0].ID != "b" || result.Documents[1] != nil || result.Documents[2].ID != "a" || result.Documents[3].ID != "a" {
				t.Fatal("batch duplicates/order/missing changed")
			}
		}
	}
	if calls.Load() != 10 || pages.Load() != 2 {
		t.Fatal("unexpected HTTP work count")
	}
	// File-input registration path uses the same native scalar contract.
	request := dynamoTestFile(t, "convex-get.json", []byte(`{"operation":"get","collection":"tasks","id":"b"}`))
	cmd := queryConvexCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", config, "--request", request})
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), `"id":"b"`) {
		t.Fatal("native file command failed", err)
	}
}

func TestConvexSignalContextCleanupAndReuse(t *testing.T) {
	previous := interruptContext
	defer func() { interruptContext = previous }()
	parent := context.Background()
	stopped := false
	var child context.Context
	interruptContext = func(given context.Context) (context.Context, func() os.Signal, func()) {
		if given != parent {
			t.Error("caller context changed")
		}
		var cancel context.CancelFunc
		child, cancel = context.WithCancel(given)
		return child, func() os.Signal { return nil }, func() { stopped = true; cancel() }
	}
	cmd := queryConvexCommand()
	cmd.SetContext(parent)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	for range 2 {
		stopped = false
		if err := cmd.Execute(); err == nil || !stopped || child.Err() == nil || cmd.Context() != parent {
			t.Fatal("signal cleanup/reuse changed")
		}
	}
	bare := queryConvexCommand()
	stopped = false
	if err := bare.RunE(bare, nil); err == nil || !stopped || child.Err() == nil || bare.Context() != nil {
		t.Fatal("nil original context not restored")
	}
}

func TestQueryConvexActualDispatchedCancellationDeadlineAndReopen(t *testing.T) {
	t.Setenv("DATATUG_CONVEX_READ_FIXTURE", convexFixtureToken)
	for _, deadline := range []bool{false, true} {
		name := "caller"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			entered, disconnected, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var slow atomic.Bool
			slow.Store(true)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _, ok := convexTestArgs(t, w, r)
				if !ok {
					return
				}
				if slow.Swap(false) {
					close(entered)
					select {
					case <-r.Context().Done():
						close(disconnected)
					case <-cleanup:
					}
					return
				}
				convexTestResponse(t, w, convexTestRow("a"))
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(cleanup) })
			config := convexTestConfig(t, server)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			timeout := "2s"
			if deadline {
				timeout = "500ms"
			}
			type result struct {
				out, diag string
				err       error
			}
			done := make(chan result, 1)
			go func() {
				out, diag, err := convexTestCommand(t, ctx, config, `{"operation":"get","collection":"tasks","id":"a"}`, timeout)
				done <- result{out, diag, err}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("read never dispatched")
			}
			start := time.Now()
			if !deadline {
				cancel(errors.New("PRIVATE_CALLER_CAUSE"))
			}
			select {
			case got := <-done:
				label := "cancelled"
				if deadline {
					label = "deadline"
				}
				if got.err == nil || got.out != "" || !strings.Contains(got.err.Error(), label) || strings.Contains(got.err.Error()+got.diag, "PRIVATE") || strings.Contains(got.diag, convexFixtureToken) {
					t.Fatal("cancel classification/output/redaction changed")
				}
			case <-time.After(750 * time.Millisecond):
				t.Fatal("dispatched read cancellation delayed")
			}
			if time.Since(start) > 750*time.Millisecond {
				t.Fatal("cancel budget exceeded")
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("owned request did not disconnect")
			}
			out, _, err := convexTestCommand(t, context.Background(), config, `{"operation":"get","collection":"tasks","id":"a"}`, "2s")
			if err != nil || out == "" {
				t.Fatal("fresh command after cancel failed", err)
			}
		})
	}
}

func TestConvexUserTokenAcquiredAtOperationTimeAndAnonymousIsExplicit(t *testing.T) {
	c := convexNativeConfig{TokenEnv: "DATATUG_CONVEX_READ_FIXTURE"}
	o, err := convexClientOptions(context.Background(), c, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(c.TokenEnv, "first-user-token")
	first, err := o.Token(context.Background())
	if err != nil || first != "first-user-token" {
		t.Fatal("first token lookup")
	}
	t.Setenv(c.TokenEnv, "rotated-user-token")
	second, err := o.Token(context.Background())
	if err != nil || second != "rotated-user-token" {
		t.Fatal("token cached")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Token(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("token preabort lost")
	}
	if _, err := convexClientOptions(context.Background(), convexNativeConfig{Anonymous: true, TokenEnv: c.TokenEnv}, time.Second); err == nil {
		t.Fatal("anonymous plus credential accepted")
	}
	anonymous, err := convexClientOptions(context.Background(), convexNativeConfig{Anonymous: true}, time.Second)
	if err != nil || anonymous.Token != nil || !anonymous.Anonymous {
		t.Fatal("explicit anonymous config failed")
	}
}
