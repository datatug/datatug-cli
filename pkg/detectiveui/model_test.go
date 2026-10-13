package detectiveui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

type fixtureService struct {
	state   detective.State
	actions []detective.ActionRequest
}

func (s *fixtureService) State(context.Context) (detective.State, error) { return s.state, nil }
func (s *fixtureService) Action(_ context.Context, r detective.ActionRequest) (detective.ActionResponse, error) {
	s.actions = append(s.actions, r)
	switch r.Type {
	case "save-check":
		s.state.Checks = []detective.Check{*r.Check}
		return detective.ActionResponse{Check: r.Check}, nil
	case "create-case", "update-case":
		s.state.Cases = []detective.Case{*r.Case}
		return detective.ActionResponse{Case: r.Case}, nil
	case "run-check":
		run := detective.CheckRun{ID: "run-1", CheckID: r.CheckID, Environment: r.Environment, CaseID: r.CaseID, ExecutionStatus: "completed", Outcome: "fail", Steps: []detective.StepResult{{StepID: "query", Kind: "query", ExecutionStatus: "completed", Outcome: "fail", Validators: []detective.ValidationResult{{ValidatorID: "each-invoice", ExecutionStatus: "completed", Outcome: "fail", RowIndex: intPtr(2), Children: []detective.StepResult{{StepID: "crm", Kind: "check", ExecutionStatus: "error", Outcome: "unknown", Message: "CRM unavailable"}}}}}}}
		s.state.Runs = []detective.CheckRun{run}
		return detective.ActionResponse{Run: &run}, nil
	case "save-query-validators":
		s.state.QueryValidators = []detective.QueryValidators{*r.QueryValidators}
		return detective.ActionResponse{QueryValidators: r.QueryValidators}, nil
	case "run-query-validators":
		run := detective.CheckRun{ID: "query-run", Kind: "query-validators", QueryID: r.QueryID, Environment: r.Environment, ExecutionStatus: "completed", Outcome: "pass"}
		s.state.Runs = []detective.CheckRun{run}
		return detective.ActionResponse{Run: &run}, nil
	}
	return detective.ActionResponse{}, nil
}
func intPtr(n int) *int { return &n }
func press(m *Model, s string) tea.Cmd {
	var k tea.KeyPressMsg
	switch s {
	case "enter":
		k.Code = tea.KeyEnter
	case "tab":
		k.Code = tea.KeyTab
	case "ctrl+s":
		k.Code = 's'
		k.Mod = tea.ModCtrl
	case "ctrl+n":
		k.Code = 'n'
		k.Mod = tea.ModCtrl
	case "esc":
		k.Code = tea.KeyEsc
	case "down":
		k.Code = tea.KeyDown
	case "up":
		k.Code = tea.KeyUp
	default:
		k.Code = []rune(s)[0]
		k.Text = s
	}
	_, c := m.Update(k)
	return c
}
func typeText(m *Model, s string) {
	for _, r := range s {
		press(m, string(r))
	}
}
func submit(m *Model) {
	cmd := press(m, "ctrl+s")
	if cmd != nil {
		m.Update(cmd())
		m.Update(m.load()())
	}
}
func TestChecksAuthoringRunAndNestedDrilldown(t *testing.T) {
	s := &fixtureService{state: detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true, CanRun: true}}}
	m := New(s, "checks")
	m.Update(m.Init()())
	m.Update(tea.WindowSizeMsg{Width: 42, Height: 10})
	press(m, "ctrl+n")
	typeText(m, "validate-invoice")
	press(m, "tab")
	typeText(m, "Validate invoice")
	press(m, "tab")
	press(m, "tab")
	typeText(m, "invoice-id:integer:scalar:required")
	press(m, "ctrl+s")
	if m.form == nil || m.form.kind != "first-step" {
		t.Fatalf("expected first step form: %#v", m.form)
	}
	typeText(m, "detail")
	press(m, "tab")
	press(m, "tab")
	typeText(m, "invoice-detail")
	submit(m)
	if len(s.actions) != 1 || s.actions[0].Type != "save-check" || m.screen != detail {
		t.Fatalf("check action: %#v screen %s", s.actions, m.screen)
	}
	if !strings.Contains(m.View().Content, "validate-invoice") {
		t.Fatal("check detail absent")
	}
	press(m, "down")
	press(m, "down")
	press(m, "v")
	if m.form == nil || m.form.kind != "add-validator" {
		t.Fatal("validator form missing")
	}
	m.form.fields[2].value = "each-invoice"
	m.form.fields[4].value = "check"
	m.form.fields[9].value = "validate-customer"
	m.form.fields[10].value = "invoice-id=@invoice-id"
	submit(m)
	if len(s.actions) != 2 || len(s.actions[1].Check.Steps[0].Validators) != 1 {
		t.Fatal("local validator not saved")
	}
	press(m, "r")
	if m.form == nil || m.form.kind != "run" {
		t.Fatal("run form missing")
	}
	typeText(m, "qa")
	press(m, "tab")
	typeText(m, "42")
	submit(m)
	if s.actions[2].Environment != "qa" || s.actions[2].Inputs["invoice-id"].Scalar.Str != "42" {
		t.Fatalf("run request: %#v", s.actions[2])
	}
	for i := 0; i < 4; i++ {
		press(m, "down")
	}
	if m.screen != runDetail || !strings.Contains(m.View().Content, "CRM unavailable") {
		t.Fatal("nested result absent")
	}
	press(m, "f")
	for i := 0; i < 3; i++ {
		press(m, "down")
	}
	if len(m.rows) != 5 || !strings.Contains(m.View().Content, "each-invoice") {
		t.Fatalf("failure filter lost nested row: %#v", m.rows)
	}
	press(m, "esc")
	if m.screen != detail || m.parent != checks {
		t.Fatalf("back navigation: %s / %s", m.screen, m.parent)
	}
}
func TestCaseContextAndLifecycle(t *testing.T) {
	s := &fixtureService{state: detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true}}}
	m := New(s, "detective")
	m.Update(m.Init()())
	press(m, "ctrl+n")
	typeText(m, "invoice-mismatch")
	press(m, "tab")
	typeText(m, "Invoice mismatch")
	submit(m)
	press(m, "c")
	m.form.fields[0].value = "invoice-ids"
	m.form.fields[1].value = "integer"
	m.form.fields[2].value = "set"
	m.form.fields[3].value = "1,2,3"
	submit(m)
	if got := s.actions[1].Case.Context["invoice-ids"]; got.Set == nil || len(got.Set.Values) != 3 {
		t.Fatalf("set context: %#v", got)
	}
	press(m, "x")
	m.form.fields[0].value = "Reconciled"
	submit(m)
	if s.actions[2].Case.Status != "closed" || s.actions[2].Case.ClosureReason != "Reconciled" {
		t.Fatal("close action missing")
	}
	if cmd := press(m, "o"); cmd != nil {
		m.Update(cmd())
		m.Update(m.load()())
	}
	if s.actions[3].Case.Status != "active" {
		t.Fatal("reopen action missing")
	}
}
func TestSearchTypingAndSmallViewport(t *testing.T) {
	s := &fixtureService{state: detective.State{Checks: []detective.Check{{ID: "join-invoices", Title: "Join invoices"}, {ID: "list-invoices", Title: "List invoices"}}, Capabilities: detective.Capabilities{CanRead: true}}}
	m := New(s, "checks")
	m.Update(m.Init()())
	m.Update(tea.WindowSizeMsg{Width: 25, Height: 8})
	press(m, "j")
	if m.filter != "j" || len(m.rows) != 1 {
		t.Fatalf("ordinary j not search: %q %d", m.filter, len(m.rows))
	}
	if !strings.Contains(m.View().Content, "Search: j") {
		t.Fatal("search not visible")
	}
}
func TestTypedValueSetRange(t *testing.T) {
	for _, tc := range []struct{ kind, card, raw string }{{"integer", "scalar", "42"}, {"integer", "set", "1,2"}, {"date", "range", "2026-01-01..2026-01-31"}} {
		v, e := parseValue(tc.kind, tc.card, tc.raw)
		if e != nil {
			t.Fatal(e)
		}
		if e = v.Validate(); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := parseValue("date", "range", "bad"); e == nil {
		t.Fatal("invalid range accepted")
	}
	if _, e := parseValue("integer", "scalar", "secret"); e == nil {
		t.Fatal("invalid integer accepted")
	}
	_ = investigation.ValueTypeString
}

func TestQueryLocalValidatorsAndManualRun(t *testing.T) {
	s := &fixtureService{state: detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true, CanRun: true}}}
	m := New(s, "checks")
	m.Update(m.Init()())
	m.openCatalog(queryValidators)
	press(m, "ctrl+n")
	typeText(m, "invoice-list")
	press(m, "ctrl+s")
	if m.form == nil || m.form.kind != "first-query-validator" {
		t.Fatal("expected local Validator form")
	}
	m.form.fields[2].value = "row-count"
	m.form.fields[3].value = "set"
	m.form.fields[4].value = "row-count"
	m.form.fields[8].value = "10"
	submit(m)
	if len(s.actions) != 1 || s.actions[0].Type != "save-query-validators" || s.actions[0].QueryValidators.QueryID != "invoice-list" {
		t.Fatalf("query Validator action: %#v", s.actions)
	}
	press(m, "r")
	if m.form == nil || m.form.kind != "run-query-validators" {
		t.Fatal("run query Validators form missing")
	}
	m.form.fields[0].value = "qa"
	m.form.fields[1].value = "invoice-id=integer:scalar:42; dates=date:range:2026-01-01..2026-01-31"
	submit(m)
	if len(s.actions) != 2 || s.actions[1].QueryID != "invoice-list" || s.actions[1].Inputs["dates"].Range == nil {
		t.Fatalf("run query Validators action: %#v", s.actions)
	}
	if m.screen != runDetail || !strings.Contains(m.View().Content, "invoice-list") {
		t.Fatal("query run detail missing")
	}
}
