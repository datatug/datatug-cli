package commands

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	athenasdk "github.com/dal-go/dalgo2athena"
)

const (
	athenaSessionHost      = "athena.us-east-1.amazonaws.com"
	athenaSessionAccount   = "111111111111"
	athenaSessionWorkGroup = "readers"
	athenaSessionOutput    = "s3://owned-results/athena//"
	athenaSessionQueryID   = "owned-session-query-1"
	athenaSessionSQL       = `SELECT "id", "amount" FROM "AwsDataCatalog"."native"."actual" ORDER BY "id" ASC NULLS LAST OFFSET 0 ROWS LIMIT 3`
)

var athenaSessionCredential = aws.Credentials{
	AccessKeyID:     "OWNSESSIONACCESS1",
	SecretAccessKey: "owned-session-synthetic-secret",
	SessionToken:    "owned-session-synthetic-token",
}

type athenaSessionEvent struct {
	op   string
	body map[string]any
	key  string
}

type athenaSessionFixture struct {
	transport   *http.Transport
	mu          sync.Mutex
	calls       []athenaSessionEvent
	queries     map[string]map[string]any
	tokens      map[string]bool
	connections map[net.Conn]bool
	changed     chan struct{}
}

type athenaSessionHook func(*athenaSessionFixture, http.ResponseWriter, *http.Request, athenaSessionEvent) bool

func (f *athenaSessionFixture) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, event := range f.calls {
		if event.op == op {
			n++
		}
	}
	return n
}

func athenaSessionWrite(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error("owned JSON response write failed")
	}
}

func athenaSessionNewFixture(t *testing.T, hook athenaSessionHook) *athenaSessionFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spec := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{athenaSessionHost},
		Subject:   pkix.Name{CommonName: athenaSessionHost},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, spec, spec, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	f := &athenaSessionFixture{
		queries: map[string]map[string]any{}, tokens: map[string]bool{},
		connections: map[net.Conn]bool{}, changed: make(chan struct{}, 1),
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handle(t, hook, w, r)
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	server.Config.ConnState = func(c net.Conn, state http.ConnState) {
		f.mu.Lock()
		switch state {
		case http.StateNew:
			f.connections[c] = true
		case http.StateClosed, http.StateHijacked:
			delete(f.connections, c)
		}
		f.mu.Unlock()
		select {
		case f.changed <- struct{}{}:
		default:
		}
	}
	server.StartTLS()
	dialer := &net.Dialer{Timeout: time.Second}
	f.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != athenaSessionHost+":443" {
				return nil, errors.New("unexpected owned authority")
			}
			return dialer.DialContext(ctx, network, server.Listener.Addr().String())
		},
		TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, NextProtos: []string{"http/1.1"}},
		DisableCompression: true, DisableKeepAlives: true,
		ForceAttemptHTTP2: false, MaxConnsPerHost: 1,
	}
	t.Cleanup(func() {
		f.transport.CloseIdleConnections()
		server.Close()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			f.mu.Lock()
			n := len(f.connections)
			f.mu.Unlock()
			if n == 0 {
				return
			}
			select {
			case <-f.changed:
			case <-timer.C:
				t.Error("owned TLS sockets did not close")
				return
			}
		}
	})
	return f
}

func (f *athenaSessionFixture) handle(t *testing.T, hook athenaSessionHook, w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	_ = r.Body.Close()
	if err != nil || len(raw) > 65536 {
		t.Error("owned request body is not bounded")
		w.WriteHeader(400)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || r.Host != athenaSessionHost ||
		r.TLS == nil || r.ProtoMajor != 1 || !r.Close ||
		r.Header.Get("Accept-Encoding") != "identity" ||
		r.Header.Get("Content-Type") != "application/x-amz-json-1.1" ||
		!strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonAthena.") {
		t.Error("owned signed HTTPS profile mismatch")
		w.WriteHeader(400)
		return
	}
	if !athenaSessionSigned(t, r, raw) {
		w.WriteHeader(403)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var body map[string]any
	var extra any
	if err = decoder.Decode(&body); err != nil || body == nil || decoder.Decode(&extra) != io.EOF {
		t.Error("owned request JSON is not one bounded object")
		w.WriteHeader(400)
		return
	}
	event := athenaSessionEvent{
		op:   strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonAthena."),
		body: body, key: athenaSessionCredential.AccessKeyID,
	}
	f.mu.Lock()
	f.calls = append(f.calls, event)
	f.mu.Unlock()
	if hook != nil && hook(f, w, r, event) {
		return
	}
	f.respond(t, w, event)
}

func athenaSessionSigned(t *testing.T, r *http.Request, raw []byte) bool {
	t.Helper()
	const prefix = "AWS4-HMAC-SHA256 "
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) {
		t.Error("official SigV4 authorization is missing")
		return false
	}
	parts := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(authorization, prefix), ", ") {
		if name, value, ok := strings.Cut(part, "="); ok {
			parts[name] = value
		}
	}
	scope := strings.Split(parts["Credential"], "/")
	stamp, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	signedHeaders, signature := parts["SignedHeaders"], parts["Signature"]
	if err != nil || len(scope) != 5 || scope[0] != athenaSessionCredential.AccessKeyID ||
		scope[1] != stamp.UTC().Format("20060102") || scope[2] != "us-east-1" ||
		scope[3] != "athena" || scope[4] != "aws4_request" || signature == "" ||
		!strings.Contains(";"+signedHeaders+";", ";host;") {
		t.Error("official SigV4 credential scope or signed headers changed")
		return false
	}
	if r.Header.Get("X-Amz-Security-Token") != athenaSessionCredential.SessionToken {
		t.Error("official SigV4 session token changed")
		return false
	}
	signed := r.Clone(context.Background())
	signed.URL.Scheme, signed.URL.Host, signed.RequestURI = "https", athenaSessionHost, ""
	signed.Host, signed.Header = athenaSessionHost, make(http.Header)
	for _, name := range strings.Split(signedHeaders, ";") {
		if name != "host" {
			signed.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), r.Header.Values(name)...)
		}
	}
	digest := sha256.Sum256(raw)
	if err = v4.NewSigner().SignHTTP(context.Background(), athenaSessionCredential, signed,
		hex.EncodeToString(digest[:]), "athena", "us-east-1", stamp); err != nil {
		t.Error("official SigV4 signing failed")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(authorization), []byte(signed.Header.Get("Authorization"))) != 1 {
		t.Error("official SigV4 authority changed")
		return false
	}
	return true
}

func athenaSessionStartRequest(t *testing.T, body map[string]any) bool {
	t.Helper()
	return athenaSessionStartRequestExpect(t, body, athenaSessionSQL, nil)
}

func athenaSessionStartRequestExpect(t *testing.T, body map[string]any, expectedSQL string, expectedParams []string) bool {
	t.Helper()
	token, ok := body["ClientRequestToken"].(string)
	contextFields, contextOK := body["QueryExecutionContext"].(map[string]any)
	result, resultOK := body["ResultConfiguration"].(map[string]any)
	encryption, encryptionOK := result["EncryptionConfiguration"].(map[string]any)
	reuse, reuseOK := body["ResultReuseConfiguration"].(map[string]any)
	byAge, byAgeOK := reuse["ResultReuseByAgeConfiguration"].(map[string]any)
	expectedFields := 6
	if expectedParams != nil {
		expectedFields = 7
	}
	if len(body) != expectedFields || !ok || len(token) != 64 || body["QueryString"] != expectedSQL ||
		body["WorkGroup"] != athenaSessionWorkGroup || !contextOK || len(contextFields) != 2 ||
		contextFields["Catalog"] != "AwsDataCatalog" || contextFields["Database"] != "native" ||
		!resultOK || len(result) != 3 || result["OutputLocation"] != athenaSessionOutput ||
		result["ExpectedBucketOwner"] != athenaSessionAccount || !encryptionOK ||
		len(encryption) != 1 || encryption["EncryptionOption"] != "SSE_S3" ||
		!reuseOK || len(reuse) != 1 || !byAgeOK || len(byAge) != 1 || byAge["Enabled"] != false {
		t.Error("Start closed request shape changed")
		return false
	}
	if expectedParams == nil {
		if _, present := body["ExecutionParameters"]; present {
			t.Error("Start invented execution parameters")
			return false
		}
		return true
	}
	parameters, parametersOK := body["ExecutionParameters"].([]any)
	if !parametersOK || len(parameters) != len(expectedParams) {
		t.Error("Start execution parameters changed")
		return false
	}
	for i, parameter := range parameters {
		if parameter != expectedParams[i] {
			t.Error("Start execution parameters changed")
			return false
		}
	}
	return true
}

func (f *athenaSessionFixture) respond(t *testing.T, w http.ResponseWriter, event athenaSessionEvent) {
	fail := func() { w.WriteHeader(400) }
	switch event.op {
	case "GetWorkGroup":
		if len(event.body) != 1 || event.body["WorkGroup"] != athenaSessionWorkGroup {
			t.Error("workgroup request changed")
			fail()
			return
		}
		athenaSessionWrite(t, w, 200, map[string]any{"WorkGroup": map[string]any{
			"Name": athenaSessionWorkGroup, "State": "ENABLED",
			"Configuration": map[string]any{
				"EnforceWorkGroupConfiguration": true, "BytesScannedCutoffPerQuery": 10000000,
				"EngineVersion": map[string]any{"EffectiveEngineVersion": "Athena engine version 3"},
				"ResultConfiguration": map[string]any{
					"OutputLocation": athenaSessionOutput, "ExpectedBucketOwner": athenaSessionAccount,
					"EncryptionConfiguration": map[string]any{"EncryptionOption": "SSE_S3"},
				},
			},
		}})
	case "StartQueryExecution":
		if !athenaSessionStartRequest(t, event.body) {
			fail()
			return
		}
		token := event.body["ClientRequestToken"].(string)
		f.mu.Lock()
		reused := f.tokens[token]
		f.tokens[token] = true
		f.queries[athenaSessionQueryID] = event.body
		f.mu.Unlock()
		if reused {
			t.Error("Start reused a query request token")
		}
		athenaSessionWrite(t, w, 200, map[string]any{"QueryExecutionId": athenaSessionQueryID})
	case "GetQueryExecution":
		if len(event.body) != 1 || event.body["QueryExecutionId"] != athenaSessionQueryID {
			t.Error("query execution request changed")
			fail()
			return
		}
		f.mu.Lock()
		query := f.queries[athenaSessionQueryID]["QueryString"]
		f.mu.Unlock()
		if query != athenaSessionSQL {
			t.Error("query execution lost the submitted query")
			fail()
			return
		}
		athenaSessionWrite(t, w, 200, map[string]any{"QueryExecution": map[string]any{
			"QueryExecutionId": athenaSessionQueryID, "Query": query,
			"WorkGroup":             athenaSessionWorkGroup,
			"QueryExecutionContext": map[string]any{"Catalog": "AwsDataCatalog", "Database": "native"},
			"EngineVersion":         map[string]any{"EffectiveEngineVersion": "Athena engine version 3"},
			"ResultConfiguration": map[string]any{
				"OutputLocation":          athenaSessionOutput + athenaSessionQueryID + ".csv",
				"ExpectedBucketOwner":     athenaSessionAccount,
				"EncryptionConfiguration": map[string]any{"EncryptionOption": "SSE_S3"},
			},
			"Status":     map[string]any{"State": "SUCCEEDED"},
			"Statistics": map[string]any{"DataScannedInBytes": json.Number("9007199254740993")},
		}})
	case "StopQueryExecution":
		if event.key != athenaSessionCredential.AccessKeyID || len(event.body) != 1 ||
			event.body["QueryExecutionId"] != athenaSessionQueryID {
			t.Error("Stop lost captured principal or query identity")
			fail()
			return
		}
		w.WriteHeader(200)
	default:
		t.Error("unexpected native operation")
		fail()
	}
}

func athenaSessionClient(t *testing.T, f *athenaSessionFixture, cfg athenaNativeConfig) (*athenasdk.Client, func()) {
	t.Helper()
	options, err := athenaClientOptions(context.Background(), cfg, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	options.Transport = f.transport
	client, err := athenasdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, f.transport.CloseIdleConnections
}

func athenaSessionRequest(t *testing.T, cfg athenaNativeConfig) admittedAthenaRead {
	t.Helper()
	raw := []byte(`{"operation":"query","collection":"items","projection":["id","amount"],"order":[{"field":"id","direction":"asc"}],"limit":3,"offset":0,"pageSize":2}`)
	request, err := admitAthenaRequest(context.Background(), cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
