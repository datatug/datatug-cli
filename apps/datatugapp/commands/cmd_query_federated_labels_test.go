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
		return guard(ctx, secureread.FederatedLeaf{Database: "crm", Collection: table, Scheme: "sqlite", AllColumns: all, Columns: columns})
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
