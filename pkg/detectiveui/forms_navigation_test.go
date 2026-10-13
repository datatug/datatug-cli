package detectiveui

import (
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/detective"
)

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
	press(m, "backspace")
	typeText(m, "X")
	if got := m.field("One"); got != "α" {
		t.Fatalf("review-only value changed: %q", got)
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
