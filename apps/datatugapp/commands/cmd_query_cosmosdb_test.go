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

	"github.com/dal-go/dalgo2cosmosdb"
)

const cosmosFixtureToken = "type=resource&ver=1.0&sig=owned+fixture/secret=="
const cosmosFixtureDocument = `{"id":"a b","tenant":"tenant-a","_etag":"native-revision","exact":9007199254740993.000,"nested":[null,true,false,-1,{"text":"<native>&"}]}`

func cosmosTestMapping(server *httptest.Server) cosmosNativeConfig {
	return cosmosNativeConfig{Endpoint: server.URL, Database: "ExistingDB", Container: "ExistingContainer", PartitionField: "tenant", AuthScopeID: "owned-fixture-reader", AuthType: "resource", TokenEnv: "DATATUG_COSMOS_READ_FIXTURE"}
}
func cosmosTestConfig(t *testing.T, server *httptest.Server) string {
	t.Helper()
	c := cosmosTestMapping(server)
	c.CAFile = dynamoTestFile(t, "cosmos-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	return dynamoTestJSON(t, "cosmos-native.json", c)
}
func cosmosTestCommand(t *testing.T, ctx context.Context, config, request, timeout string) (string, string, error) {
	t.Helper()
	cmd := queryCommand()
	var output, diagnostic bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&diagnostic)
	cmd.SetIn(strings.NewReader(request))
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"cosmosdb", "--config", config, "--request", "-", "--timeout", timeout})
	err := cmd.Execute()
	return output.String(), diagnostic.String(), err
}
func cosmosTestResponse(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Error("owned response write failed")
	}
}

func TestQueryCosmosOwnedTLSNativeJourney(t *testing.T) {
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", cosmosFixtureToken)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "type%3Dresource%26ver%3D1.0%26sig%3Downed%2Bfixture%2Fsecret%3D%3D" || r.Header.Get("x-ms-documentdb-partitionkey") != `["tenant-a"]` || r.Header.Get("x-ms-consistency-level") != "" || r.Header.Get("x-ms-session-token") != "" || r.Header.Get("x-ms-documentdb-query-enablecrosspartition") != "" {
			t.Error("native auth/partition/default consistency contract changed")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("x-ms-session-token", "private-observed-session")
		w.Header().Set("x-ms-request-charge", "1.23")
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
			var query struct {
				Query      string `json:"query"`
				Parameters []struct {
					Name  string      `json:"name"`
					Value json.Number `json:"value"`
				} `json:"parameters"`
			}
			if err != nil || len(body) > 64<<10 || decodeDynamoDocument(body, &query) != nil || query.Query != "SELECT * FROM c WHERE c.exact >= @minimum" || len(query.Parameters) != 1 || query.Parameters[0].Name != "@minimum" || query.Parameters[0].Value != "9007199254740993.000" || r.Header.Get("Content-Type") != "application/query+json" || r.Header.Get("x-ms-documentdb-isquery") != "true" || r.Header.Get("x-ms-max-item-count") != "1" || r.URL.Path != "/dbs/ExistingDB/colls/ExistingContainer/docs" {
				t.Error("parameterized query contract changed")
				w.WriteHeader(400)
				return
			}
			w.Header().Set("x-ms-continuation", "private-next-page")
			cosmosTestResponse(t, w, `{"Documents":[`+cosmosFixtureDocument+`],"_count":1}`)
			return
		}
		if r.Method != http.MethodGet {
			t.Error("read became a write")
			w.WriteHeader(400)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.WriteHeader(404)
			return
		}
		if r.URL.EscapedPath() != "/dbs/ExistingDB/colls/ExistingContainer/docs/a%20b" {
			t.Error("native ID escaping changed")
			w.WriteHeader(400)
			return
		}
		cosmosTestResponse(t, w, cosmosFixtureDocument)
	}))
	t.Cleanup(server.Close)
	config := cosmosTestConfig(t, server)
	for _, test := range []struct{ request, contains string }{
		{`{"operation":"get","key":{"id":"a b","partition":"tenant-a"}}`, `"value":"9007199254740993.000"`},
		{`{"operation":"get","key":{"id":"missing","partition":"tenant-a"}}`, `"document":null`},
		{`{"operation":"exists","key":{"id":"a b","partition":"tenant-a"}}`, `"exists":true`},
		{`{"operation":"exists","key":{"id":"missing","partition":"tenant-a"}}`, `"exists":false`},
		{`{"operation":"testConnection","key":{"id":"missing","partition":"tenant-a"}}`, `"requestCompleted":true`},
		{`{"operation":"getMany","keys":[{"id":"a b","partition":"tenant-a"},{"id":"missing","partition":"tenant-a"},{"id":"a b","partition":"tenant-a"}]}`, `},null,{`},
		{`{"operation":"getMany","keys":[]}`, `"documents":[]`},
		{`{"operation":"query","partition":"tenant-a","sql":"SELECT * FROM c WHERE c.exact >= @minimum","parameters":[{"name":"@minimum","value":9007199254740993.000}],"limit":1}`, `"hasMore":true`},
	} {
		output, diagnostic, err := cosmosTestCommand(t, context.Background(), config, test.request, "2s")
		if err != nil || !strings.Contains(output, test.contains) || !strings.Contains(diagnostic, cosmosNativePolicyNote) {
			t.Fatalf("native command failed: %v", err)
		}
		for _, secret := range []string{cosmosFixtureToken, "private-next-page", "private-observed-session", "authorizationVerified", "nextCursor"} {
			if strings.Contains(output+diagnostic, secret) {
				t.Fatal("opaque state or secret exposed")
			}
		}
	}
	if calls.Load() != 9 {
		t.Fatalf("unexpected native calls: %d", calls.Load())
	}
	// File requests use the same strict admission, including preservation of
	// native system/custom fields and nested number tags.
	request := dynamoTestFile(t, "point.json", []byte(`{"operation":"get","key":{"id":"a b","partition":"tenant-a"}}`))
	cmd := queryCosmosDBCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", config, "--request", request})
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), `"_etag":"native-revision"`) || !strings.Contains(out.String(), `"value":"-1"`) || !strings.Contains(out.String(), `\u003cnative\u003e\u0026`) {
		t.Fatal("native document output changed")
	}
}

func TestCosmosSignalContextCleanupAndCommandReuse(t *testing.T) {
	previous := interruptContext
	defer func() { interruptContext = previous }()
	parent := context.Background()
	stopped := false
	var child context.Context
	interruptContext = func(given context.Context) (context.Context, func() os.Signal, func()) {
		if given != parent {
			t.Error("stale caller context retained")
		}
		var cancel context.CancelFunc
		child, cancel = context.WithCancel(given)
		return child, func() os.Signal { return nil }, func() { stopped = true; cancel() }
	}
	cmd := queryCosmosDBCommand()
	cmd.SetContext(parent)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	for range 2 {
		stopped = false
		if err := cmd.Execute(); err == nil || !stopped || child.Err() == nil || cmd.Context() != parent {
			t.Fatal("signal scope not restored")
		}
	}
	bare := queryCosmosDBCommand()
	stopped = false
	if err := bare.RunE(bare, nil); err == nil || !stopped || child.Err() == nil || bare.Context() != nil {
		t.Fatal("nil caller context not restored")
	}
}

func TestQueryCosmosDispatchedCancellationDeadlineAndReopen(t *testing.T) {
	t.Setenv("DATATUG_COSMOS_READ_FIXTURE", cosmosFixtureToken)
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "deadline"}[deadline], func(t *testing.T) {
			dispatched, disconnected, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var slow atomic.Bool
			slow.Store(true)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if slow.Swap(false) {
					if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, (64<<10)+1)); err != nil {
						t.Error("owned request drain failed")
						return
					}
					close(dispatched)
					select {
					case <-r.Context().Done():
						close(disconnected)
					case <-cleanup:
					}
					return
				}
				cosmosTestResponse(t, w, cosmosFixtureDocument)
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(cleanup) })
			config := cosmosTestConfig(t, server)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := "2s"
			if deadline {
				timeout = "500ms"
			}
			type outcome struct {
				out, diagnostic string
				err             error
			}
			done := make(chan outcome, 1)
			go func() {
				out, diag, err := cosmosTestCommand(t, ctx, config, `{"operation":"get","key":{"id":"a b","partition":"tenant-a"}}`, timeout)
				done <- outcome{out, diag, err}
			}()
			select {
			case <-dispatched:
			case <-time.After(2 * time.Second):
				t.Fatal("native request did not dispatch")
			}
			if !deadline {
				cancel()
			}
			select {
			case result := <-done:
				label := "cancelled"
				if deadline {
					label = "deadline exceeded"
				}
				if result.err == nil || result.out != "" || !strings.Contains(result.err.Error(), label) || strings.Contains(result.diagnostic, cosmosFixtureToken) {
					t.Fatal("cancellation output/redaction changed")
				}
			case <-time.After(time.Second):
				t.Fatal("caller waited beyond bounded cancellation")
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("owned request did not disconnect")
			}
			out, _, err := cosmosTestCommand(t, context.Background(), config, `{"operation":"get","key":{"id":"a b","partition":"tenant-a"}}`, "2s")
			if err != nil || out == "" {
				t.Fatal("subsequent read failed")
			}
		})
	}
}

func TestCosmosAuthorizationUsesTypedRotatingEnvironment(t *testing.T) {
	c := cosmosNativeConfig{TokenEnv: "DATATUG_COSMOS_READ_FIXTURE", AuthType: "resource"}
	provider := cosmosAuthorization(c)
	t.Setenv(c.TokenEnv, cosmosFixtureToken)
	first, err := provider(context.Background(), dalgo2cosmosdb.AuthRequest{})
	if err != nil {
		t.Fatal("valid resource token refused")
	}
	t.Setenv(c.TokenEnv, "type=resource&ver=1.0&sig=changed")
	second, err := provider(context.Background(), dalgo2cosmosdb.AuthRequest{})
	if err != nil || first == second {
		t.Fatal("environment token was cached")
	}
	for _, value := range []string{"", "type%3Dresource%26secret", "master-key", "type=resource&ver=1.0&sig=bad\nvalue"} {
		t.Setenv(c.TokenEnv, value)
		if _, err := provider(context.Background(), dalgo2cosmosdb.AuthRequest{}); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid typed token accepted/exposed")
		}
	}
	c.AuthType = "aad"
	t.Setenv(c.TokenEnv, "owned.raw.aad-token")
	if _, err := cosmosAuthorization(c)(context.Background(), dalgo2cosmosdb.AuthRequest{}); err != nil {
		t.Fatal("raw AAD token refused")
	}
	t.Setenv(c.TokenEnv, "Bearer secret")
	if _, err := cosmosAuthorization(c)(context.Background(), dalgo2cosmosdb.AuthRequest{}); err == nil {
		t.Fatal("Bearer-prefixed value accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider(ctx, dalgo2cosmosdb.AuthRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal("preabort lost")
	}
}
