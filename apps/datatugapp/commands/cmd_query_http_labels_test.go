package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/datatug/datatug-cli/pkg/meaninglabelsfile"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A saved HTTP query reads its endpoint through the project's sensitivity
// labels like every other saved query: a protected label stored for the HTTP
// source (named by the query's id) refuses the read for a person too, before
// any row is read. Before, HTTP sources were never checked.
func TestQueryRunSaved_HTTPRefusesALabelledField(t *testing.T) {
	useInsecureLoopbackHTTPSavedQuery(t)
	projectDir := setupSavedQueryProject(t)
	run := func() (string, string, int) {
		return runQuery(t, "", "--project", projectDir, "--query", "reference/country-facts",
			"--as", "boss", "--role", "admin", "--var", "name=Canada", "--format", "json")
	}
	stdout, stderr, code := run()
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "CAD")

	store := meaninglabelsfile.Store{Dir: projectDir}
	doc, err := store.Load(context.Background())
	require.NoError(t, err)
	labelled, _, err := meaninglabels.Apply(doc, meaninglabels.Change{Action: meaninglabels.ActionSet,
		Target: meaninglabels.Target{Source: "country-facts", Table: "country-facts", Column: "currency"}, Class: meaninglabels.ClassFinancial, Reason: "test"},
		meaninglabels.Actor{ID: "owner"}, projectReadNow())
	require.NoError(t, err)
	require.NoError(t, store.Save(context.Background(), labelled, doc.Revision))

	stdout, stderr, code = run()
	require.Equal(t, exitCodeAccessDenied, code, stdout+stderr)
	assert.Empty(t, stdout)
	assert.NotContains(t, stderr, "CAD")
	var denial denialJSON
	require.NoError(t, json.Unmarshal([]byte(stderr), &denial), stderr)
	assert.Equal(t, meaninglabels.CodeFieldProtected, denial.Error.Code)
	assert.Contains(t, denial.Error.Reason, "country-facts.country-facts.currency is protected (financial)")
}
