package commands

import (
	"context"
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

func TestCloudflareKVStrictDescriptorsAndConfigurationRefuseBeforeHTTP(t *testing.T) {
	t.Setenv("DATATUG_CLOUDFLARE_KV_READ_TEST", cloudflareKVFixtureToken)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := cloudflareKVTestConfig(t, server)
	for _, raw := range []string{
		`[]`, `{}`, `{} {}`, `{"operation":"get","operation":"exists"}`, `{"Operation":"get","collection":"cache","key":"tenant/a"}`, `{"operation":"get","collection":"cache","key":"tenant/a","token":"PRIVATE"}`,
		`{"operation":"delete","collection":"cache","key":"tenant/a"}`, `{"operation":"get","collection":"wrong","key":"tenant/a"}`, `{"operation":"get","collection":"cache"}`, `{"operation":"get","collection":"cache","key":"foreign"}`, `{"operation":"get","collection":"cache","key":"tenant/\ud800"}`,
		`{"operation":"get","collection":"cache","key":"tenant/a","keys":null}`, `{"operation":"get","collection":"cache","key":"tenant/a","prefix":null}`, `{"operation":"get","collection":"cache","key":"tenant/a","sql":"SELECT"}`,
		`{"operation":"getMany","collection":"cache"}`, `{"operation":"getMany","collection":"cache","keys":null}`, `{"operation":"getMany","collection":"cache","keys":["tenant/a",""]}`, `{"operation":"getMany","collection":"cache","keys":["tenant/a","foreign"]}`,
		`{"operation":"getMany","collection":"cache","keys":[],"key":null}`, `{"operation":"list","collection":"cache","pageSize":10,"pages":1}`, `{"operation":"list","collection":"cache","prefix":null,"pageSize":10,"pages":1}`,
		`{"operation":"list","collection":"cache","prefix":"foreign","pageSize":10,"pages":1}`, `{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":1,"pages":1}`, `{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":1001,"pages":1}`,
		`{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":10,"pages":0}`, `{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":10,"pages":101}`, `{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":1000,"pages":2}`,
		`{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":10,"pages":1,"cursor":"PRIVATE"}`, `{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":10,"pages":1,"endpoint":"https://other"}`, strings.Repeat(" ", 65537),
	} {
		out, diag, err := cloudflareKVTestCommand(t, context.Background(), config, raw, "2s")
		if err == nil || out != "" || strings.Contains(err.Error()+diag, "PRIVATE") {
			t.Fatal("unsafe native descriptor admitted/leaked")
		}
	}
	oversized := cloudflareKVRequest{Operation: "getMany", Collection: "cache", Keys: make([]string, 101)}
	if err := admitCloudflareKVRead(context.Background(), cloudflareKVTestMapping(server), &oversized); err == nil {
		t.Fatal("batch key cap omitted")
	}
	for _, raw := range []string{`{`, `[]`, `{"Endpoint":"https://example.test"}`, `{"endpoint":"https://example.test","token":"PRIVATE"}`, `{"endpoint":"https://example.test","anonymous":true}`, `{"allowedPrefixes":1}`} {
		out, _, err := cloudflareKVTestCommand(t, context.Background(), dynamoTestFile(t, "kv-bad-config.json", []byte(raw)), `{"operation":"get","collection":"cache","key":"tenant/a"}`, "2s")
		if err == nil || out != "" {
			t.Fatal("unsafe config admitted")
		}
	}
	for _, change := range []func(*cloudflareKVConfig){func(c *cloudflareKVConfig) { c.AccountID = "../wrong" }, func(c *cloudflareKVConfig) { c.NamespaceID = "" }, func(c *cloudflareKVConfig) { c.TokenEnv = "literal credential" }, func(c *cloudflareKVConfig) { c.AllowedPrefixes = nil }, func(c *cloudflareKVConfig) { c.AllowedPrefixes = []string{""} }, func(c *cloudflareKVConfig) { c.Endpoint = "http://127.0.0.1" }, func(c *cloudflareKVConfig) { c.Endpoint = "https://user:PRIVATE@example.test" }, func(c *cloudflareKVConfig) { c.Endpoint = "https://example.test?PRIVATE" }, func(c *cloudflareKVConfig) { c.Endpoint = "https://example.test/client/v4" }, func(c *cloudflareKVConfig) { c.CAFile = "/missing-ca" }} {
		c := cloudflareKVTestMapping(server)
		change(&c)
		out, diag, err := cloudflareKVTestCommand(t, context.Background(), dynamoTestJSON(t, "kv-invalid.json", c), `{"operation":"get","collection":"cache","key":"tenant/a"}`, "2s")
		if err == nil || out != "" || strings.Contains(diag+err.Error(), "PRIVATE") {
			t.Fatal("config authority/CA refusal lost")
		}
	}
	for _, args := range [][]string{{}, {"--config", "-", "--request", "-"}, {"--config", config, "--request", "-", "--timeout", "0s"}, {"--config", config, "--request", "-", "--timeout", "3m"}, {"--config", "/missing-config", "--request", "-"}, {"--config", config, "--request", "/missing-request"}} {
		cmd := queryCloudflareKVCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatal("invalid command input admitted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("local refusal dispatched HTTP")
	}
}

func TestCloudflareKVHTTP404IsNeverAbsenceAndErrorsNeverLeakOrRedirect(t *testing.T) {
	t.Setenv("DATATUG_CLOUDFLARE_KV_READ_TEST", cloudflareKVFixtureToken)
	var redirects atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); w.WriteHeader(500) }))
	t.Cleanup(target.Close)
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				cloudflareKVTestReply(t, w, `{"errors":[{"code":10009,"message":"PRIVATE_REMOTE_CAUSE"}]}`)
			}))
			t.Cleanup(server.Close)
			config := cloudflareKVTestConfig(t, server)
			for _, operation := range []string{"get", "exists", "testConnection"} {
				out, diag, err := cloudflareKVTestCommand(t, context.Background(), config, `{"operation":"`+operation+`","collection":"cache","key":"tenant/a"}`, "2s")
				if err == nil || out != "" || strings.Contains(err.Error()+diag, "PRIVATE") || strings.Contains(diag, server.URL) || strings.Contains(diag, cloudflareKVFixtureToken) {
					t.Fatal("HTTPfailure became false/missing success or leaked")
				}
			}
			if out, _, err := cloudflareKVTestCommand(t, context.Background(), config, `{"operation":"getMany","collection":"cache","keys":["tenant/a","tenant/b"]}`, "2s"); err == nil || out != "" || calls.Load() != 4 {
				t.Fatal("failure retried/laterbatchdispatched/partialsuccess")
			}
		})
	}
	for _, tc := range []struct {
		name     string
		body     string
		redirect bool
	}{{"oversize", strings.Repeat("x", (1<<20)+1), false}, {"malformed-list", `{"success":true,"result":`, false}, {"redirect", "PRIVATE_REMOTE_CAUSE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			completed := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(completed)
				if tc.redirect {
					w.Header().Set("Location", target.URL)
					w.WriteHeader(307)
				}
				if _, err := io.WriteString(w, tc.body); err != nil {
					if tc.name != "oversize" || (!errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET)) {
						t.Error("unexpected owned response write failure")
					} else {
						select {
						case <-r.Context().Done():
						case <-time.After(time.Second):
							t.Error("oversize close lacked cancellation ACK")
						}
					}
				}
			}))
			t.Cleanup(server.Close)
			request := `{"operation":"get","collection":"cache","key":"tenant/a"}`
			if tc.name == "malformed-list" {
				request = `{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":10,"pages":1}`
			}
			out, diag, err := cloudflareKVTestCommand(t, context.Background(), cloudflareKVTestConfig(t, server), request, "2s")
			if err == nil || out != "" || strings.Contains(err.Error()+diag, "PRIVATE") {
				t.Fatal("remote refusal leaked or emitted prefix")
			}
			select {
			case <-completed:
			case <-time.After(2 * time.Second):
				t.Fatal("negative handler did not finish")
			}
		})
	}
	if redirects.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestCloudflareKVNoAnonymousFallbackUntrustedCAAndPreabort(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := cloudflareKVTestConfig(t, server)
	request := `{"operation":"get","collection":"cache","key":"tenant/a"}`
	for _, credential := range []string{"", "Bearer private", "bad\nprivate"} {
		t.Setenv("DATATUG_CLOUDFLARE_KV_READ_TEST", credential)
		if out, _, err := cloudflareKVTestCommand(t, context.Background(), config, request, "2s"); err == nil || out != "" {
			t.Fatal("invalid token used fallback")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, _, err := cloudflareKVTestCommand(t, ctx, config, request, "2s"); err == nil || out != "" || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal("preabort lost")
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if out, _, err := cloudflareKVTestCommand(t, expired, config, request, "2s"); err == nil || out != "" || !strings.Contains(err.Error(), "deadline") {
		t.Fatal("expired deadline lost")
	}
	t.Setenv("DATATUG_CLOUDFLARE_KV_READ_TEST", cloudflareKVFixtureToken)
	if out, _, err := cloudflareKVTestCommand(t, context.Background(), dynamoTestJSON(t, "kv-untrusted.json", cloudflareKVTestMapping(server)), request, "2s"); err == nil || out != "" {
		t.Fatal("untrusted TLS accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid auth/preabort/CA reached HTTP")
	}
}
