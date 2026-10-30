package detectiveui

import (
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestHostedViewsExplainUnavailableAgentCapabilities(t *testing.T) {
	m := New(&fixtureService{}, "detective")
	m.id = "case-1"
	for _, tc := range []struct {
		name string
		open func() (bool, string)
		want string
	}{
		{name: "timeline", open: func() (bool, string) { _, cmd := m.openTimeline(); return cmd != nil, m.feedback }, want: "Case event paging"},
		{name: "incidents", open: func() (bool, string) { _, cmd := m.openIncidents(); return cmd != nil, m.feedback }, want: "Incidentius Case links"},
		{name: "knowledge", open: func() (bool, string) { _, cmd := m.openKnowledge(); return cmd != nil, m.feedback }, want: "private knowledge proposals"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if started, feedback := tc.open(); started || !strings.Contains(feedback, tc.want) {
				t.Fatalf("unsupported %s started=%t feedback=%q", tc.name, started, feedback)
			}
		})
	}
	m.screen = timeline
	m.eventsMore = true
	if _, cmd := m.updateHostedKey("l"); cmd != nil || !strings.Contains(m.feedback, "Case event paging") {
		t.Fatalf("timeline pagination hid missing capability: %q", m.feedback)
	}
	if cmd := m.loadKnowledge(false); cmd != nil || !strings.Contains(m.feedback, "private knowledge proposals") {
		t.Fatalf("knowledge paging hid missing capability: %q", m.feedback)
	}
	m.begin("incident-create", "Create", []field{f("Environment", "qa", ""), f("Title", "Incident", "")})
	if _, handled, err := m.submitHostedForm(); !handled || err == nil || !strings.Contains(err.Error(), "Incidentius Case links") {
		t.Fatalf("incident submission did not explain missing service: handled=%t err=%v", handled, err)
	}
	m.begin("knowledge-draft", "Draft", []field{f("ID", "draft", "")})
	if _, handled, err := m.submitHostedForm(); !handled || err == nil || !strings.Contains(err.Error(), "private knowledge proposals") {
		t.Fatalf("knowledge submission did not explain missing service: handled=%t err=%v", handled, err)
	}
	m.begin("scenario-query-run", "Run", []field{f("Environment", "qa", "")})
	if _, handled, err := m.submitHostedForm(); !handled || err == nil || !strings.Contains(err.Error(), "saved-query execution receipts") {
		t.Fatalf("query submission did not explain missing service: handled=%t err=%v", handled, err)
	}
}

func TestHostedIncidentFormsValidateBeforeDispatchAndBindCaseVersion(t *testing.T) {
	s := &hostedFixture{fixtureService: &fixtureService{state: detective.State{Cases: []detective.Case{{ID: "case-1", Version: 7}}, Environments: []string{"qa"}}}}
	m := New(s, "detective")
	m.state = s.state
	m.id = "case-1"
	assertError := func(want string) {
		t.Helper()
		cmd, handled, err := m.submitHostedForm()
		if !handled || cmd != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q, got handled=%t cmd=%v err=%v", want, handled, cmd != nil, err)
		}
	}
	assertDispatch := func(want string) {
		t.Helper()
		cmd, handled, err := m.submitHostedForm()
		if !handled || cmd == nil || err != nil {
			t.Fatalf("%s did not dispatch: handled=%t err=%v", want, handled, err)
		}
		cmd()
		if s.incidentAction.Type != want || s.incidentAction.Environment != "qa" {
			t.Fatalf("wrong incident action: %+v", s.incidentAction)
		}
	}
	m.begin("incident-create", "Create", []field{f("Environment", "uat", ""), f("Title", "", ""), f("Description", "", "")})
	assertError("not admitted")
	m.form.fields[0].value = "qa"
	assertError("title required")
	m.form.fields[1].value = "Failed invoice sync"
	assertDispatch("create")
	if s.incidentAction.CaseID != "case-1" || s.incidentAction.IfMatch != 7 || s.incidentAction.Title != "Failed invoice sync" {
		t.Fatalf("Case version or title lost on Incident create: %+v", s.incidentAction)
	}
	m.begin("incident-link", "Link", []field{f("Environment", "qa", ""), f("Incident ref", "", "")})
	assertError("reference required")
	m.form.fields[1].value = "store/incident-1"
	assertDispatch("link")
	if s.incidentAction.IncidentRef != "store/incident-1" || s.incidentAction.IfMatch != 7 {
		t.Fatalf("independent Incident reference or Case CAS lost: %+v", s.incidentAction)
	}
	m.begin("incident-start-case", "Start Case", []field{f("Environment", "qa", ""), f("Incident ref", "store/incident-1", ""), f("Case ID", "", ""), f("Title", "", "")})
	assertError("Case ID and title required")
	m.form.fields[2].value = "case-from-incident"
	m.form.fields[3].value = "Investigate invoice sync"
	assertDispatch("start-case")
	if s.incidentAction.Case == nil || s.incidentAction.Case.ID != "case-from-incident" || s.incidentAction.Case.Status != "active" {
		t.Fatalf("independent Case payload lost: %+v", s.incidentAction)
	}
}

func TestHostedKnowledgeFormsRequireReviewAndTargetRevision(t *testing.T) {
	s := &hostedFixture{fixtureService: &fixtureService{}}
	m := New(s, "detective")
	m.id = "case-1"
	assertError := func(want string) {
		t.Helper()
		cmd, handled, err := m.submitHostedForm()
		if !handled || cmd != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q, got handled=%t cmd=%v err=%v", want, handled, cmd != nil, err)
		}
	}
	assertDispatch := func(want string) {
		t.Helper()
		cmd, handled, err := m.submitHostedForm()
		if !handled || cmd == nil || err != nil {
			t.Fatalf("%s did not dispatch: handled=%t err=%v", want, handled, err)
		}
		cmd()
		if s.knowledgeAction.Type != want {
			t.Fatalf("wrong knowledge action: %+v", s.knowledgeAction)
		}
	}
	m.begin("knowledge-draft", "Draft", []field{f("ID", "claim-1", ""), f("Kind", "invariant", ""), f("Title", "Invoice totals", ""), f("Statement", "Totals match", ""), f("Claim status", "hypothesis", ""), f("Evidence refs", "", ""), f("Finding refs", "", "")})
	assertError("Case evidence references required")
	m.form.fields[5].value = "evidence-1, evidence-2"
	assertDispatch("save-draft")
	if s.knowledgeAction.Proposal == nil || s.knowledgeAction.Proposal.CaseID != "case-1" || len(s.knowledgeAction.Proposal.EvidenceRefs) != 2 {
		t.Fatalf("draft lost Case evidence: %+v", s.knowledgeAction)
	}
	for _, tc := range []struct{ kind, want string }{{"knowledge-review", "review"}, {"knowledge-reject", "reject"}} {
		m.begin(tc.kind, "Review", []field{f("Review note", "", "")})
		m.form.proposal = KnowledgeProposal{ID: "claim-1", Version: 4}
		assertError("review note required")
		m.form.fields[0].value = "Checked source"
		assertDispatch(tc.want)
		if s.knowledgeAction.IfMatch != 4 || s.knowledgeAction.ReviewNote != "Checked source" {
			t.Fatalf("review CAS or note lost: %+v", s.knowledgeAction)
		}
	}
	m.begin("knowledge-export", "Export", []field{f("System", "meaninggraph", ""), f("Repository", "", ""), f("Resource ID", "invoice.total", ""), f("Expected revision", "", "")})
	m.form.proposal = KnowledgeProposal{ID: "claim-1", Version: 5}
	assertError("expected revision required")
	m.form.fields[1].value = "acme/catalog"
	m.form.fields[3].value = "rev-9"
	assertDispatch("export")
	if s.knowledgeAction.Target == nil || s.knowledgeAction.Target.ExpectedRevision != "rev-9" || s.knowledgeAction.IfMatch != 5 {
		t.Fatalf("target-bound handoff lost revision: %+v", s.knowledgeAction)
	}
}

func TestHostedMenusKeepPagingAndAuthoringReachable(t *testing.T) {
	s := &hostedFixture{fixtureService: &fixtureService{state: detective.State{Environments: []string{"qa"}}}}
	m := New(s, "detective")
	m.state = s.state
	m.id = "case-1"
	_, cmd := m.openTimeline()
	if cmd == nil {
		t.Fatal("Case timeline did not load")
	}
	m.Update(cmd())
	if m.screen != timeline || !m.eventsMore || m.eventsBefore != 40 {
		t.Fatalf("timeline page state lost: screen=%s cursor=%d", m.screen, m.eventsBefore)
	}
	_, cmd = m.updateHostedKey("l")
	if cmd == nil {
		t.Fatal("older Case events cannot be requested")
	}
	m.Update(cmd())
	if len(m.events) != 2 {
		t.Fatalf("older Case events not appended: %+v", m.events)
	}
	_, cmd = m.openIncidents()
	if cmd == nil {
		t.Fatal("Incident list did not load")
	}
	m.Update(cmd())
	if m.screen != incidentList {
		t.Fatalf("Incident list navigation failed: %s", m.screen)
	}
	m.updateHostedKey("s")
	if m.form != nil {
		t.Fatal("empty Incident list opened a start-Case form")
	}
	for _, tc := range []struct{ key, kind string }{{"c", "incident-create"}, {"l", "incident-link"}} {
		m.screen = incidentList
		m.updateHostedKey(tc.key)
		if m.form == nil || m.form.kind != tc.kind || m.field("Environment") != "qa" {
			t.Fatalf("Incident %s form unavailable: %+v", tc.kind, m.form)
		}
		m.form = nil
	}
	m.screen = incidentList
	m.rows = []row{{id: "store/incident-1"}}
	m.index = 0
	m.updateHostedKey("s")
	if m.form == nil || m.form.kind != "incident-start-case" || m.field("Incident ref") != "store/incident-1" {
		t.Fatalf("start independent Case lost selected Incident: %+v", m.form)
	}
	m.form = nil
	_, cmd = m.openKnowledge()
	if cmd == nil {
		t.Fatal("knowledge list did not load")
	}
	m.Update(cmd())
	if m.screen != knowledgeList || !m.knowledge.HasMore {
		t.Fatalf("knowledge list state lost: screen=%s page=%+v", m.screen, m.knowledge)
	}
	_, cmd = m.updateHostedKey("l")
	if cmd == nil {
		t.Fatal("older knowledge proposals cannot be requested")
	}
	m.Update(cmd())
	if len(m.knowledge.Proposals) != 2 {
		t.Fatalf("older knowledge proposals not appended: %+v", m.knowledge.Proposals)
	}
	m.screen = knowledgeList
	m.updateHostedKey("n")
	if m.form == nil || m.form.kind != "knowledge-draft" || m.field("Claim status") != "hypothesis" {
		t.Fatalf("private draft form unavailable: %+v", m.form)
	}
	m.form = nil
	m.screen = knowledgeList
	m.rebuild()
	m.updateHostedKey("enter")
	if m.screen != knowledgeDetail || m.currentProposal() == nil {
		t.Fatal("selected private proposal did not open")
	}
	for _, tc := range []struct{ key, kind string }{{"r", "knowledge-review"}, {"x", "knowledge-reject"}, {"e", "knowledge-export"}} {
		m.screen = knowledgeDetail
		m.updateHostedKey(tc.key)
		if m.form == nil || m.form.kind != tc.kind || m.form.proposal.ID != "finding-1" {
			t.Fatalf("knowledge %s form lost selected proposal: %+v", tc.kind, m.form)
		}
		m.form = nil
	}
	m.proposalID = "no-longer-visible"
	m.screen = knowledgeDetail
	m.updateHostedKey("r")
	if m.form != nil || m.currentProposal() != nil {
		t.Fatal("missing proposal opened a review form")
	}
	m.proposalID = "finding-1"
	m.updateHostedKey("unbound")
	if m.form != nil {
		t.Fatal("unbound key opened a mutation form")
	}
}

func TestScenarioStepSelectionRejectsUnavailableOrAlreadyCompletedWork(t *testing.T) {
	m := New(&fixtureService{}, "detective")
	base := detective.Case{ID: "case-1", ScenarioApplications: []detective.ScenarioApplication{{ID: "application-1", Snapshot: detective.Scenario{Steps: []detective.ScenarioStep{{ID: "inspect", Kind: "query", Ref: "saved-query"}}}, Steps: []detective.ScenarioStepReceipt{{StepID: "inspect", Status: "pending"}}}}}
	for _, tc := range []struct {
		name, row, want string
		edit            func(*detective.Case)
	}{
		{name: "malformed row", row: "inspect"},
		{name: "unknown application", row: "missing/inspect", want: "unavailable"},
		{name: "unknown step", row: "application-1/missing", want: "unavailable"},
		{name: "completed step", row: "application-1/inspect", want: "already completed", edit: func(c *detective.Case) { c.ScenarioApplications[0].Steps[0].Status = "completed" }},
		{name: "query service absent", row: "application-1/inspect", want: "saved-query execution receipts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			c.ScenarioApplications = append([]detective.ScenarioApplication(nil), base.ScenarioApplications...)
			c.ScenarioApplications[0].Steps = append([]detective.ScenarioStepReceipt(nil), base.ScenarioApplications[0].Steps...)
			if tc.edit != nil {
				tc.edit(&c)
			}
			m.form, m.feedback = nil, ""
			_, cmd := m.startScenarioStep(c, tc.row)
			if cmd != nil || m.form != nil || (tc.want != "" && !strings.Contains(m.feedback, tc.want)) {
				t.Fatalf("%s opened unreviewable step: form=%+v feedback=%q", tc.name, m.form, m.feedback)
			}
		})
	}
}

func TestScenarioQueryPrefillsOnlySchemaBoundScalarAndSetContext(t *testing.T) {
	boolean := investigation.NewBooleanValue(true)
	number := investigation.NewNumberValue(2.5)
	window := detective.ValueRange{Type: "range", Start: investigation.NewStringValue("2026-10-01"), End: investigation.NewStringValue("2026-10-13")}
	c := detective.Case{Context: map[string]detective.InputValue{
		"enabled": {Scalar: &boolean}, "threshold": {Scalar: &number}, "window": {Range: &window}, "unset": {},
	}}
	m := New(&fixtureService{}, "detective")
	text := m.caseContextAsInputs(c, []detective.Input{{Name: "missing"}, {Name: "window"}, {Name: "enabled"}, {Name: "threshold"}, {Name: "unset"}})
	if text != "enabled=boolean:scalar:true; threshold=number:scalar:2.5" {
		t.Fatalf("Scenario context prefill included an unbound or range input: %q", text)
	}
}

func TestHostedQueryFormRejectsMissingAuthorityAndUnbindableInputs(t *testing.T) {
	s := &hostedFixture{fixtureService: &fixtureService{state: detective.State{Environments: []string{"qa"}}}}
	m := New(s, "detective")
	m.state = s.state
	if err := m.validateEnvironment("qa"); err != nil {
		t.Fatalf("admitted environment rejected: %v", err)
	}
	m.state.Environments = nil
	if err := m.validateEnvironment("local"); err != nil {
		t.Fatalf("agent-supplied environment rejected without catalog: %v", err)
	}
	m.state.Environments = []string{"qa"}
	if cmd, handled, err := m.submitHostedForm(); handled || cmd != nil || err != nil {
		t.Fatalf("absent form unexpectedly submitted: handled=%t err=%v", handled, err)
	}
	m.begin("scenario-query-run", "Run", []field{f("Environment", "", ""), f("Query ID", "saved-query", ""), f("Inputs", "", "")})
	for _, tc := range []struct {
		name, env, query, inputs, want string
	}{
		{name: "environment missing", env: "", query: "saved-query", want: "select a logical environment"},
		{name: "query missing", env: "qa", query: "", want: "saved query ID required"},
		{name: "malformed input", env: "qa", query: "saved-query", inputs: "invoice_ids", want: "expected name=type"},
		{name: "range without endpoint bindings", env: "qa", query: "saved-query", inputs: "window=date:range:2026-10-01..2026-10-13", want: "requires explicit start/end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.form.fields[0].value, m.form.fields[1].value, m.form.fields[2].value = tc.env, tc.query, tc.inputs
			cmd, handled, err := m.submitHostedForm()
			if !handled || cmd != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s dispatched query: handled=%t err=%v", tc.name, handled, err)
			}
		})
	}
	m.form.fields[0].value, m.form.fields[1].value, m.form.fields[2].value = "qa", "saved-query", "invoice_id=integer:scalar:7"
	cmd, handled, err := m.submitHostedForm()
	if !handled || cmd == nil || err != nil {
		t.Fatalf("valid saved query failed to dispatch: handled=%t err=%v", handled, err)
	}
	cmd()
	if s.query.Inputs["invoice_id"].Scalar == nil || s.query.QueryID != "saved-query" {
		t.Fatalf("typed saved-query input lost: %+v", s.query)
	}
}
