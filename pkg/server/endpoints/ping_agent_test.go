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
