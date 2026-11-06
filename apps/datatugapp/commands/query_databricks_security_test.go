package commands

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
)

func TestDatabricksStrictConfigRefusals(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal([]byte(databricksTestConfigJSON), &object); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"unknown-secret", func(v map[string]any) { v["token"] = "SECRET" }},
		{"case", func(v map[string]any) { v["Catalog"] = v["catalog"] }},
		{"null", func(v map[string]any) { v["caFile"] = nil }},
		{"missing", func(v map[string]any) { delete(v, "schema") }},
		{"empty-table", func(v map[string]any) { v["tables"] = map[string]any{} }},
		{"bad-table-field", func(v map[string]any) {
			v["tables"] = map[string]any{"users": map[string]any{"Name": "T", "idColumn": "id"}}
		}},
		{"missing-table-field", func(v map[string]any) { v["tables"] = map[string]any{"users": map[string]any{"name": "T"}} }},
		{"table-null", func(v map[string]any) { v["tables"] = map[string]any{"users": nil} }},
		{"array-tables", func(v map[string]any) { v["tables"] = []string{"users"} }},
		{"bad-alias", func(v map[string]any) {
			v["tables"] = map[string]any{strings.Repeat("a", 129): map[string]any{"name": "T", "idColumn": "id"}}
		}},
		{"bad-table", func(v map[string]any) {
			v["tables"] = map[string]any{"users": map[string]any{"name": "T\x00", "idColumn": "id"}}
		}},
		{"bad-env", func(v map[string]any) { v["tokenEnv"] = "SECRET=VALUE" }},
		{"bad-auth", func(v map[string]any) { v["authKind"] = "admin" }},
		{"bad-catalog", func(v map[string]any) { v["catalog"] = "" }},
		{"bad-warehouse", func(v map[string]any) { v["warehouseId"] = "W\x00" }},
		{"bad-schema", func(v map[string]any) { v["schema"] = "S\x00" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			clone := make(map[string]any)
			for k, v := range object {
				clone[k] = v
			}
			test.change(clone)
			raw, err := json.Marshal(clone)
			if err != nil {
				t.Fatal(err)
			}
			var cfg databricksNativeConfig
			if err = decodeDatabricksConfig(raw, &cfg); err == nil {
				t.Fatal("unsafe configuration accepted", test.name)
			}
		})
	}
}

func TestDatabricksEndpointHostValidation(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		valid    bool
	}{
		{"https://adb-1234567890123456.7.azuredatabricks.net", true},
		{"https://dbc-12345678-abcd.cloud.databricks.com", true},
		{"https://1234567890.gcp.databricks.com", true},
		{"https://workspace.corp.internal", true},
		{"http://localhost", true},
		{"http://127.0.0.1", true},
		{"https://evil.attacker.com", false},
		{"ftp://adb-1234.azuredatabricks.net", false},
		{"https://adb-1234.azuredatabricks.net/path", false},
		{"https://adb-1234.azuredatabricks.net?query=1", false},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			cfg := databricksTestConfig(t)
			cfg.Endpoint = test.endpoint
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var decoded databricksNativeConfig
			err = decodeDatabricksConfig(raw, &decoded)
			if test.valid && err != nil {
				t.Fatalf("expected valid endpoint %s, got: %v", test.endpoint, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("expected invalid endpoint %s, but succeeded", test.endpoint)
			}
		})
	}
}

func databricksOwnedTLS(t *testing.T, handler http.HandlerFunc) *http.Transport {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "adb-1234567890123456.7.azuredatabricks.net"},
		DNSNames:              []string{"adb-1234567890123456.7.azuredatabricks.net"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	dialer := &net.Dialer{Timeout: time.Second}
	return &http.Transport{
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != "adb-1234567890123456.7.azuredatabricks.net:443" {
				return nil, errors.New("foreign fixture authority")
			}
			return dialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
}

func TestDatabricksPublicSDKFactoryUsesClosedSQLAndOperationENV(t *testing.T) {
	t.Setenv("DALGO_DATABRICKS_READ_TEST", "owned-token")
	c := databricksTestConfig(t)
	var tokens []string
	var lock sync.Mutex
	transport := databricksOwnedTLS(t, func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		tokens = append(tokens, r.Header.Get("Authorization"))
		lock.Unlock()
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"statement_id": "stmt-owned",
			"status":       map[string]any{"state": "SUCCEEDED"},
			"manifest": map[string]any{
				"schema": map[string]any{
					"columns": []map[string]any{
						{"name": "id", "type_name": "STRING"},
					},
				},
			},
			"result": map[string]any{
				"data_array": [][]any{{"a"}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	options, err := databricksClientOptions(context.Background(), c, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newDatabricksNativeClient(c.Endpoint, options, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	doc, err := client.Get(context.Background(), "users", "a")
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil || doc.ID != "a" {
		t.Fatal("unexpected doc result")
	}

	lock.Lock()
	defer lock.Unlock()
	if len(tokens) == 0 || tokens[0] != "Bearer owned-token" {
		t.Fatalf("unexpected authorization header: %v", tokens)
	}
}

func TestDatabricksAuthKinds(t *testing.T) {
	t.Setenv("DALGO_DATABRICKS_AUTH_TEST", "secret-test-token")
	for _, authKind := range []string{"pat", "oauthM2M", "bearer"} {
		t.Run(authKind, func(t *testing.T) {
			c := databricksTestConfig(t)
			c.AuthKind = authKind
			c.TokenEnv = "DALGO_DATABRICKS_AUTH_TEST"
			options, err := databricksClientOptions(context.Background(), c, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			cred, err := options.Token(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if cred.String() != "[Databricks credential]" {
				t.Fatal("credential disclosed value")
			}
		})
	}
}

func TestDatabricksConcurrencySafety(t *testing.T) {
	t.Setenv("DALGO_DATABRICKS_READ_TEST", "concurrent-token")
	c := databricksTestConfig(t)
	var active int32
	var maxActive int32
	transport := databricksOwnedTLS(t, func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"statement_id": "stmt-conc",
			"status":       map[string]any{"state": "SUCCEEDED"},
			"manifest": map[string]any{
				"schema": map[string]any{
					"columns": []map[string]any{
						{"name": "id", "type_name": "STRING"},
					},
				},
			},
			"result": map[string]any{
				"data_array": [][]any{{"a"}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	options, err := databricksClientOptions(context.Background(), c, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newDatabricksNativeClient(c.Endpoint, options, transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.Exists(context.Background(), "users", "a")
		}()
	}
	wg.Wait()
}

func TestDatabricksUnsafeRequestsAndConditionRefusals(t *testing.T) {
	c := databricksTestConfig(t)
	for _, raw := range []string{
		`{"operation":"unknown","collection":"users"}`,
		`{"operation":"get","collection":"missing","id":"a"}`,
		`{"operation":"get","collection":"users","id":""}`,
		`{"operation":"getMany","collection":"users","ids":null}`,
		`{"operation":"getMany","collection":"users","ids":[""]}`,
		`{"operation":"getMany","collection":"users","ids":[` + strings.Repeat(`"a",`, 101) + `"a"]}`,
		`{"operation":"query","collection":"users"}`,
		`{"operation":"query","collection":"users","limit":0}`,
		`{"operation":"query","collection":"users","limit":1001}`,
		`{"operation":"query","collection":"users","limit":1,"offset":-1}`,
		`{"operation":"query","collection":"users","limit":1,"projection":[]}`,
		`{"operation":"query","collection":"users","limit":1,"projection":[""]}`,
		`{"operation":"query","collection":"users","limit":1,"projection":["a","a"]}`,
		`{"operation":"query","collection":"users","limit":1,"order":[]}`,
		`{"operation":"query","collection":"users","limit":1,"order":[{"field":"","direction":"asc"}]}`,
		`{"operation":"query","collection":"users","limit":1,"order":[{"field":"id"}]}`,
		`{"operation":"query","collection":"users","limit":1,"order":[{"field":"id","direction":"invalid"}]}`,
		`{"operation":"query","collection":"users","limit":1,"order":[{"field":"id","direction":"asc"},{"field":"id","direction":"desc"}]}`,
		`{"operation":"query","collection":"users","limit":1,"where":null}`,
		`{"operation":"query","collection":"users","limit":1,"where":{"field":"id","operator":"=","value":9007199254740993}}`,
		`{"operation":"get","collection":"users","id":"\ud800"}`,
		`{"operation":"get","collection":"users","id":"a","id":"b"}`,
	} {
		if _, err := admitDatabricksRequest(context.Background(), c, []byte(raw)); err == nil {
			t.Fatal("unsafe request accepted", raw)
		}
	}
	for _, condition := range []string{
		`null`, `[]`, `{}`, `{"and":[]}`, `{"or":null}`, `{"and":[{}],"field":"x"}`,
		`{"field":"id","documentId":true,"operator":"=","value":"x"}`,
		`{"documentId":false,"operator":"=","value":"x"}`,
		`{"documentId":"true","operator":"=","value":"x"}`,
		`{"field":"","operator":"=","value":"x"}`,
		`{"field":"id","operator":"="}`,
		`{"field":"id","operator":"like","value":"x"}`,
		`{"field":"id","operator":1,"value":"x"}`,
		`{"field":"id","operator":">","value":null}`,
		`{"field":"id","operator":"in","value":[]}`,
		`{"field":"id","operator":"in","value":[null]}`,
		`{"field":"id","operator":"in","value":"a"}`,
		`{"field":"id","operator":"=","value":{"Kind":"number","Value":"1"}}`,
		`{"field":"id","operator":"=","value":{"kind":"number","value":"NaN"}}`,
		`{"field":"id","operator":"=","value":{"kind":"number","value":"1","other":true}}`,
		`{"field":"id","operator":"=","value":{"kind":"number","value":1}}`,
		`{"field":"id","operator":"=","value":{"kind":"object","value":"1"}}`,
		`{"field":"id","operator":"=","value":[]}`,
	} {
		raw := `{"operation":"query","collection":"users","limit":1,"where":` + condition + `}`
		if _, err := admitDatabricksRequest(context.Background(), c, []byte(raw)); err == nil {
			t.Fatal("unsafe condition accepted", condition)
		}
	}
	deep := `{"field":"x","operator":"=","value":true}`
	for i := 0; i < 10; i++ {
		deep = `{"and":[` + deep + `]}`
	}
	nodes := 0
	if _, err := databricksCondition(context.Background(), []byte(deep), 0, &nodes); err == nil {
		t.Fatal("condition depth unbounded")
	}
	members := strings.Repeat(`"x",`, 64) + `"x"`
	nodes = 0
	if _, err := databricksCondition(context.Background(), []byte(`{"field":"x","operator":"in","value":[`+members+`]}`), 0, &nodes); err == nil {
		t.Fatal("membership unbounded")
	}
}

func TestDatabricksConcreteASTPreservesDocumentIDAndExactParameters(t *testing.T) {
	c := databricksTestConfig(t)
	request := databricksTestRead(t, c, `{"operation":"query","collection":"users","limit":20,"offset":7,"projection":["id","value"],"where":{"or":[{"documentId":true,"operator":"=","value":"a%2Fb"},{"and":[{"field":"__name__","operator":"!=","value":null},{"field":"value","operator":"in","value":[{"kind":"number","value":"9223372036854775807.0001"},"text"]}]}]},"order":[{"field":"value","direction":"desc"},{"field":"id","direction":"asc"}]}`)
	q := request.query.(dal.StructuredQuery)
	if q.Limit() != 20 || q.Offset() != 7 || len(q.Columns()) != 2 || len(q.OrderBy()) != 2 {
		t.Fatal("AST limits/projection/order")
	}
	where := q.Where().(dal.GroupCondition)
	first := where.Conditions()[0].(dal.Comparison)
	if !first.Left.(dal.FieldRef).IsID() || first.Right.(dal.Constant).Value != "a%2Fb" {
		t.Fatal("document ID marker not native")
	}
	nested := where.Conditions()[1].(dal.GroupCondition)
	literal := nested.Conditions()[0].(dal.Comparison)
	if literal.Left.(dal.FieldRef).IsID() || literal.Left.(dal.FieldRef).Name() != "__name__" {
		t.Fatal("literal __name__ rewritten")
	}
	membership := nested.Conditions()[1].(dal.Comparison).Right.(dal.Array).Value.([]any)
	if membership[0].(json.Number).String() != "9223372036854775807.0001" {
		t.Fatal("numeric parameter lost precision")
	}
	for _, op := range []string{"=", "!=", ">", ">=", "<", "<=", "notIn"} {
		value := `{"kind":"number","value":"1.2300e+04"}`
		if op == "notIn" {
			value = "[" + value + "]"
		}
		raw := `{"operation":"query","collection":"users","limit":1,"where":{"field":"value","operator":"` + op + `","value":` + value + `}}`
		if _, err := admitDatabricksRequest(context.Background(), c, []byte(raw)); err != nil {
			t.Fatal("supported comparison rejected", op, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nodes := 0
	if _, err := databricksCondition(ctx, []byte(`{"field":"x","operator":"=","value":true}`), 0, &nodes); !errors.Is(err, context.Canceled) {
		t.Fatal("preabort condition", err)
	}
}
