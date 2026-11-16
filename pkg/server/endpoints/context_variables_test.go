package endpoints

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/querybody"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/require"
)

// myProjectsTugQL is the "My projects" query of the datatug/my-data project
// (backstage spec/features/my-data-project): its FROM path uses the context
// variable SneatUserPersonalSpaceID, which only the DataTug web app binds.
const myProjectsTugQL = "FROM spaces/{SneatUserPersonalSpaceID}/ext/datatug/projects\nORDER BY title"

// writeMyProjectsQuery adds "my-projects" to a run-query test project.
func writeMyProjectsQuery(t *testing.T, projectDir string) {
	t.Helper()
	queriesDir := filepath.Join(projectDir, "queries", "customers")
	mustWriteFile(t, filepath.Join(queriesDir, "my-projects.query.json"), `{
		"createdAt":"2026-01-01T00:00:00Z","id":"my-projects","title":"My projects","type":"TugQL"
	}`)
	mustWriteFile(t, filepath.Join(queriesDir, "my-projects.query.tugql"), myProjectsTugQL)
}

func requireContextVariableRefusal(t *testing.T, status int, env apicontract.ErrorEnvelope, field, variable string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, string(apicontract.ErrCodeInvalidRequest), string(env.Error.Code))
	require.Equal(t, field, env.Error.Field)
	require.Contains(t, env.Error.Message, "context variable "+variable+",")
}

// AC core-refuses-context-variables, server side: a saved query using a
// context variable answers 400 INVALID_REQUEST naming it, before its source is
// resolved (the test project has no catalog for that collection at all).
func TestExecRunQueryRefusesSavedQueryUsingContextVariable(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	writeMyProjectsQuery(t, projectDir)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })

	status, env := postRunQuery(t, apicontract.ExecutionRequest{
		Mode: apicontract.ProvenanceModeLive, StoreID: api.LocalStoreID,
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		QueryID: "customers/my-projects", Source: semanticTestSource,
	})
	requireContextVariableRefusal(t, status, env, "queryId", "SneatUserPersonalSpaceID")
}

// The same refusal holds for an ad-hoc structured TugQL document.
func TestExecRunQueryRefusesAdHocTugQLUsingContextVariable(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	document, err := querybody.Structured(&datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: "FROM users\nWHERE __key__ = @SneatUserID"})
	require.NoError(t, err)

	status, env := postRunQuery(t, apicontract.ExecutionRequest{
		Mode: apicontract.ProvenanceModeLive, StoreID: api.LocalStoreID,
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		TugQL: document, Source: semanticTestSource,
	})
	requireContextVariableRefusal(t, status, env, "tugql", "SneatUserID")
}

// A facts comparison never executes a saved query that uses a context variable.
func TestProveNativeFactsBindingRefusesContextVariables(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	writeMyProjectsQuery(t, projectDir)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })

	_, err := proveNativeFactsBinding(context.Background(), "customers/my-projects", apicontract.CompareSideSpec{
		Kind: apicontract.CompareSideFacts, StoreID: api.LocalStoreID, Project: projectID,
		Environment: scope.Environment, CohortRole: apicontract.CompareCohortAffected,
	}, "Customer", "CustomerId")
	var contractErr *contractError
	require.ErrorAs(t, err, &contractErr)
	require.Equal(t, apicontract.ErrCodeInvalidRequest, contractErr.Code)
	require.True(t, strings.Contains(contractErr.Message, "SneatUserPersonalSpaceID"), contractErr.Message)
}
