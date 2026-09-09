package endpoints

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPing(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ping", nil)
	Ping(w, r)
	if got := w.Body.String(); got != "pong" {
		t.Errorf("Ping body = %q, want %q", got, "pong")
	}
}

func TestAgentInfo(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/agent-info", nil)
	AgentInfo(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("AgentInfo status = %d, want 200", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Error("AgentInfo body should not be empty")
	}
}

// TestAgentInfo_SuccessCarriesCORSOrigin covers S87 Fix 2: writeContractResponse's
// success branch used to skip Access-Control-Allow-Origin entirely (only its
// error branch, writeContractError, set it) — agent-info always succeeds, so
// a real browser (unlike S77's own curl-based reproduction, which never
// exercises CORS at all) blocked every agent-info response outright as an
// opaque CORS failure, distinct in shape from every other request's ordinary
// HttpErrorResponse (S85's finding).
func TestAgentInfo_SuccessCarriesCORSOrigin(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/agent-info", nil)
	r.Header.Set("Origin", "http://localhost:4200")
	AgentInfo(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:4200" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:4200")
	}
}
