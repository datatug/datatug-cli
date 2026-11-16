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
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

func TestLifecycleEndpointsValidateAuthoritySelectorsAndProviderFailures(t *testing.T) {
	handlers := []http.HandlerFunc{projectLifecycleReviewHandler, projectLifecycleCatalogHandler, projectLifecycleChangeHandler}
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	for _, h := range handlers {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", "/", nil))
		if w.Code == 200 {
			t.Fatal("unbound principal reached lifecycle")
		}
	}
	lifecycleEndpointSession(t)
	for _, url := range []string{"/?storage=other&project=p", "/?storage=local&project=p&unknown=x", "/?storage=local&storage=other&project=p", "/?storage=local"} {
		w := httptest.NewRecorder()
		projectLifecycleReviewHandler(w, httptest.NewRequest("GET", url, nil))
		if w.Code == 200 {
			t.Fatal("invalid selection accepted", url)
		}
	}
	w := httptest.NewRecorder()
	projectLifecycleReviewHandler(w, httptest.NewRequest("GET", "/?storage=local&project=p&kind=future&id=q", nil))
	if w.Code != 400 {
		t.Fatal("unknown object kind accepted", w.Code)
	}
	oldR, oldC, oldL := localLifecycleReview, localLifecycleChange, localLifecycleCatalog
	t.Cleanup(func() { localLifecycleReview, localLifecycleChange, localLifecycleCatalog = oldR, oldC, oldL })
	localLifecycleReview = func(context.Context, api.LocalLifecycleSelection, datatug.ProjectObjectRef) (datatug.ProjectLifecycleReview, error) {
		return datatug.ProjectLifecycleReview{}, datatug.ErrProjectUsageIncomplete
	}
	localLifecycleCatalog = func(context.Context, api.LocalLifecycleSelection, bool) (datatug.ProjectLifecycleCatalog, error) {
		return datatug.ProjectLifecycleCatalog{}, datatug.ErrProjectUsageIncomplete
	}
	localLifecycleChange = func(context.Context, api.LocalLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
		return datatug.ProjectLifecycleAck{}, datatug.ErrProjectUsageIncomplete
	}
	for _, c := range []struct {
		h                          http.HandlerFunc
		url, method, body, content string
		want                       int
	}{
		{projectLifecycleReviewHandler, "/?storage=local&project=p&kind=query&id=q", "GET", "", "", 503},
		{projectLifecycleCatalogHandler, "/?storage=local&project=p", "GET", "", "", 503},
		{projectLifecycleCatalogHandler, "/?storage=local&project=p&archived=perhaps", "GET", "", "", 400},
		{projectLifecycleChangeHandler, "/", "POST", "{}", "text/plain", 400},
		{projectLifecycleChangeHandler, "/", "POST", "{}", "application/json", 503},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(c.method, c.url, strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.content)
		c.h(w, r)
		if w.Code != c.want {
			t.Fatalf("expected %d got%d %s", c.want, w.Code, w.Body.String())
		}
	}
}

func TestQueryTreeDeclarationFailureReturnsNoPartialQueries(t *testing.T) {
	lifecycleEndpointSession(t)
	oldLoad, oldStore := loadModuleQueriesHook, storage.NewDatatugStore
	t.Cleanup(func() { loadModuleQueriesHook, storage.NewDatatugStore = oldLoad, oldStore })
	loadModuleQueriesHook = func(string) ([]*datatug.QueryDef, map[*datatug.QueryDef]string, error) {
		return []*datatug.QueryDef{}, map[*datatug.QueryDef]string{}, nil
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) { return nil, errors.New("folder inventory unavailable") }
	if out, err := getAllQueries(context.Background(), dto.ProjectRef{StoreID: "local", ProjectID: "p"}); err == nil || out != nil {
		t.Fatal("failed declaration read exposed partial query tree")
	}
}

func TestPublicConnectionAndFolderEndpointBoundaryErrors(t *testing.T) {
	lifecycleEndpointSession(t)
	for _, url := range []string{"/?x=%zz", "/?storage=local&project=p&projectApi=local&branch=main&environment=missing&database=missing"} {
		w := httptest.NewRecorder()
		publicOVDBConnectionStateHandler(w, httptest.NewRequest("GET", url, nil))
		if w.Code == 200 {
			t.Fatal("invalid connection scope accepted")
		}
	}
	withFakeHandle(t, func() {
		w := httptest.NewRecorder()
		createQueryFolder(w, makeRequest(http.MethodPost, "/?storage=local&project=p", `{}`))
		if w.Code == http.StatusCreated {
			t.Fatal("empty query-folder command accepted")
		}
	})
	w := httptest.NewRecorder()
	writeMeaningError(w, httptest.NewRequest("POST", "/", nil), datatug.ErrProjectUsageIncomplete)
	if w.Code != 503 {
		t.Fatal("Meaning deletion bypassed incomplete inventory")
	}
	if err := detectiveError(datatug.ErrProjectUsageIncomplete); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("Detective lifecycle error did not remain bounded")
	}
	if tree := buildQueriesFolderTree(nil, nil, "~", "reports/empty"); tree == nil || len(tree.Folders) != 1 {
		t.Fatal("declared empty folders missing from endpoint tree")
	}
}
