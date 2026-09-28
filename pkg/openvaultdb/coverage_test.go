package openvaultdb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
)

type errReader struct{}

func (errReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("read failure")
}

type unsuppQuery struct {
	dal.Query
}

func TestTarget_Validate_EdgeCases(t *testing.T) {
	targets := []Target{
		{BaseURL: "http://example.com", DatabaseID: "crm/bad", Token: "tok"},
		{BaseURL: "http://example.com", DatabaseID: "crm\\bad", Token: "tok"},
		{BaseURL: "http://example.com", DatabaseID: "crm?bad", Token: "tok"},
		{BaseURL: "http://example.com", DatabaseID: "crm#bad", Token: "tok"},
		{BaseURL: "http://example.com", DatabaseID: " crm ", Token: "tok"},
		{BaseURL: "http://example.com", DatabaseID: "", Token: "tok"},
		{BaseURL: "http://example.com", DatabaseID: "crm", Token: "   "},
		{BaseURL: "http://example.com", DatabaseID: "crm", Token: ""},
	}
	for _, target := range targets {
		if err := target.Validate(); err == nil {
			t.Fatalf("expected validation error for %+v", target)
		}
	}
}

func TestClient_Evidence_And_WithMaxDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))
	defer server.Close()

	c := Client{}
	target := Target{BaseURL: server.URL, DatabaseID: "crm", Token: "tok"}

	// Test Evidence
	resp, err := c.Evidence(context.Background(), target, []byte("{}"))
	if err != nil {
		t.Fatalf("Evidence failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Evidence StatusCode = %d, want 200", resp.StatusCode)
	}

	// Test withMaxDeadline where existing deadline is shorter than maximum
	shortCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	gotCtx, gotCancel := withMaxDeadline(shortCtx, 10*time.Second)
	defer gotCancel()
	d1, _ := shortCtx.Deadline()
	d2, _ := gotCtx.Deadline()
	if d1 != d2 {
		t.Fatalf("withMaxDeadline should have preserved deadline")
	}
}

func TestClient_Do_Errors(t *testing.T) {
	c := Client{}
	validTarget := Target{BaseURL: "http://localhost:8080", DatabaseID: "crm", Token: "tok"}

	// Target validation error
	if _, err := c.do(context.Background(), Target{}, "POST", "/p", "text/plain", []byte("a"), 0); err == nil {
		t.Fatal("expected target validate error")
	}

	// Body size errors
	if _, err := c.do(context.Background(), validTarget, "POST", "/p", "text/plain", nil, 0); err == nil {
		t.Fatal("expected empty body error")
	}
	if _, err := c.do(context.Background(), validTarget, "POST", "/p", "text/plain", make([]byte, MaxRequestBytes+1), 0); err == nil {
		t.Fatal("expected max body size error")
	}

	// Invalid HTTP method
	if _, err := c.do(context.Background(), validTarget, "INVALID METHOD\n", "/p", "text/plain", []byte("a"), 0); err == nil {
		t.Fatal("expected invalid method error")
	}

	// Read error on response body
	cErr := Client{
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(errReader{}),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}
	if _, err := cErr.do(context.Background(), validTarget, "POST", "/p", "text/plain", []byte("a"), 0); err == nil {
		t.Fatal("expected body read error")
	}
}

func TestClient_ValidationErrors(t *testing.T) {
	validTarget := Target{BaseURL: "http://localhost:8080", DatabaseID: "crm", Token: "tok"}

	// Validation == 1 (Explain) with invalid auth result
	c1 := Client{
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader(`{"records":[]}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			}),
		},
	}
	if _, err := c1.Explain(context.Background(), validTarget, []byte("{}")); err == nil {
		t.Fatal("expected validation error for explain")
	}

	// Error status with invalid authorization envelope (e.g. status 400 with allowed=true)
	c2 := Client{
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 400,
					Body:       io.NopCloser(strings.NewReader(`{"authorization":` + allowResult + `}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			}),
		},
	}
	if _, err := c2.do(context.Background(), validTarget, "POST", "/p", "application/json", []byte("{}"), 0); err == nil {
		t.Fatal("expected error on 400 with allowed result")
	}

	// Error status with nested error.authorization invalid
	c3 := Client{
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 400,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"authorization":{}}}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
				}, nil
			}),
		},
	}
	if _, err := c3.do(context.Background(), validTarget, "POST", "/p", "application/json", []byte("{}"), 0); err == nil {
		t.Fatal("expected error on 400 with invalid error.authorization")
	}
}

func TestValidateAuthorizationEnvelope_Errors(t *testing.T) {
	// Not JSON
	if _, _, err := ValidateAuthorizationEnvelope([]byte("bad json")); err == nil {
		t.Fatal("expected error for bad json")
	}

	// Error field is not an object
	if _, _, err := ValidateAuthorizationEnvelope([]byte(`{"error": "string"}`)); err == nil {
		t.Fatal("expected error for non-object error field")
	}

	// Authorization field is invalid result format
	if _, _, err := ValidateAuthorizationEnvelope([]byte(`{"authorization": "string"}`)); err == nil {
		t.Fatal("expected error for non-object authorization field")
	}

	// dataRevision is not a string
	if _, _, err := ValidateAuthorizationEnvelope([]byte(`{"authorization":` + allowResult + `, "dataRevision": 123}`)); err == nil {
		t.Fatal("expected error for non-string dataRevision")
	}
}

func TestDecodeJSONObjectStrict_And_ScanJSONValue(t *testing.T) {
	// Multiple values
	if err := DecodeJSONObjectStrict([]byte("{} {}"), nil); err == nil {
		t.Fatal("expected error for multiple values")
	}

	// Not an object
	if err := DecodeJSONObjectStrict([]byte("123"), nil); err == nil {
		t.Fatal("expected error for number")
	}

	// Malformed JSON inside object
	if err := DecodeJSONObjectStrict([]byte(`{`), nil); err == nil {
		t.Fatal("expected error for unterminated object")
	}
	if err := DecodeJSONObjectStrict([]byte(`{"k":`), nil); err == nil {
		t.Fatal("expected error for missing value")
	}

	// Malformed JSON inside array (nested object duplicate key)
	if err := DecodeJSONObjectStrict([]byte(`{"k": [{"a": 1, "a": 2}]}`), nil); err == nil {
		t.Fatal("expected error for duplicate key in array")
	}

	// Test scanJSONValue with unsupported delimiter ']'
	d := json.NewDecoder(strings.NewReader("[]"))
	_, _ = d.Token() // consumes '['
	if err := scanJSONValue(d, nil); err == nil || !strings.Contains(err.Error(), "invalid JSON delimiter") {
		t.Fatalf("expected invalid JSON delimiter error, got %v", err)
	}
}

func TestSource_OpenSource_Errors(t *testing.T) {
	// File does not exist
	if _, err := OpenSource("/nonexistent/file.json"); err == nil {
		t.Fatal("expected open error")
	}

	// Type mismatch in config (baseUrl is int)
	dir := t.TempDir()
	path := filepath.Join(dir, "type_mismatch.json")
	if err := os.WriteFile(path, []byte(`{"baseUrl": 123}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSource(path); err == nil {
		t.Fatal("expected type mismatch error")
	}

	// Target Validate fails
	path2 := filepath.Join(dir, "target_bad.json")
	t.Setenv("ACL_TOKEN", "tok")
	t.Setenv("ACL_TOKEN_BASE_URL", "http://example.com")
	t.Setenv("ACL_TOKEN_PRINCIPAL_ID", "alice")
	if err := os.WriteFile(path2, []byte(`{"baseUrl":"http://example.com","databaseId":"crm/bad","tokenEnv":"ACL_TOKEN","principalId":"alice"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSource(path2); err == nil {
		t.Fatal("expected target validation error from OpenSource")
	}
}

func TestSource_UnsupportedMethods(t *testing.T) {
	s := &source{target: Target{DatabaseID: "crm-db"}}
	ctx := context.Background()

	if s.ID() != "crm-db" {
		t.Fatalf("ID = %q, want crm-db", s.ID())
	}
	if adapter := s.Adapter(); adapter.Name() != "openvaultdb" {
		t.Fatalf("adapter = %v", adapter)
	}
	if s.Schema() != nil {
		t.Fatal("expected nil schema")
	}
	if err := s.Get(ctx, nil); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("Get = %v, want dal.ErrNotSupported", err)
	}
	if exists, err := s.Exists(ctx, nil); exists || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("Exists = %v, %v, want false, dal.ErrNotSupported", exists, err)
	}
	if err := s.GetMulti(ctx, nil); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("GetMulti = %v, want dal.ErrNotSupported", err)
	}
	if err := s.RunReadonlyTransaction(ctx, nil); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("RunReadonlyTransaction = %v, want dal.ErrNotSupported", err)
	}
	if err := s.RunReadwriteTransaction(ctx, nil); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("RunReadwriteTransaction = %v, want dal.ErrNotSupported", err)
	}
	if _, err := s.ExecuteQueryToRecordsetReader(ctx, nil); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("ExecuteQueryToRecordsetReader = %v, want dal.ErrNotSupported", err)
	}

	authErr := &AuthorizationError{}
	if authErr.Error() != "OpenVaultDB access denied" {
		t.Fatalf("unexpected authErr: %s", authErr.Error())
	}
	if !errors.Is(authErr.Unwrap(), access.ErrAccessDenied) {
		t.Fatalf("expected Unwrap() to be ErrAccessDenied")
	}

	reader := &sourceReader{}
	if _, err := reader.Cursor(); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("Cursor = %v, want dal.ErrNotSupported", err)
	}
}

func TestSource_ExecuteQueryToRecordsReader_MoreErrors(t *testing.T) {
	ctx := access.WithPrincipal(context.Background(), access.Principal{ID: "alice"})
	s := &source{
		target:      Target{BaseURL: "http://example.com", DatabaseID: "crm", Token: "tok"},
		principalID: "alice",
	}

	// Unsupported query type
	if _, err := s.ExecuteQueryToRecordsReader(ctx, unsuppQuery{}); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("expected dal.ErrNotSupported, got %v", err)
	}

	// Query serialization error: structured query with no from collection
	emptyQuery := dal.From(nil).NewQuery().SelectKeysOnly(reflect.String)
	if _, err := s.ExecuteQueryToRecordsReader(ctx, emptyQuery); err == nil {
		t.Fatal("expected query serialization error")
	}

	// Query network / client error (cancelled context)
	cancCtx, cancel := context.WithCancel(ctx)
	cancel()
	q, _ := dtql.Deserialize([]byte("from: {name: customers}\n"))
	if _, err := s.ExecuteQueryToRecordsReader(cancCtx, q); err == nil {
		t.Fatal("expected network/client error")
	}

	// HTTP 403 with valid authorization denial
	denyResult := `{"apiVersion":"dtql.org/authorization/v1","requestId":"req-1","mode":"execution","scope":"request","result":"deny","allowed":false,"hypothetical":false,"operations":[],"layers":[],"blockers":[],"coverage":{"evaluation":"complete","disclosure":"full","truncated":false,"unevaluated":[]},"restrictions":[]}`
	serverDeny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"authorization":` + denyResult + `}`))
	}))
	defer serverDeny.Close()
	sDeny := &source{
		target:      Target{BaseURL: serverDeny.URL, DatabaseID: "crm", Token: "tok"},
		principalID: "alice",
	}
	var authErr *AuthorizationError
	if _, err := sDeny.ExecuteQueryToRecordsReader(ctx, q); !errors.As(err, &authErr) {
		t.Fatalf("expected AuthorizationError, got %v", err)
	}

	// HTTP 403 with non-authorization body
	serverForbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer serverForbidden.Close()
	sForbidden := &source{
		target:      Target{BaseURL: serverForbidden.URL, DatabaseID: "crm", Token: "tok"},
		principalID: "alice",
	}
	if _, err := sForbidden.ExecuteQueryToRecordsReader(ctx, q); !errors.Is(err, access.ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}

	// HTTP 500 error
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server500.Close()
	s500 := &source{
		target:      Target{BaseURL: server500.URL, DatabaseID: "crm", Token: "tok"},
		principalID: "alice",
	}
	if _, err := s500.ExecuteQueryToRecordsReader(ctx, q); err == nil || !strings.Contains(err.Error(), "OpenVaultDB query unavailable") {
		t.Fatalf("expected query unavailable error, got %v", err)
	}

	// HTTP 200 with invalid records structure
	serverBadRecords := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records": "not an array"}`))
	}))
	defer serverBadRecords.Close()
	sBadRecords := &source{
		target:      Target{BaseURL: serverBadRecords.URL, DatabaseID: "crm", Token: "tok"},
		principalID: "alice",
	}
	if _, err := sBadRecords.ExecuteQueryToRecordsReader(ctx, q); err == nil || !strings.Contains(err.Error(), "invalid OpenVaultDB records") {
		t.Fatalf("expected invalid OpenVaultDB records error, got %v", err)
	}
}
