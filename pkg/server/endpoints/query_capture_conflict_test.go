package endpoints

import (
	"net/http"
	"testing"
)

// A stale ifMatch is 409 REVISION_CONFLICT on ifMatch with the message of
// datatug-core's frozen fixture error_revision_conflict.json.
func TestCaptureQuery_StaleRevisionMatchesTheConflictFixture(t *testing.T) {
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	req := validCaptureRequest(scope)
	if w := postCapture(t, req); w.Code != http.StatusCreated {
		t.Fatalf("setup capture: status %d; body %s", w.Code, w.Body.String())
	}
	req.IfNoneMatch, req.IfMatch = false, "stale-revision"
	w := postCapture(t, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409; body %s", w.Code, w.Body.String())
	}
	env := decodeCaptureError(t, w)
	if env.Error.Code != "REVISION_CONFLICT" || env.Error.Field != "ifMatch" ||
		env.Error.Message != "the query changed since that revision was read; reload it and retry" {
		t.Errorf("error = %+v, want error_revision_conflict.json's code, field and message", env.Error)
	}
}
