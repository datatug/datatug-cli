package endpoints

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/tugqlbind"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// supportReps joins two project sources: sales, the project's chinook, and
// geo, the source of the same name (a second Chinook copy).
const supportReps = `databases (
  sales = 'datatug:///chinook'
  geo
)
from sales::Customer as c
join geo::Employee as e
  on c.SupportRepId = e.EmployeeId
where c.CustomerId <= 3
select c.CustomerId, e.LastName
`

// writeFederatedRunQueryProject is writeRunQueryTestProject with a second
// source, geo, and saved text queries that read both.
func writeFederatedRunQueryProject(t *testing.T) (projectDir, projectID string) {
	t.Helper()
	projectDir, projectID = writeRunQueryTestProject(t)
	geoDir := filepath.Join(projectDir, "geo")
	mustMkdirAll(t, geoDir)
	geoPath := copyChinookDB(t, geoDir)
	store := filestore.NewProjectStore(projectID, projectDir)
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: geoPath, DbModel: "geo"}}
	catalog.ID = "geo"
	if err := store.SaveEnvDbCatalog(context.Background(), semanticTestEnv, serverID, catalog.ID, catalog); err != nil {
		t.Fatalf("SaveEnvDbCatalog: %v", err)
	}
	writeText := func(id, text string) {
		queries := filepath.Join(projectDir, "queries")
		mustWriteFile(t, filepath.Join(queries, id+".query.json"), `{"createdAt":"2026-11-07T00:00:00Z","id":"`+id+`","type":"TugQL"}`)
		mustWriteFile(t, filepath.Join(queries, id+".query.tugql"), text)
	}
	writeText("support-reps", supportReps)
	writeText("other-project", "databases (\n  geo = 'datatug://elsewhere/geo'\n)\nfrom geo::Employee as e\nselect e.LastName\n")
	writeText("missing-source", "databases (\n  nowhere\n)\nfrom nowhere::Employee as e\nselect e.LastName\n")
	return projectDir, projectID
}

func federatedRunRequest(t *testing.T, queryID string) apicontract.ExecutionRequest {
	t.Helper()
	projectDir, projectID := writeFederatedRunQueryProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	return apicontract.ExecutionRequest{Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID, QueryID: queryID, Mode: apicontract.ProvenanceModeLive}
}

func restoreFederatedLabelGuard(t *testing.T) {
	t.Helper()
	old := federatedLabelGuardFor
	t.Cleanup(func() { federatedLabelGuardFor = old })
}

// REQ:serve-cross-database-queries — exec/run_query runs a saved text query
// that joins two project sources, each leaf checked by the label guard serve
// installs, and reports the root's source.
func TestExecRunQueryRunsASavedQueryAcrossTwoDatabases(t *testing.T) {
	restoreFederatedLabelGuard(t)
	var leaves []secureread.FederatedLeaf
	SetFederatedLabelGuard(func(projectDir string) secureread.FederatedColumnGuard {
		return func(_ context.Context, leaf secureread.FederatedLeaf) error {
			leaves = append(leaves, leaf)
			return nil
		}
	})
	result, err := computeRunQuery(context.Background(), federatedRunRequest(t, "support-reps"))
	if err != nil {
		t.Fatalf("computeRunQuery: %v", err)
	}
	if len(result.Recordset.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(result.Recordset.Rows))
	}
	if result.Provenance.Source != "chinook" {
		t.Fatalf("provenance source = %q, want the root's chinook", result.Provenance.Source)
	}
	catalogs := map[string]bool{}
	for _, leaf := range leaves {
		catalogs[leaf.Database] = true
	}
	if !catalogs["chinook"] || !catalogs["geo"] || len(catalogs) != 2 {
		t.Fatalf("leaves checked against %v, want chinook and geo", catalogs)
	}
}

// Without a label guard, a policy-governed federated read is refused.
func TestExecRunQueryFederatedReadWithoutALabelGuardIsRefused(t *testing.T) {
	restoreFederatedLabelGuard(t)
	federatedLabelGuardFor = nil
	if _, err := computeRunQuery(context.Background(), federatedRunRequest(t, "support-reps")); err == nil {
		t.Fatal("a policy-governed federated read ran with no label guard")
	}
}

func TestExecRunQueryFederatedBindingFailures(t *testing.T) {
	var ce *contractError
	req := federatedRunRequest(t, "other-project")
	_, err := computeRunQuery(context.Background(), req)
	if !errors.As(err, &ce) || ce.Code != apicontract.ErrCodeInvalidRequest {
		t.Fatalf("another project: %v", err)
	}
	req.QueryID = "missing-source"
	_, err = computeRunQuery(context.Background(), req)
	if !errors.As(err, &ce) || ce.Code != apicontract.ErrCodeSourceUnavailable {
		t.Fatalf("missing source: %v", err)
	}
}

// An agent's federated read is judged like raw SQL on every bound source.
func TestExecRunQueryAgentFederatedReadIsGuardedPerSource(t *testing.T) {
	req := federatedRunRequest(t, "support-reps")
	ctx := context.WithValue(context.Background(), agentContextKey{}, true)
	old := agentRawRefusalHook
	t.Cleanup(func() { agentRawRefusalHook = old })
	var judged []string
	refused := errors.New("refused")
	agentRawRefusalHook = func(_ context.Context, _, env, source string) error {
		judged = append(judged, env+"/"+source)
		if source == "geo" {
			return refused
		}
		return nil
	}
	if _, err := computeRunQuery(ctx, req); !errors.Is(err, refused) {
		t.Fatalf("agent read: %v", err)
	}
	if len(judged) != 1 || judged[0] != "local/geo" {
		t.Fatalf("judged %v, want geo first in alias order (geo, sales), where the refusal stops it", judged)
	}
	if err := agentFederatedGuard(ctx, req.Project, map[string]tugqlbind.Binding{"sales": {Catalog: "chinook", Env: "local"}}); err != nil {
		t.Fatalf("a source the hook admits: %v", err)
	}
}

func TestFederatedExecutorKeepsTheSessionWithoutAGuard(t *testing.T) {
	restoreFederatedLabelGuard(t)
	federatedLabelGuardFor = nil
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	if federatedExecutor(executor, nil) != executor {
		t.Fatal("no guard to install, yet the executor was replaced")
	}
}
