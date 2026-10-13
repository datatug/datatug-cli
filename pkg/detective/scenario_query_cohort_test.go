package detective

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestScenarioQueryReceiptMustMatchCurrentCaseCohort(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	if _, err := s.SaveScenario(ctx, Scenario{ID: "triage", Title: "Triage", Steps: []ScenarioStep{{ID: "inspect", Kind: "query", Ref: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	seven := investigation.NewIntegerValue("7")
	c, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Case", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, "triage", "alice")
	if err != nil {
		t.Fatal(err)
	}
	proof := &ScenarioQueryProof{Execution: apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "execution-1"}, QueryID: "q", Environment: "qa", ExecutionStatus: "completed"}
	request := ActionRequest{Type: "record-scenario-step", Project: "p", CaseID: c.ID, ApplicationID: c.ScenarioApplications[0].ID, StepID: "inspect", StepStatus: "completed", QueryID: "q", Environment: "qa", Execution: &proof.Execution}
	ninetyNine := investigation.NewIntegerValue("99")
	request.Inputs = map[string]InputValue{"invoice_id": {Scalar: &ninetyNine}}
	if _, err := s.ExecuteAction(ctx, "wrong-cohort-proof", "alice", request, nil, nil, nil, proof); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrelated query receipt completed Case Scenario: %v", err)
	}
	current, err := s.GetCase(ctx, c.ID)
	if err != nil || current.ScenarioApplications[0].Steps[0].Status == "completed" {
		t.Fatalf("unrelated receipt changed Scenario progress: %+v %v", current, err)
	}
	request.Inputs = map[string]InputValue{"invoice_id": {Scalar: &seven}}
	completed, err := s.ExecuteAction(ctx, "exact-cohort-proof", "alice", request, nil, nil, nil, proof)
	if err != nil || completed.Case == nil || completed.Case.ScenarioApplications[0].Steps[0].Status != "completed" {
		t.Fatalf("exact authorized query receipt did not complete Scenario: %+v %v", completed, err)
	}
}
