package detectiveui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

type annotatedQueryService struct{ fixtureService }

func (s *annotatedQueryService) Action(_ context.Context, request detective.ActionRequest) (detective.ActionResponse, error) {
	s.actions = append(s.actions, request)
	index := 1
	ref := *request.Execution
	nestedRow := 0
	run := detective.CheckRun{ID: "validation-1", Kind: "query-validators", QueryID: request.QueryID, Environment: request.Environment, ExecutionStatus: "completed", Outcome: "fail", Steps: []detective.StepResult{{StepID: "query", Kind: "query", QueryID: request.QueryID, Execution: &ref, Validators: []detective.ValidationResult{{ValidatorID: "invoice-required", RowIndex: &index, Outcome: "fail", ExecutionStatus: "completed", Children: []detective.StepResult{{StepID: "child-query", Kind: "query", QueryID: "customer-check", Validators: []detective.ValidationResult{{ValidatorID: "customer-required", RowIndex: &nestedRow, Outcome: "fail"}}}}}}}}}
	s.state.Runs = []detective.CheckRun{run}
	return detective.ActionResponse{Run: &run}, nil
}

func TestNativePasteUpdatesOnlyFocusedEditableText(t *testing.T) {
	m := New(&fixtureService{}, "checks")
	m.state.Checks = []detective.Check{{ID: "invoice-check", Title: "Invoices"}}
	m.rebuild()
	m.Update(tea.PasteMsg{Content: "invoice"})
	if m.filter != "invoice" || len(m.rows) != 1 {
		t.Fatalf("catalog paste lost or misrouted: filter=%q rows=%+v", m.filter, m.rows)
	}
	m.begin("new-case", "Case", []field{f("ID", "", ""), f("Title", "", "")})
	m.Update(tea.PasteMsg{Content: "case-1"})
	if m.field("ID") != "case-1" || m.filter != "invoice" {
		t.Fatalf("form paste did not respect focus: form=%q filter=%q", m.field("ID"), m.filter)
	}
	m.form.fields[0].readOnly = true
	m.Update(tea.PasteMsg{Content: "override"})
	if m.field("ID") != "case-1" {
		t.Fatalf("read-only review value changed by paste: %q", m.field("ID"))
	}
	m.busy = true
	m.form.fields[0].readOnly = false
	m.Update(tea.PasteMsg{Content: "override"})
	if m.field("ID") != "case-1" {
		t.Fatalf("in-flight form changed by paste: %q", m.field("ID"))
	}
}

func TestUnacceptedNativeEditsCannotMutateCachedCaseOrValidator(t *testing.T) {
	value := investigation.NewIntegerValue("7")
	check := detective.Check{ID: "check-1", Title: "Invoices", Steps: []detective.Step{{ID: "query", Kind: "query", QueryID: "invoices", Validators: []detective.Validator{{ID: "required", Kind: "non-null", Scope: "row", Field: "InvoiceId"}}}}}
	caseItem := detective.Case{ID: "case-1", Title: "Invoice case", Context: map[string]detective.InputValue{"invoice_id": {Scalar: &value}}}
	m := New(&fixtureService{}, "detective")
	m.state.Cases = []detective.Case{caseItem}
	m.state.Checks = []detective.Check{check}
	m.screen, m.parent, m.id = detail, cases, "case-1"
	m.rebuild()
	m.detailKey("c")
	m.form.fields[0].value = "invoice_id"
	m.form.fields[1].value = "integer"
	m.form.fields[3].value = "99"
	req, err := m.submitForm()
	if err != nil || req == nil || req.Case.Context["invoice_id"].Scalar.Str != "99" {
		t.Fatalf("Case edit not formed: %+v %v", req, err)
	}
	if m.state.Cases[0].Context["invoice_id"].Scalar.Str != "7" {
		t.Fatal("rejected/canceled Case edit changed cached context before server acceptance")
	}
	m.screen, m.parent, m.id = detail, checks, "check-1"
	m.rebuild()
	for i := range m.rows {
		if m.rows[i].kind == "validator" {
			m.index = i
		}
	}
	m.detailKey("e")
	m.form.fields[6].value = "CustomerId"
	req, err = m.submitForm()
	if err != nil || req == nil || req.Check.Steps[0].Validators[0].Field != "CustomerId" {
		t.Fatalf("Check Validator edit not formed: %+v %v", req, err)
	}
	if m.state.Checks[0].Steps[0].Validators[0].Field != "InvoiceId" {
		t.Fatal("rejected/canceled Validator edit changed cached Check before server acceptance")
	}
}

func TestReusableValidatorAndScenarioSchemaStepsHaveTypedEditForms(t *testing.T) {
	m := New(&fixtureService{}, "detective")
	m.state.Validators = []detective.PublishedValidator{{ID: "invoice-required", Title: "Invoice required", Version: 2, Validator: detective.Validator{ID: "required", Scope: "row", Kind: "non-null", Field: "InvoiceId"}}}
	m.state.Scenarios = []detective.Scenario{{ID: "reconcile", Title: "Reconcile", Version: 3, Inputs: []detective.Input{{Name: "invoice_ids", Type: investigation.ValueTypeInteger, Cardinality: "set", Required: true}}, Steps: []detective.ScenarioStep{{ID: "inspect", Kind: "query", Ref: "invoices"}}}}
	m.screen, m.parent, m.id = detail, validators, "invoice-required"
	m.rebuild()
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-promoted" || m.field("Field") != "InvoiceId" {
		t.Fatalf("reusable Validator has no semantic edit: %+v", m.form)
	}
	setNativeField(m, "Field", "CustomerId")
	req, err := m.submitForm()
	if err != nil || req == nil || req.Type != "save-validator" || req.Validator.Version != 2 || req.Validator.Validator.Field != "CustomerId" || m.state.Validators[0].Validator.Field != "InvoiceId" {
		t.Fatalf("reusable Validator semantic edit was lost or changed cached state: %+v, %v", req, err)
	}
	m.screen, m.parent, m.id = detail, scenarios, "reconcile"
	m.rebuild()
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-scenario" || m.field("Inputs") != "invoice_ids:integer:set:required" {
		t.Fatalf("Scenario input schema unavailable for edit: %+v", m.form)
	}
	setNativeField(m, "Inputs", "invoice_ids:integer:set:required, customer_id:integer:scalar")
	req, err = m.submitForm()
	if err != nil || req == nil || req.Type != "save-scenario" || len(req.Scenario.Inputs) != 2 || req.Scenario.Version != 3 || len(m.state.Scenarios[0].Inputs) != 1 {
		t.Fatalf("Scenario input schema edit was lost or changed cached state: %+v, %v", req, err)
	}
	m.screen = detail
	m.rebuild()
	for i := range m.rows {
		if m.rows[i].kind == "scenario-step" {
			m.index = i
		}
	}
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-scenario-step" || m.form.stepIndex != 0 || m.field("Ref") != "invoices" {
		t.Fatalf("selected Scenario step unavailable for edit: %+v", m.form)
	}
	setNativeField(m, "Ref", "invoices-reviewed")
	req, err = m.submitForm()
	if err != nil || req == nil || req.Type != "save-scenario" || req.Scenario.Steps[0].Ref != "invoices-reviewed" || m.state.Scenarios[0].Steps[0].Ref != "invoices" {
		t.Fatalf("Scenario selected step edit was lost or changed cached state: %+v, %v", req, err)
	}
}

func setNativeField(m *Model, name, value string) {
	for i := range m.form.fields {
		if m.form.fields[i].label == name {
			m.form.fields[i].value = value
			return
		}
	}
	panic("missing native field " + name)
}

func TestExactQueryRowsRemainVisibleWithRootValidatorAnnotations(t *testing.T) {
	service := &annotatedQueryService{}
	m := New(service, "checks")
	m.parent, m.id, m.screen = queryValidators, "invoice-list", detail
	ref := &apicontract.ExecutionRef{StoreID: "p", ProjectID: "p", ExecutionID: "receipt-1"}
	result := SavedQueryResult{Result: apicontract.Result{Execution: ref, Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "InvoiceId", Type: "integer"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("7")}, {apicontract.NewIntegerValue("9")}}}}, QueryRevision: "rev-1", ResultFingerprint: "fingerprint-1"}
	_, cmd := m.Update(queryRunMsg{result: result, queryID: "invoice-list", environment: "qa", validators: true})
	if cmd == nil {
		t.Fatal("exact query Validators were not dispatched")
	}
	m.Update(cmd())
	m.Update(m.load()())
	if m.screen != runDetail || m.queryExecution == nil || m.queryExecution.ExecutionID != "receipt-1" {
		t.Fatalf("exact query receipt lost: screen=%s ref=%+v", m.screen, m.queryExecution)
	}
	joined := m.View().Content
	if !strings.Contains(joined, "InvoiceId=7") || !strings.Contains(joined, "InvoiceId=9") || !strings.Contains(joined, "invoice-required") || !strings.Contains(joined, "receipt-1") {
		t.Fatalf("result rows or annotation missing from native detail: %s", joined)
	}
	for _, row := range m.rows {
		if row.kind == "query-row" && strings.Contains(row.label, "InvoiceId=7") && strings.Contains(row.label, "invoice-required") {
			t.Fatalf("failure annotation attributed to wrong root row: %+v", row)
		}
		if row.kind == "query-row" && strings.Contains(row.label, "customer-required") {
			t.Fatalf("nested Validator row index attributed to root grid: %+v", row)
		}
	}
	ordinary := New(service, "detective")
	ordinary.parent, ordinary.id, ordinary.screen = cases, "case-1", detail
	ordinary.Update(queryRunMsg{result: result, queryID: "invoice-list", environment: "qa"})
	if ordinary.screen != queryDetail || !strings.Contains(ordinary.View().Content, "InvoiceId=9") {
		t.Fatalf("ordinary query rows disappeared after receipt: screen=%s view=%s", ordinary.screen, ordinary.View().Content)
	}
	caseStep := New(service, "detective")
	caseStep.parent, caseStep.id, caseStep.screen = cases, "case-1", detail
	_, cmd = caseStep.Update(queryRunMsg{result: result, queryID: "invoice-list", environment: "qa", applicationID: "application-1", stepID: "inspect"})
	if cmd == nil {
		t.Fatal("Scenario query receipt was not submitted")
	}
	_, cmd = caseStep.Update(actedMsg{response: detective.ActionResponse{Case: &detective.Case{ID: "case-1"}}})
	caseStep.Update(cmd())
	if caseStep.screen != queryDetail || !strings.Contains(caseStep.View().Content, "receipt-1") || !strings.Contains(caseStep.View().Content, "InvoiceId=7") {
		t.Fatalf("Scenario query rows disappeared after Case step completion: screen=%s view=%s", caseStep.screen, caseStep.View().Content)
	}
}
