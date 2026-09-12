package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/apicore/verify"
)

// withApicoreHandle installs apicore.Execute, the Handler `datatug serve`
// registers, with the serve auth hook, restoring both afterwards.
func withApicoreHandle(t *testing.T) {
	t.Helper()
	savedHandle, savedCtx, savedHook := handle, getContextFromRequest, apicore.GetAuthTokenFromHttpRequest
	t.Cleanup(func() {
		handle, getContextFromRequest, apicore.GetAuthTokenFromHttpRequest = savedHandle, savedCtx, savedHook
	})
	handle = apicore.Execute
	getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
	apicore.GetAuthTokenFromHttpRequest = api.AuthTokenFromHTTPRequest
}

// withApicoreErrorHandle installs a handle that runs the worker and
// answers its error the way apicore.Execute does once a request is
// verified and decoded: apicore.ReturnJSON, whose httpserver.HandleError
// knows only 400, 401 and 500 and writes the error's own text. It serves
// delete_query here, whose nil verify options apicore.Execute cannot take.
func withApicoreErrorHandle(t *testing.T) {
	t.Helper()
	savedHandle, savedCtx := handle, getContextFromRequest
	t.Cleanup(func() { handle, getContextFromRequest = savedHandle, savedCtx })
	getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
	handle = func(w http.ResponseWriter, r *http.Request, _ apicore.RequestDTO, _ verify.RequestOptions,
		successStatusCode int, getCtx apicore.ContextProvider, worker apicore.Worker) {
		ctx, _ := getCtx(r)
		response, err := worker(ctx)
		apicore.ReturnJSON(ctx, w, r, successStatusCode, err, response)
	}
}

// decodeLegacyErrorBody decodes w's body as a legacyErrorBody, failing on
// anything else.
func decodeLegacyErrorBody(t *testing.T, w *httptest.ResponseRecorder) legacyErrorDetails {
	t.Helper()
	var body legacyErrorBody
	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("body %q is not a legacyErrorBody: %v", w.Body.String(), err)
	}
	if body.Error.RequestID == "" {
		t.Errorf("body %q carries no request ID", w.Body.String())
	}
	return body.Error
}

// assertBodyOmits fails when w's body contains any of leaks.
func assertBodyOmits(t *testing.T, w *httptest.ResponseRecorder, leaks ...string) {
	t.Helper()
	for _, leak := range leaks {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("body %q leaks %q", w.Body.String(), leak)
		}
	}
}

// A legacy access denial reached the client as apicore's 500, its body the
// denial's full text: resource path, policy, rule and role. It is a 403
// ACCESS_DENIED whose fixed message names nothing the request did not.
func TestLegacyQueryWrites_AccessDenialAnswers403(t *testing.T) {
	withApicoreHandle(t)
	scope, _ := captureTestSetup(t, "sam", []string{"support"}, true)
	query := `"query":{"id":"q1","folderPath":"~","title":"q1","type":"SQL"}`
	tests := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		target  string
		body    string
	}{
		{"create_query", createQuery, http.MethodPost, "/datatug/queries/create_query?project=" + scope.Project,
			`{"storage":"local","project":"` + scope.Project + `",` + query + `}`},
		{"update_query", updateQuery, http.MethodPut, "/datatug/queries/update_query?project=" + scope.Project + "&id=q1",
			`{"storage":"local","project":"` + scope.Project + `","ID":"q1",` + query + `}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler(w, httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body)))
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403; body %s", w.Code, w.Body.String())
			}
			got := decodeLegacyErrorBody(t, w)
			if got.Code != "ACCESS_DENIED" || got.Message != "access denied: the serving principal may not save this query" {
				t.Errorf("error %+v, want ACCESS_DENIED with the fixed message", got)
			}
			assertBodyOmits(t, w, "datatug_projects", `"semantic-test"`, "policy", "rule", "role", "support")
		})
	}
	t.Run("delete_query", func(t *testing.T) {
		withApicoreErrorHandle(t)
		denial := &accesspolicies.WriteDeniedError{Policy: "protect-policy", Operation: access.Delete,
			Resource: "/datatug_projects/p1/queries/revenue", Reason: `matched rule "admin/protect-revenue" (deny) via role:admin`}
		handler := legacyQueryDelete(func(context.Context, dto.ProjectItemRef) error { return denial })
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodDelete, "/datatug/queries/delete_query?project="+scope.Project+"&id=Revenue", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("status %d, want 403; body %s", w.Code, w.Body.String())
		}
		got := decodeLegacyErrorBody(t, w)
		if got.Code != "ACCESS_DENIED" || got.Message != "access denied: the serving principal may not delete this query" {
			t.Errorf("error %+v, want ACCESS_DENIED with the fixed message", got)
		}
		assertBodyOmits(t, w, "datatug_projects", "protect", "revenue", "role:admin")
	})
}

// A legacy query write that timed out waiting for the query store answers
// 504, not apicore's indistinct 500.
func TestLegacyQueryWrites_TimeoutAnswers504(t *testing.T) {
	withApicoreErrorHandle(t)
	handler := legacyQueryDelete(func(context.Context, dto.ProjectItemRef) error {
		return fmt.Errorf("the query store stayed busy: %w", context.DeadlineExceeded)
	})
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodDelete, "/datatug/queries/delete_query?id=q", nil))
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504; body %s", w.Code, w.Body.String())
	}
	got := decodeLegacyErrorBody(t, w)
	if got.Code != "TIMEOUT" || got.Message != "the query store stayed busy with another write, so nothing was deleted; retry" {
		t.Errorf("error %+v, want TIMEOUT with the fixed message", got)
	}
}

// Only apicore's 500 is rewritten: a status apicore already chose, such as
// a 400 for a bad request, passes through with its body.
func TestLegacyQueryWriteResponse_RewritesOnly500(t *testing.T) {
	for _, observed := range []error{context.DeadlineExceeded, &accesspolicies.WriteDeniedError{Operation: access.Set}} {
		rec := httptest.NewRecorder()
		w := newLegacyQueryWriteResponse(rec, "queries/create_query", "save", "saved")
		if err := w.observe(observed); err != observed {
			t.Fatalf("observe returned %v, want %v unchanged", err, observed)
		}
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte("bad request")); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusBadRequest || rec.Body.String() != "bad request" {
			t.Errorf("observed %T: got %d %q, want the 400 and its body kept", observed, rec.Code, rec.Body.String())
		}
	}
}

// A store failure's text can hold an absolute server path and OS error
// text, and apicore wrote it verbatim as the 500 body. Every legacy query
// write 500 now carries a fixed message; the detail goes to the agent log.
func TestLegacyQueryWrites_500BodiesAreGeneric(t *testing.T) {
	const storeErr = "remove /Users/operator/projects/secret-project/queries/q1.query.json: operation not permitted"
	t.Run("a store error through apicore", func(t *testing.T) {
		withApicoreErrorHandle(t)
		handler := legacyQueryDelete(func(context.Context, dto.ProjectItemRef) error { return errors.New(storeErr) })
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodDelete, "/datatug/queries/delete_query?id=q1", nil))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500; body %s", w.Code, w.Body.String())
		}
		got := decodeLegacyErrorBody(t, w)
		if got.Code != "INTERNAL" || got.Message != "the query could not be deleted; the agent log has the details under this request ID" {
			t.Errorf("error %+v, want INTERNAL with the fixed message", got)
		}
		assertBodyOmits(t, w, "/Users", "secret-project", "q1.query.json", "operation not permitted")
	})
	t.Run("a 500 the route never observed", func(t *testing.T) {
		// apicore answers 500 on its own when a successful response fails
		// its validation; the route saw no error, and the body is still
		// apicore's text.
		rec := httptest.NewRecorder()
		w := newLegacyQueryWriteResponse(rec, "queries/update_query", "save", "saved")
		_ = w.observe(nil)
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte(`{"error":{"message":"response is not valid: ` + storeErr + `"}}`)); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500", rec.Code)
		}
		got := decodeLegacyErrorBody(t, rec)
		if got.Code != "INTERNAL" || got.Message != "the query could not be saved; the agent log has the details under this request ID" {
			t.Errorf("error %+v, want INTERNAL with the fixed message", got)
		}
		assertBodyOmits(t, rec, "/Users", "response is not valid")
	})
}

// A legacy write that kept losing to other writes of the same query
// answers 409, not apicore's 500.
func TestLegacyQueryWrites_ConflictAnswers409(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/datatug/queries/update_query?id=q1", nil)
	w := newLegacyQueryWriteResponse(rec, "queries/update_query", "save", "saved")
	err := w.observe(fmt.Errorf("saving q1: %w", api.ErrLegacyQueryWriteConflict))
	apicore.ReturnJSON(r.Context(), w, r, http.StatusCreated, err, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	got := decodeLegacyErrorBody(t, rec)
	if got.Code != "REVISION_CONFLICT" || got.Message != "the query kept changing while it was being saved, so nothing was saved; reload it and retry" {
		t.Errorf("error %+v, want REVISION_CONFLICT with the fixed message", got)
	}
}
