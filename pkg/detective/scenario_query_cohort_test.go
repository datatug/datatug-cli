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

func TestScenarioQueryReceiptBindsImmutableRangeStepToCurrentCase(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	scenario := Scenario{ID: "window-review", Title: "Window review", Inputs: []Input{{Name: "window", Type: investigation.ValueTypeDate, Cardinality: "range", Required: true}}, Steps: []ScenarioStep{{ID: "inspect", Kind: "query", Ref: "q", Bindings: []Binding{{Input: "from_date", FromInput: "window", Part: "start"}, {Input: "through_date", FromInput: "window", Part: "end"}}}}}
	scenario, err := s.SaveScenario(ctx, scenario, "alice")
	if err != nil {
		t.Fatal(err)
	}
	start, end := investigation.NewDateValue("2026-10-01"), investigation.NewDateValue("2026-10-07")
	c, err := s.SaveCase(ctx, Case{ID: "range-case", Title: "Range case", Context: map[string]InputValue{"window": {Range: &ValueRange{Type: "range", Start: start, End: end}}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, scenario.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	// A later catalog edit must not replace the bindings already applied to
	// this Case. The immutable application snapshot is the proof authority.
	scenario.Steps[0].Bindings[0].Input = "other_start"
	if _, err := s.SaveScenario(ctx, scenario, "alice"); err != nil {
		t.Fatal(err)
	}
	proof := &ScenarioQueryProof{Execution: apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "range-execution"}, QueryID: "q", Environment: "qa", ExecutionStatus: "completed"}
	request := ActionRequest{Type: "record-scenario-step", Project: "p", CaseID: c.ID, ApplicationID: c.ScenarioApplications[0].ID, StepID: "inspect", StepStatus: "completed", QueryID: "q", Environment: "qa", Execution: &proof.Execution}
	request.Inputs = map[string]InputValue{"from_date": {Scalar: &start}, "through_date": {Scalar: &end}}
	missingStep := request
	missingStep.StepID = "absent"
	if _, err := s.ExecuteAction(ctx, "range-missing-step", "alice", missingStep, nil, nil, nil, proof); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing applied Scenario step accepted: %v", err)
	}
	wrongQuery := *proof
	wrongQuery.QueryID = "other"
	if _, err := s.ExecuteAction(ctx, "range-wrong-query", "alice", request, nil, nil, nil, &wrongQuery); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrelated query proof accepted: %v", err)
	}
	changedStart := investigation.NewDateValue("2026-10-02")
	request.Inputs["from_date"] = InputValue{Scalar: &changedStart}
	if _, err := s.ExecuteAction(ctx, "range-wrong-start", "alice", request, nil, nil, nil, proof); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed start bound accepted: %v", err)
	}
	request.Inputs["from_date"] = InputValue{Scalar: &start}
	changedEnd := investigation.NewDateValue("2026-10-08")
	request.Inputs["through_date"] = InputValue{Scalar: &changedEnd}
	if _, err := s.ExecuteAction(ctx, "range-wrong-end", "alice", request, nil, nil, nil, proof); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed end bound accepted: %v", err)
	}
	request.Inputs["through_date"] = InputValue{Scalar: &end}
	completed, err := s.ExecuteAction(ctx, "range-exact", "alice", request, nil, nil, nil, proof)
	if err != nil || completed.Case == nil || completed.Case.ScenarioApplications[0].Steps[0].Status != "completed" {
		t.Fatalf("exact range proof did not complete step: %+v %v", completed, err)
	}
}

func TestScenarioQueryReceiptRejectsAppliedStepWithoutRangeBindings(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	if _, err := s.SaveScenario(ctx, Scenario{ID: "legacy", Title: "Legacy", Steps: []ScenarioStep{{ID: "inspect", Kind: "query", Ref: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	start, end := investigation.NewDateValue("2026-10-01"), investigation.NewDateValue("2026-10-07")
	c, err := s.SaveCase(ctx, Case{ID: "legacy-case", Title: "Legacy Case", Context: map[string]InputValue{"window": {Range: &ValueRange{Type: "range", Start: start, End: end}}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, "legacy", "alice")
	if err != nil {
		t.Fatal(err)
	}
	proof := &ScenarioQueryProof{Execution: apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "legacy-execution"}, QueryID: "q", Environment: "qa", ExecutionStatus: "completed"}
	request := ActionRequest{Type: "record-scenario-step", Project: "p", CaseID: c.ID, ApplicationID: c.ScenarioApplications[0].ID, StepID: "inspect", StepStatus: "completed", QueryID: "q", Environment: "qa", Execution: &proof.Execution, Inputs: map[string]InputValue{"from_date": {Scalar: &start}, "through_date": {Scalar: &end}}}
	if _, err := s.ExecuteAction(ctx, "legacy-missing-binding", "alice", request, nil, nil, nil, proof); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbound Case range accepted: %v", err)
	}
}

func TestScenarioQueryReceiptRejectsMisalignedApplicationSteps(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	bindings := []Binding{{Input: "from_date", FromInput: "window", Part: "start"}, {Input: "through_date", FromInput: "window", Part: "end"}}
	scenario := Scenario{ID: "ordered", Title: "Ordered", Inputs: []Input{{Name: "window", Type: investigation.ValueTypeDate, Cardinality: "range", Required: true}}, Steps: []ScenarioStep{{ID: "inspect", Kind: "query", Ref: "q", Bindings: bindings}, {ID: "followup", Kind: "query", Ref: "q", Bindings: bindings}}}
	if _, err := s.SaveScenario(ctx, scenario, "alice"); err != nil {
		t.Fatal(err)
	}
	start, end := investigation.NewDateValue("2026-10-01"), investigation.NewDateValue("2026-10-07")
	c, err := s.SaveCase(ctx, Case{ID: "ordered-case", Title: "Ordered case", Context: map[string]InputValue{"window": {Range: &ValueRange{Type: "range", Start: start, End: end}}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, scenario.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	proof := &ScenarioQueryProof{Execution: apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "ordered-execution"}, QueryID: "q", Environment: "qa", ExecutionStatus: "completed"}
	request := ActionRequest{Type: "record-scenario-step", Project: "p", CaseID: c.ID, ApplicationID: c.ScenarioApplications[0].ID, StepID: "inspect", StepStatus: "completed", QueryID: "q", Environment: "qa", Execution: &proof.Execution, Inputs: map[string]InputValue{"from_date": {Scalar: &start}, "through_date": {Scalar: &end}}}
	for _, tc := range []struct {
		name   string
		mutate func(*ScenarioApplication)
	}{
		{"reordered-receipts", func(application *ScenarioApplication) {
			application.Steps[0], application.Steps[1] = application.Steps[1], application.Steps[0]
			application.Steps[0].Status = "skipped" // Allow the second receipt if its snapshot identity were ignored.
		}},
		{"missing-receipt", func(application *ScenarioApplication) {
			application.Steps = application.Steps[:1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := c
			corrupt.ScenarioApplications = append([]ScenarioApplication(nil), c.ScenarioApplications...)
			application := &corrupt.ScenarioApplications[0]
			application.Steps = append([]ScenarioStepReceipt(nil), application.Steps...)
			tc.mutate(application)
			if err := writeItem(s.privateItemDir("cases"), c.ID, corrupt); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ExecuteAction(ctx, "misaligned-"+tc.name, "alice", request, nil, nil, nil, proof); !errors.Is(err, ErrInvalid) {
				t.Fatalf("misaligned application step accepted: %v", err)
			}
			persisted, err := s.GetCase(ctx, c.ID)
			if err != nil || persisted.Version != corrupt.Version || persisted.ScenarioApplications[0].Steps[len(persisted.ScenarioApplications[0].Steps)-1].Status == "completed" {
				t.Fatalf("rejected proof changed persisted Case: %+v %v", persisted, err)
			}
		})
	}
}
