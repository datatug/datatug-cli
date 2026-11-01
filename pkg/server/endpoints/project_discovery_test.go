package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryDiscoveryHTTPStrictSelectedScopeAndProjection(t *testing.T) {
	scope, dir := realCaptureSetup(t, "reader", []string{"reader"})
	branch := runGit(t, dir, "symbolic-ref", "--short", "HEAD")
	write := func(name, body string) {
		t.Helper()
		filename := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o700))
		require.NoError(t, os.WriteFile(filename, []byte(body), 0o600))
	}
	write("dbs/mysql/mysql:active:3306.dbserver.json", `{"id":"mysql:active:3306","server":{"driver":"mysql","host":"active","port":3306}}`)
	write("dbs/mysql/mysql:archived:3306.dbserver.json", `{"id":"mysql:archived:3306","server":{"driver":"mysql","host":"archived","port":3306}}`)
	write("datatug-meanings.json", `{"version":1,"entities":[{"documentId":"project","conceptId":"active","name":"Active","origin":"project","fields":[]},{"documentId":"project","conceptId":"hidden","name":"Hidden","origin":"project","fields":[]}]}`)
	write(".datatug-visibility.json", `{"version":1,"archived":[{"kind":"server","id":"mysql/mysql:archived:3306"},{"kind":"meaning","id":"project/hidden"}]}`)
	query := url.Values{"storage": {api.LocalStoreID}, "project": {scope.Project}, "branch": {branch}}.Encode()
	for _, route := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"dbservers", projectDbServerDiscoveryHandler}, {"meanings", projectMeaningDiscoveryHandler},
	} {
		t.Run(route.name, func(t *testing.T) {
			request := func(raw string) *httptest.ResponseRecorder {
				response := httptest.NewRecorder()
				route.handler(response, httptest.NewRequest(http.MethodGet, "/datatug/"+route.name+"/discovery?"+raw, nil))
				require.Equal(t, "no-store, no-transform", response.Header().Get("Cache-Control"))
				return response
			}
			response := request(query)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "archived")
			require.NotContains(t, response.Body.String(), "Hidden")
			if route.name == "dbservers" {
				var rows []datatug.ProjDbServer
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &rows))
				require.Len(t, rows, 1)
				require.Equal(t, "mysql:active:3306", rows[0].ID)
			} else {
				var rows datatug.ProjectMeaningsCatalog
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &rows))
				require.Len(t, rows.Entities, 1)
				require.Equal(t, "active", rows.Entities[0].ConceptID)
			}
			for _, invalid := range []string{"", query + "&branch=other", query + "&spaceID=foreign", query + "&branch=%zz", "storage=local&branch=" + url.QueryEscape(branch), "project=" + scope.Project + "&branch=" + url.QueryEscape(branch), "storage=local&project=" + scope.Project, "storage=foreign&project=" + scope.Project + "&branch=" + url.QueryEscape(branch)} {
				require.Equal(t, http.StatusBadRequest, request(invalid).Code, invalid)
			}
			require.Equal(t, http.StatusConflict, request("storage=local&project="+scope.Project+"&branch=unselected").Code)
		})
	}
	write(".datatug-visibility.json", `{"version":1,"archived":"invalid"}`)
	for _, handler := range []http.HandlerFunc{projectDbServerDiscoveryHandler, projectMeaningDiscoveryHandler} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodGet, "/?"+query, nil))
		require.GreaterOrEqual(t, response.Code, 400)
		require.NotContains(t, response.Body.String(), "mysql:active:3306")
		require.NotContains(t, response.Body.String(), "Hidden")
	}
}

func TestOrdinaryDiscoveryRoutesKeepTheRequestGuard(t *testing.T) {
	quietLog(t)
	found := map[string]bool{}
	for _, route := range recordRoutes(true, Capabilities{}) {
		if route.path != "/datatug/meanings/discovery" && route.path != "/datatug/dbservers/discovery" {
			continue
		}
		found[route.path] = true
		response := httptest.NewRecorder()
		route.handler(response, guardRequest(http.MethodGet, route.path, "foreign.invalid", "https://foreign.invalid", "cross-site"))
		require.Equal(t, http.StatusForbidden, response.Code)
	}
	require.Len(t, found, 2)
}

func TestDiscoveryHTTPRejectsAnonymousBeforeSelectorResolution(t *testing.T) {
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{"known": t.TempDir()}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	for _, handler := range []http.HandlerFunc{projectMeaningDiscoveryHandler, projectDbServerDiscoveryHandler} {
		for _, query := range []string{"", "?project=unknown&branch=main", "?storage=local&project=missing&branch=main", "?storage=local&project=known&branch=main"} {
			response := httptest.NewRecorder()
			handler(response, httptest.NewRequest(http.MethodGet, "/"+query, nil))
			if response.Code != http.StatusForbidden {
				t.Fatal("anonymous selector resolution preceded principal check", query, response.Code, response.Body)
			}
		}
	}
}
