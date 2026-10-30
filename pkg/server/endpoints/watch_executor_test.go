package endpoints

import (
	"context"
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage"
	"os"
	"path/filepath"
	"testing"
)

func TestWatchExecutorUsesExistingSavedQueryAndPinsSource(t *testing.T) {
	ctx := context.Background()
	dir, project := writeRunQueryTestProject(t)
	previous := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = previous }()
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	private := t.TempDir()
	api.ConfigureSecureSession(secureread.Session{Unrestricted: true}, map[string]string{project: dir}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	defer api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	configureExecutionEvidence(t, project, dir)
	store, err := detective.NewLocalStore(project, dir, private)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveCheck(ctx, detective.Check{ID: "watch-check", Title: "Saved Check", Inputs: []detective.Input{{Name: "CustomerId", Type: "integer", Cardinality: "scalar", Required: true}}, Steps: []detective.Step{{ID: "query", Kind: "query", QueryID: "customers/customer-invoices", Bindings: []detective.Binding{{Input: "CustomerId", FromInput: "CustomerId"}}}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	value := apicontract.NewIntegerValue("1")
	inputs := map[string]detective.InputValue{"CustomerId": {Scalar: &value}}
	run, fingerprint, err := ExecuteWatchCheck(ctx, project, semanticTestEnv, "watch-check", "1", "", "watch-real-first", inputs)
	if err != nil || run.ExecutionStatus != "completed" || fingerprint == "" {
		t.Fatalf("existing Check execution %+v %q %v", run, fingerprint, err)
	}
	if _, _, err = ExecuteWatchCheck(ctx, project, semanticTestEnv, "watch-check", "999", fingerprint, "watch-wrong-root", inputs); !errors.Is(err, detective.ErrConflict) {
		t.Fatal("root changed accepted", err)
	}
	// A same-version saved-query edit must change the catalog admission fingerprint
	// before the query runner can dispatch anything.
	path := filepath.Join(dir, "queries", "customers", "customer-invoices.query.tugql.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(body, []byte("\n# changed saved query revision\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	original := detectiveComputeRunQueryWithOptions
	defer func() { detectiveComputeRunQueryWithOptions = original }()
	detectiveComputeRunQueryWithOptions = func(context.Context, apicontract.ExecutionRequest, runQueryOptions) (apicontract.Result, error) {
		t.Error("changed dependency dispatched a query")
		return apicontract.Result{}, nil
	}
	if _, _, err = ExecuteWatchCheck(ctx, project, semanticTestEnv, "watch-check", "1", fingerprint, "watch-changed-query", inputs); !errors.Is(err, detective.ErrConflict) {
		t.Fatal("dependent revision change accepted", err)
	}
	// An internal adapter must never turn a malformed absent result into false.
	oldAction := watchExecuteAction
	defer func() { watchExecuteAction = oldAction }()
	watchExecuteAction = func(*detective.LocalStore, context.Context, string, string, detective.ActionRequest, string, string, detective.QueryRunner, detective.ExecutionMetadata, detective.QueryRevisionLookup) (detective.ActionResponse, string, error) {
		return detective.ActionResponse{}, "", nil
	}
	if _, _, err = ExecuteWatchCheck(ctx, project, semanticTestEnv, "watch-check", "1", "", "watch-empty-result", inputs); !errors.Is(err, detective.ErrInvalid) {
		t.Fatal("absent Check result accepted", err)
	}
}
func TestWatchExecutorInvalidAndSecuredAssetsFailClosed(t *testing.T) {
	ctx := context.Background()
	for _, input := range []struct{ env, op string }{{"../escape", "op"}, {"local", ""}} {
		if _, _, e := ExecuteWatchCheck(ctx, "p", input.env, "check", "1", "", input.op, nil); !errors.Is(e, detective.ErrInvalid) {
			t.Fatal(e)
		}
	}
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	if _, _, e := ExecuteWatchCheck(ctx, "missing", "local", "check", "1", "", "op", nil); e == nil {
		t.Fatal("unavailable asset accepted")
	}
}
