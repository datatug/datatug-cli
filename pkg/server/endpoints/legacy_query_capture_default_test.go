//go:build !datatug_query_capture

package endpoints

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// This build's datatug-core has no capture field: the block is dropped when
// the body decodes, the query is saved, and neither its file nor the
// response carries a capture.
func assertClientCaptureOutcome(t *testing.T, w *httptest.ResponseRecorder, queryFile string) {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201; body %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(queryFile)
	if err != nil {
		t.Fatalf("the query was not saved: %v", err)
	}
	for _, where := range map[string]string{"file": string(data), "response": w.Body.String()} {
		if strings.Contains(where, "capture") || strings.Contains(where, "mallory") {
			t.Errorf("%q carries the client's capture block", where)
		}
	}
}
