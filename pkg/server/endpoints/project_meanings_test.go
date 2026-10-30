package endpoints

import (
	"context"
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeaningsEndpointContractAndErrors(t *testing.T) {
	session, sessionErr := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	if sessionErr != nil {
		t.Fatal(sessionErr)
	}
	api.ConfigureSecureSession(session, map[string]string{"p": t.TempDir()}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	originalCatalog, originalChange := localMeaningsCatalog, localMeaningChange
	t.Cleanup(func() { localMeaningsCatalog, localMeaningChange = originalCatalog, originalChange })
	localMeaningsCatalog = func(_ context.Context, ref dto.ProjectRef, branch string) (datatug.ProjectMeaningsCatalog, error) {
		if ref.ProjectID != "p" || ref.StoreID != "local" || branch != "main" {
			t.Fatalf("wrong scope %#v %s", ref, branch)
		}
		return datatug.ProjectMeaningsCatalog{Version: 1}, nil
	}
	localMeaningChange = func(_ context.Context, r api.LocalMeaningWrite) (datatug.ProjectMeaningAck, error) {
		if r.ProjectID != "p" || r.Branch != "main" || r.OperationID != "operation-1" {
			t.Fatal("wrong write scope")
		}
		return datatug.ProjectMeaningAck{Receipt: r.OperationID}, nil
	}
	w := httptest.NewRecorder()
	projectMeaningsCatalogHandler(w, httptest.NewRequest("GET", "/datatug/meanings/catalog?storage=local&project=p&branch=main", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("catalog %d", w.Code)
	}
	w = httptest.NewRecorder()
	projectMeaningChangeHandler(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"storage":"local","project":"p","branch":"main","operationId":"operation-1","ifMatch":"revision","change":{"action":"add-entity","conceptId":"person","name":"Person","description":""}}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "operation-1") {
		t.Fatalf("write %d %s", w.Code, w.Body.String())
	}
	for _, raw := range []string{"", strings.Repeat("x", (64<<10)+1)} {
		w = httptest.NewRecorder()
		projectMeaningChangeHandler(w, httptest.NewRequest("POST", "/", strings.NewReader(raw)))
		if w.Code != 400 {
			t.Fatalf("bad body %d", w.Code)
		}
	}
	w = httptest.NewRecorder()
	projectMeaningsCatalogHandler(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 400 {
		t.Fatalf("missing project %d", w.Code)
	}
	localMeaningsCatalog = func(context.Context, dto.ProjectRef, string) (datatug.ProjectMeaningsCatalog, error) {
		return datatug.ProjectMeaningsCatalog{}, datatug.ErrMeaningIncomplete
	}
	w = httptest.NewRecorder()
	projectMeaningsCatalogHandler(w, httptest.NewRequest("GET", "/?project=p&storage=local", nil))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	localMeaningChange = func(context.Context, api.LocalMeaningWrite) (datatug.ProjectMeaningAck, error) {
		return datatug.ProjectMeaningAck{}, datatug.ErrMeaningConflict
	}
	w = httptest.NewRecorder()
	projectMeaningChangeHandler(w, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	for _, err := range []error{datatug.ErrMeaningConflict, datatug.ErrMeaningReferenced, datatug.ErrMeaningIncomplete, datatug.ErrMeaningOperationConflict, errors.New("storage failed")} {
		w = httptest.NewRecorder()
		writeMeaningError(w, httptest.NewRequest(http.MethodGet, "/", nil), err)
		if w.Code < 400 {
			t.Fatal("error accepted")
		}
	}
}
