package endpoints

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// TestWriteContractError_UnclassifiedErrorIsAFixedMessage: an error no
// contract route classified used to be written to the client as its own
// text. That text is written by whatever failed - a store, a driver, the OS
// - and carries absolute server paths and OS error text. Every route that
// answers through writeContractError shares the fallback: queries/capture,
// exec/run_query and the semantic routes.
func TestWriteContractError_UnclassifiedErrorIsAFixedMessage(t *testing.T) {
	const detail = "open /Users/operator/secret-project/queries/q1.query.json: permission denied"
	w := httptest.NewRecorder()
	writeContractError(w, httptest.NewRequest(http.MethodPost, "/datatug/queries/capture", nil), errors.New(detail))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	env := decodeCaptureError(t, w)
	if env.Error.Code != string(codeInternal) || env.Error.Message != unclassifiedErrorMessage {
		t.Errorf("error = %+v, want %s with the fixed message", env.Error, codeInternal)
	}
	if env.Error.RequestID == "" {
		t.Error("the body carries no request ID to find the error in the log by")
	}
	for _, leak := range []string{"/Users", "secret-project", "q1.query.json", "permission denied"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("body %q leaks %q", w.Body.String(), leak)
		}
	}
}

// A typed contract error still answers with its own code, message and
// field: only the unclassified fallback is fixed.
func TestWriteContractError_TypedErrorsPassThrough(t *testing.T) {
	w := httptest.NewRecorder()
	writeContractError(w, httptest.NewRequest(http.MethodPost, "/datatug/queries/capture", nil),
		newInvalidRequest("query.id", "segment \"q/1\" must not contain any of <>:\"/\\|?*"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	env := decodeCaptureError(t, w)
	if env.Error.Code != string(apicontract.ErrCodeInvalidRequest) || env.Error.Field != "query.id" ||
		!strings.Contains(env.Error.Message, "must not contain any of") {
		t.Errorf("error = %+v, want the typed error unchanged", env.Error)
	}
}
