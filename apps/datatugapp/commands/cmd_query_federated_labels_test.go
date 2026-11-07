package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/meaninglabelsfile"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notesWithCustomer joins the demo's support notes to chinook.Customer.
func notesWithCustomer(columns string) string {
	return `from:
  database: support
  name: support-notes
  alias: n
  joins:
    - from: {database: chinook, name: Customer, alias: c, scan: {orderBy: [{field: CustomerId}], limit: 100}}
      on: [{left: {field: CustomerId, source: n}, op: '==', right: {field: CustomerId, source: c}}]
columns:
` + columns
}

// The demo leak: a saved federated query joining support notes to Customer
// must not hand an agent the Email the project labels pii, while a query of
// unlabelled columns still returns rows.
func TestSavedFederatedQueryRefusesLabelledColumnsForAnAgent(t *testing.T) {
	dir := demoForTest(t)
	queries := filepath.Join(dir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeSavedQuery(t, queries, "notes-emails", "TugQL", notesWithCustomer("  - {field: Note, source: n}\n  - {field: Email, source: c}\n"))
	writeSavedQuery(t, queries, "notes-names", "TugQL", notesWithCustomer("  - {field: CustomerId, source: c}\n  - {field: FirstName, source: c}\n"))
	writeSavedQuery(t, queries, "notes-by-email", "TugQL", notesWithCustomer("  - {field: FirstName, source: c}\n")+
		"where: {op: '==', left: {field: Email, source: c}, right: {value: someone@example.com}}\n")
	writeSavedQuery(t, queries, "notes-everything", "TugQL", strings.Split(notesWithCustomer(""), "columns:")[0])

	stdout, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--as", "agent", "--role", "agent", "--format", "json", "--query", "notes-emails")
	require.Equal(t, exitCodeAccessDenied, code, stdout+stderr)
	assert.NotContains(t, stdout+stderr, "@", "no email reaches the agent")
	var denial denialJSON
	require.NoError(t, json.Unmarshal([]byte(stderr), &denial), stderr)
	assert.Equal(t, meaninglabels.CodeFieldProtected, denial.Error.Code)
	assert.Contains(t, denial.Error.Reason, "chinook.Customer.Email is protected (pii)")
	assert.Contains(t, denial.Error.Reason, "federated reads cannot mask labelled columns yet")

	for _, id := range []string{"notes-by-email", "notes-everything"} {
		_, stderr, code = runCLI(t, "", "query", "run", "--project", "datatug-demo", "--as", "agent", "--role", "agent", "--query", id)
		assert.Equal(t, exitCodeAccessDenied, code, id)
		assert.Contains(t, stderr, "access denied: ", id)
		assert.Contains(t, stderr, "chinook.Customer", id)
	}
	// The streaming path refuses the same way: every column of Customer.
	writeSavedQuery(t, queries, "customers-stream", "TugQL", "from: {database: chinook, name: Customer}\n")
	stdout, stderr, code = runCLI(t, "", "query", "run", "--project", "datatug-demo", "--as", "agent", "--role", "agent", "--format", "jsonl", "--query", "customers-stream")
	assert.Equal(t, exitCodeAccessDenied, code, stderr)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "chinook.Customer.Email is protected (pii)")

	stdout, stderr, code = runCLI(t, "", "query", "run", "--project", "datatug-demo", "--as", "agent", "--role", "agent", "--format", "json", "--query", "notes-names")
	require.Equal(t, 0, code, stderr)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.NotContains(t, row, "Email")
		assert.NotEmpty(t, row["FirstName"])
	}

	// A label document that cannot be read refuses the read.
	require.NoError(t, os.WriteFile(filepath.Join(dir, meaninglabelsfile.FileName), []byte("{not json"), 0o644))
	_, stderr, code = runCLI(t, "", "query", "run", "--project", "datatug-demo", "--as", "agent", "--role", "agent", "--query", "notes-names")
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Contains(t, stderr, "datatug-labels.json")
}

func TestFederatedLabelGuardWildcards(t *testing.T) {
	restoreVar(t, &loadLabels)
	restoreVar(t, &listCollections)
	restoreVar(t, &describeCollection)
	restoreVar(t, &listSourceViews)
	doc := meaninglabels.NewDocument()
	doc, _, err := meaninglabels.Apply(doc, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "crm", Table: "Account", Column: "Hidden"}, Class: meaninglabels.ClassFinancial, Reason: "test"}, meaninglabels.Actor{ID: "owner"}, projectReadNow())
	require.NoError(t, err)
	loadLabels = func(context.Context, string) (meaninglabels.Document, error) { return doc, nil }
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("Account", ""), dal.NewRootCollectionRef("Plain", "")}, nil
	}
	listSourceViews = func(context.Context, dal.DB) ([]dbschema.SourceViewDef, error) { return nil, nil }
	fields := []dbschema.FieldDef{{Name: "Id"}, {Name: "Region"}}
	describeCollection = func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{Fields: fields}, nil
	}
	guard := federatedLabelGuard("/project")
	ctx := context.Background()
	leaf := func(table string, all bool, columns ...string) error {
		return guard(ctx, secureread.FederatedLeaf{Database: "crm", Collection: table, Scheme: "sqlite", AllColumns: all, Columns: columns, Open: openNothing})
	}
	assert.NoError(t, leaf("Plain", true), "every column of an unlabelled table")
	assert.NoError(t, leaf("Account", false, "Id", "Region"))
	var protected *secureread.FieldProtectedError
	// A stored label the description does not list still refuses a wildcard.
	require.ErrorAs(t, leaf("Account", true), &protected)
	assert.Contains(t, protected.Message, "crm.Account.Hidden is protected (financial)")
	// A read-time suggestion on a listed column refuses a wildcard.
	fields = append(fields, dbschema.FieldDef{Name: "email"})
	require.ErrorAs(t, leaf("Plain", true), &protected)
	assert.Contains(t, protected.Message, "crm.Plain.email is protected (pii)")
	// Columns that cannot be listed refuse a wildcard.
	describeCollection = func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return nil, errors.New("no description")
	}
	require.ErrorAs(t, leaf("Plain", true), &protected)
	assert.Contains(t, protected.Message, "columns of crm.Plain cannot be listed")
	assert.NoError(t, leaf("Plain", false, "Id"), "named columns need no description")
}

// writeTargetedQuery writes a saved query that names its catalog.
func writeTargetedQuery(t *testing.T, queriesDir, id, queryType, catalog, text string) {
	t.Helper()
	writeSavedQuery(t, queriesDir, id, queryType, text)
	meta := `{"createdAt":"2026-11-07T00:00:00Z","id":"` + id + `","type":"` + queryType + `","targets":[{"catalog":"` + catalog + `"}]}`
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, id+".query.json"), []byte(meta), 0o644))
}

func agentSavedQuery(t *testing.T, id string, extra ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runCLI(t, "", append([]string{"query", "run", "--project", "datatug-demo", "--as", "agent", "--role", "agent", "--format", "json", "--query", id}, extra...)...)
}

// A single-source saved TugQL query is checked as one leaf of its catalog.
func TestSavedSingleSourceTugQLRefusesLabelledColumnsForAnAgent(t *testing.T) {
	dir := demoForTest(t)
	queries := filepath.Join(dir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeTargetedQuery(t, queries, "customer-emails", "TugQL", "chinook", "from: {name: Customer}\ncolumns: [{field: FirstName}, {field: Email}]\n")
	writeTargetedQuery(t, queries, "customer-all", "TugQL", "chinook", "from: {name: Customer}\n")
	writeTargetedQuery(t, queries, "customer-by-email", "TugQL", "chinook", "from: {name: Customer}\ncolumns: [{field: FirstName}]\nwhere: {op: '==', left: {field: Email}, right: {value: someone@example.com}}\n")
	writeTargetedQuery(t, queries, "customer-names", "TugQL", "chinook", "from: {name: Customer}\ncolumns: [{field: CustomerId}, {field: FirstName}]\n")

	for _, id := range []string{"customer-emails", "customer-all", "customer-by-email"} {
		stdout, stderr, code := agentSavedQuery(t, id)
		require.Equal(t, exitCodeAccessDenied, code, id+": "+stdout+stderr)
		assert.NotContains(t, stdout+stderr, "@", id)
		var denial denialJSON
		require.NoError(t, json.Unmarshal([]byte(stderr), &denial), stderr)
		assert.Equal(t, meaninglabels.CodeFieldProtected, denial.Error.Code, id)
		assert.Contains(t, denial.Error.Reason, "chinook.Customer.Email is protected (pii)", id)
	}
	stdout, stderr, code := agentSavedQuery(t, "customer-names")
	require.Equal(t, 0, code, stderr)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
	require.Len(t, rows, 8)
	for _, row := range rows {
		assert.NotContains(t, row, "Email")
	}
}

// Saved native SQL is refused without an opaque-SQL grant, and with one it is
// refused on a source with protected columns, even where a policy allows it.
func TestSavedNativeSQLRefusesALabelledSourceForAnAgent(t *testing.T) {
	dir := demoForTest(t)
	queries := filepath.Join(dir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeTargetedQuery(t, queries, "customer-emails-sql", "SQL", "chinook", "SELECT FirstName, Email FROM Customer WHERE Country = 'Canada'")

	stdout, stderr, code := agentSavedQuery(t, "customer-emails-sql")
	assert.NotZero(t, code)
	assert.NotContains(t, stdout+stderr, "@")
	assert.Contains(t, stderr, "opaque-query grant")

	expectProtected := func(name string) {
		t.Helper()
		stdout, stderr, code := agentSavedQuery(t, "customer-emails-sql", "--allow-opaque-sql")
		require.Equal(t, exitCodeAccessDenied, code, name+": "+stdout+stderr)
		assert.NotContains(t, stdout+stderr, "@", name)
		var denial denialJSON
		require.NoError(t, json.Unmarshal([]byte(stderr), &denial), stderr)
		assert.Equal(t, meaninglabels.CodeFieldProtected, denial.Error.Code, name)
		assert.Contains(t, denial.Error.Reason, "native SQL cannot be masked", name)
		assert.Contains(t, denial.Error.Reason, "use a TugQL query", name)
	}
	expectProtected("the demo policy")
	// A policy that grants the agent opaque SQL still cannot unmask Email.
	policy := filepath.Join(dir, "policies", "agent.yaml")
	data, err := os.ReadFile(policy)
	require.NoError(t, err)
	const grant = "ruleSets:\n  agent:\n    - opaqueQuery: true\n      rules:\n        - id: agent-opaque-sql\n          effect: allow\n          operations: [query]\n"
	granted := strings.Replace(string(data), "ruleSets:\n  agent:\n", grant, 1)
	require.NotEqual(t, string(data), granted)
	require.NoError(t, os.WriteFile(policy, []byte(granted), 0o644))
	expectProtected("an opaque-SQL grant")
}

func openNothing(context.Context) (dal.DB, error) { return nil, nil }

func TestNativeSQLLabelGuard(t *testing.T) {
	restoreVar(t, &loadLabels)
	restoreVar(t, &openBackend)
	restoreVar(t, &listCollections)
	restoreVar(t, &describeCollection)
	ctx := context.Background()
	guard := nativeSQLLabelGuard("/p")
	check := func() error { return guard(ctx, "crm", "sqlite:///x.sqlite") }
	var protected *secureread.FieldProtectedError
	loadLabels = func(context.Context, string) (meaninglabels.Document, error) {
		return meaninglabels.Document{}, errors.New("broken")
	}
	require.ErrorAs(t, check(), &protected)
	assert.Contains(t, protected.Message, "datatug-labels.json")

	doc := meaninglabels.NewDocument()
	loadLabels = func(context.Context, string) (meaninglabels.Document, error) { return doc, nil }
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, errors.New("no source") }
	require.ErrorAs(t, check(), &protected, "a source that cannot be opened to check refuses")
	assert.Contains(t, protected.Message, "source crm cannot be opened")

	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, nil }
	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return []dal.CollectionRef{dal.NewRootCollectionRef("Account", "")}, nil
	}
	fields := []dbschema.FieldDef{{Name: "Id"}, {Name: "Region"}}
	describeCollection = func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
		return &dbschema.CollectionDef{Fields: fields}, nil
	}
	assert.NoError(t, check(), "a source with no protected column")
	fields = append(fields, dbschema.FieldDef{Name: "email"})
	require.ErrorAs(t, check(), &protected, "a read-time suggestion")
	assert.Contains(t, protected.Message, "source crm has columns protected")

	listCollections = func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) {
		return nil, errors.New("no list")
	}
	assert.NoError(t, check(), "an unlabelled source whose tables cannot be listed")
	doc, _, err := meaninglabels.Apply(doc, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "crm", Table: "Account", Column: "Hidden"}, Class: meaninglabels.ClassPII, Reason: "test"}, meaninglabels.Actor{ID: "owner"}, projectReadNow())
	require.NoError(t, err)
	require.ErrorAs(t, check(), &protected, "a labelled source")
}

func TestFederatedLabelGuardOpensOnlyWhatItNeeds(t *testing.T) {
	restoreVar(t, &loadLabels)
	ctx := context.Background()
	doc := meaninglabels.NewDocument()
	loadLabels = func(context.Context, string) (meaninglabels.Document, error) { return doc, nil }
	guard := federatedLabelGuard("/p")
	opens := 0
	failing := func(context.Context) (dal.DB, error) { opens++; return nil, errors.New("down") }
	// An unlabelled source: named columns open nothing; a wildcard whose
	// columns cannot be listed passes; a suggestion still refuses.
	assert.NoError(t, guard(ctx, secureread.FederatedLeaf{Database: "crm", Collection: "Account", Columns: []string{"Id"}, Open: failing}))
	assert.Zero(t, opens)
	assert.NoError(t, guard(ctx, secureread.FederatedLeaf{Database: "crm", Collection: "Account", AllColumns: true, Open: failing}))
	assert.Equal(t, 1, opens)
	var protected *secureread.FieldProtectedError
	require.ErrorAs(t, guard(ctx, secureread.FederatedLeaf{Database: "crm", Collection: "Account", Columns: []string{"email"}, Open: failing}), &protected)
	// A labelled source that cannot be opened refuses.
	doc, _, err := meaninglabels.Apply(doc, meaninglabels.Change{Action: meaninglabels.ActionSet, Target: meaninglabels.Target{Source: "crm", Table: "Other", Column: "Hidden"}, Class: meaninglabels.ClassPII, Reason: "test"}, meaninglabels.Actor{ID: "owner"}, projectReadNow())
	require.NoError(t, err)
	require.ErrorAs(t, guard(ctx, secureread.FederatedLeaf{Database: "crm", Collection: "Account", Columns: []string{"Id"}, Open: failing}), &protected)
	assert.Contains(t, protected.Message, "source crm cannot be opened")
}

func TestChatSourceCatalogs(t *testing.T) {
	assert.Equal(t, map[string]string{"sqlite:///a": "a", "unavailable://b": "b"}, chatSourceCatalogs(map[string]string{"a": "sqlite:///a"}, "unavailable://b", "b"))
}

// Without policies, a saved query still never prints a labelled column.
func TestSavedQueriesCheckLabelsForEveryPrincipal(t *testing.T) {
	dir := demoForTest(t)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "policies")))
	queries := filepath.Join(dir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeTargetedQuery(t, queries, "customer-emails", "TugQL", "chinook", "from: {name: Customer}\ncolumns: [{field: Email}]\n")
	writeTargetedQuery(t, queries, "customer-emails-sql", "SQL", "chinook", "SELECT Email FROM Customer")
	writeTargetedQuery(t, queries, "customer-names", "TugQL", "chinook", "from: {name: Customer}\ncolumns: [{field: FirstName}]\nlimit: 3\n")
	for _, id := range []string{"customer-emails", "customer-emails-sql"} {
		stdout, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--format", "json", "--query", id)
		require.Equal(t, exitCodeAccessDenied, code, id+": "+stdout+stderr)
		assert.NotContains(t, stdout+stderr, "@", id)
		assert.Contains(t, stderr, meaninglabels.CodeFieldProtected, id)
	}
	stdout, stderr, code := runCLI(t, "", "query", "run", "--project", "datatug-demo", "--format", "json", "--query", "customer-names")
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "FirstName")
}

// chat reads saved queries and the model's own TugQL through a session that
// checks the demo's labels.
func TestChatChecksTheProjectLabels(t *testing.T) {
	dir := demoForTest(t)
	queries := filepath.Join(dir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeTargetedQuery(t, queries, "customer-emails", "TugQL", "chinook", "from: {name: Customer}\ncolumns: [{field: FirstName}, {field: Email}]\n")
	writeTargetedQuery(t, queries, "customer-emails-sql", "SQL", "chinook", "SELECT Email FROM Customer")
	writeTargetedQuery(t, queries, "customer-names", "TugQL", "chinook", "from: {name: Customer}\ncolumns: [{field: FirstName}]\n")
	ctx := context.Background()
	projectDir, store, err := resolveQueryProject("datatug-demo")
	require.NoError(t, err)
	env, err := resolveQueryEnvironment(ctx, store, "")
	require.NoError(t, err)
	_, sourceURLs, err := buildChatProjectCatalog(ctx, projectDir, store, env)
	require.NoError(t, err)
	session, err := resolveServeSession(projectDir, serveFlags{projectDir: projectDir, as: "agent", roles: []string{"agent"}})
	require.NoError(t, err)
	session.AllowOpaqueSQL = true
	installProjectLabels(&session, projectDir, chatSourceCatalogs(sourceURLs, sourceURLs["chinook"], "chinook"))
	executor := secureread.NewExecutor(session)
	service := chatSavedQueries{projectDir: projectDir, store: store, executor: executor, env: env, session: session}
	var protected *secureread.FieldProtectedError
	for _, id := range []string{"customer-emails", "customer-emails-sql"} {
		_, err = service.Run(ctx, id)
		require.ErrorAs(t, err, &protected, id)
		assert.Contains(t, protected.Message, "chinook", id)
	}
	result, err := service.Run(ctx, "customer-names")
	require.NoError(t, err)
	assert.NotEmpty(t, result.Result.Rows)
	// The model's generated TugQL runs through executor.RunTugQL.
	_, err = executor.RunTugQL(ctx, sourceURLs["chinook"], []byte("from: {name: Customer}\ncolumns: [{field: Email}]\n"), nil)
	require.ErrorAs(t, err, &protected)
	assert.Contains(t, protected.Message, "chinook.Customer.Email is protected (pii)")
}
