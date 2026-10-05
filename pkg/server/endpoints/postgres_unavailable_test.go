package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/internal/pgstandin"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parts of a PostgreSQL source that must never be shown: a marker in the password, one in the user name and one
// in a query parameter.
const (
	pgRoutePassword = "PWMARKER-dt02-routes"
	pgRouteUser     = "USERMARKER-dt02-routes"
	pgRouteQuery    = "QUERYMARKER-dt02-routes"
	pgRouteSource   = "postgres://" + pgRouteUser + ":" + pgRoutePassword + "@db.example.com:5433/shop?application_name=" + pgRouteQuery
)

// registerPostgresShop registers the PostgreSQL source "shop" in the project the way a scan does: a catalog of the
// postgres driver whose path is a connection descriptor, a project file that names an environment variable and nothing
// else, and the variable holds the URL. So the routes resolve the source through the real resolver, to "env:NAME".
func registerPostgresShop(t *testing.T, projectDir, projectID, sourceURL string) {
	t.Helper()
	t.Setenv("DATATUG_DT02_ROUTES_PG_URL", sourceURL)
	descriptor := filepath.Join(projectDir, "connections", semanticTestEnv, "shop.json")
	mustMkdirAll(t, filepath.Dir(descriptor))
	mustWriteFile(t, descriptor, `{"dsnEnv":"DATATUG_DT02_ROUTES_PG_URL"}`+"\n")

	projStore := filestore.NewProjectStore(projectID, projectDir)
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "postgres"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "postgres", Path: "connections/" + semanticTestEnv + "/shop.json", DbModel: "shop"}}
	catalog.ID = "shop"
	require.NoError(t, projStore.SaveEnvDbCatalog(context.Background(), semanticTestEnv, serverID, catalog.ID, catalog))
}

// standInOpener puts an opener of PostgreSQL databases in place for the length of the test.
func standInOpener(t *testing.T, open func() (*dalgo2postgres.Database, error)) {
	t.Helper()
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return open()
	}))
}

// assertNoRouteMarkers fails when what a route answered or logged shows the password, the user name or the parameter.
func assertNoRouteMarkers(t *testing.T, name string, shown ...string) {
	t.Helper()
	for _, text := range shown {
		for _, marker := range []string{pgRoutePassword, pgRouteUser, pgRouteQuery} {
			assert.NotContains(t, text, marker, name)
		}
	}
}

// getSemanticColumns asks the semantic/columns route about the "customers" table of the one source and decodes the
// error envelope it answers.
func getSemanticColumns(t *testing.T, sourceURL string) (status int, env apicontract.ErrorEnvelope, body string) {
	t.Helper()
	projectDir, projectID := writeSemanticTestProject(t)
	registerPostgresShop(t, projectDir, projectID, sourceURL)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	q := url.Values{
		urlParamProjectID: {scope.Project}, "environment": {scope.Environment}, "securityContextId": {scope.SecurityContextID},
		"source": {"shop"}, "collection": {"customers"},
	}
	rec := httptest.NewRecorder()
	semanticColumnsHandler(rec, httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+q.Encode(), nil))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec.Code, env, rec.Body.String()
}

// postAdhocRunQuery asks exec/run_query to run a DTQL over the one source and decodes the error envelope it answers.
func postAdhocRunQuery(t *testing.T, sourceURL string) (status int, env apicontract.ErrorEnvelope, body string) {
	t.Helper()
	projectDir, projectID := writeRunQueryTestProject(t)
	registerPostgresShop(t, projectDir, projectID, sourceURL)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	payload, err := json.Marshal(apicontract.ExecutionRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		Source: "shop", DTQL: "from: {name: customers}\n", Mode: apicontract.ProvenanceModeLive,
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	runQueryHandler(rec, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(payload)))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec.Code, env, rec.Body.String()
}

// With the preview off, the semantic route and exec/run_query answer a PostgreSQL source the same way a file source
// that is not there is answered: SOURCE_UNAVAILABLE, with the fixed sentence of the preview. Neither is a generic
// internal error (which says nothing) or an invalid request (which blames the client); and the opener is never called.
func TestRoutes_APostgresSourceWithThePreviewOffIsUnavailableWithThePreviewSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	opened := false
	standInOpener(t, func() (*dalgo2postgres.Database, error) { opened = true; return &dalgo2postgres.Database{}, nil })

	for name, ask := range map[string]func(*testing.T, string) (int, apicontract.ErrorEnvelope, string){
		"semantic/columns": getSemanticColumns,
		"exec/run_query":   postAdhocRunQuery,
	} {
		logged := captureAgentLog(t)
		status, env, body := ask(t, pgRouteSource)
		assert.Equal(t, http.StatusServiceUnavailable, status, name)
		assert.Equal(t, string(apicontract.ErrCodeSourceUnavailable), env.Error.Code, name)
		assert.Equal(t, dbcopy.ErrPostgresPreview.Error(), env.Error.Message, name)
		assertNoRouteMarkers(t, name, body, logged.String())
	}
	assert.False(t, opened, "the opener is never called")
}

// With the preview on, a URL that turns the read-only session off is refused by the route as the source being
// unavailable, with the sentence that names the parameter and the one place that writes.
func TestRoutes_APostgresSourceThatTurnsReadOnlyOffIsUnavailable(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	opened := false
	standInOpener(t, func() (*dalgo2postgres.Database, error) { opened = true; return &dalgo2postgres.Database{}, nil })

	status, env, body := getSemanticColumns(t, pgRouteSource+"&default_transaction_read_only=off")
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, string(apicontract.ErrCodeSourceUnavailable), env.Error.Code)
	assert.Contains(t, env.Error.Message, "default_transaction_read_only")
	assertNoRouteMarkers(t, "semantic/columns", body)
	assert.False(t, opened)
}

// With the preview on, a read of a PostgreSQL source through the session's policies is refused before the source is
// opened, and the route says the source is unavailable, with the fixed sentence.
func TestRoutes_APolicyReadOfAPostgresSourceIsUnavailable(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	opened := false
	standInOpener(t, func() (*dalgo2postgres.Database, error) { opened = true; return &dalgo2postgres.Database{}, nil })

	status, env, body := postAdhocRunQuery(t, pgRouteSource)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, string(apicontract.ErrCodeSourceUnavailable), env.Error.Code)
	assert.Equal(t, dbcopy.ErrPostgresPolicyReads.Error(), env.Error.Message)
	assertNoRouteMarkers(t, "exec/run_query", body)
	assert.False(t, opened)
}

// A source that opened and whose pool cannot make a connection again (the server was restarted, the password was
// changed) is unavailable as well, and the route shows one fixed sentence: not the text pgx writes, which names the
// user, in the body or in the agent log.
func TestRoutes_APostgresSourceWhoseConnectionIsLostIsUnavailableWithAFixedSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	// The stand-in is built here, on the goroutine of the test: the opener runs on another one, where a failure of
	// the setup (FailNow) would end that goroutine and not the test.
	standIn := pgstandin.Unreachable(t, pgRouteUser, pgRoutePassword)
	standInOpener(t, func() (*dalgo2postgres.Database, error) {
		return standIn, nil
	})

	logged := captureAgentLog(t)
	status, env, body := getSemanticColumns(t, pgRouteSource)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, string(apicontract.ErrCodeSourceUnavailable), env.Error.Code)
	assert.Equal(t, "the connection to the PostgreSQL server was lost and could not be made again", env.Error.Message)
	assertNoRouteMarkers(t, "semantic/columns", body, logged.String())
	assert.False(t, strings.Contains(body, "failed to connect"))
}

// exec/run_query, for a session with no policy, says the same: a source whose pool cannot make a connection again is
// unavailable, with one fixed sentence, and the body shows nothing pgx wrote.
func TestRoutes_ARunQueryOnAPostgresSourceWhoseConnectionIsLostIsUnavailableWithAFixedSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	// The stand-in is built here, on the goroutine of the test: the opener runs on another one, where a failure of
	// the setup (FailNow) would end that goroutine and not the test.
	standIn := pgstandin.Unreachable(t, pgRouteUser, pgRoutePassword)
	standInOpener(t, func() (*dalgo2postgres.Database, error) {
		return standIn, nil
	})
	unrestricted, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	require.NoError(t, err)
	saved := secureExecutorHook
	secureExecutorHook = func() (*secureread.Executor, bool) { return secureread.NewExecutor(unrestricted), true }
	t.Cleanup(func() { secureExecutorHook = saved })

	status, env, body := postAdhocRunQuery(t, pgRouteSource)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, string(apicontract.ErrCodeSourceUnavailable), env.Error.Code)
	assert.Equal(t, "the connection to the PostgreSQL server was lost and could not be made again", env.Error.Message)
	assertNoRouteMarkers(t, "exec/run_query", body)
}
