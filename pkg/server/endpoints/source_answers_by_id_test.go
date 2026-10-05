package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An answer names a source by its ID only: where a route answers that the data file of a source is
// not there, or that a source could not be opened, the sentence is built at the route from the ID
// of the source, for every scheme and every serve route. The text of pkg/dbcopy, which holds the
// display form of the source (the path of its data file, the host and database of a PostgreSQL
// one), goes to the log of the server.

// chinookCatalogID is the ID of the catalog of the project's one SQLite source (its model is the
// source "chinook"): exec/select and exec/execute_commands name a source by the catalog.
const chinookCatalogID = "chinook-local"

// sourceMissingSentence is what a route answers for the data file of the source id that is not there.
func sourceMissingSentence(id string) string {
	return `the data file of source "` + id + `" does not exist (run ` + "`datatug demo`" + ` to fetch the demo project's data fixtures)`
}

// sourceNotOpenedSentence is what a route answers for the source id that cannot be opened.
func sourceNotOpenedSentence(id string) string { return `source "` + id + `" could not be opened` }

// breakChinook makes the data file of the project's one SQLite source what the case says: not
// there, or there but not a database that can be opened (a folder).
func breakChinook(t *testing.T, projectDir string, missing bool) {
	t.Helper()
	path := filepath.Join(projectDir, "chinook.sqlite")
	require.NoError(t, os.Remove(path))
	if !missing {
		require.NoError(t, os.Mkdir(path, 0o755))
	}
}

// routeAnswer is what a route answered, reduced to what the tests below look at.
type routeAnswer struct {
	status  int
	code    string
	message string
	body    string
}

// readAnswer reads the body of either envelope: the legacy {error, code} and the contract's
// {error:{code, message}}.
func readAnswer(t *testing.T, rec *httptest.ResponseRecorder) routeAnswer {
	t.Helper()
	answer := routeAnswer{status: rec.Code, body: rec.Body.String()}
	var legacy struct {
		Error any    `json:"error"`
		Code  string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &legacy), rec.Body.String())
	switch e := legacy.Error.(type) {
	case string:
		answer.code, answer.message = legacy.Code, e
	case map[string]any:
		answer.code, _ = e["code"].(string)
		answer.message, _ = e["message"].(string)
	}
	return answer
}

// fileSourceRoutes are the serve routes that open the file source of the project, each asking for
// the one source "chinook".
func fileSourceRoutes(t *testing.T, scope apicontract.Scope) map[string]fileSourceRoute {
	t.Helper()
	return map[string]fileSourceRoute{
		"semantic/columns": {semanticTestSource, func() routeAnswer {
			q := url.Values{
				urlParamProjectID: {scope.Project}, "environment": {scope.Environment}, "securityContextId": {scope.SecurityContextID},
				"source": {semanticTestSource}, "collection": {"Customer"},
			}
			rec := httptest.NewRecorder()
			semanticColumnsHandler(rec, httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+q.Encode(), nil))
			return readAnswer(t, rec)
		}},
		"exec/run_query": {semanticTestSource, func() routeAnswer {
			payload, err := json.Marshal(apicontract.ExecutionRequest{
				Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
				Source: semanticTestSource, DTQL: "from: {name: Customer}\n", Mode: apicontract.ProvenanceModeLive,
			})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			runQueryHandler(rec, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(payload)))
			return readAnswer(t, rec)
		}},
		"exec/select": {chinookCatalogID, func() routeAnswer {
			q := url.Values{"proj": {scope.Project}, "env": {scope.Environment}, "db": {chinookCatalogID}, "from": {"Customer"}}
			rec := httptest.NewRecorder()
			executeSelectHandler(rec, httptest.NewRequest(http.MethodGet, "/datatug/exec/select?"+q.Encode(), nil))
			return readAnswer(t, rec)
		}},
		"exec/execute_commands": {chinookCatalogID, func() routeAnswer {
			body := `{"commands":[{"type":"SQL","text":"select 1","env":"` + scope.Environment + `","db":"` + chinookCatalogID + `"}]}`
			rec := httptest.NewRecorder()
			executeCommandsHandler(rec, httptest.NewRequest(http.MethodPost, "/datatug/exec/execute_commands?project="+url.QueryEscape(scope.Project), strings.NewReader(body)))
			return readAnswer(t, rec)
		}},
	}
}

// fileSourceRoute is a route that opens the file source, and the ID by which it names the source.
type fileSourceRoute struct {
	id  string
	ask func() routeAnswer
}

func TestRoutes_AFileSourceIsAnsweredByItsIDAndNeverByItsPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		missing  bool
		sentence func(id string) string
	}{
		{"the data file is not there", true, sourceMissingSentence},
		{"the data file cannot be opened", false, sourceNotOpenedSentence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectDir, projectID := writeSemanticTestProject(t)
			scope := configureSemanticSessionWithCapabilities(t, projectDir, projectID, "alice", []string{"admin"}, api.Capabilities{AllowOpaqueSQL: true})
			breakChinook(t, projectDir, tc.missing)

			for name, route := range fileSourceRoutes(t, scope) {
				t.Run(name, func(t *testing.T) {
					logged := captureAgentLog(t)
					answer := route.ask()
					assert.Equal(t, http.StatusServiceUnavailable, answer.status, answer.body)
					assert.Equal(t, "SOURCE_UNAVAILABLE", answer.code)
					assert.Contains(t, answer.message, tc.sentence(route.id))
					for _, shown := range []string{projectDir, "chinook.sqlite", ".sqlite", string(filepath.Separator)} {
						assert.NotContains(t, answer.body, shown, "the answer holds the path of the data file")
					}
					assert.Contains(t, logged.String(), "chinook.sqlite", "the text of dbcopy, with the path, is in the log of the server")
				})
			}
		})
	}
}

// A PostgreSQL source that cannot be opened is answered as every PostgreSQL failure is: the adapter's fixed sentence and
// the hint of where its connection string is read from (the variable here), which name no host, port, database, user or
// password. The answer and the log say the same.
func TestRoutes_APostgresSourceThatCannotBeOpenedIsAnsweredByItsID(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	standInOpener(t, func() (*dalgo2postgres.Database, error) {
		return nil, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433", Database: "shop"}
	})
	logged := captureAgentLog(t)
	status, env, body := getSemanticColumns(t, pgRouteSource)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, string(apicontract.ErrCodeSourceUnavailable), env.Error.Code)
	const want = "the server could not be reached; the PostgreSQL connection string is read from the environment variable DATATUG_DT02_ROUTES_PG_URL"
	assert.Equal(t, want, env.Error.Message)
	for _, shown := range []string{"db.example.com", "5433", "postgres://", "env:"} {
		assert.NotContains(t, body, shown)
		assert.NotContains(t, logged.String(), shown)
	}
	assertNoRouteMarkers(t, "semantic/columns", body, logged.String())
	assert.Contains(t, logged.String(), want, "the log of the server says the same")
}

// A source whose related rows are asked for is answered by its ID too.
func TestRoutes_RelatedRowsOfAFileSourceThatCannotBeOpenedAreAnsweredByItsID(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	breakChinook(t, projectDir, false)
	lookupID := encodeLookupID(semanticTestSource, "Invoice", "CustomerId")

	_, err := computeSemanticRelatedRows(context.Background(), apicontract.RelatedRowsRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		LookupID: lookupID, Value: apicontract.NewIntegerValue("1"),
	})
	var ce *contractError
	require.True(t, isContractErrorCode(err, apicontract.ErrCodeSourceUnavailable, &ce), "%v", err)
	assert.Equal(t, sourceNotOpenedSentence(semanticTestSource), ce.Message)
	assert.NotContains(t, ce.Message, projectDir)
}

// The fixed sentences that name no source are answered as they are, with the PostgreSQL preview off.
func TestRoutes_ARefusalThatNamesNoSourceIsAnsweredAsItIs(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	status, env, _ := getSemanticColumns(t, pgRouteSource)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, dbcopy.ErrPostgresPreview.Error(), env.Error.Message)
}

// An error of a missing data file that did not come through a route that knows its source is
// answered by a sentence that names no source, never by its own text.
func TestHandleError_AMissingDataFileThatNamesNoSourceIsAFixedSentence(t *testing.T) {
	logged := captureAgentLog(t)
	rec := httptest.NewRecorder()
	handleError(dbcopy.CheckSourceFile("/private/MARKER-path/x.db"), rec, httptest.NewRequest(http.MethodGet, "/", nil))
	answer := readAnswer(t, rec)
	assert.Equal(t, http.StatusServiceUnavailable, answer.status)
	assert.Equal(t, "SOURCE_UNAVAILABLE", answer.code)
	assert.Equal(t, sourceFileMissingSentence, answer.message)
	assert.NotContains(t, answer.body, "MARKER")
	_ = logged
}

// An error that api.SourceUnavailable built is answered as the source being unavailable, with its
// own sentence.
func TestHandleError_ASourceUnavailableAnswerIsA503WithItsOwnSentence(t *testing.T) {
	rec := httptest.NewRecorder()
	failure := api.SourceUnavailable("chinook", dbcopy.CheckSourceFile("/private/MARKER-path/x.db"))
	require.NotNil(t, failure)
	handleError(failure, rec, httptest.NewRequest(http.MethodGet, "/", nil))
	answer := readAnswer(t, rec)
	assert.Equal(t, http.StatusServiceUnavailable, answer.status)
	assert.Equal(t, "SOURCE_UNAVAILABLE", answer.code)
	assert.Equal(t, sourceMissingSentence("chinook"), answer.message)
}

// A read of the rows that fails at the source after the source was resolved (its file went missing
// between the two, or the read opens it again) is answered by the ID of the source too.
func TestRoutes_RelatedRowsThatFailAtTheSourceAreAnsweredByItsID(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	saved := runStructuredRelatedHook
	t.Cleanup(func() { runStructuredRelatedHook = saved })
	runStructuredRelatedHook = func(*secureread.Executor, context.Context, string, dal.StructuredQuery, map[string]any) (secureread.Result, error) {
		return secureread.Result{}, dbcopy.CheckSourceFile(filepath.Join(projectDir, "MARKER-gone", "chinook.sqlite"))
	}

	_, err := computeSemanticRelatedRows(context.Background(), apicontract.RelatedRowsRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		LookupID: encodeLookupID(semanticTestSource, "Invoice", "CustomerId"), Value: apicontract.NewIntegerValue("1"),
	})
	var ce *contractError
	require.True(t, isContractErrorCode(err, apicontract.ErrCodeSourceUnavailable, &ce), "%v", err)
	assert.Equal(t, sourceMissingSentence(semanticTestSource), ce.Message)
	assert.NotContains(t, ce.Message, "MARKER")
}
