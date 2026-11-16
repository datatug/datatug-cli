package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const cloudflareKVFixtureToken = "owned-cloudflare-read-token"
const cloudflareKVFixtureBase = "/client/v4/accounts/owned_account/storage/kv/namespaces/existing_namespace"
const cloudflareKVFixtureMetadata = `{ "wide":9007199254740993.000, "__proto__":{"constructor":"own"}, "prototype":true }`

func cloudflareKVTestMapping(server *httptest.Server) cloudflareKVConfig {
	return cloudflareKVConfig{Endpoint: server.URL, AccountID: "owned_account", NamespaceID: "existing_namespace", CollectionAlias: "cache", AllowedPrefixes: []string{"tenant/"}, TokenEnv: "DATATUG_CLOUDFLARE_KV_READ_TEST"}
}
func cloudflareKVTestConfig(t *testing.T, server *httptest.Server) string {
	t.Helper()
	c := cloudflareKVTestMapping(server)
	c.CAFile = dynamoTestFile(t, "kv-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	return dynamoTestJSON(t, "kv-config.json", c)
}
func cloudflareKVTestCommand(t *testing.T, ctx context.Context, config, request, timeout string) (string, string, error) {
	t.Helper()
	cmd := queryCommand()
	cmd.SetContext(ctx)
	cmd.SetIn(strings.NewReader(request))
	var out, diag bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diag)
	cmd.SetArgs([]string{"cloudflare-kv", "--config", config, "--request", "-", "--timeout", timeout})
	err := cmd.Execute()
	return out.String(), diag.String(), err
}
func cloudflareKVTestHTTP(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Method != "GET" || r.TLS == nil || r.ProtoMajor != 1 || r.Header.Get("Authorization") != "Bearer "+cloudflareKVFixtureToken || r.Header.Get("X-Auth-Key") != "" || r.Header.Get("X-Auth-Email") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "identity" {
		t.Error("fixed read-only verifiedTLS/HTTP1/token policy changed")
	}
}
func cloudflareKVTestReply(t *testing.T, w http.ResponseWriter, data string) {
	t.Helper()
	if _, err := io.WriteString(w, data); err != nil {
		t.Error("owned reply failed")
	}
}

func TestQueryCloudflareKVOwnedTLSNativeBytesMetadataAndWholePages(t *testing.T) {
	t.Setenv("DATATUG_CLOUDFLARE_KV_READ_TEST", cloudflareKVFixtureToken)
	var calls, pages atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudflareKVTestHTTP(t, r)
		calls.Add(1)
		if r.URL.Path == cloudflareKVFixtureBase+"/keys" {
			if r.URL.Query().Get("limit") != "10" {
				t.Error("REST minimum/page size lost")
			}
			if r.URL.Query().Get("prefix") == "tenant/empty/" {
				cloudflareKVTestReply(t, w, `{"success":true,"result":[],"result_info":{"cursor":null}}`)
				return
			}
			if r.URL.Query().Get("prefix") != "tenant/" {
				t.Error("literal prefix lost")
			}
			switch pages.Add(1) {
			case 1:
				if r.URL.Query().Get("cursor") != "" {
					t.Error("first cursor not empty")
				}
				cloudflareKVTestReply(t, w, `{"success":true,"result":[],"result_info":{"cursor":"private/one%😀"}}`)
			case 2:
				if r.URL.Query().Get("cursor") != "private/one%😀" {
					t.Error("opaque position changed")
				}
				cloudflareKVTestReply(t, w, `{"success":true,"result":[{"name":"tenant/a","metadata":`+cloudflareKVFixtureMetadata+`,"expiration":18446744073709551615},{"name":"tenant/b","metadata":null}],"result_info":{"cursor":"private/two"}}`)
			default:
				t.Error("extra list request")
			}
			return
		}
		key := strings.TrimPrefix(r.URL.Path, cloudflareKVFixtureBase+"/values/")
		if r.URL.EscapedPath() != cloudflareKVFixtureBase+"/values/"+url.PathEscape(key) || r.URL.RawQuery != "" {
			t.Error("key not exactly one encoded segment")
		}
		w.Header().Set("Expiration", "18446744073709551615")
		if key != "tenant/empty" {
			cloudflareKVTestReply(t, w, string([]byte{0, 255, 128, 'n', 'u', 'l', 'l'}))
		}
	}))
	t.Cleanup(server.Close)
	config := cloudflareKVTestConfig(t, server)
	for _, request := range []string{`{"operation":"get","collection":"cache","key":"tenant/raw"}`, `{"operation":"get","collection":"cache","key":"tenant/empty"}`, `{"operation":"get","collection":"cache","key":"tenant/a/b%2F😀"}`, `{"operation":"exists","collection":"cache","key":"tenant/raw"}`, `{"operation":"testConnection","collection":"cache","key":"tenant/raw"}`, `{"operation":"getMany","collection":"cache","keys":["tenant/raw","tenant/empty","tenant/raw"]}`, `{"operation":"getMany","collection":"cache","keys":[]}`, `{"operation":"list","collection":"cache","prefix":"tenant/","pageSize":10,"pages":2}`} {
		out, diag, err := cloudflareKVTestCommand(t, context.Background(), config, request, "2s")
		if err != nil || !strings.Contains(out, `"format":"cloudflare_kv_raw_v1"`) || !strings.Contains(diag, "DataTug application policies are not applied") {
			t.Fatal("native command failed", err)
		}
		for _, secret := range []string{cloudflareKVFixtureToken, server.URL, "private/one", "private/two"} {
			if strings.Contains(out+diag, secret) {
				t.Fatal("credentials/endpoint/cursor leaked")
			}
		}
		if strings.Contains(request, `"operation":"get"`) {
			var result struct {
				Entry cloudflareKVWireEntry `json:"entry"`
			}
			if json.Unmarshal([]byte(out), &result) != nil || result.Entry.Value.Kind != "bytes" || result.Entry.Expiration == nil || result.Entry.Expiration.Kind != "uint64" || result.Entry.Expiration.Value != "18446744073709551615" {
				t.Fatal("native tags/uint64 not lossless")
			}
			decoded, err := base64.StdEncoding.DecodeString(result.Entry.Value.Base64)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(request, `"key":"tenant/empty"`) {
				if result.Entry.Value.Base64 != "" || len(decoded) != 0 {
					t.Fatal("empty present bytes changed")
				}
			} else if !bytes.Equal(decoded, []byte{0, 255, 128, 'n', 'u', 'l', 'l'}) {
				t.Fatal("raw binary/JSON-null text coerced")
			}
		}
		if strings.Contains(request, `"operation":"getMany"`) && strings.Contains(request, `"keys":["tenant/raw"`) {
			var result struct {
				Entries []cloudflareKVWireEntry `json:"entries"`
			}
			if json.Unmarshal([]byte(out), &result) != nil || len(result.Entries) != 3 || result.Entries[0].Key != "tenant/raw" || result.Entries[1].Value.Base64 != "" || result.Entries[2].Key != "tenant/raw" || result.Entries[0].Value.Base64 != result.Entries[2].Value.Base64 {
				t.Fatal("ordered independent duplicate bytes changed")
			}
		}
		if strings.Contains(request, `"operation":"list"`) {
			var result struct {
				Keys    []cloudflareKVWireKey `json:"keys"`
				Pages   int                   `json:"pages"`
				HasMore bool                  `json:"hasMore"`
			}
			if json.Unmarshal([]byte(out), &result) != nil || len(result.Keys) != 2 || result.Pages != 2 || !result.HasMore || result.Keys[0].Metadata == nil || result.Keys[0].Metadata.Kind != "json" || result.Keys[0].Metadata.Value != cloudflareKVFixtureMetadata || result.Keys[1].Metadata == nil || result.Keys[1].Metadata.Value != "null" {
				t.Fatal("original metadata JSON text/null/whole page contract lost")
			}
		}
	}
	if calls.Load() != 10 || pages.Load() != 2 {
		t.Fatal("unexpected retries/native request count")
	}
	out, _, err := cloudflareKVTestCommand(t, context.Background(), config, `{"operation":"list","collection":"cache","prefix":"tenant/empty/","pageSize":10,"pages":3}`, "2s")
	if err != nil || !strings.Contains(out, `"keys":[]`) || !strings.Contains(out, `"pages":1`) || !strings.Contains(out, `"hasMore":false`) {
		t.Fatal("terminal empty page", err)
	}
	path := dynamoTestFile(t, "kv-request.json", []byte(`{"operation":"get","collection":"cache","key":"tenant/raw"}`))
	cmd := queryCloudflareKVCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", config, "--request", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal("file-input registration", err)
	}
}

func TestCloudflareKVNamespaceWideGrantAndDefaultAliasAreExplicit(t *testing.T) {
	t.Setenv("DATATUG_CLOUDFLARE_KV_READ_TEST", cloudflareKVFixtureToken)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudflareKVTestHTTP(t, r)
		if r.URL.Query().Get("prefix") != "" {
			t.Error("explicit empty prefix changed")
		}
		cloudflareKVTestReply(t, w, `{"success":true,"result":[{"name":"bare-key"}]}`)
	}))
	t.Cleanup(server.Close)
	config := cloudflareKVTestMapping(server)
	config.CollectionAlias = ""
	config.AllowedPrefixes = []string{""}
	config.AllowNamespaceAccess = true
	config.CAFile = dynamoTestFile(t, "wide-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	out, _, err := cloudflareKVTestCommand(t, context.Background(), dynamoTestJSON(t, "wide.json", config), `{"operation":"list","collection":"kv","prefix":"","pageSize":10,"pages":1}`, "2s")
	if err != nil || !strings.Contains(out, `"name":"bare-key"`) || strings.Contains(out, `"metadata"`) {
		t.Fatal("explicit wide grant/default alias/absent metadata", err)
	}
}

func TestQueryCloudflareKVDispatchedCallerDeadlineDisconnectAndReopen(t *testing.T) {
	t.Setenv("DATATUG_CLOUDFLARE_KV_READ_TEST", cloudflareKVFixtureToken)
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
				cloudflareKVTestHTTP(t, r)
				if slow.Swap(false) {
					close(entered)
					select {
					case <-r.Context().Done():
						close(disconnected)
					case <-cleanup:
					}
					return
				}
				cloudflareKVTestReply(t, w, "raw")
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(cleanup) })
			config := cloudflareKVTestConfig(t, server)
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
				out, diag, err := cloudflareKVTestCommand(t, ctx, config, `{"operation":"get","collection":"cache","key":"tenant/a"}`, timeout)
				done <- result{out, diag, err}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("native GET not dispatched")
			}
			start := time.Now()
			if !deadline {
				cancel(errors.New("PRIVATE_CALLER_CAUSE"))
			}
			select {
			case r := <-done:
				label := "cancelled"
				if deadline {
					label = "deadline"
				}
				if r.err == nil || r.out != "" || !strings.Contains(r.err.Error(), label) || strings.Contains(r.diag+r.err.Error(), "PRIVATE") || strings.Contains(r.diag, cloudflareKVFixtureToken) {
					t.Fatal("dispatched cancellation redaction/no prefix", r.err)
				}
			case <-time.After(750 * time.Millisecond):
				t.Fatal("dispatched cancellation delayed")
			}
			if time.Since(start) > 750*time.Millisecond {
				t.Fatal("prompt cancellation budget exceeded")
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("owned request did not disconnect")
			}
			if out, _, err := cloudflareKVTestCommand(t, context.Background(), config, `{"operation":"get","collection":"cache","key":"tenant/a"}`, "2s"); err != nil || out == "" {
				t.Fatal("read after disconnect", err)
			}
		})
	}
}

func TestCloudflareKVSignalContextCleanupReuseAndOperationTimeENV(t *testing.T) {
	previous := interruptContext
	defer func() { interruptContext = previous }()
	parent := context.Background()
	stopped := false
	var child context.Context
	interruptContext = func(given context.Context) (context.Context, func() os.Signal, func()) {
		if given != parent {
			t.Error("parent context changed")
		}
		var cancel context.CancelFunc
		child, cancel = context.WithCancel(given)
		return child, func() os.Signal { return nil }, func() { stopped = true; cancel() }
	}
	cmd := queryCloudflareKVCommand()
	cmd.SetContext(parent)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	for range 2 {
		stopped = false
		if err := cmd.Execute(); err == nil || !stopped || child.Err() == nil || cmd.Context() != parent {
			t.Fatal("signal lifecycle/reuse")
		}
	}
	bare := queryCloudflareKVCommand()
	stopped = false
	if err := bare.RunE(bare, nil); err == nil || !stopped || child.Err() == nil || bare.Context() != nil {
		t.Fatal("nil caller lifecycle")
	}
	config := cloudflareKVConfig{TokenEnv: "DATATUG_CLOUDFLARE_KV_READ_TEST"}
	options, err := cloudflareKVOptions(context.Background(), config, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if config.origin() != "https://api.cloudflare.com" || options.Endpoint != "https://api.cloudflare.com/client/v4" {
		t.Fatal("fixed API base changed")
	}
	t.Setenv(config.TokenEnv, "first-owned-token")
	first, err := options.Token(context.Background())
	if err != nil || first != "first-owned-token" {
		t.Fatal("initial env credential")
	}
	t.Setenv(config.TokenEnv, "rotated-owned-token")
	next, err := options.Token(context.Background())
	if err != nil || next != "rotated-owned-token" {
		t.Fatal("token cached at construction")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cloudflareKVOptions(ctx, config, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal("options caller cancellation lost")
	}
}
