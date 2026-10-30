package detective

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestLegacyRunWrapperPreservesExecutionMetadata(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "metadata-check", Title: "Metadata", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	metadataCalls := 0
	run, err := s.Run(ctx, "metadata-check", "qa", "", "alice", nil, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		r := result([]string{"id"}, [][]Value{{investigation.NewIntegerValue("7")}})
		r.Execution = &apicontract.ExecutionRef{StoreID: "local", ProjectID: "p", ExecutionID: "execution-1"}
		return r, nil
	}, func(context.Context, *apicontract.ExecutionRef) (string, string, error) {
		metadataCalls++
		return "revision-1", "fingerprint-1", nil
	})
	if err != nil || run.ID == "" || metadataCalls != 1 || len(run.Steps) != 1 || run.Steps[0].QueryRevision != "revision-1" {
		t.Fatalf("legacy Check run lost execution provenance: run=%+v metadataCalls=%d err=%v", run, metadataCalls, err)
	}
}

func TestLegacyQueryValidatorWrapperRejectsBadReferenceAndRetainedResult(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	runnerCalls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		runnerCalls++
		return apicontract.Result{}, nil
	}
	if _, err := s.RunQueryValidators(ctx, "../escape", "qa", "", "alice", nil, runner); !errors.Is(err, ErrInvalid) || runnerCalls != 0 {
		t.Fatalf("invalid query reference reached runner: calls=%d err=%v", runnerCalls, err)
	}
	if _, err := s.RunQueryValidatorsForResult(ctx, "q", "qa", "", "alice", nil, apicontract.Result{}, runner); err == nil || runnerCalls != 0 {
		t.Fatalf("unverified retained result reached runner: calls=%d err=%v", runnerCalls, err)
	}
}

func TestLegacyRunReturnsExactCompletedRunWhenCaseStatusLookupFails(t *testing.T) {
	s := testStore(t)
	run := CheckRun{ID: "run-1", CaseID: "case-1", ExecutionStatus: "completed"}
	request := ActionRequest{Type: "run-check", Project: "p", CheckID: "check-1", CaseID: "case-1", Environment: "qa"}
	got, err := s.legacyRunResult(context.Background(), "missing-operation", "alice", request, ActionResponse{Run: &run}, nil)
	if got.ID != run.ID || err == nil {
		t.Fatalf("completed run identity lost when Case status unavailable: got=%+v err=%v", got, err)
	}
}
