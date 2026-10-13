package detectiveui

import (
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/detective"
)

func submissionModel(kind string, fields []field) *Model {
	m := New(&fixtureService{}, "detective")
	m.begin(kind, kind, fields)
	return m
}

func TestNativeCheckAndValidatorFormsProduceReviewableActions(t *testing.T) {
	m := submissionModel("new-check", []field{f("ID", "invoice", ""), f("Title", "Validate invoice", ""), f("Inputs", "invoice_id:integer:scalar:required", "")})
	if req, err := m.submitForm(); err != nil || req != nil || m.form.kind != "first-step" || len(m.form.check.Inputs) != 1 {
		t.Fatalf("Check authoring did not request first step: %+v, %v, %+v", req, err, m.form)
	}
	m.form.fields = stepFields(detective.Step{ID: "load", Kind: "query", QueryID: "invoice-lines"})
	req, err := m.submitForm()
	if err != nil || req.Type != "save-check" || req.Check == nil || req.Check.Steps[0].QueryID != "invoice-lines" {
		t.Fatalf("first query step = %+v, %v", req, err)
	}
	check := *req.Check
	m = submissionModel("edit-check", []field{f("ID", "renamed", ""), f("Title", "Validate", "")})
	m.form.check = check
	if _, err := m.submitForm(); err == nil || !strings.Contains(err.Error(), "cannot change") {
		t.Fatalf("Check ID rename accepted: %v", err)
	}
	m = submissionModel("add-step", stepFields(detective.Step{ID: "customer", Kind: "check", CheckID: "validate-customer"}))
	m.form.check = check
	req, err = m.submitForm()
	if err != nil || len(req.Check.Steps) != 2 || req.Check.Steps[1].CheckID != "validate-customer" {
		t.Fatalf("nested Check authoring = %+v, %v", req, err)
	}
	check = *req.Check
	m = submissionModel("add-validator", validatorFields(detective.Validator{ID: "required", Scope: "row", Kind: "non-null", Severity: "error", Field: "InvoiceId"}, ""))
	m.form.check, m.form.stepIndex = check, 0
	req, err = m.submitForm()
	if err != nil || len(req.Check.Steps[0].Validators) != 1 || req.Check.Steps[0].Validators[0].Field != "InvoiceId" {
		t.Fatalf("local Validator authoring = %+v, %v", req, err)
	}
	check = *req.Check
	m = submissionModel("edit-step", stepFields(detective.Step{ID: "load", Kind: "query", QueryID: "invoice-lines-v2"}))
	m.form.check, m.form.stepIndex = check, 0
	req, err = m.submitForm()
	if err != nil || req.Check.Steps[0].QueryID != "invoice-lines-v2" || len(req.Check.Steps[0].Validators) != 1 {
		t.Fatalf("step edit erased Validator: %+v, %v", req, err)
	}
	m = submissionModel("promote-validator", validatorFields(detective.Validator{ID: "required", Scope: "row", Kind: "non-null", Severity: "error", Field: "InvoiceId"}, "shared-required"))
	req, err = m.submitForm()
	if err != nil || req.Type != "save-validator" || req.Validator == nil || req.Validator.ID != "shared-required" {
		t.Fatalf("promotion = %+v, %v", req, err)
	}
	m = submissionModel("new-query-validators", []field{f("Query ID", "invoice-lines", "")})
	if req, err := m.submitForm(); err != nil || req != nil || m.form.kind != "first-query-validator" {
		t.Fatalf("query-local Validator authoring = %+v, %v", req, err)
	}
	m.form.fields = validatorFields(detective.Validator{ID: "required", Scope: "row", Kind: "non-null", Severity: "error", Field: "InvoiceId"}, "")
	req, err = m.submitForm()
	if err != nil || req.Type != "save-query-validators" || req.QueryValidators.QueryID != "invoice-lines" || len(req.QueryValidators.Validators) != 1 {
		t.Fatalf("query-local Validator save = %+v, %v", req, err)
	}
	m = submissionModel("run-query-validators", []field{f("Environment", "qa", ""), f("Inputs", "invoice_id=integer:scalar:7", "")})
	m.form.queryValidators = *req.QueryValidators
	req, err = m.submitForm()
	if err != nil || req.Type != "run-query-validators" || req.QueryID != "invoice-lines" || req.Inputs["invoice_id"].Scalar == nil {
		t.Fatalf("manual query Validator run = %+v, %v", req, err)
	}
}

func TestNativeCaseAndScenarioFormsPreserveSemanticIntent(t *testing.T) {
	m := submissionModel("new-case", []field{f("ID", "case-7", ""), f("Title", "Invoice mismatch", "")})
	req, err := m.submitForm()
	if err != nil || req.Type != "create-case" || req.Case.Status != "active" {
		t.Fatalf("Case create = %+v, %v", req, err)
	}
	caseItem := *req.Case
	m = submissionModel("context", []field{f("Name", "invoice_ids", ""), f("Type", "integer", ""), f("Cardinality", "set", ""), f("Value", "1,2,3,4,5,6,7", "")})
	m.form.caseItem = caseItem
	req, err = m.submitForm()
	if err != nil || req.Case.Context["invoice_ids"].Set == nil || len(req.Case.Context["invoice_ids"].Set.Values) != 7 {
		t.Fatalf("Case cohort edit = %+v, %v", req, err)
	}
	caseItem = *req.Case
	m = submissionModel("evidence", []field{f("ID", "source-1", ""), f("Kind", "external", ""), f("Ref", "evidence.pdf", ""), f("Note", "signed", "")})
	m.form.caseItem = caseItem
	req, err = m.submitForm()
	if err != nil || len(req.Case.Evidence) != 1 || req.Case.Evidence[0].Ref != "evidence.pdf" {
		t.Fatalf("Case evidence = %+v, %v", req, err)
	}
	caseItem = *req.Case
	m = submissionModel("finding", []field{f("ID", "finding-1", ""), f("Text", "Totals differ", ""), f("Status", "confirmed", ""), f("Evidence refs", "source-1", "")})
	m.form.caseItem = caseItem
	req, err = m.submitForm()
	if err != nil || len(req.Case.Findings) != 1 || req.Case.Findings[0].EvidenceRefs[0] != "source-1" {
		t.Fatalf("Case finding = %+v, %v", req, err)
	}
	caseItem = *req.Case
	m = submissionModel("event", []field{f("Message", "Reviewed CRM result", "")})
	m.form.caseItem = caseItem
	req, err = m.submitForm()
	if err != nil || req.Type != "append-event" || req.Event.Message != "Reviewed CRM result" {
		t.Fatalf("Case note = %+v, %v", req, err)
	}
	m = submissionModel("close", []field{f("Reason", "Reconciled", "")})
	m.form.caseItem = caseItem
	req, err = m.submitForm()
	if err != nil || req.Case.Status != "closed" || req.Case.ClosureReason != "Reconciled" {
		t.Fatalf("Case closure = %+v, %v", req, err)
	}
	m = submissionModel("link-incident", []field{f("Incident ref", "ops:INC-1", "")})
	m.form.caseItem = caseItem
	if _, err := m.submitForm(); err == nil || !strings.Contains(err.Error(), "verified Incidentius") {
		t.Fatalf("unverified incident link accepted: %v", err)
	}
	m = submissionModel("link-scenario", []field{f("Scenario ID", "triage", "")})
	m.form.caseItem = caseItem
	m.state.Scenarios = []detective.Scenario{{ID: "triage", Title: "Triage"}}
	req, err = m.submitForm()
	if err != nil || req.Type != "apply-scenario" || req.CaseID != "case-7" || req.ScenarioID != "triage" || req.ApplicationID == "" {
		t.Fatalf("Scenario application = %+v, %v", req, err)
	}
	m = submissionModel("new-scenario", []field{f("ID", "triage", ""), f("Title", "Triage", "")})
	if req, err := m.submitForm(); err != nil || req != nil || m.form.kind != "first-scenario-step" {
		t.Fatalf("Scenario authoring did not request first step: %+v, %v", req, err)
	}
	m.form.fields = []field{f("ID", "validate", ""), f("Kind", "check", ""), f("Ref", "invoice", "")}
	req, err = m.submitForm()
	if err != nil || req.Type != "save-scenario" || len(req.Scenario.Steps) != 1 || req.Scenario.Steps[0].Ref != "invoice" {
		t.Fatalf("Scenario step = %+v, %v", req, err)
	}
}
