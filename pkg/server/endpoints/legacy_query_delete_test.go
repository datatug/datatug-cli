package endpoints

import (
	"context"
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
)

// TestLegacyDeleteQuery_ThroughApicoreVerification drives DELETE
// queries/delete_query through the real request lifecycle - apicore.Execute
// verifying the request before the worker runs, exactly as `datatug serve`
// registers it (RegisterDatatugHandlersWithCapabilities is given
// apicore.Execute as its handler) - rather than through a test handle that
// skips verification.
//
// deleteProjItem used to pass nil verify options, and apicore.Execute calls
// options.MinimumContentLength() on them before anything else: every real
// delete_query request panicked in the handler goroutine, which
// net/http turns into a closed connection with no response at all. The
// route's 403, 504 and 500 answers were unreachable in production and only
// ever exercised through a stub handle.
func TestLegacyDeleteQuery_ThroughApicoreVerification(t *testing.T) {
	withApicoreHandle(t)
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	target := "/datatug/queries/delete_query?project=" + scope.Project + "&id=q1"

	t.Run("a delete that succeeds", func(t *testing.T) {
		var got dto.ProjectItemRef
		handler := legacyQueryDelete(func(_ context.Context, ref dto.ProjectItemRef) error {
			got = ref
			return nil
		})
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodDelete, target, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d, want 200; body %s", w.Code, w.Body.String())
		}
		if body := strings.TrimSpace(w.Body.String()); body != "{}" {
			t.Errorf("body %q, want an empty JSON object: apicore panics on a nil response with a 200", body)
		}
		if got.ProjectID != scope.Project || got.ID != "q1" {
			t.Errorf("the worker got ref %+v, want project %q and id %q", got, scope.Project, "q1")
		}
	})

	denial := &accesspolicies.WriteDeniedError{Policy: "protect-policy", Operation: access.Delete,
		Resource: "/datatug_projects/p1/queries/revenue", Reason: `matched rule "admin/protect-revenue" (deny) via role:admin`}
	answers := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
		leaks   []string
	}{
		{"an access denial", denial, http.StatusForbidden, "ACCESS_DENIED",
			"access denied: the serving principal may not delete this query",
			[]string{"datatug_projects", "protect", "revenue", "role:admin"}},
		{"a store that stayed locked", fmt.Errorf("the query store stayed busy: %w", context.DeadlineExceeded),
			http.StatusGatewayTimeout, "TIMEOUT",
			"the query store stayed busy with another write, so nothing was deleted; retry", nil},
		{"a store failure", errors.New("remove /Users/operator/secret-project/queries/q1.query.json: operation not permitted"),
			http.StatusInternalServerError, "INTERNAL",
			"the query could not be deleted; the agent log has the details under this request ID",
			[]string{"/Users", "secret-project", "operation not permitted"}},
		{"a write conflict", fmt.Errorf("deleting q1: %w", api.ErrLegacyQueryWriteConflict),
			http.StatusConflict, string(codeRevisionConflict),
			"the query kept changing while it was being deleted, so nothing was deleted; reload it and retry", nil},
	}
	for _, answer := range answers {
		t.Run(answer.name, func(t *testing.T) {
			handler := legacyQueryDelete(func(context.Context, dto.ProjectItemRef) error { return answer.err })
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest(http.MethodDelete, target, nil))
			if w.Code != answer.status {
				t.Fatalf("status %d, want %d; body %s", w.Code, answer.status, w.Body.String())
			}
			got := decodeLegacyErrorBody(t, w)
			if got.Code != answer.code || got.Message != answer.message {
				t.Errorf("error %+v, want %s with the fixed message %q", got, answer.code, answer.message)
			}
			assertBodyOmits(t, w, answer.leaks...)
		})
	}
}
