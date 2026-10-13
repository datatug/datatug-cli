package detectiveui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
)

type executionFixtureService struct{ fixtureService }

func (*executionFixtureService) RunSavedQuery(context.Context, SavedQueryRun) (SavedQueryResult, error) {
	return SavedQueryResult{Result: apicontract.Result{}}, nil
}

func TestNativeCatalogFormsAndSmallTerminalFocus(t *testing.T) {
	m := New(&fixtureService{}, "checks")
	m.height = 8
	for _, entry := range []struct {
		screen screen
		kind   string
	}{
		{checks, "new-check"}, {cases, "new-case"}, {scenarios, "new-scenario"},
		{validators, "new-promoted"}, {queryValidators, "new-query-validators"},
	} {
		m.screen = entry.screen
		m.startNew()
		if m.screen != formScreen || m.form.kind != entry.kind || m.returnScreen != entry.screen {
			t.Fatalf("catalog %s opened %+v", entry.screen, m.form)
		}
		press(m, "esc")
		if m.screen != entry.screen || m.form != nil {
			t.Fatalf("esc did not return to %s", entry.screen)
		}
	}
	m.begin("test", "Visible form", []field{f("One", "αβ", "help"), f("Two", "", ""), f("Three", "", ""), f("Four", "", ""), f("Five", "", "")})
	if !strings.Contains(m.formView(), "↓ more fields") {
		t.Fatal("small terminal hid overflow indicator")
	}
	press(m, "backspace")
	if got := m.field("One"); got != "α" {
		t.Fatalf("backspace split a UTF-8 rune: %q", got)
	}
	press(m, "shift+tab")
	if m.form.index != 4 || !strings.Contains(m.formView(), "↑ earlier fields") {
		t.Fatalf("reverse focus/wrapped viewport = %d, %q", m.form.index, m.formView())
	}
	press(m, "tab")
	if m.form.index != 0 {
		t.Fatalf("forward focus failed to wrap: %d", m.form.index)
	}
	m.form.fields[0].readOnly = true
	if !strings.Contains(m.formView(), "[review]") {
		t.Fatal("read-only extracted value lacked review marker")
	}
	press(m, "backspace")
	typeText(m, "X")
	if got := m.field("One"); got != "α" {
		t.Fatalf("review-only value changed: %q", got)
	}
}

func TestNativeFormEscapeAndValidationFeedback(t *testing.T) {
	m := New(&fixtureService{}, "detective")
	m.screen = formScreen
	m.updateForm(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.screen != cases {
		t.Fatal("missing form did not return to home")
	}
	m.begin("run", "Run", []field{f("Environment", "", "")})
	m.pendingApplicationID, m.pendingStepID = "application", "step"
	m.returnScreen = ""
	press(m, "esc")
	if m.screen != cases || m.pendingApplicationID != "" || m.pendingStepID != "" {
		t.Fatalf("run cancellation left pending Scenario step: %+v", m)
	}
	m.begin("new-case", "New Case", []field{f("ID", "case", ""), f("Title", "", "")})
	press(m, "ctrl+s")
	if !strings.Contains(m.feedback, "title") || m.form == nil {
		t.Fatalf("invalid Case form silently closed: %q", m.feedback)
	}
}

func TestNativeDetailFormsExposeCheckCaseScenarioAndValidatorActions(t *testing.T) {
	check := detective.Check{ID: "check", Title: "Check", Steps: []detective.Step{{ID: "load", Kind: "query", QueryID: "q", Validators: []detective.Validator{{ID: "required", Scope: "row", Kind: "non-null", Field: "InvoiceId"}}}}}
	caseItem := detective.Case{ID: "case", Title: "Case", Status: "active"}
	scenario := detective.Scenario{ID: "scenario", Title: "Scenario"}
	local := detective.QueryValidators{QueryID: "q", Validators: []detective.Validator{{ID: "required", Scope: "row", Kind: "non-null", Field: "InvoiceId"}}}
	m := New(&fixtureService{}, "detective")
	m.state = detective.State{Checks: []detective.Check{check}, Cases: []detective.Case{caseItem}, Scenarios: []detective.Scenario{scenario}, QueryValidators: []detective.QueryValidators{local}, Environments: []string{"qa"}}
	m.screen, m.parent, m.id = detail, checks, "check"
	m.rows = []row{{kind: "step", id: "load"}}
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-step" || m.form.stepIndex != 0 {
		t.Fatalf("Check step edit = %+v", m.form)
	}
	m.screen = detail
	m.rows = []row{{kind: "validator", id: "load/required"}}
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-validator" || m.form.validatorIndex != 0 {
		t.Fatalf("local Validator edit = %+v", m.form)
	}
	m.screen = detail
	m.detailKey("p")
	if m.form == nil || m.form.kind != "promote-validator" || m.form.validator.ID != "required" {
		t.Fatalf("Validator promotion = %+v", m.form)
	}
	m.screen = detail
	m.detailKey("a")
	if m.form == nil || m.form.kind != "add-step" {
		t.Fatalf("Check step add = %+v", m.form)
	}
	m.screen = detail
	m.rows = []row{{kind: "check", id: "check"}}
	m.detailKey("v")
	if !strings.Contains(m.feedback, "Select a query step") {
		t.Fatalf("Validator attached without a query step: %q", m.feedback)
	}
	m.rows = []row{{kind: "step", id: "load"}}
	m.detailKey("v")
	if m.form == nil || m.form.kind != "add-validator" {
		t.Fatalf("query-step Validator add = %+v", m.form)
	}
	m.screen, m.parent, m.id = detail, cases, "case"
	for key, kind := range map[string]string{"c": "context", "e": "evidence", "f": "finding", "s": "link-scenario", "r": "choose-check", "t": "event", "x": "close"} {
		m.screen = detail
		m.detailKey(key)
		if m.form == nil || m.form.kind != kind || m.form.caseItem.ID != "case" {
			t.Fatalf("Case key %s opened %+v", key, m.form)
		}
	}
	m.screen, m.parent, m.id = detail, scenarios, "scenario"
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-scenario" {
		t.Fatalf("Scenario edit = %+v", m.form)
	}
	m.screen = detail
	m.detailKey("a")
	if m.form == nil || m.form.kind != "add-scenario-step" {
		t.Fatalf("Scenario step add = %+v", m.form)
	}
	m.screen, m.parent, m.id = detail, queryValidators, "q"
	m.rows = []row{{kind: "validator", id: "required"}}
	for key, kind := range map[string]string{"e": "edit-query-validator", "v": "add-query-validator", "p": "promote-validator", "r": "run-query-validators"} {
		m.screen = detail
		m.detailKey(key)
		if m.form == nil || m.form.kind != kind {
			t.Fatalf("query Validator key %s opened %+v", key, m.form)
		}
	}
}

func TestNativeDetailKeysFailClosedWithoutSelectedResourceOrStep(t *testing.T) {
	m := New(&fixtureService{}, "detective")
	m.screen = detail
	for _, parent := range []screen{checks, cases, scenarios, queryValidators} {
		m.parent = parent
		if _, cmd := m.detailKey("e"); cmd != nil || m.form != nil {
			t.Fatalf("missing %s opened an edit form", parent)
		}
	}
	m.screen = checks
	m.startNew() // ordinary catalog is known; unknown screens must not open a form.
	m.form = nil
	m.screen = history
	m.startNew()
	if m.form != nil {
		t.Fatal("history opened a new catalog item")
	}
	check := detective.Check{ID: "check", Title: "Check", Steps: []detective.Step{{ID: "load", Kind: "query", QueryID: "q"}}}
	m.state.Checks = []detective.Check{check}
	m.parent, m.id, m.screen = checks, "check", detail
	m.rows = nil
	if got := m.selectedStep(&check); got != -1 {
		t.Fatalf("unselected Check step = %d", got)
	}
	m.detailKey("e")
	if m.form == nil || m.form.kind != "edit-check" {
		t.Fatalf("Check metadata fallback = %+v", m.form)
	}
	m.form = nil
	m.rows = []row{{kind: "check", id: "check"}}
	m.detailKey("p")
	if !strings.Contains(m.feedback, "Select a local Validator") {
		t.Fatalf("promotion without selection = %q", m.feedback)
	}
	m.screen = detail
	m.detailKey("r")
	if m.form == nil || m.form.kind != "run" {
		t.Fatalf("Check run form = %+v", m.form)
	}
	m.form = nil
	m.screen = detail
	m.detailKey("h")
	if m.screen != history {
		t.Fatal("Check history was not opened")
	}
	m.state.Cases = []detective.Case{{ID: "case", Title: "Case", Status: "closed"}}
	m.parent, m.id, m.screen = cases, "case", detail
	m.rows = nil
	m.detailKey("i")
	if !strings.Contains(m.feedback, "Incidentius") {
		t.Fatalf("Incidentius capability absence was hidden: %q", m.feedback)
	}
	m.detailKey("enter")
	if m.screen != detail {
		t.Fatal("enter without application step changed Case view")
	}
	_, cmd := m.detailKey("o")
	if cmd == nil {
		t.Fatal("reopen Case did not create an action")
	}
	m.Update(cmd())
	m.screen = detail
	_, cmd = m.detailKey("z")
	if cmd == nil {
		t.Fatal("archive Case did not create an action")
	}
	m.screen = detail
	m.detailKey("h")
	if m.screen != history {
		t.Fatal("Case history was not opened")
	}
	m.state.QueryValidators = []detective.QueryValidators{{QueryID: "q", Validators: []detective.Validator{{ID: "v"}}}}
	m.parent, m.id, m.screen = queryValidators, "q", detail
	m.rows = []row{{kind: "query-validator", id: "q"}}
	m.detailKey("e")
	if !strings.Contains(m.feedback, "Select a local Validator") {
		t.Fatalf("query-local edit without selection = %q", m.feedback)
	}
	m.detailKey("p")
	if !strings.Contains(m.feedback, "Select a local Validator") {
		t.Fatalf("query-local promotion without selection = %q", m.feedback)
	}
	m.detailKey("q")
	if m.feedback == "" {
		t.Fatal("exact query validation silently proceeded without execution service")
	}
	m.screen = detail
	m.detailKey("h")
	if m.screen != history {
		t.Fatal("query Validator history was not opened")
	}
	m.screen = detail
	m.detailKey("unassigned")
	if m.screen != detail {
		t.Fatal("ordinary typing was consumed as a detail command")
	}
	m.service = &executionFixtureService{}
	m.detailKey("q")
	if m.form == nil || m.form.kind != "query-validators-exact" || m.field("Query ID") != "q" {
		t.Fatalf("exact saved-query validation form = %+v", m.form)
	}
}

func TestNativeCaseRunSelectionAndLifecycleKeepConfirmedContext(t *testing.T) {
	m := New(&fixtureService{}, "detective")
	if _, cmd := m.startRun("missing", "case"); cmd != nil || !strings.Contains(m.feedback, "not in catalog") {
		t.Fatalf("unknown Check opened a run: %q", m.feedback)
	}
	check := detective.Check{ID: "invoice", Title: "Invoice", Inputs: []detective.Input{{Name: "invoice_id", Type: "integer", Cardinality: "scalar"}}}
	m.state.Checks = []detective.Check{check}
	m.state.Environments = []string{"qa"}
	m.state.Cases = []detective.Case{{ID: "unrelated", Context: map[string]detective.InputValue{"invoice_id": {}}}, {ID: "case", Context: map[string]detective.InputValue{"invoice_id": {}}}}
	m.startRun("invoice", "case")
	if m.form == nil || m.form.kind != "run" || m.field("Environment") != "qa" {
		t.Fatalf("Case run selection = %+v", m.form)
	}
	if m.checkByID("missing") != nil {
		t.Fatal("missing Check resolved from catalog")
	}
	closed := detective.Case{ID: "case", Status: "closed"}
	_, cmd := m.lifecycle(closed, "active", "re-reviewed")
	if cmd == nil {
		t.Fatal("Case lifecycle action absent")
	}
	message := cmd()
	if message == nil {
		t.Fatal("Case lifecycle produced no response")
	}
	actions := m.service.(*fixtureService).actions
	if len(actions) != 1 || actions[0].Case.Status != "active" || actions[0].Case.ClosureReason != "re-reviewed" {
		t.Fatalf("Case lifecycle intent = %+v", actions)
	}
}
