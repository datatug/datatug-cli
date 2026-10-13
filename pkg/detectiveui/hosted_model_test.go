package detectiveui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

type hostedFixture struct {
	*fixtureService
	query           SavedQueryRun
	knowledgeAction KnowledgeAction
}

type scenarioVersionFixture struct {
	stateCalls int
	actions    []detective.ActionRequest
}

func (s *scenarioVersionFixture) State(context.Context) (detective.State, error) {
	s.stateCalls++
	return detective.State{Cases: []detective.Case{{ID: "case-1", Version: int64(s.stateCalls)}}, Capabilities: detective.Capabilities{CanRead: true, CanRun: true, CanWrite: true}}, nil
}

func (s *scenarioVersionFixture) Action(_ context.Context, request detective.ActionRequest) (detective.ActionResponse, error) {
	s.actions = append(s.actions, request)
	if request.Type == "run-check" {
		return detective.ActionResponse{Run: &detective.CheckRun{ID: "run-1", CaseID: "case-1", ExecutionStatus: "completed"}}, nil
	}
	return detective.ActionResponse{}, nil
}

func TestScenarioCheckRefreshesCaseBeforeRecordingStep(t *testing.T) {
	service := &scenarioVersionFixture{}
	m := New(service, "detective")
	m.Update(m.Init()())
	m.pendingApplicationID, m.pendingStepID = "application-1", "inspect"
	_, refresh := m.Update(actedMsg{response: detective.ActionResponse{Run: &detective.CheckRun{ID: "run-1", CaseID: "case-1", ExecutionStatus: "completed"}}, caseLinkStatus: "linked"})
	if refresh == nil || service.stateCalls != 1 || len(service.actions) != 0 {
		t.Fatal("Scenario step recording started before Case refresh")
	}
	_, record := m.Update(refresh())
	if record == nil || service.stateCalls != 2 || len(service.actions) != 0 {
		t.Fatal("Case refresh did not precede Scenario step recording")
	}
	m.Update(record())
	if len(service.actions) != 1 || service.actions[0].Type != "record-scenario-step" || service.actions[0].RunID != "run-1" {
		t.Fatalf("Scenario step proof missing after refresh: %#v", service.actions)
	}
}

func TestScenarioCheckWaitsWhenCompletedRunCannotLinkCase(t *testing.T) {
	service := &scenarioVersionFixture{}
	m := New(service, "detective")
	m.Update(m.Init()())
	m.pendingApplicationID, m.pendingStepID = "application-1", "inspect"
	_, refresh := m.Update(actedMsg{response: detective.ActionResponse{Run: &detective.CheckRun{ID: "run-1", CaseID: "case-1", ExecutionStatus: "completed"}}, caseLinkStatus: "case-changed"})
	if refresh == nil {
		t.Fatal("completed run should still refresh history")
	}
	m.Update(refresh())
	if len(service.actions) != 0 || m.pendingApplicationID != "" || !strings.Contains(m.feedback, "case-changed") {
		t.Fatalf("unlinked run incorrectly completed Scenario step: actions=%#v feedback=%q", service.actions, m.feedback)
	}
}

func TestUnavailableScenarioCheckDoesNotLeakPendingStepToAnotherRun(t *testing.T) {
	caseItem := detective.Case{ID: "case-1", Title: "Investigation", Status: "active", ScenarioApplications: []detective.ScenarioApplication{{ID: "application-1", Snapshot: detective.Scenario{Steps: []detective.ScenarioStep{{ID: "inspect", Kind: "check", Ref: "missing-check"}}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	service := &fixtureService{}
	m := New(service, "detective")
	m.state.Cases = []detective.Case{caseItem}
	m.state.Checks = []detective.Check{{ID: "other-check", Title: "Other", Steps: []detective.Step{{ID: "load", Kind: "query", QueryID: "q"}}}}
	m.startScenarioStep(caseItem, "application-1/inspect")
	if m.form != nil || !strings.Contains(m.feedback, "not in catalog") || m.pendingApplicationID != "" || m.pendingStepID != "" {
		t.Fatalf("unavailable Scenario step leaked to later runs: form=%+v pending=%s/%s feedback=%q", m.form, m.pendingApplicationID, m.pendingStepID, m.feedback)
	}
	_, next := m.Update(actedMsg{response: detective.ActionResponse{Run: &detective.CheckRun{ID: "unrelated-run", CheckID: "other-check", CaseID: "case-1", ExecutionStatus: "completed"}}, caseLinkStatus: "linked"})
	if next == nil || m.pendingRunID != "" {
		t.Fatalf("unrelated run inherited Scenario attribution: pendingRunID=%q", m.pendingRunID)
	}
	m.Update(next())
	if len(service.actions) != 0 {
		t.Fatalf("unrelated run recorded Scenario step: %#v", service.actions)
	}
}

func TestAvailableScenarioCheckKeepsAttributionUntilRun(t *testing.T) {
	caseItem := detective.Case{ID: "case-1", Title: "Investigation", Status: "active", ScenarioApplications: []detective.ScenarioApplication{{ID: "application-1", Snapshot: detective.Scenario{Steps: []detective.ScenarioStep{{ID: "inspect", Kind: "check", Ref: "available-check"}}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	m := New(&fixtureService{}, "detective")
	m.state.Cases = []detective.Case{caseItem}
	m.state.Checks = []detective.Check{{ID: "available-check", Title: "Available", Steps: []detective.Step{{ID: "load", Kind: "query", QueryID: "q"}}}}
	m.startScenarioStep(caseItem, "application-1/inspect")
	if m.form == nil || m.form.kind != "run" || m.pendingApplicationID != "application-1" || m.pendingStepID != "inspect" {
		t.Fatalf("available Check lost Scenario attribution: form=%+v pending=%s/%s", m.form, m.pendingApplicationID, m.pendingStepID)
	}
	press(m, "esc")
	if m.pendingApplicationID != "" || m.pendingStepID != "" {
		t.Fatalf("cancelled run retained Scenario attribution: %s/%s", m.pendingApplicationID, m.pendingStepID)
	}
}

func TestFailedScenarioQueryDoesNotAttributeLaterCheckRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  queryRunMsg
	}{
		{name: "execution failed", msg: queryRunMsg{err: errors.New("source unavailable")}},
		{name: "receipt missing", msg: queryRunMsg{result: SavedQueryResult{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fixtureService{}
			m := New(service, "detective")
			m.pendingApplicationID, m.pendingStepID = "application-1", "inspect"
			m.Update(tc.msg)
			if m.pendingApplicationID != "" || m.pendingStepID != "" {
				t.Fatalf("failed Scenario query retained step: %s/%s", m.pendingApplicationID, m.pendingStepID)
			}
			_, refresh := m.Update(actedMsg{response: detective.ActionResponse{Run: &detective.CheckRun{ID: "other-run", CheckID: "other-check", CaseID: "case-1", ExecutionStatus: "completed"}}, caseLinkStatus: "linked"})
			if refresh == nil || m.pendingRunID != "" {
				t.Fatalf("unrelated run inherited Scenario attribution: %q", m.pendingRunID)
			}
			m.Update(refresh())
			if len(service.actions) != 0 {
				t.Fatalf("unrelated run recorded Scenario step: %#v", service.actions)
			}
		})
	}
}

func TestCancelledScenarioQueryDoesNotAttributeLaterCheckRun(t *testing.T) {
	caseItem := detective.Case{ID: "case-1", Title: "Investigation", Status: "active", ScenarioApplications: []detective.ScenarioApplication{{ID: "application-1", Snapshot: detective.Scenario{Steps: []detective.ScenarioStep{{ID: "inspect", Kind: "query", Ref: "saved-query"}}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	m := New(&hostedFixture{fixtureService: &fixtureService{}}, "detective")
	m.state.Cases = []detective.Case{caseItem}
	m.startScenarioStep(caseItem, "application-1/inspect")
	if m.form == nil || m.form.kind != "scenario-query-run" {
		t.Fatalf("Scenario query form unavailable: %+v", m.form)
	}
	press(m, "esc")
	if m.pendingApplicationID != "" || m.pendingStepID != "" {
		t.Fatalf("cancelled Scenario query retained step: %s/%s", m.pendingApplicationID, m.pendingStepID)
	}
	_, refresh := m.Update(actedMsg{response: detective.ActionResponse{Run: &detective.CheckRun{ID: "other-run", CheckID: "other-check", CaseID: "case-1", ExecutionStatus: "completed"}}, caseLinkStatus: "linked"})
	if refresh == nil || m.pendingRunID != "" {
		t.Fatalf("unrelated run inherited cancelled Scenario step: %q", m.pendingRunID)
	}
}

func TestHistoricalRunDoesNotInheritPriorCaseLinkStatus(t *testing.T) {
	m := New(&scenarioVersionFixture{}, "detective")
	m.state.Runs = []detective.CheckRun{{ID: "older-run", CheckID: "check-1"}}
	m.lastRunLinkStatus = "case-changed"
	m.screen = history
	m.rebuild()
	press(m, "enter")
	if m.screen != runDetail || m.runID != "older-run" || m.lastRunLinkStatus != "" {
		t.Fatalf("historical run inherited unrelated link status: screen=%s run=%s status=%q", m.screen, m.runID, m.lastRunLinkStatus)
	}
}

func (s *hostedFixture) CaseEvents(context.Context, string, int) (detective.CaseEventsPage, error) {
	return detective.CaseEventsPage{CaseID: "case-1", Events: []detective.CaseEvent{{ID: "event-1", Type: "note", Message: "reviewed invoice"}}, HasMore: true, NextBefore: 40}, nil
}
func (s *hostedFixture) LinkedIncidents(context.Context, string) (IncidentLinks, error) {
	return IncidentLinks{CaseID: "case-1"}, nil
}
func (s *hostedFixture) IncidentAction(context.Context, IncidentAction) (IncidentResponse, error) {
	return IncidentResponse{}, nil
}
func (s *hostedFixture) KnowledgeProposals(context.Context, string) (KnowledgePage, error) {
	return KnowledgePage{Proposals: []KnowledgeProposal{{ID: "finding-1", CaseID: "case-1", Kind: "invariant", Title: "Invoice totals", Status: "reviewed", Version: 2}}, HasMore: true, NextCursor: "finding-1"}, nil
}
func (s *hostedFixture) KnowledgeAction(_ context.Context, a KnowledgeAction) (KnowledgeResponse, error) {
	s.knowledgeAction = a
	if a.Type == "export" {
		handoff := KnowledgeHandoff{Kind: "knowledge-handoff", ProposalID: a.ProposalID, ProposalVersion: a.IfMatch, Target: *a.Target, Status: "ready-for-target-review"}
		return KnowledgeResponse{Proposal: &KnowledgeProposal{ID: a.ProposalID, Version: a.IfMatch + 1, Handoffs: []KnowledgeHandoff{handoff}}, Export: &handoff}, nil
	}
	return KnowledgeResponse{}, nil
}
func (s *hostedFixture) RunSavedQuery(_ context.Context, request SavedQueryRun) (SavedQueryResult, error) {
	s.query = request
	return SavedQueryResult{Result: apicontract.Result{Execution: &apicontract.ExecutionRef{StoreID: "firestore", ProjectID: "project-1", ExecutionID: "receipt-1"}}}, nil
}

func TestHostedScenarioQueryUsesCaseCohortAndExactReceipt(t *testing.T) {
	set, err := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("7"))
	if err != nil {
		t.Fatal(err)
	}
	caseItem := detective.Case{ID: "case-1", Title: "Invoice reconciliation", Status: "active", Context: map[string]detective.InputValue{"invoice_ids": {Set: &set}, "unrelated": {Scalar: valuePtr(investigation.NewStringValue("omit"))}}, ScenarioApplications: []detective.ScenarioApplication{{ID: "application-1", ScenarioID: "reconcile", Snapshot: detective.Scenario{Inputs: []detective.Input{{Name: "invoice_ids", Type: "integer", Cardinality: "set"}}, Steps: []detective.ScenarioStep{{ID: "inspect", Kind: "query", Ref: "detective/invoice-list-set"}}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	s := &hostedFixture{fixtureService: &fixtureService{state: detective.State{Cases: []detective.Case{caseItem}, Environments: []string{"qa"}, Capabilities: detective.Capabilities{CanRead: true, CanWrite: true, CanRun: true}}}}
	m := New(s, "detective")
	m.Update(m.Init()())
	press(m, "enter")
	for i, row := range m.rows {
		if row.kind == "application-step" {
			m.index = i
			break
		}
	}
	press(m, "enter")
	if m.form == nil || m.form.kind != "scenario-query-run" || m.field("Environment") != "qa" || !strings.Contains(m.field("Inputs"), "invoice_ids=integer:set:") || strings.Contains(m.field("Inputs"), "unrelated") {
		t.Fatalf("Scenario query form did not bind Case cohort: %#v", m.form)
	}
	cmd := press(m, "ctrl+s")
	if cmd == nil {
		t.Fatal("query execution not dispatched")
	}
	_, next := m.Update(cmd())
	if next == nil {
		t.Fatal("Scenario receipt not dispatched")
	}
	m.Update(next())
	if len(s.actions) == 0 || s.actions[len(s.actions)-1].Type != "record-scenario-step" || s.actions[len(s.actions)-1].Execution == nil || s.actions[len(s.actions)-1].Execution.ExecutionID != "receipt-1" || s.actions[len(s.actions)-1].CaseID != "case-1" {
		t.Fatalf("Scenario proof missing: %#v", s.actions)
	}
	if m.pendingApplicationID != "" || m.pendingStepID != "" {
		t.Fatalf("completed Scenario query retained step: %s/%s", m.pendingApplicationID, m.pendingStepID)
	}
	if len(s.query.Inputs) != 1 || s.query.Inputs["invoice_ids"].Set == nil || len(s.query.Inputs["invoice_ids"].Set.Values) != 2 {
		t.Fatalf("query cohort changed: %#v", s.query.Inputs)
	}
}
func valuePtr(v detective.Value) *detective.Value { return &v }

func TestHostedCaseHistoryAndKnowledgeNavigationAtSmallSize(t *testing.T) {
	s := &hostedFixture{fixtureService: &fixtureService{state: detective.State{Cases: []detective.Case{{ID: "case-1", Title: "Case", Status: "active"}}, Capabilities: detective.Capabilities{CanRead: true}}}}
	m := New(s, "detective")
	m.Update(m.Init()())
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	press(m, "enter")
	cmd := press(m, "g")
	if cmd == nil {
		t.Fatal("timeline request missing")
	}
	m.Update(cmd())
	if m.screen != timeline || len(m.rows) == 0 || !strings.Contains(m.rows[0].label, "reviewed invoice") || !strings.Contains(m.View().Content, "Timeline") {
		t.Fatalf("timeline unavailable: %s", m.View().Content)
	}
	press(m, "esc")
	cmd = press(m, "k")
	if cmd == nil {
		t.Fatal("knowledge request missing")
	}
	m.Update(cmd())
	if m.screen != knowledgeList || len(m.rows) == 0 || !strings.Contains(m.rows[0].label, "Invoice totals") {
		t.Fatalf("knowledge list unavailable: %s", m.View().Content)
	}
	press(m, "enter")
	if m.screen != knowledgeDetail || !strings.Contains(m.View().Content, "Proposal") {
		t.Fatal("proposal detail unavailable")
	}
	press(m, "e")
	if m.form == nil || m.form.kind != "knowledge-export" {
		t.Fatal("native export form unavailable")
	}
	m.form.fields[1].value = "acme/catalog"
	m.form.fields[2].value = "invoice.total"
	m.form.fields[3].value = "rev-7"
	cmd = press(m, "ctrl+s")
	if cmd == nil {
		t.Fatal("export not dispatched")
	}
	m.Update(cmd())
	if s.knowledgeAction.Target == nil || s.knowledgeAction.Target.ExpectedRevision != "rev-7" || m.screen != exportDetail || !strings.Contains(m.View().Content, "Private Export") {
		t.Fatalf("target-bound handoff unavailable: %#v, %s", s.knowledgeAction, m.View().Content)
	}
	press(m, "esc")
	if proposal := m.currentProposal(); proposal == nil || proposal.Version != 3 || len(proposal.Handoffs) != 1 || len(m.rows) < 6 || !strings.Contains(m.rows[len(m.rows)-1].label, "Handoff") {
		t.Fatalf("export version and handoff not retained in Proposal detail: %#v", proposal)
	}
	for range m.rows {
		press(m, "down")
	}
	if !strings.Contains(m.View().Content, "Handoff") {
		t.Fatal("cold-resume handoff cannot be reached at a small terminal size")
	}
}

func TestCaseRunPrefillsConfirmedSetAndRejectsStandaloneCheck(t *testing.T) {
	set, err := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("7"))
	if err != nil {
		t.Fatal(err)
	}
	s := &fixtureService{state: detective.State{Cases: []detective.Case{{ID: "case-1", Title: "Case", Context: map[string]detective.InputValue{"invoice_ids": {Set: &set}}}}, Checks: []detective.Check{{ID: "all-invoices", Title: "All invoices"}, {ID: "selected-invoices", Title: "Selected invoices", Inputs: []detective.Input{{Name: "invoice_ids", Type: "integer", Cardinality: "set", Required: true}}}}, Environments: []string{"qa"}, Capabilities: detective.Capabilities{CanRead: true, CanRun: true}}}
	m := New(s, "detective")
	m.Update(m.Init()())
	press(m, "enter")
	m.startRun("all-invoices", "case-1")
	if m.form != nil || !strings.Contains(m.feedback, "no input for confirmed Case cohort") {
		t.Fatalf("standalone Check could claim Case evidence: %s", m.feedback)
	}
	m.startRun("selected-invoices", "case-1")
	if m.form == nil || m.field("invoice_ids") != "1, 7" || m.field("Environment") != "qa" {
		t.Fatalf("Case context not prefilled: %#v", m.form)
	}
}
