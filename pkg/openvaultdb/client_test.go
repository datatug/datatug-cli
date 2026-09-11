package openvaultdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const allowResult = `{"apiVersion":"dtql.org/authorization/v1","requestId":"req-1","mode":"execution","scope":"request","result":"allow","allowed":true,"hypothetical":false,"operations":[{"id":"u1","requestOperationId":"u1","action":"update","resource":{"databaseId":"crm","path":"/customers/101","rowId":"101","columns":[["name"]]},"result":"allow","restrictionIds":[],"allOf":[],"executionClass":"dtql"}],"layers":[{"layerId":"owner","source":{"ownerId":"owner","provider":"ingitdb","databaseId":"crm","kind":"ingitdb"},"aclState":"enabled","result":"allow","decisions":[{"operationId":"u1","result":"allow","scope":"operation","restrictionIds":[]}]}],"blockers":[],"coverage":{"evaluation":"complete","disclosure":"full","truncated":false,"unevaluated":[]},"restrictions":[]}`

func TestClientQueryUsesConfiguredTargetAndBearer(t *testing.T) {
	var gotPath, gotAuth, gotType, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotType = r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))
	defer upstream.Close()

	response, err := (Client{}).Query(context.Background(), Target{
		BaseURL: upstream.URL, DatabaseID: "crm east", Token: "very-secret",
	}, []byte("from: {name: customers}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || gotPath != "/v1/databases/crm%20east/dtql" ||
		gotAuth != "Bearer very-secret" || gotType != "application/yaml" ||
		gotBody != "from: {name: customers}\n" {
		t.Fatalf("unexpected request/response: status=%d path=%q auth=%q type=%q body=%q", response.StatusCode, gotPath, gotAuth, gotType, gotBody)
	}
}

func TestClientCancellationAndErrorsDoNotExposeToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Client{}).Query(ctx, Target{BaseURL: "http://127.0.0.1:1", DatabaseID: "crm", Token: "do-not-leak"}, []byte("x"))
	if err == nil || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("error must be safe and report cancellation: %v", err)
	}
}

func TestTargetRejectsURLAuthorityFeatures(t *testing.T) {
	for _, base := range []string{"file:///tmp/db", "https://user:pass@example.test", "https://example.test?next=http://evil", "https://example.test/#secret"} {
		if err := (Target{BaseURL: base, DatabaseID: "db", Token: "token"}).Validate(); err == nil {
			t.Errorf("accepted unsafe URL %q", base)
		}
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, MaxResponseBytes+1))
	}))
	defer upstream.Close()
	_, err := (Client{}).Query(context.Background(), Target{BaseURL: upstream.URL, DatabaseID: "db", Token: "token"}, []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected bounded response error, got %v", err)
	}
}

func TestClientRejectsRedirectWithoutForwardingBearer(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	_, err := (Client{}).Query(context.Background(), Target{BaseURL: upstream.URL, DatabaseID: "db", Token: "secret"}, []byte("x"))
	if err == nil || redirected {
		t.Fatalf("redirect must fail closed without reaching destination: err=%v redirected=%v", err, redirected)
	}
}

func TestClientRejectsDuplicateOrNonJSONResponse(t *testing.T) {
	for _, response := range []struct{ contentType, body string }{
		{"text/html", "<html>secret diagnostic</html>"},
		{"application/json", `{"records":[],"records":[]}`},
	} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", response.contentType)
			_, _ = w.Write([]byte(response.body))
		}))
		_, err := (Client{}).Query(context.Background(), Target{BaseURL: upstream.URL, DatabaseID: "db", Token: "token"}, []byte("x"))
		upstream.Close()
		if err == nil || strings.Contains(err.Error(), "secret diagnostic") {
			t.Fatalf("unsafe response accepted/reflected: %v", err)
		}
	}
}

func TestClientUpdateRequiresCompletedExecutionAllow(t *testing.T) {
	for _, result := range []struct {
		name string
		body string
		ok   bool
	}{
		{"execution allow", allowResult, true},
		{"inspect allow", strings.Replace(allowResult, `"mode":"execution"`, `"mode":"inspect"`, 1), false},
		{"duplicate nested key", strings.Replace(allowResult, `"requestId":"req-1"`, `"requestId":"req-1","requestId":"req-2"`, 1), false},
	} {
		t.Run(result.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"authorization":` + result.body + `,"dataRevision":"r2"}`))
			}))
			defer upstream.Close()
			_, err := (Client{}).Update(context.Background(), Target{BaseURL: upstream.URL, DatabaseID: "crm", Token: "token"}, "customers", "101", []byte(`{"action":"update"}`))
			if (err == nil) != result.ok {
				t.Fatalf("err=%v, want success=%v", err, result.ok)
			}
		})
	}
}

func TestClientBoundsDeadlineAndSanitizesTransportError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 2100*time.Millisecond {
			t.Fatalf("dry-run deadline was not bounded: %v %v", deadline, ok)
		}
		return nil, errors.New("transport mentioned do-not-leak and https://secret.example")
	})
	_, err := (Client{HTTP: &http.Client{Transport: transport}}).Explain(context.Background(),
		Target{BaseURL: "https://vault.example", DatabaseID: "db", Token: "do-not-leak"}, []byte(`{}`))
	if err == nil || strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), "secret.example") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestAuthorizationErrorEnvelope(t *testing.T) {
	denial := strings.ReplaceAll(allowResult, `"allow"`, `"deny"`)
	denial = strings.ReplaceAll(denial, `"allowed":true`, `"allowed":false`)
	denial = strings.ReplaceAll(denial, `"blockers":[]`, `"blockers":[{"operationId":"u1","code":"ACCESS_DENIED","scope":"operation","layerId":"owner"}]`)
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"owner error", `{"error":{"code":"access_denied","authorization":` + denial + `}}`, true},
		{"ambiguous error", `{"authorization":` + denial + `,"error":{"authorization":` + denial + `}}`, false},
		{"allowed error", `{"error":{"authorization":` + allowResult + `}}`, false},
		{"missing authorization", `{"error":{"code":"access_denied"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _, err := ValidateAuthorizationEnvelope([]byte(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("validation=%v", err)
			}
			if tc.valid && (result.Allowed || len(result.Blockers) != 1 || result.Blockers[0].LayerID != "owner") {
				t.Fatal("owner denial lost")
			}
		})
	}
}
