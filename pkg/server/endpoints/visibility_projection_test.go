package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/projectvisibility"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/stretchr/testify/require"
)

func TestQueryDiscoveryRefusesCorruptVisibilityWithoutReturningDefinitions(t *testing.T) {
	_, _, _, _, _, root := localVisibilityTransportFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "queries"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), []byte(`{`), 0600))
	rows, err := getAllQueries(context.Background(), dto.ProjectRef{StoreID: "local", ProjectID: "p"})
	require.Error(t, err)
	require.Nil(t, rows)
	require.NotContains(t, err.Error(), root)
}

func TestIncidentListRefusesCorruptVisibilityWithoutReturningPrivateRows(t *testing.T) {
	router, scopes, paths := configureIncidentHTTPState(t, "alpha")
	performIncidentJSON(t, router, http.MethodPost, "/datatug/incidents", incidentCreateFixture(scopes["alpha"], "visibility-create", "Private Incident title"), http.StatusCreated)
	require.NoError(t, os.WriteFile(filepath.Join(paths["alpha"], ".datatug-visibility.json"), []byte(`{`), 0600))
	w := performIncidentRequest(t, router, http.MethodGet, "/datatug/incidents?"+incidentScopeValues(scopes["alpha"]).Encode(), nil, http.StatusInternalServerError)
	require.NotContains(t, w.Body.String(), "Private Incident title")
	require.NotContains(t, w.Body.String(), paths["alpha"])
}

func TestSessionVisibilityListKeepsExplicitPrivateReadAvailable(t *testing.T) {
	_, _, service, principal, caps, root := localVisibilityTransportFixture(t)
	ctx := context.Background()
	for _, id := range []string{"legacy-hidden", "overlay-hidden", "visible"} {
		created, err := service.Execute(ctx, principal, sessioncontext.Command{OpID: "create-" + id, Action: "createSession", SessionID: id, BagID: "bag-" + id, DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, Title: id, Scope: &sessioncontext.Scope{EnvironmentID: "dev", IdentityDomain: caps.Environments[0].IdentityDomain}})
		require.NoError(t, err)
		if id == "legacy-hidden" {
			_, err = service.Execute(ctx, principal, sessioncontext.Command{OpID: "archive-" + id, Action: "transitionSession", SessionID: id, DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, ExpectedVersion: created.Receipt.SessionVersion, Transition: "archive"})
			require.NoError(t, err)
		}
	}
	metadata := filepath.Join(root, ".datatug-visibility.json")
	require.NoError(t, os.WriteFile(metadata, []byte(`{"version":1,"archived":[{"kind":"session","id":"overlay-hidden"}]}`), 0600))
	query := url.Values{"storage": {"local"}, "project": {"p"}, "databaseId": {principal.DatabaseID}, "projectId": {principal.ProjectID}}
	w := httptest.NewRecorder()
	sessionContextHandler(w, httptest.NewRequest(http.MethodGet, "/session-context/sessions?"+query.Encode(), nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "legacy-hidden")
	require.NotContains(t, w.Body.String(), "overlay-hidden")
	require.Contains(t, w.Body.String(), "visible")
	for _, id := range []string{"legacy-hidden", "overlay-hidden"} {
		snapshot, err := service.GetSnapshot(ctx, principal, id)
		require.NoError(t, err)
		require.Equal(t, id, snapshot.Session.ID)
	}
	query.Del("databaseId")
	query.Del("projectId")
	require.NoError(t, os.WriteFile(metadata, []byte(`{`), 0600))
	w = httptest.NewRecorder()
	sessionContextHandler(w, httptest.NewRequest(http.MethodGet, "/session-context/capabilities?"+query.Encode(), nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

func TestProjectFullEndpointUsesResolverCompleteModelProjection(t *testing.T) {
	_, _, _, _, _, root := localVisibilityTransportFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dbmodels"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dbmodels", "sales.dbmodel.json"), []byte(`{"id":"sales","schemas":[{"id":"dbo","tables":[]}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), []byte(`{"version":1,"archived":[{"kind":"data-model","id":"sales"}]}`), 0600))
	w := httptest.NewRecorder()
	getProjectFull(w, httptest.NewRequest(http.MethodGet, "/projects/project_full?storage=local&id=p", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"archived":true`)
	require.Contains(t, w.Body.String(), `"id":"sales"`)
	require.Contains(t, w.Body.String(), `"id":"dbo"`)
}

func TestVisibilityProjectionCodecFailureReturnsNoPrivatePayload(t *testing.T) {
	_, _, _, _, _, root := localVisibilityTransportFixture(t)
	previous := marshalVisibilityProjection
	t.Cleanup(func() { marshalVisibilityProjection = previous })
	for _, stage := range []string{"encode", "decode", "non-object row"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("codec unavailable")
			marshalVisibilityProjection = func(any) ([]byte, error) {
				switch stage {
				case "encode":
					return nil, failure
				case "decode":
					return []byte(`{`), nil
				default:
					return []byte(`{"checks":[null,{"id":"check-a"}],"dbModels":[null,{"id":"m"}]}`), nil
				}
			}
			project, projectErr := annotateProjectModelVisibility(root, &datatug.Project{})
			state, stateErr := annotateDetectiveVisibility("p", detective.State{})
			if stage != "non-object row" {
				require.Error(t, projectErr)
				require.Nil(t, project)
				require.Error(t, stateErr)
				require.Nil(t, state)
			} else {
				require.NoError(t, projectErr)
				require.NoError(t, stateErr)
				require.Nil(t, project["dbModels"].([]any)[0])
				require.Nil(t, state["checks"].([]any)[0])
				require.Equal(t, false, state["checks"].([]any)[1].(map[string]any)["archived"])
			}
		})
	}
}

func TestVisibilityCaseRestoreUsesNativeHistoryAndRejectsStaleVersions(t *testing.T) {
	runtime, scope, _, _, _, _ := localVisibilityTransportFixture(t)
	ctx := context.Background()
	store, actor, err := detectiveStore("p", api.SecurityContextID())
	require.NoError(t, err)
	created, err := store.SaveCase(ctx, detective.Case{ID: "legacy-case", Title: "Retained Case"}, actor, "create-case")
	require.NoError(t, err)
	created.Status, created.ClosureReason = "closed", "Investigation completed"
	closed, err := store.SaveCase(ctx, created, actor, "update-case")
	require.NoError(t, err)
	closed.Status = "archived"
	archived, err := store.SaveCase(ctx, closed, actor, "update-case")
	require.NoError(t, err)
	rows, err := runtime.Inventory(ctx, scope)
	require.NoError(t, err)
	var target projectvisibility.Target
	for _, row := range rows {
		if row.Item.Ref.Kind == "case" && row.Item.Ref.ID == archived.ID {
			target = row
		}
	}
	require.True(t, target.Item.Archived)
	restored, err := runtime.Restore(ctx, scope, target, "native-case-restore")
	require.NoError(t, err)
	require.False(t, restored.Item.Archived)
	current, err := store.GetCase(ctx, archived.ID)
	require.NoError(t, err)
	require.Equal(t, archived.Version+1, current.Version)
	require.Equal(t, "active", current.Status)
	require.Len(t, current.Timeline, len(archived.Timeline)+1)
	_, err = runtime.Restore(ctx, scope, target, "native-case-stale")
	require.ErrorIs(t, err, projectvisibility.ErrRestoreConflict)
	privateCases, err := os.ReadDir(filepath.Join(api.GetCapabilities().EvidencePrivateDir, "detective"))
	require.NoError(t, err)
	require.Len(t, privateCases, 1)
	casePath := filepath.Join(api.GetCapabilities().EvidencePrivateDir, "detective", privateCases[0].Name(), "cases", archived.ID+".json")
	require.NoError(t, os.WriteFile(casePath, []byte(`{`), 0600))
	_, err = runtime.Restore(ctx, scope, target, "native-case-unreadable")
	require.ErrorIs(t, err, detective.ErrOutcomeUncertain)
	target.Item.Ref.ID = "missing-case"
	_, err = runtime.Restore(ctx, scope, target, "native-case-missing")
	require.Error(t, err)
}

func TestVisibilityDiscoveryProjectionRetainsResolverDefinitions(t *testing.T) {
	_, _, _, _, _, root := localVisibilityTransportFixture(t)
	metadata := filepath.Join(root, ".datatug-visibility.json")
	require.NoError(t, os.WriteFile(metadata, []byte(`{"version":1,"archived":[{"kind":"data-model","id":"sales"},{"kind":"check","id":"check-a"},{"kind":"query-validator","id":"reports/customer"},{"kind":"query-folder","id":"hidden"},{"kind":"query","id":"folder/hidden-query"}]}`), 0600))
	var project datatug.Project
	require.NoError(t, json.Unmarshal([]byte(`{"id":"p","dbModels":[null,{"id":"sales","schemas":[{"id":"dbo","tables":[]}]},{"id":"active"}]}`), &project))
	response, err := annotateProjectModelVisibility(root, &project)
	require.NoError(t, err)
	models := response["dbModels"].([]any)
	require.Len(t, models, 3)
	require.Nil(t, models[0])
	require.Equal(t, true, models[1].(map[string]any)["archived"])
	require.Equal(t, false, models[2].(map[string]any)["archived"])
	require.Len(t, models[1].(map[string]any)["schemas"], 1)
	require.Len(t, project.DbModels, 3, "projection removed a resolver definition")
	var state detective.State
	require.NoError(t, json.Unmarshal([]byte(`{"checks":[null,{"id":"check-a"}],"queryValidators":[{"queryId":"reports/customer"}],"cases":[{"id":"legacy","status":"archived"},{"id":"active-case","status":"active"}]}`), &state))
	annotated, err := annotateDetectiveVisibility("p", state)
	require.NoError(t, err)
	require.Len(t, annotated["checks"], 2)
	require.Equal(t, true, annotated["checks"].([]any)[1].(map[string]any)["archived"])
	require.Equal(t, true, annotated["queryValidators"].([]any)[0].(map[string]any)["archived"])
	require.Equal(t, true, annotated["cases"].([]any)[0].(map[string]any)["archived"])
	require.Equal(t, false, annotated["cases"].([]any)[1].(map[string]any)["archived"])
	require.Len(t, state.Checks, 2, "direct Check resolution must keep complete state")
	var tree datatug.QueriesFolder
	require.NoError(t, json.Unmarshal([]byte(`{"folders":[{"id":"hidden","items":[{"id":"retained"}]},{"id":"folder","items":[{"id":"hidden-query"},{"id":"visible"}]}]}`), &tree))
	require.NoError(t, filterLocalQueryVisibility("p", &tree, ""))
	require.Len(t, tree.Folders, 1)
	require.Equal(t, "folder", tree.Folders[0].ID)
	require.Len(t, tree.Folders[0].Items, 1)
	require.Equal(t, "visible", tree.Folders[0].Items[0].ID)
	require.NoError(t, filterLocalQueryVisibility("p", nil, ""))
	numbers, err := decodeVisibilityProjection([]byte(`{"version":9007199254740993}`))
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), numbers["version"])
	_, err = decodeVisibilityProjection([]byte(`{`))
	require.Error(t, err)

	// Corrupt visibility must fail discovery clearly, without returning a
	// partially annotated private payload; owning definitions are not modified.
	require.NoError(t, os.WriteFile(metadata, []byte(`{`), 0600))
	modelsResponse, err := annotateProjectModelVisibility(root, &project)
	require.Error(t, err)
	require.Nil(t, modelsResponse)
	stateResponse, err := annotateDetectiveVisibility("p", state)
	require.Error(t, err)
	require.Nil(t, stateResponse)
	_, err = filterLocalVisible("p", "check", []string{"check-a"}, func(s string) string { return s })
	require.Error(t, err)
	require.Error(t, filterLocalQueryVisibility("p", &tree, ""))
	_, err = localVisibilityHidden("unknown", "check", "check-a")
	require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	_, err = filterLocalVisible("unknown", "check", []string{"check-a"}, func(s string) string { return s })
	require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	_, err = annotateDetectiveVisibility("unknown", state)
	require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	require.ErrorIs(t, filterLocalQueryVisibility("unknown", &tree, ""), sessioncontext.ErrForbidden)
}

func TestVisibilityRuntimeRefusesUnreadableOwningInventory(t *testing.T) {
	runtime, scope, _, _, _, root := localVisibilityTransportFixture(t)
	ctx := context.Background()
	invalid := scope
	invalid.Storage = "foreign"
	_, err := PrepareLocalVisibilityItems(ctx, invalid)
	require.Error(t, err)
	previous := detectiveResolvePrivateDir
	detectiveResolvePrivateDir = func(string) (string, error) { return "", errors.New("private root unavailable") }
	_, err = PrepareLocalVisibilityItems(ctx, scope)
	require.ErrorContains(t, err, "private root unavailable")
	detectiveResolvePrivateDir = previous
	t.Cleanup(func() { detectiveResolvePrivateDir = previous })
	check := filepath.Join(root, "checks", "check-a.json")
	before, err := os.ReadFile(check)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(check, []byte(`{`), 0600))
	rows, err := runtime.Inventory(ctx, scope)
	require.Error(t, err)
	require.Nil(t, rows)
	require.NoError(t, os.WriteFile(check, before, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "datatug-meanings.json"), []byte(`{`), 0600))
	rows, err = runtime.Inventory(ctx, scope)
	require.Error(t, err)
	require.Nil(t, rows)
}

func TestLocalProjectTransportRejectsNativeErrorsAndPreservesPrivatePaths(t *testing.T) {
	localVisibilityTransportFixture(t)
	registry, err := localprojects.Open(t.TempDir(), nil)
	require.NoError(t, err)
	api.ConfigureLocalProjectRegistry(registry)
	t.Cleanup(func() { api.ConfigureLocalProjectRegistry(nil) })
	for _, test := range []struct {
		body    string
		handler http.HandlerFunc
	}{
		{`{"store":"local","id":"INVALID/ID","title":"Project","operationId":"create"}`, ProjectAgentEndpoints{}.createProject},
		{`{"storage":"local","project":"unregistered","archived":true,"operationId":"archive"}`, archiveLocalProject},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		test.handler(w, r)
		require.NotEqual(t, http.StatusOK, w.Code)
		require.NotContains(t, w.Body.String(), "/private/")
	}
}
