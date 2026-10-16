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
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/require"
)

func TestQualifiedProjectReadEndpointsMatchWebWire(t *testing.T) {
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	branch := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD")
	write := func(relative, body string) {
		t.Helper()
		file := filepath.Join(projectDir, relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
	}
	write("datatug-project.json", `{"id":"`+scope.Project+`","title":"Qualified project"}`)
	write("boards/sales.board.json", `{"id":"sales","title":"Sales","parameters":[{"id":"region","type":"string"}],"requiredParams":[["region"]]}`)
	write("entities/qualified-customer.entity.json", `{"id":"qualified-customer","title":"Customer","extends":{"def":"https://example.test/customer.json"},"sourceOfRecord":{"db":"local","objType":"table","objName":"Customer"},"options":{"sources":[]}}`)
	write("environments/local/local.env.json", `{"id":"local","title":"Local","vars":{"token":"secret-value"},"dbServers":[{"id":"private-server"}],"presentation":{"label":"Local","icon":"desktop-outline","tone":"neutral"}}`)
	write("connections/demo-db.json", `{"format":"datatug-demo-connections/v1","connections":[{"id":"chinook-sqlite","dataset":"chinook","storage":"sqlite","readiness":"public-api","source":"https://demodb.dev/ovdb/v1/databases/chinook","fixtureSha256":"7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15","browserFixture":{"url":"https://chinook.demodb.dev/data/chinook.sqlite","bytes":1007616},"password":"secret-value"}],"credentials":{"apiKey":"secret-value"}}`)
	query := func(path string, values url.Values, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path+"?"+values.Encode(), nil)
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	selectors := url.Values{"storage": {api.LocalStoreID}, "project": {scope.Project}, "branch": {branch}}
	assertOK := func(response *httptest.ResponseRecorder) {
		t.Helper()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}

	boards := query("/datatug/boards/all_boards", selectors, getBoards)
	assertOK(boards)
	require.JSONEq(t, `[{"id":"sales","title":"Sales","parameters":[{"id":"region","type":"string"}],"requiredParams":[["region"]]}]`, boards.Body.String())
	boardQuery := cloneValues(selectors)
	boardQuery.Set("id", "sales")
	board := query("/datatug/boards/board", boardQuery, getBoard)
	assertOK(board)
	require.Contains(t, board.Body.String(), `"title":"Sales"`)

	entities := query("/datatug/entities/all_entities", selectors, getEntities)
	assertOK(entities)
	require.JSONEq(t, `[{"id":"Customer"},{"id":"qualified-customer","title":"Customer"}]`, entities.Body.String())
	entityQuery := cloneValues(selectors)
	entityQuery.Set("id", "qualified-customer")
	entity := query("/datatug/entities/entity", entityQuery, getEntity)
	assertOK(entity)
	require.Contains(t, entity.Body.String(), `"extends":{"def":"https://example.test/customer.json"}`)
	require.Contains(t, entity.Body.String(), `"sourceOfRecord"`)
	require.Contains(t, entity.Body.String(), `"options"`)

	environments := query("/datatug/environments/all_environments", selectors, getEnvironments)
	assertOK(environments)
	require.JSONEq(t, `[{"id":"local","title":"Local","presentation":{"label":"Local","icon":"desktop-outline","tone":"neutral"}}]`, environments.Body.String())
	require.NotContains(t, environments.Body.String(), "secret-value")
	require.NotContains(t, environments.Body.String(), "private-server")
	catalog := query("/datatug/projects/connection_catalog", selectors, projectConnectionCatalogHandler)
	assertOK(catalog)
	require.Contains(t, catalog.Body.String(), `"format":"datatug-demo-connections/v1"`)
	require.NotContains(t, catalog.Body.String(), "secret-value")

	summaryQuery := cloneValues(selectors)
	summaryQuery.Set("id", scope.Project)
	summary := query("/datatug/projects/project_summary", summaryQuery, getProjectSummary)
	assertOK(summary)
	var summaryBody map[string]any
	require.NoError(t, json.Unmarshal(summary.Body.Bytes(), &summaryBody))
	require.NotEmpty(t, summaryBody)

	runGit(t, projectDir, "branch", "feature")
	feature := "feature"
	featureQuery := cloneValues(selectors)
	featureQuery.Set("branch", feature)
	require.Equal(t, http.StatusConflict, query("/datatug/boards/all_boards", featureQuery, getBoards).Code)
	missingBoardID := cloneValues(selectors)
	require.Equal(t, http.StatusBadRequest, query("/datatug/boards/board", missingBoardID, getBoard).Code)
	missingEntityID := cloneValues(selectors)
	require.Equal(t, http.StatusBadRequest, query("/datatug/entities/entity", missingEntityID, getEntity).Code)
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{scope.Project: projectDir}, api.Capabilities{})
	deniedBoard := query("/datatug/boards/board", boardQuery, getBoard)
	require.Equal(t, http.StatusForbidden, deniedBoard.Code, deniedBoard.Body.String())
	deniedEnvironments := query("/datatug/environments/all_environments", selectors, getEnvironments)
	require.Equal(t, http.StatusForbidden, deniedEnvironments.Code, deniedEnvironments.Body.String())
}

func cloneValues(source url.Values) url.Values {
	result := make(url.Values, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}
