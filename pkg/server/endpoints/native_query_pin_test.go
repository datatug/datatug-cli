package endpoints

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestNativeAcceptedSavedQueryExecutesExactBodyAfterDefinitionDeletion(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_saved.json", &req)
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	configureExecutionEvidence(t, projectID, projectDir)
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	req.QueryID, req.Record = "customers/customer-invoices", true
	store, err := api.ProjectStoreFor(projectID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.(datatug.RevisionedQueriesStore).LoadQueryRevision(context.Background(), req.QueryID)
	if err != nil {
		t.Fatal(err)
	}
	accepted := saved.Query
	for _, name := range []string{"customer-invoices.query.json", "customer-invoices.query.dtql"} {
		if err = os.Remove(filepath.Join(projectDir, "queries", "customers", name)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := computeRunQueryWithOptions(context.Background(), req, runQueryOptions{expectedQueryRevision: string(saved.Revision), acceptedQuery: &accepted})
	if err != nil || result.Execution == nil {
		t.Fatal("exact accepted saved body did not execute through ordinary secured source path", err)
	}
	evidence, err := api.ExecutionEvidenceStoreByID(projectID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := evidence.Execution(context.Background(), *result.Execution)
	if err != nil || record.QueryRevision != string(saved.Revision) {
		t.Fatal("execution lost accepted revision", err)
	}
	broken := accepted
	broken.ID = "other"
	if _, err = computeRunQueryWithOptions(context.Background(), req, runQueryOptions{expectedQueryRevision: string(saved.Revision), acceptedQuery: &broken}); err == nil {
		t.Fatal("query identity mismatch accepted")
	}
	if _, err = computeRunQueryWithOptions(context.Background(), req, runQueryOptions{acceptedQuery: &accepted}); err == nil {
		t.Fatal("query snapshot without revision accepted")
	}
}

func TestNativeQueryRunnerRejectsCorruptPrivateQueryJSON(t *testing.T) {
	old := detectiveAcceptedQuery
	defer func() { detectiveAcceptedQuery = old }()
	detectiveAcceptedQuery = func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, bool) {
		return sessioncontext.DetectiveAcceptedQueryPin{Revision: "revision", DefinitionJSON: []byte("{")}, true
	}
	_, err := detectiveQueryRunner("p", "security")(context.Background(), "q", "qa", nil)
	if err == nil {
		t.Fatal("corrupt private query reached source")
	}
	var failure *detective.QueryFailure
	if !errors.As(err, &failure) || failure.Code != "SOURCE_UNAVAILABLE" {
		t.Fatal(err)
	}
}
