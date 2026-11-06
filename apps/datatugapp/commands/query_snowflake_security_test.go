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
	"fmt"
	"io"
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
	snowsdk "github.com/dal-go/dalgo2snowflake"
)

func TestSnowflakeStrictConfigRefusals(t *testing.T) {
	var object map[string]any
	if err := json.Unmarshal([]byte(snowflakeTestConfigJSON), &object); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"unknown-secret", func(v map[string]any) { v["token"] = "SECRET" }},
		{"case", func(v map[string]any) { v["Database"] = v["database"] }},
		{"null", func(v map[string]any) { v["caFile"] = nil }},
		{"missing", func(v map[string]any) { delete(v, "schema") }},
		{"empty-table", func(v map[string]any) { v["tables"] = map[string]any{} }},
		{"bad-table-field", func(v map[string]any) {
			v["tables"] = map[string]any{"users": map[string]any{"Name": "T", "idColumn": "ID"}}
		}},
		{"missing-table-field", func(v map[string]any) { v["tables"] = map[string]any{"users": map[string]any{"name": "T"}} }},
		{"table-null", func(v map[string]any) { v["tables"] = map[string]any{"users": nil} }},
		{"array-tables", func(v map[string]any) { v["tables"] = []string{"users"} }},
		{"bad-alias", func(v map[string]any) {
			v["tables"] = map[string]any{strings.Repeat("a", 129): map[string]any{"name": "T", "idColumn": "ID"}}
		}},
		{"bad-table", func(v map[string]any) {
			v["tables"] = map[string]any{"users": map[string]any{"name": "T\x00", "idColumn": "ID"}}
		}},
		{"bad-env", func(v map[string]any) { v["tokenEnv"] = "SECRET=VALUE" }},
		{"bad-auth", func(v map[string]any) { v["authKind"] = "admin" }},
		{"bad-database", func(v map[string]any) { v["database"] = "" }},
		{"bad-warehouse", func(v map[string]any) { v["warehouse"] = "W\x00" }},
		{"bad-role", func(v map[string]any) { v["role"] = "R\x00" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var clone map[string]any
			data, _ := json.Marshal(object)
			if err := json.Unmarshal(data, &clone); err != nil {
				t.Fatal(err)
			}
			test.change(clone)
			raw, err := json.Marshal(clone)
			if err != nil {
				t.Fatal(err)
			}
			var c snowflakeNativeConfig
			if err = decodeSnowflakeConfig(raw, &c); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
	for _, endpoint := range []string{"http://owned.snowflakecomputing.com", "https://evil.invalid", "https://user:SECRET@owned.snowflakecomputing.com", "https://owned.snowflakecomputing.com:443", "https://owned.snowflakecomputing.com/path", "https://owned.snowflakecomputing.com?token=SECRET", "https://owned.snowflakecomputing.com#fragment", "https://%"} {
		var v map[string]any
		original, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(original, &v); err != nil {
			t.Fatal(err)
		}
		v["endpoint"] = endpoint
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var c snowflakeNativeConfig
		if err = decodeSnowflakeConfig(raw, &c); err == nil {
			t.Fatal("unsafe origin accepted", endpoint)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{"endpoint":"a","endpoint":"b"}`, `{"endpoint":"\ud800"}`, snowflakeTestConfigJSON + ` {}`} {
		var c snowflakeNativeConfig
		if err := decodeSnowflakeConfig([]byte(raw), &c); err == nil {
			t.Fatal("malformed config accepted")
		}
	}
}

func TestSnowflakeStrictRequestAndConditionRefusals(t *testing.T) {
	c := snowflakeTestConfig(t)
	for _, raw := range []string{
		`{"operation":"sql","collection":"users"}`, `{"operation":"metadata","collection":"users"}`, `{"operation":"get","collection":"users","id":"a","sql":"SELECT SECRET"}`, `{"operation":"get","collection":"users","id":null}`, `{"operation":"get","collection":"users","id":1}`, `{"operation":"get","collection":"foreign","id":"a"}`, `{"operation":"get","collection":"users","id":""}`, `{"operation":"getMany","collection":"users","ids":null}`, `{"operation":"getMany","collection":"users"}`, `{"operation":"getMany","collection":"users","ids":["a",""]}`,
		`{"operation":"query","collection":"users"}`, `{"operation":"query","collection":"users","limit":0}`, `{"operation":"query","collection":"users","limit":1001}`, `{"operation":"query","collection":"users","limit":1,"offset":-1}`, `{"operation":"query","collection":"users","limit":1,"offset":1000000001}`, `{"operation":"query","collection":"users","limit":1,"projection":[]}`, `{"operation":"query","collection":"users","limit":1,"projection":["ID","ID"]}`, `{"operation":"query","collection":"users","limit":1,"projection":[""]}`, `{"operation":"query","collection":"users","limit":1,"order":[]}`, `{"operation":"query","collection":"users","limit":1,"order":[{"field":"ID","direction":"DESC"}]}`, `{"operation":"query","collection":"users","limit":1,"order":[{"field":"ID"}]}`, `{"operation":"query","collection":"users","limit":1,"order":[{"field":"ID","direction":"asc","sql":"SECRET"}]}`, `{"operation":"query","collection":"users","limit":1,"order":[{"field":"ID","direction":"asc"},{"field":"ID","direction":"desc"}]}`,
		`{"operation":"query","collection":"users","limit":1,"where":null}`, `{"operation":"query","collection":"users","limit":1,"where":{"field":"ID","operator":"=","value":9007199254740993}}`,
		`{"operation":"get","collection":"users","id":"\ud800"}`, `{"operation":"get","collection":"users","id":"a","ID":"b"}`, `{"operation":"get","collection":"users","id":"a","id":"b"}`, `{"operation":"get","collection":"users","id":"a"} {}`,
	} {
		if _, err := admitSnowflakeRequest(context.Background(), c, []byte(raw)); err == nil {
			t.Fatal("unsafe request accepted", raw)
		}
	}
	for _, condition := range []string{`null`, `[]`, `{}`, `{"and":[]}`, `{"or":null}`, `{"and":[{}],"field":"x"}`, `{"field":"ID","documentId":true,"operator":"=","value":"x"}`, `{"documentId":false,"operator":"=","value":"x"}`, `{"documentId":"true","operator":"=","value":"x"}`, `{"field":"","operator":"=","value":"x"}`, `{"field":"ID","operator":"="}`, `{"field":"ID","operator":"like","value":"x"}`, `{"field":"ID","operator":1,"value":"x"}`, `{"field":"ID","operator":">","value":null}`, `{"field":"ID","operator":"in","value":[]}`, `{"field":"ID","operator":"in","value":[null]}`, `{"field":"ID","operator":"in","value":"a"}`, `{"field":"ID","operator":"=","value":{"Kind":"number","Value":"1"}}`, `{"field":"ID","operator":"=","value":{"kind":"number","value":"NaN"}}`, `{"field":"ID","operator":"=","value":{"kind":"number","value":"1","other":true}}`, `{"field":"ID","operator":"=","value":{"kind":"number","value":1}}`, `{"field":"ID","operator":"=","value":{"kind":"object","value":"1"}}`, `{"field":"ID","operator":"=","value":[]}`} {
		raw := `{"operation":"query","collection":"users","limit":1,"where":` + condition + `}`
		if _, err := admitSnowflakeRequest(context.Background(), c, []byte(raw)); err == nil {
			t.Fatal("unsafe condition accepted", condition)
		}
	}
	deep := `{"field":"x","operator":"=","value":true}`
	for i := 0; i < 10; i++ {
		deep = `{"and":[` + deep + `]}`
	}
	nodes := 0
	if _, err := snowflakeCondition(context.Background(), []byte(deep), 0, &nodes); err == nil {
		t.Fatal("condition depth unbounded")
	}
	members := strings.Repeat(`"x",`, 64) + `"x"`
	nodes = 0
	if _, err := snowflakeCondition(context.Background(), []byte(`{"field":"x","operator":"in","value":[`+members+`]}`), 0, &nodes); err == nil {
		t.Fatal("membership unbounded")
	}
}
func TestSnowflakeConcreteASTPreservesDocumentIDAndExactParameters(t *testing.T) {
	c := snowflakeTestConfig(t)
	request := snowflakeTestRead(t, c, `{"operation":"query","collection":"users","limit":20,"offset":7,"projection":["ID","VALUE"],"where":{"or":[{"documentId":true,"operator":"=","value":"a%2Fb"},{"and":[{"field":"__name__","operator":"!=","value":null},{"field":"VALUE","operator":"in","value":[{"kind":"number","value":"9223372036854775807.0001"},"text"]}]}]},"order":[{"field":"VALUE","direction":"desc"},{"field":"ID","direction":"asc"}]}`)
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
		raw := `{"operation":"query","collection":"users","limit":1,"where":{"field":"VALUE","operator":"` + op + `","value":` + value + `}}`
		if _, err := admitSnowflakeRequest(context.Background(), c, []byte(raw)); err != nil {
			t.Fatal("supported comparison rejected", op, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nodes := 0
	if _, err := snowflakeCondition(ctx, []byte(`{"field":"x","operator":"=","value":true}`), 0, &nodes); !errors.Is(err, context.Canceled) {
		t.Fatal("preabort condition", err)
	}
}

func snowflakeOwnedTLS(t *testing.T, handler http.HandlerFunc) *http.Transport {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "owned.snowflakecomputing.com"}, DNSNames: []string{"owned.snowflakecomputing.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
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
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	dialer := &net.Dialer{Timeout: time.Second}
	return &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "owned.snowflakecomputing.com:443" {
			return nil, errors.New("foreign fixture authority")
		}
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}}
}

const snowflakeOwnedHandle = "11111111-2222-3333-4444-555555555555"

func snowflakeOwnedPayload(id string) map[string]any {
	return map[string]any{"statementHandle": snowflakeOwnedHandle, "resultSetMetaData": map[string]any{"numRows": 1, "rowType": []any{map[string]any{"name": "ID", "type": "text"}, map[string]any{"name": "N", "type": "fixed", "precision": 38, "scale": 9, "nullable": nil}, map[string]any{"name": "NIL", "type": "text"}}, "partitionInfo": []any{map[string]any{"rowCount": 1}}}, "data": [][]any{{id, "9223372036854775807.123456789", nil}}}
}
func TestSnowflakePublicSDKFactoryUsesClosedSQLAndOperationENV(t *testing.T) {
	t.Setenv("DALGO_SNOWFLAKE_READ_TEST", "owned-oauth")
	c := snowflakeTestConfig(t)
	var tokens, kinds []string
	var lock sync.Mutex
	transport := snowflakeOwnedTLS(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Statement string
			Bindings  map[string]struct{ Type, Value string }
			Timeout   int
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/api/v2/statements" || !strings.Contains(body.Statement, `WHERE "ID" = ? LIMIT 2`) || body.Bindings["1"].Type != "TEXT" || body.Bindings["1"].Value != "a%2Fb" || body.Timeout <= 0 {
			t.Error("closed native SQL/bindings changed")
		}
		lock.Lock()
		tokens = append(tokens, r.Header.Get("Authorization"))
		kinds = append(kinds, r.Header.Get("X-Snowflake-Authorization-Token-Type"))
		lock.Unlock()
		if err := json.NewEncoder(w).Encode(snowflakeOwnedPayload("a%2Fb")); err != nil {
			t.Error(err)
		}
	})
	for _, kind := range []string{"oauth", "keypairJwt"} {
		c.AuthKind = kind
		if kind == "keypairJwt" {
			t.Setenv("DALGO_SNOWFLAKE_READ_TEST", "owned-jwt")
		}
		options, err := snowflakeClientOptions(context.Background(), c, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		client, err := newSnowflakeNativeClient(c.Endpoint, options, transport)
		if err != nil {
			t.Fatal(err)
		}
		r := snowflakeTestRead(t, c, `{"operation":"get","collection":"users","id":"a%2Fb"}`)
		var out strings.Builder
		if err = deliverSnowflakeRead(context.Background(), &out, client, transport.CloseIdleConnections, r); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "9223372036854775807.123456789") {
			t.Fatal("native SQL string coerced")
		}
	}
	lock.Lock()
	defer lock.Unlock()
	if strings.Join(tokens, ",") != "Bearer owned-oauth,Bearer owned-jwt" || strings.Join(kinds, ",") != "OAUTH,KEYPAIR_JWT" {
		t.Fatal("operation-time ENV/kind changed", tokens, kinds)
	}
}
func TestSnowflakeActualDispatchedCancellationDrainsCapturedCancelBeforeOutput(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "caller"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			c := snowflakeTestConfig(t)
			dispatch, disconnected, cancelled, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			var requests, auth atomic.Int64
			transport := snowflakeOwnedTLS(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if strings.HasSuffix(r.URL.Path, "/cancel") {
					if r.Header.Get("Authorization") != "Bearer captured-1" {
						t.Error("cleanup refreshed submission principal")
					}
					once.Do(func() { close(cancelled) })
					if err := json.NewEncoder(w).Encode(map[string]any{"statementHandle": snowflakeOwnedHandle}); err != nil {
						t.Error(err)
					}
					return
				}
				if r.Method == http.MethodPost {
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Error(err)
					}
					w.WriteHeader(202)
					if err := json.NewEncoder(w).Encode(map[string]any{"statementHandle": snowflakeOwnedHandle}); err != nil {
						t.Error(err)
					}
					return
				}
				close(dispatch)
				select {
				case <-r.Context().Done():
					close(disconnected)
				case <-cleanup:
				}
			})
			t.Cleanup(func() { close(cleanup) })
			options, err := snowflakeClientOptions(context.Background(), c, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			options.PollInterval = 0
			options.ImmediatePolling = true
			options.Token = func(context.Context) (snowsdk.Credential, error) {
				return snowsdk.OAuthToken(fmt.Sprintf("captured-%d", auth.Add(1))), nil
			}
			client, err := newSnowflakeNativeClient(c.Endpoint, options, transport)
			if err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			r := snowflakeTestRead(t, c, `{"operation":"get","collection":"users","id":"a"}`)
			var out strings.Builder
			done := make(chan error, 1)
			go func() { done <- deliverSnowflakeRead(ctx, &out, client, transport.CloseIdleConnections, r) }()
			select {
			case <-dispatch:
			case <-time.After(2 * time.Second):
				t.Fatal("no actual server dispatch")
			}
			start := time.Now()
			if !deadline {
				cancel()
			}
			var result error
			select {
			case result = <-done:
			case <-time.After(2500 * time.Millisecond):
				t.Fatal("bounded shutdown did not finish")
			}
			if result == nil || out.Len() != 0 || time.Since(start) > 2500*time.Millisecond {
				t.Fatal("cancellation prefix/delay", result)
			}
			expected := "cancelled"
			if deadline {
				expected = "deadline"
			}
			if !strings.Contains(result.Error(), expected) {
				t.Fatal("original caller/deadline error changed", result)
			}
			for _, ack := range []<-chan struct{}{disconnected, cancelled} {
				select {
				case <-ack:
				case <-time.After(time.Second):
					t.Fatal("disconnect/owned cancel not observed")
				}
			}
			if requests.Load() != 3 || auth.Load() != 2 {
				t.Fatal("unexpected retry/new cancellation auth", requests.Load(), auth.Load())
			}
		})
	}
}
