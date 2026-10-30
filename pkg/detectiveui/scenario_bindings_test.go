package detectiveui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestScenarioBindingFormCreateEditCancelAndValidation(t *testing.T) {
	m := New(&fixtureService{}, "detective")
	m.screen = scenarios
	m.startNew()
	setNativeField(m, "ID", "reconcile")
	setNativeField(m, "Title", "Reconcile invoices")
	setNativeField(m, "Inputs", "customer:integer:scalar, invoices:integer:set, window:date:range")
	if req, err := m.submitForm(); err != nil || req != nil || m.form.kind != "first-scenario-step" {
		t.Fatalf("Scenario create did not enter step editor: %+v %v", req, err)
	}
	setNativeField(m, "ID", "inspect")
	setNativeField(m, "Kind", "query")
	setNativeField(m, "Ref", "invoices")
	setNativeField(m, "Bindings", "customer_id=customer, invoice_ids=invoices, from_date=window.start")
	if req, err := m.submitForm(); err == nil || req != nil || !strings.Contains(err.Error(), "bindings") || m.form.kind != "first-scenario-step" {
		t.Fatalf("missing range end did not keep editable form open: %+v %v", req, err)
	}
	setNativeField(m, "Bindings", "customer_id=customer, invoice_ids=invoices, from_date=window.start, to_date=window.end")
	req, err := m.submitForm()
	if err != nil || req == nil || req.Type != "save-scenario" || req.Scenario == nil || len(req.Scenario.Steps[0].Bindings) != 4 || detective.ValidateScenario(*req.Scenario) != nil {
		t.Fatalf("typed Scenario bindings did not save: %+v %v", req, err)
	}
	saved := *req.Scenario
	m.state.Scenarios = []detective.Scenario{saved}
	m.screen, m.parent, m.id = detail, scenarios, saved.ID
	m.rebuild()
	for i, row := range m.rows {
		if row.kind == "scenario-step" {
			m.index = i
			break
		}
	}
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-scenario-step" || m.field("Bindings") != "customer_id=customer, invoice_ids=invoices, from_date=window.start, to_date=window.end" {
		t.Fatalf("saved bindings not available for edit: %+v", m.form)
	}
	setNativeField(m, "Ref", "invoices-reviewed")
	req, err = m.submitForm()
	if err != nil || req == nil || req.Scenario.Steps[0].Ref != "invoices-reviewed" || len(req.Scenario.Steps[0].Bindings) != 4 || m.state.Scenarios[0].Steps[0].Ref != "invoices" {
		t.Fatalf("step edit lost bindings or mutated cached Scenario: %+v %v", req, err)
	}
	setNativeField(m, "Bindings", "bad-binding")
	press(m, "ctrl+s")
	if m.form == nil || m.form.kind != "edit-scenario-step" || !strings.Contains(m.feedback, "binding") {
		t.Fatalf("invalid binding did not remain editable with feedback: form=%+v feedback=%q", m.form, m.feedback)
	}
	press(m, "esc")
	if m.form != nil || m.screen != detail || len(m.state.Scenarios[0].Steps[0].Bindings) != 4 {
		t.Fatalf("cancel did not return to unchanged Scenario detail: form=%+v screen=%s", m.form, m.screen)
	}
	m.index = 0
	m.detailKey("e")
	setNativeField(m, "Inputs", "customer:integer:scalar, invoices:integer:set")
	if req, err := m.submitForm(); err == nil || req != nil || !strings.Contains(err.Error(), "bindings") {
		t.Fatalf("removing a bound Scenario input was accepted: %+v %v", req, err)
	}
	press(m, "esc")
	if m.screen != detail || m.form != nil {
		t.Fatalf("schema error/cancel did not return to Scenario detail: screen=%s form=%+v", m.screen, m.form)
	}
	m.screen = scenarios
	m.startNew()
	setNativeField(m, "ID", "abandoned")
	setNativeField(m, "Title", "Abandoned draft")
	if req, err := m.submitForm(); err != nil || req != nil || m.form.kind != "first-scenario-step" {
		t.Fatalf("new Scenario did not reach step editor: %+v %v", req, err)
	}
	press(m, "esc")
	if m.screen != scenarios || m.form != nil || len(m.state.Scenarios) != 1 {
		t.Fatalf("canceling first step did not return to catalog without saving: screen=%s form=%+v", m.screen, m.form)
	}
}

func TestScenarioQueryPrefillsBoundCaseValuesFromAppliedSnapshot(t *testing.T) {
	set, err := apicontract.NewTypedValueSet(investigation.NewIntegerValue("7"), investigation.NewIntegerValue("9"))
	if err != nil {
		t.Fatal(err)
	}
	start, end := investigation.NewDateValue("2026-10-01"), investigation.NewDateValue("2026-10-07")
	step := detective.ScenarioStep{ID: "inspect", Kind: "query", Ref: "invoice-list", Bindings: []detective.Binding{{Input: "customer_id", FromInput: "customer"}, {Input: "invoice_ids", FromInput: "invoices"}, {Input: "from_date", FromInput: "window", Part: "start"}, {Input: "to_date", FromInput: "window", Part: "end"}}}
	c := detective.Case{ID: "case-1", Status: "active", Context: map[string]detective.InputValue{"customer": {Scalar: valuePtr(investigation.NewIntegerValue("2"))}, "invoices": {Set: &set}, "window": {Range: &detective.ValueRange{Type: "range", Start: start, End: end}}, "unrelated": {Scalar: valuePtr(investigation.NewStringValue("omit"))}}, ScenarioApplications: []detective.ScenarioApplication{{ID: "application-1", Snapshot: detective.Scenario{Inputs: []detective.Input{{Name: "customer", Type: "integer", Cardinality: "scalar"}, {Name: "invoices", Type: "integer", Cardinality: "set"}, {Name: "window", Type: "date", Cardinality: "range"}}, Steps: []detective.ScenarioStep{step}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	service := &hostedFixture{fixtureService: &fixtureService{state: detective.State{Cases: []detective.Case{c}, Environments: []string{"qa"}}}}
	m := New(service, "detective")
	m.state = service.state
	m.id = c.ID
	m.startScenarioStep(c, "application-1/inspect")
	if m.form == nil || m.form.kind != "scenario-query-run" {
		t.Fatalf("bound Scenario query form did not open: %q", m.feedback)
	}
	text := m.field("Inputs")
	for _, want := range []string{"customer_id=integer:scalar:2", "invoice_ids=integer:set:7, 9", "from_date=date:scalar:2026-10-01", "to_date=date:scalar:2026-10-07"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing bound value %q in %q", want, text)
		}
	}
	if strings.Contains(text, "customer=") || strings.Contains(text, "invoices=") || strings.Contains(text, "window=") || strings.Contains(text, "unrelated=") {
		t.Fatalf("Scenario source names or unrelated Case facts leaked into query parameters: %q", text)
	}
	values, err := parseNamedValues(text)
	if err != nil || detective.MatchScenarioQueryCohort(step, c.Context, values) != nil {
		t.Fatalf("prefill does not satisfy server cohort matcher: %q, %v", text, err)
	}
	cmd := press(m, "ctrl+s")
	if cmd == nil {
		t.Fatalf("bound Scenario query was not dispatched: %q", m.feedback)
	}
	_, next := m.Update(cmd())
	if next == nil {
		t.Fatal("bound Scenario query did not advance to receipt recording")
	}
	m.Update(next())
	if len(service.query.Inputs) != 4 || service.query.Inputs["from_date"].Scalar == nil || service.query.Inputs["to_date"].Scalar == nil || service.query.Inputs["invoice_ids"].Set == nil {
		t.Fatalf("query transport lost bound parameters: %+v", service.query.Inputs)
	}
	if len(service.actions) == 0 || service.actions[len(service.actions)-1].Type != "record-scenario-step" || service.actions[len(service.actions)-1].CaseID != c.ID || service.actions[len(service.actions)-1].Execution == nil {
		t.Fatalf("Scenario receipt did not refer to this Case and execution: %+v", service.actions)
	}
}

func TestScenarioQueryRefusesIncompleteAppliedRangeBinding(t *testing.T) {
	start, end := investigation.NewDateValue("2026-10-01"), investigation.NewDateValue("2026-10-07")
	step := detective.ScenarioStep{ID: "inspect", Kind: "query", Ref: "invoice-list", Bindings: []detective.Binding{{Input: "from_date", FromInput: "window", Part: "start"}}}
	c := detective.Case{ID: "case-1", Context: map[string]detective.InputValue{"window": {Range: &detective.ValueRange{Type: "range", Start: start, End: end}}}, ScenarioApplications: []detective.ScenarioApplication{{ID: "application-1", Snapshot: detective.Scenario{Inputs: []detective.Input{{Name: "window", Type: "date", Cardinality: "range"}}, Steps: []detective.ScenarioStep{step}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	m := New(&hostedFixture{fixtureService: &fixtureService{}}, "detective")
	m.startScenarioStep(c, "application-1/inspect")
	if m.form != nil || m.pendingApplicationID != "" || m.pendingStepID != "" || !strings.Contains(m.feedback, "bindings") {
		t.Fatalf("incomplete applied range binding opened a run: form=%+v pending=%s/%s feedback=%q", m.form, m.pendingApplicationID, m.pendingStepID, m.feedback)
	}
}

func TestScenarioQueryPrefillPreservesQuotedStringValuesThroughExecution(t *testing.T) {
	company := investigation.NewStringValue("  ACME; Inc.  ")
	set, err := apicontract.NewTypedValueSet(investigation.NewStringValue("ACME, Inc."), investigation.NewStringValue(" Beta "))
	if err != nil {
		t.Fatal(err)
	}
	step := detective.ScenarioStep{ID: "inspect", Kind: "query", Ref: "companies", Bindings: []detective.Binding{{Input: "company_param", FromInput: "company"}, {Input: "names_param", FromInput: "names"}}}
	c := detective.Case{ID: "case-strings", Context: map[string]detective.InputValue{"company": {Scalar: &company}, "names": {Set: &set}}, ScenarioApplications: []detective.ScenarioApplication{{ID: "application-strings", Snapshot: detective.Scenario{Inputs: []detective.Input{{Name: "company", Type: investigation.ValueTypeString, Cardinality: "scalar"}, {Name: "names", Type: investigation.ValueTypeString, Cardinality: "set"}}, Steps: []detective.ScenarioStep{step}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	service := &hostedFixture{fixtureService: &fixtureService{state: detective.State{Cases: []detective.Case{c}, Environments: []string{"qa"}}}}
	m := New(service, "detective")
	m.state, m.id = service.state, c.ID
	m.startScenarioStep(c, "application-strings/inspect")
	if m.form == nil || m.form.kind != "scenario-query-run" {
		t.Fatalf("string Scenario query form did not open: %q", m.feedback)
	}
	text := m.field("Inputs")
	if !strings.Contains(text, `company_param=string:scalar:"  ACME; Inc.  "`) || !strings.Contains(text, `names_param=string:set:`) || !strings.Contains(text, `"ACME, Inc."`) || !strings.Contains(text, `" Beta "`) {
		t.Fatalf("Scenario form lost quoted string boundaries: %q", text)
	}
	parsed, err := parseNamedValues(text)
	if err != nil || parsed["company_param"].Scalar == nil || parsed["company_param"].Scalar.Str != company.Str || parsed["names_param"].Set == nil || !reflect.DeepEqual(*parsed["names_param"].Set, set) {
		t.Fatalf("typed string prefill changed before execution: %+v %v", parsed, err)
	}
	if err := detective.MatchScenarioQueryCohort(step, c.Context, parsed); err != nil {
		t.Fatalf("lossless prefill did not satisfy Case cohort: %v", err)
	}
	cmd := press(m, "ctrl+s")
	if cmd == nil {
		t.Fatalf("string Scenario query was not dispatched: %q", m.feedback)
	}
	cmd()
	if service.query.Inputs["company_param"].Scalar == nil || service.query.Inputs["company_param"].Scalar.Str != company.Str || service.query.Inputs["names_param"].Set == nil || !reflect.DeepEqual(*service.query.Inputs["names_param"].Set, set) {
		t.Fatalf("query transport changed typed string values: %+v", service.query.Inputs)
	}
}
