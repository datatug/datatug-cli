package endpoints

import (
	"errors"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNativeErrorVocabularyNeverExposesDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{sessioncontext.ErrInvalid, 400, "invalid_request"}, {core.ErrInvalid, 400, "invalid_request"},
		{sessioncontext.ErrForbidden, 403, "access_denied"}, {sessioncontext.ErrNotFound, 404, "operation_not_found"}, {core.ErrNotFound, 404, "operation_not_found"},
		{sessioncontext.ErrConflict, 409, "operation_conflict"}, {core.ErrConflict, 409, "operation_conflict"}, {sessioncontext.ErrReadOnly, 409, "session_fenced"},
		{core.ErrCapacity, 413, "capacity_exceeded"}, {&http.MaxBytesError{Limit: 1}, 413, "capacity_exceeded"}, {errors.New("private /path and Case label"), 503, "session_detective_unavailable"},
	} {
		w := httptest.NewRecorder()
		writeSessionDetectiveError(w, httptest.NewRequest(http.MethodGet, "http://localhost/", nil), tc.err)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestNativeScopeRequiresExactPrivateSelectors(t *testing.T) {
	p := sessioncontext.Principal{DatabaseID: "db", ProjectID: "project"}
	n := sessioncontext.NativeNamespace{NamespaceID: "namespace"}
	base := url.Values{"project": {"source"}, "databaseId": {"db"}, "projectId": {"project"}, "namespaceId": {"namespace"}}
	scope, e := sessionDetectiveScope(base, p, n)
	if e != nil || scope.Storage != "local" {
		t.Fatal(scope, e)
	}
	for _, key := range []string{"branch", "actor", "securityContextId"} {
		q := cloneNativeQuery(base)
		q.Set(key, "grant")
		if _, e = sessionDetectiveScope(q, p, n); !errors.Is(e, sessioncontext.ErrInvalid) {
			t.Fatal(key, e)
		}
	}
	q := cloneNativeQuery(base)
	q.Add("project", "duplicate")
	if _, e = sessionDetectiveScope(q, p, n); !errors.Is(e, sessioncontext.ErrInvalid) {
		t.Fatal(e)
	}
	for _, key := range []string{"spaceID", "databaseId", "projectId", "namespaceId"} {
		q := cloneNativeQuery(base)
		q.Set(key, "foreign")
		if _, e = sessionDetectiveScope(q, p, n); !errors.Is(e, sessioncontext.ErrForbidden) {
			t.Fatal(key, e)
		}
	}
}

func cloneNativeQuery(in url.Values) url.Values {
	out := url.Values{}
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}
