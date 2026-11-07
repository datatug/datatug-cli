package commands

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/querybody"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/tugqlbind"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatAgentSession is the demo's agent principal, as `chat --as agent --role agent` resolves it.
func chatAgentSession(t *testing.T, dir string) secureread.Session {
	t.Helper()
	session, err := resolveServeSession(dir, serveFlags{projectDir: dir, as: "agent", roles: []string{"agent"}})
	require.NoError(t, err)
	return session
}

// REQ:cross-database-sources — chat runs a saved text query that joins two
// project sources: each alias is bound to its project source and each leaf is
// read through the session's policies, as `query run` does.
func TestChatRunsASavedTextQueryAcrossTwoDatabases(t *testing.T) {
	dir := demoForTest(t)
	addGeoSource(t, dir)
	writeTextQuery(t, dir, "invoice-countries", strings.Replace(invoiceCountries, "datatug://datatug-demo/chinook", "datatug:///chinook", 1))
	store, id := filestore.NewSingleProjectStore(dir, "datatug-demo")
	var warnings bytes.Buffer
	service := chatSavedQueries{projectDir: dir, store: store.GetProjectStore(id), env: demoInstallEnv, projectID: id, session: chatAgentSession(t, dir), warn: &warnings}

	query, err := service.Run(context.Background(), "invoice-countries")
	require.NoError(t, err)
	require.NotEmpty(t, query.Result.Rows)
	for _, row := range query.Result.Rows {
		assert.Equal(t, "Canada", row.Data["BillingCountry"], "the agent policy filters each leaf")
		assert.Equal(t, "CA", row.Data["code"])
	}
	assert.Equal(t, "chinook", query.SourceID, "the root's source")
	assert.Contains(t, query.Source, "sqlite")
	assert.Contains(t, query.TugQL, `"database":"geo"`)

	// With no warning writer, a federated read still runs.
	service.warn = nil
	_, err = service.RunTugQLWithVariables(context.Background(), "invoice-countries", map[string]string{})
	require.NoError(t, err)
}

func TestChatRefusesAnUnbindableSavedTextQuery(t *testing.T) {
	dir := demoForTest(t)
	writeTextQuery(t, dir, "bad-target", "databases (\n  geo = 'sqlite:///tmp/geo.sqlite'\n)\nfrom geo::countries as c\nselect c.code\n")
	// A structured document names its databases by each source's database key;
	// a subquery's source that names none is refused too.
	queries := filepath.Join(dir, "queries")
	require.NoError(t, os.WriteFile(filepath.Join(queries, "unqualified.query.json"), []byte(`{"createdAt":"2026-11-07T00:00:00Z","id":"unqualified","type":"TugQL"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(queries, "unqualified.query.tugql.yaml"), []byte("from: {database: chinook, name: Invoice}\nwhere: {exists: {query: {from: {name: Customer}}}}\n"), 0o644))
	store, id := filestore.NewSingleProjectStore(dir, "datatug-demo")
	service := chatSavedQueries{projectDir: dir, store: store.GetProjectStore(id), env: demoInstallEnv, projectID: id, session: chatAgentSession(t, dir)}

	_, err := service.Run(context.Background(), "bad-target")
	assert.True(t, tugqlbind.IsError(err), err)
	assert.ErrorContains(t, err, "connection string")
	assert.NotContains(t, err.Error(), "/tmp/geo.sqlite")
	_, err = service.Run(context.Background(), "unqualified")
	assert.True(t, tugqlbind.IsError(err), err)
	assert.ErrorContains(t, err, "source Customer names no database")
}

// Chat checks each leaf strictly against the labels of its alias's source: a
// stored label refuses whatever the alias is called, and a column only a
// read-time suggestion marks refuses too, even a policy-free owner, because
// what chat reads goes to an AI model.
func TestChatFederatedReadsCheckLabelsStrictly(t *testing.T) {
	dir := demoForTest(t)
	crm := filepath.Join(t.TempDir(), "crm.sqlite")
	db, err := sql.Open("sqlite3", crm)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE Account (id INTEGER PRIMARY KEY, email TEXT NOT NULL); INSERT INTO Account (email) VALUES ('a@example.com')")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	store, id := filestore.NewSingleProjectStore(dir, "datatug-demo")
	_, err = api.AddProjectSource(context.Background(), store.GetProjectStore(id), dir, api.ProjectSourceRequest{Environment: demoInstallEnv, ID: "crm", SQLitePath: crm})
	require.NoError(t, err)
	writeTextQuery(t, dir, "customer-emails", "databases (\n  people = 'datatug:///chinook'\n)\nfrom people::Customer as c\nselect c.Email\n")
	writeTextQuery(t, dir, "account-emails", "databases (\n  crm\n)\nfrom crm::Account as a\nselect a.email\n")

	agent := chatSavedQueries{projectDir: dir, store: store.GetProjectStore(id), env: demoInstallEnv, projectID: id, session: chatAgentSession(t, dir)}
	_, err = agent.Run(context.Background(), "customer-emails")
	var protected *secureread.FieldProtectedError
	require.ErrorAs(t, err, &protected)
	assert.Contains(t, protected.Message, "chinook.Customer.Email is protected")

	owner := chatSavedQueries{projectDir: dir, store: store.GetProjectStore(id), env: demoInstallEnv, projectID: id, session: secureread.Session{LocalProjectOwner: true, Unrestricted: true}}
	_, err = owner.Run(context.Background(), "account-emails")
	require.ErrorAs(t, err, &protected, "a suggestion refuses chat's read")
	assert.Contains(t, protected.Message, "crm.Account.email")

	// The same read, checked as `query run` checks a policy-free owner, reads
	// on with a warning: chat's refusal is its strictness.
	definition, err := store.GetProjectStore(id).LoadQuery(context.Background(), "account-emails")
	require.NoError(t, err)
	_, bindings, err := federatedTugQLBindings(context.Background(), store.GetProjectStore(id), dir, demoInstallEnv, definition)
	require.NoError(t, err)
	session := owner.session
	var warnings bytes.Buffer
	installBindingLabels(&session, bindings, &warnings, false)
	document, err := querybody.Structured(definition)
	require.NoError(t, err)
	_, err = secureread.NewExecutor(session).RunFederatedTugQL(context.Background(), []byte(document), tugqlbind.URLs(bindings), nil)
	require.NoError(t, err)
	assert.Contains(t, warnings.String(), "email")
}
