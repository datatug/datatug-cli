package endpoints

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/tugqlbind"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
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
	writeText("by-catalog-id", "databases (\n  crm = 'datatug:///chinook-local'\n  geo\n)\nfrom crm::Customer as c\njoin geo::Employee as e\n  on c.SupportRepId = e.EmployeeId\nwhere c.CustomerId <= 3\nselect c.CustomerId, e.LastName\n")
	writeText("other-env", "databases (\n  geo = 'datatug:///geo?env=prod'\n)\nfrom geo::Employee as e\nselect e.LastName\n")
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
	var ce *contractError
	if _, err := computeRunQuery(context.Background(), federatedRunRequest(t, "support-reps")); !errors.As(err, &ce) || ce.Code != meaninglabels.CodeFieldProtected {
		t.Fatalf("a policy-governed federated read with no label guard: %v", err)
	}
}

// A leaf is checked by its catalog's label id, whichever of its names the
// query wrote; a label refusal answers FIELD_PROTECTED.
func TestExecRunQueryFederatedLabelsUseTheCatalogID(t *testing.T) {
	restoreFederatedLabelGuard(t)
	SetFederatedLabelGuard(func(string) secureread.FederatedColumnGuard {
		return func(_ context.Context, leaf secureread.FederatedLeaf) error {
			if leaf.Database == "chinook" && leaf.Collection == "Customer" {
				return &secureread.FieldProtectedError{Message: "chinook.Customer.Email is protected"}
			}
			return nil
		}
	})
	var ce *contractError
	_, err := computeRunQuery(context.Background(), federatedRunRequest(t, "by-catalog-id"))
	if !errors.As(err, &ce) || ce.Code != meaninglabels.CodeFieldProtected || ce.Message != "chinook.Customer.Email is protected" {
		t.Fatalf("crm = chinook-local: %v", err)
	}
}

func TestExecRunQueryFederatedRefusesASourceOrAnOverride(t *testing.T) {
	var ce *contractError
	req := federatedRunRequest(t, "support-reps")
	req.Source = "chinook"
	if _, err := computeRunQuery(context.Background(), req); !errors.As(err, &ce) || ce.Code != apicontract.ErrCodeInvalidRequest || ce.Field != "source" {
		t.Fatalf("with a source: %v", err)
	}
	req.Source = ""
	ctx := context.WithValue(context.Background(), watchCloudSourcesKey{}, map[string]watchCloudSource{req.QueryID: {}})
	if _, err := computeRunQuery(ctx, req); !errors.Is(err, watch.ErrForbidden) {
		t.Fatalf("with a cloud-watch override: %v", err)
	}
}

func TestServedCatalog(t *testing.T) {
	ctx := context.Background()
	if _, _, err := servedCatalog(ctx, nil, "", "local", "prod", "geo"); !tugqlbind.IsError(err) {
		t.Fatalf("another environment: %v", err)
	}
	oldResolve, oldEligible := resolveSourceHook, eligibleTargetsHook
	t.Cleanup(func() { resolveSourceHook, eligibleTargetsHook = oldResolve, oldEligible })
	resolveSourceHook = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return api.ResolvedSource{ID: "notes", Kind: api.SourceKindHTTP, URL: "project://x"}, nil
	}
	if _, _, err := servedCatalog(ctx, nil, "", "local", "local", "notes"); !tugqlbind.IsError(err) {
		t.Fatalf("an HTTP source: %v", err)
	}
	resolveSourceHook = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return api.ResolvedSource{ID: "chinook-local", Kind: api.SourceKindSQL, URL: "sqlite:///c.db"}, nil
	}
	boom := errors.New("boom")
	eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return nil, boom
	}
	var unavailable bindingSourceError
	if _, _, err := servedCatalog(ctx, nil, "", "local", "local", "chinook-local"); !errors.As(err, &unavailable) || !errors.Is(unavailable.err, boom) {
		t.Fatalf("targets that cannot be listed: %v", err)
	}
	eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return nil, nil
	}
	if _, _, err := servedCatalog(ctx, nil, "", "local", "local", "chinook-local"); !errors.As(err, &unavailable) {
		t.Fatalf("a catalog that is no listed target is not read: %v", err)
	}
}

func TestExecRunQueryFederatedBindingFailures(t *testing.T) {
	var ce *contractError
	req := federatedRunRequest(t, "other-project")
	_, err := computeRunQuery(context.Background(), req)
	if !errors.As(err, &ce) || ce.Code != apicontract.ErrCodeInvalidRequest {
		t.Fatalf("another project: %v", err)
	}
	req.QueryID = "other-env"
	_, err = computeRunQuery(context.Background(), req)
	if !errors.As(err, &ce) || ce.Code != apicontract.ErrCodeInvalidRequest {
		t.Fatalf("another environment: %v", err)
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
	query, err := tugql.Deserialize([]byte("from: {database: sales, name: Customer}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := agentFederatedGuard(ctx, req.Project, query, map[string]tugqlbind.Binding{"sales": {Catalog: "chinook", Env: "local"}}); err != nil {
		t.Fatalf("a source the hook admits: %v", err)
	}
}

// A query the policy denies gets the policy's answer before any label one.
func TestExecRunQueryAgentFederatedReadDeniedByPolicy(t *testing.T) {
	projectDir, projectID := writeFederatedRunQueryProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "bob", []string{"support"})
	req := apicontract.ExecutionRequest{Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID, QueryID: "support-reps", Mode: apicontract.ProvenanceModeLive}
	old := agentRawRefusalHook
	t.Cleanup(func() { agentRawRefusalHook = old })
	agentRawRefusalHook = func(context.Context, string, string, string) error { return errors.New("label answer") }
	var ce *contractError
	_, err := computeRunQuery(context.WithValue(context.Background(), agentContextKey{}, true), req)
	if !errors.As(err, &ce) || ce.Code != apicontract.ErrCodeAccessDenied {
		t.Fatalf("support reads no Employee: %v", err)
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
