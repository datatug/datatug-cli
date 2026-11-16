package endpoints

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func lifecycleEndpointSession(t *testing.T) {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{"p": t.TempDir()}, api.Capabilities{AllowWrites: true})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
}
func TestProjectLifecycleHTTPContract(t *testing.T) {
	lifecycleEndpointSession(t)
	oldReview, oldChange, oldCatalog := localLifecycleReview, localLifecycleChange, localLifecycleCatalog
	t.Cleanup(func() {
		localLifecycleReview, localLifecycleChange, localLifecycleCatalog = oldReview, oldChange, oldCatalog
	})
	localLifecycleReview = func(_ context.Context, selection api.LocalLifecycleSelection, object datatug.ProjectObjectRef) (datatug.ProjectLifecycleReview, error) {
		if selection.StoreID != "local" || selection.ProjectID != "p" || selection.Branch != "feature/x" || object.Kind != "query" || object.ID != "q" || selection.Environment != "prod" {
			t.Fatal("lost review scope")
		}
		return datatug.ProjectLifecycleReview{Revision: "r1", Complete: false}, nil
	}
	w := httptest.NewRecorder()
	projectLifecycleReviewHandler(w, httptest.NewRequest("GET", "/?storage=local&project=p&branch=feature%2Fx&kind=query&id=q&environment=prod", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store, no-transform" {
		t.Fatalf("review %d %s", w.Code, w.Body.String())
	}
	calls := 0
	localLifecycleChange = func(_ context.Context, request api.LocalLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
		calls++
		if request.ProjectID != "p" || request.Branch != "feature/x" || request.ExpectedRevision != "r1" || request.OperationID != "op1" || request.Environment != "prod" {
			t.Fatal("lost write scope")
		}
		return datatug.ProjectLifecycleAck{OperationID: request.OperationID, Revision: "r2", Change: request.Change}, nil
	}
	body := `{"storage":"local","project":"p","branch":"feature/x","environment":"prod","operationId":"op1","expectedRevision":"r1","change":{"object":{"kind":"query","id":"q"},"action":"archive"}}`
	request := httptest.NewRequest("POST", "/", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	w = httptest.NewRecorder()
	projectLifecycleChangeHandler(w, request)
	if w.Code != 200 || calls != 1 {
		t.Fatalf("write %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{strings.Replace(body, `"storage":"local"`, `"storage":"local","storage":"other"`, 1), strings.Replace(body, `"storage":"local"`, `"actorID":"other","storage":"local"`, 1), strings.Repeat("x", (32<<10)+1)} {
		request = httptest.NewRequest("POST", "/", strings.NewReader(bad))
		request.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		projectLifecycleChangeHandler(w, request)
		if w.Code != 400 {
			t.Fatalf("bad command %d %s", w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("malformed command reached provider")
	}
	localLifecycleCatalog = func(_ context.Context, selection api.LocalLifecycleSelection, archived bool) (datatug.ProjectLifecycleCatalog, error) {
		if !archived || selection.ProjectID != "p" {
			t.Fatal("lost catalog selector")
		}
		return datatug.ProjectLifecycleCatalog{Revision: "r1", Items: []datatug.ProjectUsageItem{}}, nil
	}
	w = httptest.NewRecorder()
	projectLifecycleCatalogHandler(w, httptest.NewRequest("GET", "/?storage=local&project=p&archived=true", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestProjectLifecycleHTTPErrorsAreBoundedAndPrivate(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{datatug.ErrProjectUsageIncomplete, 503, "REFERENCES_INCOMPLETE"}, {datatug.ErrProjectObjectReferenced, 409, "ITEM_REFERENCED"},
		{datatug.ErrProjectLifecycleConflict, 409, "LIFECYCLE_CONFLICT"}, {datatug.ErrProjectLifecycleInvalid, 400, "INVALID_REQUEST"},
		{api.ErrProjectMutationOutcomeUncertain, 409, "OUTCOME_UNCERTAIN"}, {errors.New("private/history/other-user"), 500, "INTERNAL"},
	} {
		w := httptest.NewRecorder()
		writeLifecycleError(w, httptest.NewRequest(http.MethodPost, "/", nil), tc.err)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "other-user") {
			t.Fatalf("unsafe error %d %s", w.Code, w.Body.String())
		}
	}
}
