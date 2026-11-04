package commands

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestConvexStrictNativeInputAndControlCaseRefuseBeforeHTTP(t *testing.T) {
	t.Setenv("DATATUG_CONVEX_READ_FIXTURE", convexFixtureToken)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := convexTestConfig(t, server)
	for _, request := range []string{
		`[]`, `{} {}`, `{"operation":"get","operation":"exists"}`, `{"operation":"get","Operation":"get"}`, `{"operation":"get","token":"private"}`, `{"operation":"delete"}`, `{"operation":"get"}`,
		`{"operation":"get","collection":"unknown","id":"a"}`, `{"operation":"get","collection":"tasks","id":"a","ids":[]}`, `{"operation":"get","collection":"tasks","id":"a","pages":1}`,
		`{"operation":"getMany","collection":"tasks"}`, `{"operation":"getMany","collection":"tasks","ids":[],"id":"a"}`, `{"operation":"getMany","collection":"unknown","ids":[]}`,
		`{"operation":"query","query":"unknown","pageSize":1,"pages":1}`, `{"operation":"query","query":"list","pageSize":0,"pages":1}`, `{"operation":"query","query":"list","pageSize":1001,"pages":1}`, `{"operation":"query","query":"list","pageSize":1,"pages":101}`, `{"operation":"query","query":"list","pageSize":1000,"pages":2}`, `{"operation":"query","query":"list","pageSize":1,"pages":1,"collection":"tasks"}`,
		`{"operation":"query","query":"list","pageSize":1,"pages":1,"args":[]}`, `{"operation":"query","query":"list","pageSize":1,"pages":1,"args":null}`, `{"operation":"query","query":"list","pageSize":1,"pages":1,"args":{"filter":{"$integer":"AA=="}}}`, `{"operation":"query","query":"list","pageSize":1,"pages":1,"cursor":"private"}`,
		`{"operation":"get","collection":"tasks","id":"\ud800"}`, strings.Repeat(" ", 65537),
	} {
		out, diag, err := convexTestCommand(t, context.Background(), config, request, "2s")
		if err == nil || out != "" || strings.Contains(err.Error()+diag, convexFixtureToken) {
			t.Fatal("unsafe/native malformed input admitted")
		}
	}
	// Remaining key/argument semantic checks are owned by the pinned SDK before
	// token/header dispatch; they are not a CLI guess of the remote ID schema.
	for _, request := range []string{`{"operation":"get","collection":"tasks","id":"😀"}`, `{"operation":"getMany","collection":"tasks","ids":["a",""]}`, `{"operation":"query","query":"list","pageSize":1,"pages":1,"args":{"tenant":"override"}}`} {
		out, _, err := convexTestCommand(t, context.Background(), config, request, "2s")
		if err == nil || out != "" {
			t.Fatal("invalid native scope/key admitted")
		}
	}
	for _, raw := range []string{`{"Endpoint":"https://example.test","anonymous":true}`, `{"endpoint":"https://example.test","anonymous":true,"collections":{"tasks":{"GetFunction":"dalgo:getTask"}}}`, `{"endpoint":"https://example.test","anonymous":true,"queries":{"list":{"Function":"dalgo:listTasks"}}}`, `{"collections":{"tasks":null}}`, `{"queries":{"list":null}}`} {
		path := dynamoTestFile(t, "bad-control.json", []byte(raw))
		out, _, err := convexTestCommand(t, context.Background(), path, `{"operation":"get","collection":"tasks","id":"a"}`, "2s")
		if err == nil || out != "" {
			t.Fatal("nonexact control fields admitted")
		}
	}
	for _, mutate := range []func(*convexNativeConfig){
		func(c *convexNativeConfig) { c.TokenEnv = "literal token" }, func(c *convexNativeConfig) { c.Anonymous = true }, func(c *convexNativeConfig) { c.Endpoint = "http://127.0.0.1" }, func(c *convexNativeConfig) { c.Endpoint = "https://user:private@example.test" }, func(c *convexNativeConfig) { c.Endpoint = "https://example.test?private" }, func(c *convexNativeConfig) { c.CAFile = "/missing-ca" }, func(c *convexNativeConfig) {
			x := c.Collections["tasks"]
			x.GetFunction = "api/mutation"
			c.Collections["tasks"] = x
		}, func(c *convexNativeConfig) {
			x := c.Collections["tasks"]
			x.StaticArgs = []byte(`[]`)
			c.Collections["tasks"] = x
		}, func(c *convexNativeConfig) {
			x := c.Queries["list"]
			x.StaticArgs = []byte(`{"x":{"$bytes":"bad"}}`)
			c.Queries["list"] = x
		},
	} {
		mapping := convexTestMapping(server)
		mutate(&mapping)
		out, _, err := convexTestCommand(t, context.Background(), dynamoTestJSON(t, "bad-mapping.json", mapping), `{"operation":"get","collection":"tasks","id":"a"}`, "2s")
		if err == nil || out != "" {
			t.Fatal("unsafe mapping admitted")
		}
	}
	for _, args := range [][]string{{}, {"--config", "-", "--request", "-"}, {"--config", config, "--request", "-", "--timeout", "0s"}, {"--config", config, "--request", "-", "--timeout", "3m"}, {"--config", "/missing-config", "--request", "-"}, {"--config", config, "--request", "/missing-request"}} {
		cmd := queryConvexCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatal("unsafe flags/files admitted")
		}
	}
	oversized := convexNativeRequest{Operation: "getMany", Collection: "tasks", IDs: make([]string, 101)}
	if _, err := admitConvexRead(context.Background(), convexTestMapping(server), oversized); err == nil {
		t.Fatal("key bound omitted")
	}
	if calls.Load() != 0 {
		t.Fatal("local refusal performed HTTP")
	}
}

func TestConvexTLSRemoteErrorsAndNoSecretNoPrefixNoRedirect(t *testing.T) {
	t.Setenv("DATATUG_CONVEX_READ_FIXTURE", convexFixtureToken)
	var redirects atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); w.WriteHeader(500) }))
	t.Cleanup(target.Close)
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		redirect bool
	}{{"denied", 403, "PRIVATE_REMOTE_CAUSE", false}, {"error-status", 200, `{"status":"error","errorMessage":"PRIVATE_REMOTE_CAUSE","errorData":{"token":"PRIVATE_REMOTE_CAUSE"}}`, false}, {"malformed", 200, `{"status":`, false}, {"foreign-id", 200, `{"status":"success","value":{"_id":"foreign"}}`, false}, {"oversize", 200, strings.Repeat("x", (1<<20)+1), false}, {"redirect", 307, "PRIVATE_REMOTE_CAUSE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			completed := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(completed)
				if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 65537)); err != nil {
					t.Error("owned request drain failed")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.redirect {
					w.Header().Set("Location", target.URL)
				}
				w.WriteHeader(tc.status)
				if _, err := io.WriteString(w, tc.body); err != nil {
					if tc.name != "oversize" || (!errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET)) {
						t.Error("unexpected owned response write failure")
					} else {
						select {
						case <-r.Context().Done():
						case <-time.After(time.Second):
							t.Error("oversize peer close lacked cancellation acknowledgement")
						}
					}
				}
			}))
			t.Cleanup(server.Close)
			out, diag, err := convexTestCommand(t, context.Background(), convexTestConfig(t, server), `{"operation":"get","collection":"tasks","id":"a"}`, "2s")
			if err == nil || out != "" || strings.Contains(err.Error()+diag, "PRIVATE") || strings.Contains(diag, convexFixtureToken) || strings.Contains(diag, server.URL) {
				t.Fatal("remote failure leaked output/secret/endpoint")
			}
			select {
			case <-completed:
			case <-time.After(2 * time.Second):
				t.Fatal("owned negative handler did not complete")
			}
		})
	}
	if redirects.Load() != 0 {
		t.Fatal("query redirect followed")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := convexTestConfig(t, server)
	for _, credential := range []string{"", "Bearer private", "bad\nprivate"} {
		t.Setenv("DATATUG_CONVEX_READ_FIXTURE", credential)
		out, _, err := convexTestCommand(t, context.Background(), config, `{"operation":"get","collection":"tasks","id":"a"}`, "2s")
		if err == nil || out != "" {
			t.Fatal("invalid token fallback")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, _, err := convexTestCommand(t, ctx, config, `{"operation":"get","collection":"tasks","id":"a"}`, "2s"); err == nil || out != "" || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal("caller preabort lost")
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if out, _, err := convexTestCommand(t, expired, config, `{"operation":"get","collection":"tasks","id":"a"}`, "2s"); err == nil || out != "" || !strings.Contains(err.Error(), "deadline") {
		t.Fatal("expired deadline lost")
	}
	t.Setenv("DATATUG_CONVEX_READ_FIXTURE", convexFixtureToken)
	untrusted := dynamoTestJSON(t, "untrusted.json", convexTestMapping(server))
	if out, _, err := convexTestCommand(t, context.Background(), untrusted, `{"operation":"get","collection":"tasks","id":"a"}`, "2s"); err == nil || out != "" {
		t.Fatal("untrusted TLS accepted")
	}
	if redirects.Load() != 0 {
		t.Fatal("invalid credentials/preabort/untrustedCA dispatched HTTP")
	}
}

func TestConvexExplicitAnonymousQueryAndTerminalNativePage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("anonymous mode sent credentials")
		}
		if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 65537)); err != nil {
			t.Error("owned body drain failed")
		}
		convexTestResponse(t, w, `{"page":[],"isDone":true,"continueCursor":""}`)
	}))
	t.Cleanup(server.Close)
	mapping := convexTestMapping(server)
	mapping.TokenEnv = ""
	mapping.Anonymous = true
	mapping.CAFile = dynamoTestFile(t, "anon-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	config := dynamoTestJSON(t, "anon-config.json", mapping)
	out, _, err := convexTestCommand(t, context.Background(), config, `{"operation":"query","query":"list","pageSize":1,"pages":3}`, "2s")
	if err != nil || !strings.Contains(out, `"documents":[]`) || !strings.Contains(out, `"pages":1`) || !strings.Contains(out, `"hasMore":false`) {
		t.Fatal("explicit anonymous/terminal page changed", err)
	}
}

func TestConvexMalformedTrustedConfigurationIsRejectedWithoutHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	for _, raw := range []string{`{`, `[]`, `{"collections":[]}`, `{"queries":1}`, `{"collections":{"tasks":[]}}`, `{"queries":{"list":"not-a-control-record"}}`} {
		config := dynamoTestFile(t, "malformed-convex-config.json", []byte(raw))
		output, diagnostic, err := convexTestCommand(t, context.Background(), config, `{"operation":"get","collection":"tasks","id":"a"}`, "2s")
		if err == nil || output != "" || !strings.Contains(err.Error(), "invalid or unreadable") || strings.Contains(diagnostic, server.URL) {
			t.Fatal("malformed trusted configuration did not fail closed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("configuration parsing refusal performed HTTP")
	}
}
