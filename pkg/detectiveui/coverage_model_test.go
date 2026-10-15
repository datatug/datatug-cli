package detectiveui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/incidents"
)

type mockActionStatusService struct {
	fixtureService
	status string
	err    error
}

func (s *mockActionStatusService) ActionWithStatus(_ context.Context, r detective.ActionRequest) (detective.ActionResponse, string, error) {
	resp, _ := s.Action(context.Background(), r)
	return resp, s.status, s.err
}

type mockHistoryPagerService struct {
	fixtureService
	pageState detective.State
	err       error
}

func (s *mockHistoryPagerService) StatePage(_ context.Context, _ string) (detective.State, error) {
	return s.pageState, s.err
}

func TestCoverage100_Model_AllUpdateMessages(t *testing.T) {
	t.Parallel()

	svc := &fixtureService{state: detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true}}}
	m := New(svc, "checks")

	// 1. CanLeave
	if !m.CanLeave() {
		t.Fatalf("expected CanLeave to be true at catalog with empty filter")
	}
	m.filter = "search"
	if m.CanLeave() {
		t.Fatalf("expected CanLeave to be false when filter is non-empty")
	}
	m.filter = ""

	// 2. WindowSizeMsg
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.width != 80 || m.height != 24 {
		t.Fatalf("expected width 80 height 24")
	}

	// 3. loadedMsg with error -> lines 191-193
	m.pendingApplicationID = "app1"
	m.pendingStepID = "step1"
	m.pendingRunID = "run1"
	m.Update(loadedMsg{err: errors.New("load error")})
	if m.feedback != "load error" || m.pendingRunID != "" {
		t.Fatalf("expected load error feedback and cleared pending fields")
	}

	// 4. loadedMsg on runDetail with lastRunLinkStatus -> line 197
	m.screen = runDetail
	m.lastRunLinkStatus = "linked"
	m.Update(loadedMsg{state: detective.State{}})
	if m.feedback != "Case link: linked" {
		t.Fatalf("expected Case link feedback: %s", m.feedback)
	}

	// 5. loadedMsg with pendingRunID -> lines 201-205
	m.pendingApplicationID = "app1"
	m.pendingStepID = "step1"
	m.pendingRunID = "run1"
	m.caseID = "c1"
	_, cmd := m.Update(loadedMsg{state: detective.State{}})
	if cmd == nil {
		t.Fatalf("expected act command for pending scenario step")
	}

	// 6. moreRunsMsg error and success -> lines 208-218
	m.Update(moreRunsMsg{err: errors.New("pager error")})
	if m.feedback != "pager error" {
		t.Fatalf("expected pager error feedback")
	}
	m.Update(moreRunsMsg{state: detective.State{Runs: []detective.CheckRun{{ID: "r2"}}, RunsHasMore: true, RunsNextCursor: "cur"}})
	if m.state.RunsHasMore != true || m.state.RunsNextCursor != "cur" {
		t.Fatalf("expected runs updated from moreRunsMsg")
	}

	// 7. actedMsg error with showQueryOnCase == true -> lines 224-228
	m.showQueryOnCase = true
	m.Update(actedMsg{err: errors.New("act error")})
	if m.showQueryOnCase != false || m.screen != queryDetail {
		t.Fatalf("expected queryDetail after act error on case query")
	}

	// 8. actedMsg error without showQueryOnCase -> line 222
	m.Update(actedMsg{err: errors.New("simple act error")})
	if m.feedback != "simple act error" {
		t.Fatalf("expected simple act error feedback")
	}

	// 9. actedMsg with Scenario response -> lines 247-250
	m.Update(actedMsg{response: detective.ActionResponse{Scenario: &detective.Scenario{ID: "scen-saved"}}})
	if m.id != "scen-saved" || m.screen != detail || m.parent != scenarios {
		t.Fatalf("expected scenario detail screen after save")
	}

	// 10. actedMsg with Validator response -> lines 252-253
	m.Update(actedMsg{response: detective.ActionResponse{Validator: &detective.PublishedValidator{ID: "val-saved"}}})
	if m.screen != validators {
		t.Fatalf("expected validators screen after validator save")
	}

	// 11. actedMsg with QueryValidators response -> lines 255-258
	m.Update(actedMsg{response: detective.ActionResponse{QueryValidators: &detective.QueryValidators{QueryID: "q-saved"}}})
	if m.id != "q-saved" || m.parent != queryValidators || m.screen != detail {
		t.Fatalf("expected queryValidators detail after save")
	}

	// 12. actedMsg with Case response and showQueryOnCase -> lines 242-243
	m.showQueryOnCase = true
	m.Update(actedMsg{response: detective.ActionResponse{Case: &detective.Case{ID: "case-saved"}}})
	if m.screen != queryDetail || m.showQueryOnCase != false {
		t.Fatalf("expected queryDetail after case save with showQueryOnCase")
	}

	// 13. actedMsg with Run response:
	// a) completed run with linked status and pendingApplicationID -> lines 266-272
	m.pendingApplicationID = "app1"
	m.pendingStepID = "step1"
	runCompleted := detective.CheckRun{ID: "r-comp", ExecutionStatus: "completed"}
	m.Update(actedMsg{response: detective.ActionResponse{Run: &runCompleted}, caseLinkStatus: "linked"})
	if m.pendingRunID != "r-comp" {
		t.Fatalf("expected pendingRunID set to r-comp")
	}

	// b) completed run with unverified link status -> lines 276-278
	m.pendingApplicationID = "app1"
	m.pendingStepID = "step1"
	m.Update(actedMsg{response: detective.ActionResponse{Run: &runCompleted}, caseLinkStatus: ""})
	if m.feedback != "Scenario step remains pending: Case link unverified" {
		t.Fatalf("expected unverified feedback: %s", m.feedback)
	}

	// c) completed run with failed link status -> line 275
	m.pendingApplicationID = "app1"
	m.pendingStepID = "step1"
	m.Update(actedMsg{response: detective.ActionResponse{Run: &runCompleted}, caseLinkStatus: "failed"})
	if m.feedback != "Scenario step remains pending: failed" {
		t.Fatalf("expected failed feedback: %s", m.feedback)
	}

	// 14. eventsMsg error and success -> lines 286-295
	m.Update(eventsMsg{err: errors.New("events error")})
	if m.feedback != "events error" {
		t.Fatalf("expected events error feedback")
	}
	m.Update(eventsMsg{page: detective.CaseEventsPage{Events: []detective.CaseEvent{{ID: "ev1"}}, HasMore: true, NextBefore: 10}, older: false})
	if len(m.events) != 1 || m.eventsMore != true {
		t.Fatalf("expected events updated")
	}

	// 15. incidentsMsg error and success -> lines 299-304
	m.Update(incidentsMsg{err: errors.New("incidents error")})
	if m.feedback != "incidents error" {
		t.Fatalf("expected incidents error feedback")
	}
	m.Update(incidentsMsg{links: IncidentLinks{Incidents: []incidents.IncidentView{{Title: "inc1"}}}})
	if len(m.incidents.Incidents) != 1 {
		t.Fatalf("expected incidents updated")
	}

	// 16. knowledgeMsg error, initial page, and older page -> lines 308-319
	m.Update(knowledgeMsg{err: errors.New("knowledge error")})
	if m.feedback != "knowledge error" {
		t.Fatalf("expected knowledge error feedback")
	}
	m.Update(knowledgeMsg{page: KnowledgePage{Proposals: []KnowledgeProposal{{ID: "k1"}}}, older: false})
	if len(m.knowledge.Proposals) != 1 {
		t.Fatalf("expected 1 knowledge proposal")
	}
	m.Update(knowledgeMsg{page: KnowledgePage{Proposals: []KnowledgeProposal{{ID: "k2"}}, NextCursor: "c2", HasMore: true}, older: true})
	if len(m.knowledge.Proposals) != 2 || m.knowledge.NextCursor != "c2" {
		t.Fatalf("expected 2 knowledge proposals after older page")
	}

	// 17. incidentActedMsg error, link-pending, and success -> lines 322-337
	m.Update(incidentActedMsg{err: errors.New("inc act error")})
	if m.feedback != "inc act error" {
		t.Fatalf("expected inc act error feedback")
	}
	ref := incidents.IncidentRef{StoreID: "proj", IncidentID: "inc-1"}
	m.Update(incidentActedMsg{result: IncidentResponse{Incident: incidents.IncidentView{Ref: ref}, CaseLinkStatus: "link-pending"}})
	if m.feedback != "Incident proj/inc-1 created; Case changed. Link it to the latest Case." {
		t.Fatalf("expected link-pending feedback: %s", m.feedback)
	}
	m.Update(incidentActedMsg{result: IncidentResponse{Incident: incidents.IncidentView{Ref: ref}, CaseLinkStatus: "linked", Case: &detective.Case{ID: "c-inc"}}})
	if m.feedback != "Incident proj/inc-1 linked" || m.id != "c-inc" || m.screen != detail {
		t.Fatalf("expected linked incident result: %s", m.feedback)
	}

	// 18. knowledgeActedMsg error and update -> lines 341-351
	m.Update(knowledgeActedMsg{err: errors.New("know act error")})
	if m.feedback != "know act error" {
		t.Fatalf("expected know act error feedback")
	}
	m.knowledge.Proposals = []KnowledgeProposal{{ID: "kp1", Title: "Old"}}
	m.Update(knowledgeActedMsg{result: KnowledgeResponse{Proposal: &KnowledgeProposal{ID: "kp1", Title: "New"}}})
	if m.knowledge.Proposals[0].Title != "New" {
		t.Fatalf("expected updated proposal title")
	}
}

func TestCoverage100_Model_NavigationAndKeys(t *testing.T) {
	t.Parallel()

	// 1. act() with actionStatusService -> line 176
	statusSvc := &mockActionStatusService{
		status: "linked",
	}
	mStatus := New(statusSvc, "checks")
	cmdAct := mStatus.act(detective.ActionRequest{Type: "run-check"})
	msgAct := cmdAct()
	acted, ok := msgAct.(actedMsg)
	if !ok || acted.caseLinkStatus != "linked" {
		t.Fatalf("expected caseLinkStatus linked from actionStatusService")
	}

	// 2. F-keys navigation -> lines 488-508
	m := New(&fixtureService{}, "checks")
	m.Update(tea.KeyPressMsg{Code: ' ', Text: "f1"})
	if m.screen != checks {
		t.Fatalf("f1 failed")
	}
	m.Update(tea.KeyPressMsg{Code: ' ', Text: "f2"})
	if m.screen != cases {
		t.Fatalf("f2 failed")
	}
	m.Update(tea.KeyPressMsg{Code: ' ', Text: "f3"})
	if m.screen != scenarios {
		t.Fatalf("f3 failed")
	}
	m.Update(tea.KeyPressMsg{Code: ' ', Text: "f4"})
	if m.screen != validators {
		t.Fatalf("f4 failed")
	}
	m.Update(tea.KeyPressMsg{Code: ' ', Text: "f5"})
	if m.screen != queryValidators {
		t.Fatalf("f5 failed")
	}

	// 3. esc key on various screens -> lines 512-538
	m.filter = "filter"
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.filter != "" {
		t.Fatalf("esc should clear filter")
	}

	screensToParent := []struct {
		start screen
		want  screen
	}{
		{detail, checks},
		{runDetail, detail},
		{queryDetail, detail},
		{history, detail},
		{timeline, detail},
		{incidentList, detail},
		{knowledgeList, detail},
		{aiList, detail},
		{knowledgeDetail, knowledgeList},
		{exportDetail, knowledgeDetail},
		{aiDetail, aiList},
	}
	for _, sc := range screensToParent {
		m.parent = checks
		m.screen = sc.start
		m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.screen != sc.want {
			t.Fatalf("esc on %v: got %v, want %v", sc.start, m.screen, sc.want)
		}
	}
	// default esc -> quit
	m.screen = checks
	_, quitCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if quitCmd == nil {
		t.Fatalf("expected quit command on default esc")
	}

	// 4. up, down, pgup, pgdown keys -> lines 543-562
	m.height = 10
	m.rows = []row{{"r0", "", "0", 0}, {"r1", "", "1", 0}, {"r2", "", "2", 0}, {"r3", "", "3", 0}, {"r4", "", "4", 0}}
	m.index = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.index != 1 {
		t.Fatalf("down failed")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.index <= 1 {
		t.Fatalf("pgdown failed")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.index != 1 {
		t.Fatalf("pgup failed to decrement: got %d", m.index)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.index != 0 {
		t.Fatalf("second pgup failed to reach 0: got %d", m.index)
	}
	m.index = 2
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.index != 1 {
		t.Fatalf("up failed")
	}

	// 5. Catalog screens: enter, backspace, filter text -> lines 565-590
	m.screen = checks
	m.rows = []row{{"row1", "check", "chk-1", 0}}
	m.index = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != detail || m.id != "chk-1" {
		t.Fatalf("enter on catalog item failed")
	}

	m.screen = checks
	m.filter = "abc"
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.filter != "ab" {
		t.Fatalf("backspace failed: %s", m.filter)
	}
	m.Update(tea.KeyPressMsg{Text: "x"})
	if m.filter != "abx" {
		t.Fatalf("filter type failed: %s", m.filter)
	}

	// 6. history screen: L key with and without historyPager -> lines 593-601
	m.screen = history
	m.state.RunsHasMore = true
	m.state.RunsNextCursor = "c1"
	// Without historyPager
	m.service = &fixtureService{}
	m.Update(tea.KeyPressMsg{Text: "l"})
	if m.feedback != "History paging is unavailable from this agent client" {
		t.Fatalf("expected unavailable feedback")
	}
	// With historyPager
	pagerSvc := &mockHistoryPagerService{pageState: detective.State{Runs: []detective.CheckRun{{ID: "hp1"}}}}
	m.service = pagerSvc
	_, cmdPage := m.Update(tea.KeyPressMsg{Text: "l"})
	if cmdPage == nil {
		t.Fatalf("expected page command")
	}
	msgMore := cmdPage()
	m.Update(msgMore)
	// Enter on history run -> line 603
	m.rows = []row{{"run-row", "run", "run-hist-1", 0}}
	m.index = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != runDetail || m.runID != "run-hist-1" {
		t.Fatalf("expected runDetail from history enter")
	}

	// 7. runDetail: f key (toggle failedOnly), enter key -> lines 616-626
	m.screen = runDetail
	m.runID = "rd1"
	m.state.Runs = []detective.CheckRun{{ID: "rd1"}}
	m.Update(tea.KeyPressMsg{Text: "f"})
	if !m.failedOnly {
		t.Fatalf("expected failedOnly toggled to true")
	}
	m.rows = []row{{"Run detail row", "result", "step1", 0}}
	m.index = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.feedback != "Run detail row" {
		t.Fatalf("expected feedback set to label: %s", m.feedback)
	}

	// 8. queryDetail: enter key -> lines 629-631
	m.screen = queryDetail
	m.rows = []row{{"Query row 1", "query-row", "0", 0}}
	m.index = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.feedback != "Query row 1" {
		t.Fatalf("expected feedback set to query row label: %s", m.feedback)
	}
}

func TestCoverage100_Model_RebuildAndRendering(t *testing.T) {
	t.Parallel()

	// 1. rebuildDetail checks, cases, scenarios, validators, queryValidators
	cCheck := detective.Check{
		ID:      "chk-full",
		Title:   "Check Full",
		Version: 1,
		Inputs:  []detective.Input{{Name: "in1", Type: "string", Cardinality: "scalar"}},
		Steps: []detective.Step{
			{ID: "s-chk", Kind: "check", CheckID: "nested-chk"},
			{ID: "s-qry", Kind: "query", QueryID: "nested-qry", Validators: []detective.Validator{{ID: "v1", Scope: "row", Kind: "non-null", Severity: "error"}}},
		},
	}
	app1 := detective.ScenarioApplication{
		ID:              "app1",
		ScenarioID:      "scen1",
		ScenarioVersion: 1,
		Steps: []detective.ScenarioStepReceipt{
			{StepID: "step1", Status: "completed", EvidenceOutcome: "pass"},
		},
	}
	cCase := detective.Case{
		ID:                   "case-full",
		Title:                "Case Full",
		Status:               "active",
		Context:              map[string]detective.InputValue{"k1": {}},
		Evidence:             []detective.EvidenceRef{{ID: "ev1", Kind: "run", Ref: "r1"}},
		Findings:             []detective.Finding{{ID: "f1", Status: "open", Text: "finding 1"}},
		IncidentRefs:         []string{"inc-1"},
		ScenarioRefs:         []string{"scen-1"},
		ScenarioApplications: []detective.ScenarioApplication{app1},
		Timeline:             []detective.CaseEvent{{ID: "ev-t", At: time.Now().UTC(), Type: "note", Message: "hello"}},
	}
	cScen := detective.Scenario{
		ID:    "scen-full",
		Title: "Scen Full",
		Steps: []detective.ScenarioStep{{ID: "ss1", Kind: "query", Ref: "q1"}},
	}
	cVal := detective.PublishedValidator{
		ID:        "val-full",
		Title:     "Val Full",
		Validator: detective.Validator{Scope: "row", Kind: "non-null", Severity: "error"},
	}
	cQV := detective.QueryValidators{
		QueryID: "qv-full",
		Version: 1,
	}

	m := New(&fixtureService{}, "checks")
	m.state = detective.State{
		Checks:          []detective.Check{cCheck},
		Cases:           []detective.Case{cCase},
		Scenarios:       []detective.Scenario{cScen},
		Validators:      []detective.PublishedValidator{cVal},
		QueryValidators: []detective.QueryValidators{cQV},
	}

	// Render each detail screen
	m.screen = detail
	m.parent = checks
	m.id = "chk-full"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows for check detail")
	}

	m.parent = cases
	m.id = "case-full"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows for case detail")
	}

	m.parent = scenarios
	m.id = "scen-full"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows for scenario detail")
	}

	m.parent = validators
	m.id = "val-full"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows for validator detail")
	}

	m.parent = queryValidators
	m.id = "qv-full"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows for queryValidators detail")
	}

	// Nil cases for each detail type -> lines 747, 763, 798, 807, 813
	m.id = "non-existent"
	for _, p := range []screen{checks, cases, scenarios, validators, queryValidators} {
		m.parent = p
		m.rows = nil
		m.rebuildDetail()
		if len(m.rows) != 0 {
			t.Fatalf("expected 0 rows for non-existent %v", p)
		}
	}

	// 2. rebuildRun and rebuildQueryRows and queryCellText
	execRef := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "exec-1"}
	intZero := 0
	valRes := detective.ValidationResult{
		ValidatorID:     "val-row",
		ExecutionStatus: "completed",
		Outcome:         "fail",
		Severity:        "error",
		RowIndex:        &intZero,
		Message:         "failed row",
		Children: []detective.StepResult{
			{Kind: "check", StepID: "child-step", ExecutionStatus: "error", Outcome: "unknown", Message: "child fail"},
		},
	}
	stepRes := detective.StepResult{
		Kind:            "query",
		StepID:          "q-step",
		QueryID:         "q-run",
		ExecutionStatus: "completed",
		Outcome:         "fail",
		Severity:        "error",
		RowCount:        1,
		Message:         "step msg",
		Execution:       &execRef,
		Validators:      []detective.ValidationResult{valRes},
		Children: []detective.StepResult{
			{Kind: "check", StepID: "sub-step", ExecutionStatus: "completed", Outcome: "pass", Severity: "info"},
		},
	}
	run := detective.CheckRun{
		ID:              "run-full",
		Kind:            "query-validators",
		QueryID:         "q-run",
		Environment:     "qa",
		ExecutionStatus: "completed",
		Outcome:         "fail",
		Severity:        "error",
		DurationMs:      42,
		Actor:           "alice",
		Steps:           []detective.StepResult{stepRes},
	}
	m.state.Runs = []detective.CheckRun{run}
	m.screen = runDetail
	m.runID = "run-full"
	m.queryID = "q-run"
	m.queryEnvironment = "qa"
	m.queryExecution = &execRef
	m.queryResult = &SavedQueryResult{
		QueryRevision:     "rev1",
		ResultFingerprint: "fp1",
		Result: apicontract.Result{
			Execution: &execRef,
			Truncated: true,
			Recordset: apicontract.Recordset{
				Columns: []apicontract.Column{{Name: "c_bool"}, {Name: "c_num"}, {Name: "c_null"}},
				Rows: [][]apicontract.TypedValue{
					{
						{Type: apicontract.ValueTypeBoolean, Bool: true},
						{Type: apicontract.ValueTypeNumber, Num: 12.34},
						{Type: apicontract.ValueTypeNull},
						{Type: apicontract.ValueTypeString, Str: "control\x00char"},
					},
				},
			},
		},
	}
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows for runDetail with queryResult")
	}

	// 3. hints() for every screen -> lines 984-1034
	screens := []screen{
		formScreen,
		detail,
		runDetail,
		queryDetail,
		history,
		timeline,
		incidentList,
		knowledgeList,
		knowledgeDetail,
		exportDetail,
		aiList,
		aiDetail,
		checks,
	}
	for _, sc := range screens {
		m.screen = sc
		if sc == detail {
			for _, p := range []screen{checks, cases, scenarios, queryValidators} {
				m.parent = p
				if h := m.hints(); h == "" {
					t.Fatalf("empty hints for detail %v", p)
				}
			}
		} else {
			if h := m.hints(); h == "" {
				t.Fatalf("empty hints for screen %v", sc)
			}
		}
	}
	// history with RunsHasMore = false
	m.screen = history
	m.state.RunsHasMore = false
	if h := m.hints(); h != "Enter run · Esc back" {
		t.Fatalf("expected simple history hints: %s", h)
	}

	// 4. View() rendering branches -> lines 939-983
	m.width = 80
	m.height = 20
	m.busy = true
	m.feedback = "Operation completed"
	m.filter = "test-filter"
	m.screen = runDetail
	viewRun := m.View()
	if viewRun.Content == "" {
		t.Fatalf("expected non-empty View")
	}

	// View with empty rows
	m.rows = nil
	viewEmpty := m.View()
	if viewEmpty.Content == "" {
		t.Fatalf("expected non-empty View on empty rows")
	}

	// View on formScreen
	m.screen = formScreen
	m.form = &form{kind: "test-form", title: "Test Form", fields: []field{{label: "F1", value: "V1"}}}
	viewForm := m.View()
	if viewForm.Content == "" {
		t.Fatalf("expected non-empty View on formScreen")
	}
}

func TestCoverage100_Model_RemainingGaps(t *testing.T) {
	t.Parallel()

	svc := &fixtureService{
		state: detective.State{
			Capabilities: detective.Capabilities{CanRead: true, CanWrite: true},
			Scenarios: []detective.Scenario{
				{ID: "scen-1", Title: "Scen One"},
			},
			Validators: []detective.PublishedValidator{
				{ID: "val-1", Title: "Val One", Validator: detective.Validator{ID: "val-1"}},
			},
			QueryValidators: []detective.QueryValidators{
				{QueryID: "q1", Validators: []detective.Validator{{ID: "v1"}}, Version: 1},
			},
			Runs: []detective.CheckRun{
				{ID: "run-1", CheckID: "chk-1"},
			},
			RunsHasMore: true,
		},
	}
	m := New(svc, "checks")
	m.state = svc.state

	// 1. aiPageMsg error (lines 394-396) and older page (lines 398-400)
	m.busy = true
	m.Update(aiPageMsg{err: errors.New("ai page failed")})
	if m.busy || m.feedback != "ai page failed" {
		t.Fatalf("expected ai page error handling")
	}

	m.Update(aiPageMsg{
		older: true,
		page: AIPage{
			Proposals:  []ai.Proposal{{ID: "prop-2"}},
			HasMore:    true,
			NextCursor: "next",
		},
	})
	if !m.aiPage.HasMore || m.aiPage.NextCursor != "next" {
		t.Fatalf("expected older proposals appended")
	}

	// 2. aiGeneratedMsg error (lines 408-410)
	m.busy = true
	m.Update(aiGeneratedMsg{err: errors.New("ai gen failed")})
	if m.busy || m.feedback != "ai gen failed" {
		t.Fatalf("expected ai gen error handling")
	}

	// 3. aiReviewedMsg error (lines 421-423)
	m.busy = true
	m.Update(aiReviewedMsg{err: errors.New("ai rev failed")})
	if m.busy || m.feedback != "ai rev failed" {
		t.Fatalf("expected ai rev error handling")
	}

	// 4. aiConfirmedMsg error (lines 432-434)
	m.busy = true
	m.Update(aiConfirmedMsg{err: errors.New("ai conf failed")})
	if m.busy || m.feedback != "ai conf failed" {
		t.Fatalf("expected ai conf error handling")
	}

	// 5. tea.KeyPressMsg ctrl+c (lines 440-441)
	_, quitCmd := m.Update(tea.KeyPressMsg{Text: "ctrl+c"})
	if quitCmd == nil {
		t.Fatalf("expected quitCmd for ctrl+c")
	}

	// 6. tea.KeyPressMsg while busy (lines 443-444)
	m.busy = true
	_, nilCmd := m.Update(tea.KeyPressMsg{Text: "a"})
	if nilCmd != nil {
		t.Fatalf("expected nil cmd when busy")
	}
	m.busy = false

	// 7. Unhandled message type -> default return (line 463)
	_, unhandledCmd := m.Update(struct{ custom int }{custom: 1})
	if unhandledCmd != nil {
		t.Fatalf("expected nil cmd on unhandled msg")
	}

	// 8. nativePasteText with control characters (line 482)
	pasted := nativePasteText("hello\x07\x00world\r\nline2", false)
	if pasted != "helloworld line2" {
		t.Fatalf("unexpected pasted text: %q", pasted)
	}

	// 9. ctrl+a on catalog screen -> openAI() (lines 510-511)
	m.screen = checks
	m.Update(tea.KeyPressMsg{Text: "ctrl+a"})

	// 10. updateKey default unhandled return on unknown screen (line 637)
	m.screen = screen("unknown")
	m.Update(tea.KeyPressMsg{Text: "z"})

	// 11. rebuild() matching filter on scenarios, validators, queryValidators (lines 673-687)
	m.screen = scenarios
	m.filter = "Scen"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected matching scenarios")
	}

	m.screen = validators
	m.filter = "Val"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected matching validators")
	}

	m.screen = queryValidators
	m.filter = "q1"
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected matching queryValidators")
	}

	// 12. rebuild() on incidentList (lines 702-704)
	m.screen = incidentList
	m.filter = ""
	m.incidents = IncidentLinks{
		Incidents: []incidents.IncidentView{
			{Ref: incidents.IncidentRef{StoreID: "proj", IncidentID: "inc-1"}, Status: "active", Title: "Issue 1"},
		},
	}
	m.rebuild()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows populated for incidentList")
	}

	// 13. hints() on history with RunsHasMore = true (lines 1008-1009)
	m.screen = history
	m.state.RunsHasMore = true
	hint := m.hints()
	if hint != "Enter run · L load older runs · Esc back" {
		t.Fatalf("unexpected hint: %s", hint)
	}
}
