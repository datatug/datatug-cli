package commands

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCosmosStrictInputAndConfigRefuseBeforeDispatch(t *testing.T) {
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", cosmosFixtureToken)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := cosmosTestConfig(t, server)
	for _, request := range []string{
		`[]`, `{} {}`, `{"operation":"get","operation":"exists"}`, `{"operation":"get","token":"secret"}`,
		`{"operation":"delete","key":{"id":"a","partition":"p"}}`,
		`{"operation":"get"}`, `{"operation":"testConnection"}`,
		`{"operation":"get","key":{"id":"a","partition":"p"},"keys":[]}`,
		`{"operation":"get","key":{"id":"a","partition":"p"},"parameters":[]}`,
		`{"operation":"get","key":{"id":"a","partition":"p"},"partition":"p"}`,
		`{"operation":"get","key":{"id":"a","partition":"p"},"sql":"SELECT * FROM c"}`,
		`{"operation":"get","key":{"id":"a","partition":"p"},"limit":1}`,
		`{"operation":"get","key":{"id":"a/b","partition":"p"}}`,
		`{"operation":"get","key":{"id":"a","partition":""}}`,
		`{"operation":"get","key":{"id":"a","partition":1}}`,
		`{"operation":"get","key":{"id":"\ud800","partition":"p"}}`,
		`{"operation":"getMany"}`, `{"operation":"getMany","key":{"id":"a","partition":"p"},"keys":[]}`,
		`{"operation":"getMany","keys":[],"limit":1}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":0}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":1001}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":1,"key":{"id":"a","partition":"p"}}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":1,"keys":[]}`,
		`{"operation":"query","partition":"p","limit":1}`,
		`{"operation":"query","sql":"SELECT * FROM c","limit":1}`,
		`{"operation":"query","sql":"DELETE FROM c","partition":"p","limit":1}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":1,"parameters":[{"name":"@x"}]}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":1,"parameters":[{"name":"x","value":null}]}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":1,"continuation":"private"}`,
		`{"operation":"query","sql":"SELECT * FROM c","partition":"p","limit":1,"session":"private"}`,
	} {
		out, diagnostic, err := cosmosTestCommand(t, context.Background(), config, request, "2s")
		if err == nil || out != "" || strings.Contains(err.Error()+diagnostic, cosmosFixtureToken) {
			t.Fatal("invalid native request accepted or exposed token")
		}
	}
	for _, mutate := range []func(*cosmosNativeConfig){
		func(c *cosmosNativeConfig) { c.TokenEnv = "literal secret" },
		func(c *cosmosNativeConfig) { c.AuthType = "master" },
		func(c *cosmosNativeConfig) { c.Database = "" },
		func(c *cosmosNativeConfig) { c.Container = "bad/container" },
		func(c *cosmosNativeConfig) { c.PartitionField = "_etag" },
		func(c *cosmosNativeConfig) { c.AuthScopeID = "" },
		func(c *cosmosNativeConfig) { c.Endpoint = "http://127.0.0.1:1234" },
		func(c *cosmosNativeConfig) { c.Endpoint = "https://user:secret@example.test" },
		func(c *cosmosNativeConfig) { c.Endpoint = "https://example.test?secret" },
		func(c *cosmosNativeConfig) { c.CAFile = "/no-such-owned-ca" },
	} {
		mapping := cosmosTestMapping(server)
		mutate(&mapping)
		out, _, err := cosmosTestCommand(t, context.Background(), dynamoTestJSON(t, "bad-config.json", mapping), `{"operation":"get","key":{"id":"a","partition":"p"}}`, "2s")
		if err == nil || out != "" {
			t.Fatal("invalid config accepted")
		}
	}
	for _, args := range [][]string{
		{}, {"--config", "-", "--request", "-"}, {"--config", config, "--request", "-", "--timeout", "0s"}, {"--config", config, "--request", "-", "--timeout", "3m"},
		{"--config", "/no-such-config", "--request", "-"},
		{"--config", dynamoTestFile(t, "unknown.json", []byte(`{"token":"secret"}`)), "--request", "-"},
		{"--config", config, "--request", "/no-such-request"},
	} {
		cmd := queryCosmosDBCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatal("invalid flags/files accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("refused input dispatched")
	}
	base := cosmosTestMapping(server)
	for _, r := range []cosmosNativeRequest{
		{Operation: "getMany", Keys: make([]cosmosNativeKey, 101)},
		{Operation: "query", SQL: "SELECT * FROM c", Partition: "p", Limit: 1, Parameters: make([]cosmosParameter, 101)},
	} {
		if _, err := admitCosmosRead(base, r); err == nil {
			t.Fatal("local work admission exceeded")
		}
	}
}

func TestCosmosTLSProviderFailuresNoOutputNoRedirectOrSecret(t *testing.T) {
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", cosmosFixtureToken)
	var targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { targetCalls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(target.Close)
	for _, test := range []struct {
		name           string
		status         int
		body, location string
	}{
		{"forbidden", 403, "secret-sql-and-token", ""},
		{"throttled", 429, "secret-sql-and-token", ""},
		{"malformed", 200, "secret-sql-and-token", ""},
		{"foreign-native-id", 200, `{"id":"foreign","tenant":"tenant-a"}`, ""},
		{"foreign-partition", 200, `{"id":"a b","tenant":"foreign"}`, ""},
		{"duplicate-field", 200, `{"id":"a b","tenant":"tenant-a","x":1,"x":2}`, ""},
		{"oversize", 200, strings.Repeat("x", (1<<20)+1), ""},
		{"redirect", 302, "secret-sql-and-token", target.URL},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if test.location != "" {
					w.Header().Set("Location", test.location)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(server.Close)
			out, diag, err := cosmosTestCommand(t, context.Background(), cosmosTestConfig(t, server), `{"operation":"get","key":{"id":"a b","partition":"tenant-a"}}`, "2s")
			if err == nil || out != "" || strings.Contains(err.Error()+diag, "secret-sql-and-token") || strings.Contains(err.Error()+diag, cosmosFixtureToken) || strings.Contains(err.Error()+diag, server.URL) {
				t.Fatal("failed native request exposed output/cause/secrets")
			}
		})
	}
	if targetCalls.Load() != 0 {
		t.Fatal("authorization-bearing redirect followed")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { targetCalls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := cosmosTestConfig(t, server)
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", "")
	if out, _, err := cosmosTestCommand(t, context.Background(), config, `{"operation":"get","key":{"id":"a","partition":"p"}}`, "2s"); err == nil || out != "" {
		t.Fatal("missing token accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, _, err := cosmosTestCommand(t, ctx, config, `{"operation":"get","key":{"id":"a","partition":"p"}}`, "2s"); err == nil || out != "" || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal("preabort lost")
	}
	ctx, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if out, _, err := cosmosTestCommand(t, ctx, config, `{"operation":"get","key":{"id":"a","partition":"p"}}`, "2s"); err == nil || out != "" || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatal("expired deadline lost")
	}
	if targetCalls.Load() != 0 {
		t.Fatal("missing credentials or preabort dispatched")
	}
	// An untrusted owned TLS endpoint is refused with verification enabled.
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", cosmosFixtureToken)
	if out, _, err := cosmosTestCommand(t, context.Background(), dynamoTestJSON(t, "untrusted.json", cosmosTestMapping(server)), `{"operation":"get","key":{"id":"a","partition":"p"}}`, "2s"); err == nil || out != "" {
		t.Fatal("untrusted TLS accepted")
	}
}

func TestCosmosAADWireAndEmptyQueryUseNoSessionOverride(t *testing.T) {
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", "owned.raw.aad-token")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, (64<<10)+1)); err != nil {
			t.Error("owned query request drain failed")
			return
		}
		if r.Header.Get("Authorization") != "type=aad&ver=1.0&sig=owned.raw.aad-token" || r.Header.Get("x-ms-consistency-level") != "" || r.Header.Get("x-ms-session-token") != "" {
			t.Error("AAD/default consistency contract changed")
		}
		cosmosTestResponse(t, w, `{"Documents":[],"_count":0}`)
	}))
	t.Cleanup(server.Close)
	trusted := cosmosTestConfig(t, server)
	data, err := readDynamoFile(context.Background(), trusted)
	if err != nil {
		t.Fatal(err)
	}
	var config cosmosNativeConfig
	if err := decodeDynamoDocument(data, &config); err != nil {
		t.Fatal(err)
	}
	config.AuthType = "aad"
	out, _, err := cosmosTestCommand(t, context.Background(), dynamoTestJSON(t, "aad.json", config), `{"operation":"query","partition":"tenant-a","sql":"SELECT * FROM c","limit":1}`, "2s")
	if err != nil || !strings.Contains(out, `"hasMore":false`) || !strings.Contains(out, `"documents":[]`) || calls.Load() != 1 {
		t.Fatal("empty native query/AAD request failed")
	}
}

func TestCosmosBatchAggregateBodyBudgetRefusesWholeAndStopsLaterReads(t *testing.T) {
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", cosmosFixtureToken)
	var calls atomic.Int32
	// Each document fits individually; the second response exceeds the one
	// operation's aggregate raw-body budget. The third key must not dispatch.
	document := `{"id":"a","tenant":"p","payload":"` + strings.Repeat("x", 600000) + `"}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, document)
	}))
	t.Cleanup(server.Close)
	out, diagnostic, err := cosmosTestCommand(t, context.Background(), cosmosTestConfig(t, server), `{"operation":"getMany","keys":[{"id":"a","partition":"p"},{"id":"a","partition":"p"},{"id":"a","partition":"p"}]}`, "2s")
	if err == nil || out != "" || calls.Load() != 2 || strings.Contains(diagnostic, cosmosFixtureToken) {
		t.Fatal("aggregate batch limit leaked partial data or dispatched a later key")
	}
}
