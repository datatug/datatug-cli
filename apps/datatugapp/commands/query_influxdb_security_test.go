package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo2influxdb"
)

func TestInfluxConfigurationRefusesUnsafeTransportAndBadFiles(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:1234", "https://user:secret@example.test", "https://example.test?secret=token", "https://example.test?", "https://example.test#secret", "https://example.test/api", "https://example.test/%2F", "https:opaque", "https://", "https://bad host"} {
		if _, err := influxTransport(context.Background(), influxNativeConfig{Endpoint: endpoint}); !errors.Is(err, errDynamoInput) {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	for _, path := range []string{"/no-such-owned-fixture-file", t.TempDir(), dynamoTestFile(t, "not-ca.pem", []byte("not a certificate"))} {
		if _, err := influxTransport(context.Background(), influxNativeConfig{Endpoint: "https://example.test", CAFile: path}); err == nil {
			t.Fatal("invalid CA input accepted")
		}
	}
	transport, err := influxTransport(context.Background(), influxNativeConfig{Endpoint: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.MinVersion < 0x0303 || !transport.DisableCompression || transport.MaxConnsPerHost != 1 {
		t.Fatal("HTTPS/proxy/compression bounds changed")
	}
	for _, args := range [][]string{
		{}, {"--config", "-", "--request", "-"}, {"--config", "missing", "--request", "-", "--timeout", "0s"}, {"--config", "missing", "--request", "-", "--timeout", "3m"},
		{"--config", "missing", "--request", "-"},
		{"--config", dynamoTestFile(t, "bad.json", []byte(`{"endpoint":"secret","unknown":true}`)), "--request", "-"},
	} {
		cmd := queryInfluxDBCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatal("invalid CLI configuration accepted")
		}
	}
}

func TestInfluxStrictRequestAndMappingRefusalBeforeDispatch(t *testing.T) {
	t.Setenv("DATATUG_INFLUX_READ_FIXTURE", "owned-secret-token")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := influxTestConfig(t, server)
	for _, request := range []string{
		`{"operation":"delete","collection":"events","key":"a"}`,
		`{"operation":"get","collection":"unknown","key":"a"}`,
		`{"operation":"get","collection":"events"}`,
		`{"operation":"get","collection":"events","key":"a","keys":[]}`,
		`{"operation":"exists","collection":"events","key":"a","projection":[]}`,
		`{"operation":"getMany","collection":"events","key":"a","keys":[]}`,
		`{"operation":"getMany","collection":"events"}`,
		`{"operation":"query","collection":"events","key":"a","limit":1}`,
		`{"operation":"query","collection":"events","limit":0}`,
		`{"operation":"query","collection":"events","limit":1001}`,
		`{"operation":"query","collection":"events","limit":1,"filters":[{"field":"amount","operator":"==","value":9007199254740993}]}`,
		`{"operation":"query","collection":"events","limit":1,"startAfter":[9007199254740993,"a"]}`,
		`{"operation":"query","collection":"events","limit":1,"projection":["foreign"]}`,
		`{"operation":"query","collection":"events","limit":1,"filters":[{"field":"amount","operator":"SQL","value":null}]}`,
		`{"operation":"query","collection":"events","limit":1,"startAfter":[{"kind":"number","value":"1"}]}`,
		`{"operation":"query","collection":"events","limit":1,"order":[{"Field":"label"}]}`,
		`{"operation":"query","collection":"events","limit":1,"q":"SELECT secret"}`,
		`{"operation":"get","operation":"query","collection":"events","key":"a"}`,
		`{"operation":"get","collection":"events","key":"\ud800"}`,
		`{"operation":"get","collection":"events","key":"a"} {}`,
		`[]`,
	} {
		output, diagnostic, err := influxTestCommand(t, context.Background(), config, request, "2s")
		if err == nil || output != "" || strings.Contains(err.Error()+diagnostic, "owned-secret-token") {
			t.Fatal("malformed/unsafe read accepted or leaked a token")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input dispatched")
	}
	mapping := influxTestMapping(server)
	mapping.TokenEnv = "literal token value"
	bad := dynamoTestJSON(t, "bad-token-name.json", mapping)
	if output, _, err := influxTestCommand(t, context.Background(), bad, `{"operation":"get","collection":"events","key":"a"}`, "2s"); err == nil || output != "" {
		t.Fatal("invalid token variable name accepted")
	}
	mapping.TokenEnv = "DATATUG_INFLUX_READ_FIXTURE"
	mapping.Database = ""
	bad = dynamoTestJSON(t, "bad-database.json", mapping)
	if output, _, err := influxTestCommand(t, context.Background(), bad, `{"operation":"get","collection":"events","key":"a"}`, "2s"); err == nil || output != "" {
		t.Fatal("invalid database accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid mapping dispatched")
	}
	// Successful admission still refuses unreadable request paths and unsafe
	// endpoint/CA configuration before any token-bearing operation.
	for _, requestPath := range []string{"/no-such-owned-request", t.TempDir()} {
		cmd := queryInfluxDBCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--config", config, "--request", requestPath})
		if err := cmd.Execute(); err == nil {
			t.Fatal("bad request path accepted")
		}
	}
	mapping = influxTestMapping(server)
	mapping.Endpoint = "http://127.0.0.1:1"
	bad = dynamoTestJSON(t, "http.json", mapping)
	if _, _, err := influxTestCommand(t, context.Background(), bad, `{"operation":"get","collection":"events","key":"a"}`, "2s"); err == nil {
		t.Fatal("plain HTTP accepted")
	}
}

func TestInfluxRequestCollectionWorkBounds(t *testing.T) {
	config := influxNativeConfig{TokenEnv: "READ_TOKEN", Tables: map[string]dalgo2influxdb.Table{"events": {}}}
	key := "a"
	for _, request := range []influxNativeRequest{
		{Operation: "getMany", Collection: "events", Keys: make([]string, 1001)},
		{Operation: "query", Collection: "events", Limit: 1, Filters: make([]influxFilterJSON, 65)},
		{Operation: "query", Collection: "events", Limit: 1, Order: make([]dalgo2influxdb.Order, 17)},
		{Operation: "query", Collection: "events", Limit: 1, Projection: make([]string, 129)},
		{Operation: "query", Collection: "events", Limit: 1, StartAfter: make([]json.RawMessage, 18)},
		{Operation: "get", Collection: "events", Key: &key, StartAfter: []json.RawMessage{}},
	} {
		if _, err := admitInfluxRead(config, request); err == nil {
			t.Fatal("over-budget or mixed operation accepted")
		}
	}
}

func TestInfluxOwnedTLSProviderFailuresRedactedAndNoRedirect(t *testing.T) {
	t.Setenv("DATATUG_INFLUX_READ_FIXTURE", "owned-secret-token")
	var targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(target.Close)
	for _, test := range []struct {
		name                        string
		status                      int
		body, contentType, location string
	}{
		{"forbidden", 403, "owned-secret-token URL cause", "application/json", ""},
		{"malformed", 200, "owned-secret-token", "application/json", ""},
		{"foreign", 200, `[{"__dalgo_key":"foreign","amount":1,"label":null,"active":true}]`, "application/json", ""},
		{"numeric-object", 200, `[{"__dalgo_key":"a","amount":{"secret":"token"},"label":null,"active":true}]`, "application/json", ""},
		{"duplicate-key", 200, `[{"__dalgo_key":"a","amount":1,"amount":2,"label":null,"active":true}]`, "application/json", ""},
		{"oversize", 200, strings.Repeat("x", (4<<20)+1), "application/json", ""},
		{"wrong-type", 200, "[]", "text/plain", ""},
		{"redirect", 302, "secret", "application/json", target.URL},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				if test.location != "" {
					w.Header().Set("Location", test.location)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(server.Close)
			output, diagnostic, err := influxTestCommand(t, context.Background(), influxTestConfig(t, server), `{"operation":"get","collection":"events","key":"a"}`, "2s")
			if err == nil || output != "" || strings.Contains(err.Error()+diagnostic, "owned-secret-token") || strings.Contains(err.Error()+diagnostic, server.URL) {
				t.Fatal("provider failure leaked output, token or endpoint")
			}
		})
	}
	if targetCalls.Load() != 0 {
		t.Fatal("redirect followed")
	}
	// No token, pre-cancel and expired deadline must not make a network call.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(500) }))
	t.Cleanup(server.Close)
	config := influxTestConfig(t, server)
	t.Setenv("DATATUG_INFLUX_READ_FIXTURE", "")
	if output, _, err := influxTestCommand(t, context.Background(), config, `{"operation":"get","collection":"events","key":"a"}`, "2s"); err == nil || output != "" {
		t.Fatal("missing token succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, _, err := influxTestCommand(t, ctx, config, `{"operation":"get","collection":"events","key":"a"}`, "2s"); err == nil || output != "" || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal("preabort changed")
	}
	ctx, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if output, _, err := influxTestCommand(t, ctx, config, `{"operation":"get","collection":"events","key":"a"}`, "2s"); err == nil || output != "" || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatal("expired deadline changed")
	}
	if targetCalls.Load() != 0 {
		t.Fatal("invalid credential/cancellation dispatched")
	}
}
