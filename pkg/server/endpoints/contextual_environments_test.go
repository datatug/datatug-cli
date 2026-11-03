package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/require"
)

func TestBoundProjectFullContextualWireKeepsRawAndGuard(t *testing.T) {
	oldStore, oldContext := storage.NewDatatugStore, getContextFromRequest
	t.Cleanup(func() { storage.NewDatatugStore, getContextFromRequest = oldStore, oldContext })
	getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
	root, project := boundEndpointFixture(t)
	mustWriteFile(t, filepath.Join(root, "datatug-project.json"), `{"id":"`+project+`","title":"Bound project"}`)
	request := func(extra string) map[string]json.RawMessage {
		w := httptest.NewRecorder()
		getProjectFull(w, httptest.NewRequest(http.MethodGet, "/datatug/projects/project_full?"+url.Values{"id": {project}}.Encode()+extra, nil))
		require.Equal(t, http.StatusOK, w.Code)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &fields))
		return fields
	}
	initial := request("")
	var view api.ContextualEnvironments
	require.NoError(t, json.Unmarshal(initial["contextualEnvironments"], &view))
	require.Equal(t, 1, view.Version)
	require.Equal(t, "available", view.Status)
	require.Equal(t, api.ContextualEnvironmentScope{ProjectID: project, EnvironmentID: "local", CatalogID: "chinook-local"}, view.Scope)
	require.Len(t, *view.Items, 1)
	require.True(t, view.Parent.ReadOnly)
	require.False(t, view.Parent.Archived)
	registry, err := localprojects.Open("", map[string]string{project: root})
	require.NoError(t, err)
	_, err = registry.SetArchived(context.Background(), localprojects.Archive{Storage: "local", Project: project, Archived: true, OperationID: "archive-context"})
	require.NoError(t, err)
	archived := request("")
	require.NoError(t, json.Unmarshal(archived["contextualEnvironments"], &view))
	require.True(t, view.Parent.Archived)
	require.Len(t, *view.Items, 1)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), []byte(`{"version":1,"archived":[{"kind":"environment","id":"local"}]}`), 0600))
	child := request("")
	var childWire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(child["contextualEnvironments"], &childWire))
	require.JSONEq(t, `[]`, string(childWire["items"]))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-project-state.json"), []byte(`{broken`), 0600))
	unavailable := request("")
	var missing map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(unavailable["contextualEnvironments"], &missing))
	require.JSONEq(t, `"unavailable"`, string(missing["status"]))
	require.JSONEq(t, `"TEMPORARILY_UNAVAILABLE"`, string(missing["reasonCode"]))
	require.NotContains(t, missing, "items")
	require.NotContains(t, missing, "parent")
	for _, body := range []map[string]json.RawMessage{archived, child, unavailable, request("&branch=foreign")} {
		for _, key := range []string{"id", "environments", "dbModels", "entities"} {
			require.JSONEq(t, string(initial[key]), string(body[key]), "existing raw field changed: %s", key)
		}
	}
	guard := NewRequestGuard("127.0.0.1", 8989)
	for _, path := range []string{"/datatug/environments/state", "/datatug/projects/registry"} {
		w := httptest.NewRecorder()
		called := false
		guard.Wrap(func(http.ResponseWriter, *http.Request) { called = true })(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusForbidden, w.Code)
		require.False(t, called)
	}
}

func TestBoundProjectFullContextualFinalAdmissionFence(t *testing.T) {
	for _, kind := range []string{"read-canceled", "session-replaced"} {
		t.Run(kind, func(t *testing.T) {
			oldStore, oldContext, oldRead := storage.NewDatatugStore, getContextFromRequest, boundContextualEnvironments
			t.Cleanup(func() {
				storage.NewDatatugStore, getContextFromRequest, boundContextualEnvironments = oldStore, oldContext, oldRead
			})
			getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
			root, project := boundEndpointFixture(t)
			mustWriteFile(t, filepath.Join(root, "datatug-project.json"), `{"id":"`+project+`"}`)
			boundContextualEnvironments = func(ctx context.Context, scope api.ContextualEnvironmentScope) (api.ContextualEnvironments, error) {
				if kind == "read-canceled" {
					expired, cancel := context.WithCancel(ctx)
					cancel()
					return oldRead(expired, scope)
				}
				view, err := oldRead(ctx, scope)
				// Replacement after a complete metadata read must not publish under the previous visit.
				api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
				return view, err
			}
			w := httptest.NewRecorder()
			getProjectFull(w, httptest.NewRequest(http.MethodGet, "/datatug/projects/project_full?"+url.Values{"id": {project}}.Encode(), nil))
			require.Equal(t, http.StatusForbidden, w.Code)
			require.Contains(t, w.Body.String(), "ACCESS_DENIED")
			require.NotContains(t, w.Body.String(), "contextualEnvironments")
		})
	}
}
