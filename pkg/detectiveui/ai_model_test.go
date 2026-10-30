package detectiveui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/investigation"
)

type aiUIFixture struct {
	*fixtureService
	page      AIPage
	generated AIGenerate
	reviewed  AIReview
	confirmed ai.ConfirmRequest
}

func (s *aiUIFixture) AIProposals(context.Context, string) (AIPage, error) { return s.page, nil }
func (s *aiUIFixture) GenerateAI(_ context.Context, a AIGenerate) (ai.Response, error) {
	s.generated = a
	return ai.Response{Proposals: s.page.Proposals}, nil
}
func (s *aiUIFixture) ReviewAI(_ context.Context, a AIReview) (ai.Proposal, error) {
	s.reviewed = a
	return s.page.Proposals[0], nil
}
func (s *aiUIFixture) ConfirmAI(_ context.Context, a ai.ConfirmRequest) (ai.Receipt, error) {
	s.confirmed = a
	return ai.Receipt{OperationID: "confirmed-1"}, nil
}

func TestNativeAIProposalPromptCitationTypedReviewAndExplicitConfirmation(t *testing.T) {
	value := investigation.NewIntegerValue("7")
	orders, _ := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("2"), investigation.NewIntegerValue("3"), investigation.NewIntegerValue("4"), investigation.NewIntegerValue("5"))
	invoices, _ := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("2"), investigation.NewIntegerValue("3"), investigation.NewIntegerValue("4"), investigation.NewIntegerValue("5"), investigation.NewIntegerValue("6"), investigation.NewIntegerValue("7"))
	dates := detective.ValueRange{Type: "range", Start: investigation.NewDateValue("2026-01-01"), End: investigation.NewDateValue("2026-01-31")}
	proposal := ai.Proposal{ID: "proposal-1", Digest: "digest-1", Draft: ai.Draft{Kind: ai.KindContext, Title: "Invoice 7", Context: map[string]ai.ExtractedValue{"invoice_ids": {Value: detective.InputValue{Set: &invoices}, Provenance: []ai.Provenance{{SourceID: "paste-1", Start: 0, End: 9, Quote: "invoice 7", Confidence: .8, Ambiguous: true}}}, "order_ids": {Value: detective.InputValue{Set: &orders}}, "date_window": {Value: detective.InputValue{Range: &dates}}, "invoice_id": {Value: detective.InputValue{Scalar: &value}, Provenance: []ai.Provenance{{SourceID: "paste-1", Start: 0, End: 9, Quote: "invoice 7", Confidence: .8, Ambiguous: true}}}}, Ambiguities: []string{"invoice may be unconfirmed"}}}
	s := &aiUIFixture{fixtureService: &fixtureService{state: detective.State{Cases: []detective.Case{{ID: "case-1", Title: "Case", Version: 2}}, Capabilities: detective.Capabilities{CanRead: true, CanWrite: true}}}, page: AIPage{Proposals: []ai.Proposal{proposal}}}
	m := New(s, "detective")
	m.Update(m.Init()())
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
	_, load := m.openAI()
	if load == nil {
		t.Fatal("AI proposal load missing")
	}
	m.Update(load())
	if m.screen != aiList || len(m.rows) != 1 {
		t.Fatalf("AI catalog missing: %s", m.View().Content)
	}
	press(m, "n")
	if m.form == nil || m.form.kind != "ai-generate" {
		t.Fatal("AI prompt form missing")
	}
	m.form.fields[0].value = "invoice 7"
	m.form.fields[1].value = "case-1"
	m.form.fields[2].value = "invoice_ids=7"
	cmd := press(m, "ctrl+s")
	if cmd == nil {
		t.Fatal("model generation not dispatched")
	}
	_, refresh := m.Update(cmd())
	if refresh == nil {
		t.Fatal("private proposal refresh missing")
	}
	m.Update(refresh())
	if s.generated.Text != "invoice 7" || s.generated.CaseID != "case-1" || s.generated.ExpectedCounts["invoice_ids"] != 7 {
		t.Fatalf("typed prompt lost: %+v", s.generated)
	}
	if m.screen != aiDetail || len(m.rows) < 4 {
		t.Fatalf("review details missing: %s", m.View().Content)
	}
	press(m, "down")
	press(m, "down")
	if !strings.Contains(m.View().Content, "Uncertainty") {
		t.Fatalf("uncertainty inaccessible: %s", m.View().Content)
	}
	for m.index < len(m.rows)-1 && m.rows[m.index].kind != "citation" {
		press(m, "down")
	}
	if !strings.Contains(m.View().Content, "Citation") {
		t.Fatal("citation inaccessible in a small terminal")
	}
	press(m, "c")
	if m.form != nil {
		t.Fatal("unreviewed context was offered for confirmation")
	}
	press(m, "r")
	if m.form == nil || m.form.kind != "ai-context-review" {
		t.Fatal("typed context review unavailable")
	}
	if len(m.form.fields) != 17 {
		t.Fatalf("all extracted fields not present: %d", len(m.form.fields))
	}
	for i := range m.form.fields {
		if m.form.fields[i].label == "Context 3 value" {
			m.form.fields[i].value = "1, 2, 3, 4, 5, 6, 7"
		}
	}
	cmd = press(m, "ctrl+s")
	if cmd == nil {
		t.Fatalf("review not dispatched: %s", m.feedback)
	}
	_, refresh = m.Update(cmd())
	if refresh == nil {
		t.Fatal("reviewed proposal refresh missing")
	}
	m.Update(refresh())
	if s.reviewed.Context == nil || s.reviewed.Context.ExpectedVersion != 2 || len(s.reviewed.Context.Context) != 4 || len(s.reviewed.Context.Context["invoice_ids"].Set.Values) != 7 || len(s.reviewed.Context.Context["order_ids"].Set.Values) != 5 || s.reviewed.Context.Context["date_window"].Range == nil {
		t.Fatalf("reviewed typed cohort missing: %+v", s.reviewed)
	}

	// A concrete reviewed Case proposal needs a separate explicit confirmation.
	s.page.Proposals[0] = ai.Proposal{ID: "reviewed-1", ParentProposalID: "proposal-1", Digest: "digest-2", Draft: ai.Draft{Kind: ai.KindCase, Title: "Reviewed Case", Case: &ai.CaseEdit{CaseID: "case-1", ExpectedVersion: 2}}}
	m.aiPage = s.page
	m.aiProposalID = "reviewed-1"
	m.rebuild()
	press(m, "c")
	if m.form == nil || m.form.kind != "ai-confirm" {
		t.Fatal("confirmation form missing")
	}
	if cmd := press(m, "ctrl+s"); cmd != nil || m.feedback == "" {
		t.Fatal("empty confirmation accepted")
	}
	setAIField(m, "Confirmation", "CONFIRM")
	cmd = press(m, "ctrl+s")
	if cmd == nil {
		t.Fatal("explicit confirmation not dispatched")
	}
	m.Update(cmd())
	if s.confirmed.ProposalID != "reviewed-1" || s.confirmed.Digest != "digest-2" {
		t.Fatalf("confirmed wrong proposal: %+v", s.confirmed)
	}
}

func TestNativeAIReviewCorrectsValidatorAndCheckBinding(t *testing.T) {
	check := detective.Check{ID: "review-check", Title: "Review", Steps: []detective.Step{{ID: "detail", Kind: "query", QueryID: "invoice-detail", Bindings: []detective.Binding{{Input: "invoice_id", FromInput: "invoice_ids"}}, Validators: []detective.Validator{{ID: "customer", Scope: "row", Kind: "non-null", Severity: "error", Field: "wrong_field"}}}}}
	proposal := ai.Proposal{ID: "draft-check", Digest: "digest", Draft: ai.Draft{Kind: ai.KindCheck, Title: "Review", Check: &check}}
	s := &aiUIFixture{fixtureService: &fixtureService{state: detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true}}}, page: AIPage{Proposals: []ai.Proposal{proposal}}}
	m := New(s, "checks")
	m.Update(m.Init()())
	m.aiPage = s.page
	m.aiProposalID = proposal.ID
	m.screen = aiDetail
	m.rebuild()
	for i, row := range m.rows {
		if row.kind == "ai-check-validator" {
			m.index = i
			break
		}
	}
	press(m, "e")
	if m.form == nil || m.form.kind != "ai-validator-edit" {
		t.Fatal("generated Validator has no typed editor")
	}
	for i := range m.form.fields {
		if m.form.fields[i].label == "Field" {
			m.form.fields[i].value = "customer_id"
		}
	}
	cmd := press(m, "ctrl+s")
	if cmd == nil {
		t.Fatalf("Validator correction not dispatched: %s", m.feedback)
	}
	_, refresh := m.Update(cmd())
	if refresh != nil {
		m.Update(refresh())
	}
	if s.reviewed.Draft == nil || s.reviewed.Draft.Draft.Check == nil || s.reviewed.Draft.Draft.Check.Steps[0].Validators[0].Field != "customer_id" {
		t.Fatalf("Validator field not corrected: %+v", s.reviewed)
	}
	// Reopen original for a distinct binding edit; both are typed native forms.
	m.aiPage = s.page
	m.aiProposalID = proposal.ID
	m.screen = aiDetail
	m.rebuild()
	for i, row := range m.rows {
		if row.kind == "ai-check-step" {
			m.index = i
			break
		}
	}
	press(m, "e")
	if m.form == nil || m.form.kind != "ai-step-edit" {
		t.Fatal("generated Check step has no typed editor")
	}
	for i := range m.form.fields {
		if m.form.fields[i].label == "Bindings" {
			m.form.fields[i].value = "invoice_id=@invoice_id"
		}
	}
	cmd = press(m, "ctrl+s")
	if cmd == nil {
		t.Fatalf("Check binding correction not dispatched: %s", m.feedback)
	}
	m.Update(cmd())
	bindings := s.reviewed.Draft.Draft.Check.Steps[0].Bindings
	if len(bindings) != 1 || bindings[0].FromColumn != "invoice_id" {
		t.Fatalf("Check binding not corrected: %+v", bindings)
	}
}

func TestNativeAIReviewsCorrectResourceSemantics(t *testing.T) {
	tests := []struct {
		name    string
		draft   ai.Draft
		prepare func(*Model)
		assert  func(*testing.T, ai.Draft)
	}{
		{
			name:  "saved query text and typed inputs",
			draft: ai.Draft{Kind: ai.KindQuery, Title: "Invoice query", Query: &ai.QueryDraft{ID: "invoice-list", Title: "Invoice list", Type: "sql", Text: "select 1"}},
			prepare: func(m *Model) {
				setAIField(m, "Query text", "select invoice_id from invoices")
				setAIField(m, "Inputs", "invoice_ids:integer:set:required")
			},
			assert: func(t *testing.T, got ai.Draft) {
				if got.Query == nil || got.Query.Text != "select invoice_id from invoices" || len(got.Query.Inputs) != 1 || got.Query.Inputs[0].Cardinality != "set" {
					t.Fatalf("query correction lost: %+v", got.Query)
				}
			},
		},
		{
			name:    "check declared inputs",
			draft:   ai.Draft{Kind: ai.KindCheck, Title: "Invoice check", Check: &detective.Check{ID: "invoice-check", Title: "Invoice check"}},
			prepare: func(m *Model) { setAIField(m, "Inputs", "invoice_ids:integer:set:required") },
			assert: func(t *testing.T, got ai.Draft) {
				if got.Check == nil || len(got.Check.Inputs) != 1 || got.Check.Inputs[0].Name != "invoice_ids" {
					t.Fatalf("check input correction lost: %+v", got.Check)
				}
			},
		},
		{
			name:    "validator field",
			draft:   ai.Draft{Kind: ai.KindValidator, Title: "Customer present", Validator: &detective.PublishedValidator{ID: "customer-present", Title: "Customer present", Validator: detective.Validator{ID: "customer-present", Kind: "non-null", Scope: "row", Field: "wrong_field", Severity: "error"}}},
			prepare: func(m *Model) { setAIField(m, "Field", "customer_id") },
			assert: func(t *testing.T, got ai.Draft) {
				if got.Validator == nil || got.Validator.Validator.Field != "customer_id" {
					t.Fatalf("validator correction lost: %+v", got.Validator)
				}
			},
		},
		{
			name:    "scenario declared inputs",
			draft:   ai.Draft{Kind: ai.KindScenario, Title: "Reconcile invoices", Scenario: &detective.Scenario{ID: "reconcile", Title: "Reconcile invoices"}},
			prepare: func(m *Model) { setAIField(m, "Inputs", "invoice_ids:integer:set:required") },
			assert: func(t *testing.T, got ai.Draft) {
				if got.Scenario == nil || len(got.Scenario.Inputs) != 1 || got.Scenario.Inputs[0].Cardinality != "set" {
					t.Fatalf("scenario input correction lost: %+v", got.Scenario)
				}
			},
		},
		{
			name:    "case title",
			draft:   ai.Draft{Kind: ai.KindCase, Title: "Case edit", Case: &ai.CaseEdit{CaseID: "case-1", ExpectedVersion: 2, Title: "Wrong title"}},
			prepare: func(m *Model) { setAIField(m, "Case title", "Invoice reconciliation") },
			assert: func(t *testing.T, got ai.Draft) {
				if got.Case == nil || got.Case.Title != "Invoice reconciliation" || got.Case.ExpectedVersion != 2 {
					t.Fatalf("case correction lost version: %+v", got.Case)
				}
			},
		},
		{
			name:    "manual run cohort",
			draft:   ai.Draft{Kind: ai.KindRun, Title: "Run invoice check", Run: &ai.RunDraft{CheckID: "invoice-check", Environment: "qa"}},
			prepare: func(m *Model) { setAIField(m, "Input invoice_ids", "1,2,3,7") },
			assert: func(t *testing.T, got ai.Draft) {
				if got.Run == nil || got.Run.Environment != "qa" || got.Run.Inputs["invoice_ids"].Set == nil || len(got.Run.Inputs["invoice_ids"].Set.Values) != 4 {
					t.Fatalf("run cohort correction lost: %+v", got.Run)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proposal := ai.Proposal{ID: "draft-1", Digest: "digest-1", Draft: tc.draft}
			service := &aiUIFixture{fixtureService: &fixtureService{state: detective.State{Checks: []detective.Check{{ID: "invoice-check", Inputs: []detective.Input{{Name: "invoice_ids", Type: "integer", Cardinality: "set", Required: true}}}}, Environments: []string{"qa"}, Capabilities: detective.Capabilities{CanRead: true, CanWrite: true, CanRun: true}}}, page: AIPage{Proposals: []ai.Proposal{proposal}}}
			m := New(service, "detective")
			m.Update(m.Init()())
			m.screen, m.aiPage, m.aiProposalID = aiDetail, service.page, proposal.ID
			m.rebuild()
			press(m, "r")
			if m.form == nil || m.form.kind != "ai-draft-review" {
				t.Fatalf("typed review form missing for %s", tc.name)
			}
			tc.prepare(m)
			cmd := press(m, "ctrl+s")
			if cmd == nil {
				t.Fatalf("review was not dispatched: %s", m.feedback)
			}
			m.Update(cmd())
			if service.reviewed.Type != "draft" || service.reviewed.Draft == nil || service.reviewed.Draft.ProposalID != proposal.ID || service.reviewed.Draft.Digest != proposal.Digest {
				t.Fatalf("review lost proposal identity: %+v", service.reviewed)
			}
			tc.assert(t, service.reviewed.Draft.Draft)
		})
	}
}

func setAIField(m *Model, label, value string) {
	for i := range m.form.fields {
		if m.form.fields[i].label == label {
			m.form.fields[i].value = value
			return
		}
	}
	panic("missing AI review field: " + label)
}

func TestNativeAIReviewEditsNestedCheckAndScenarioSteps(t *testing.T) {
	check := detective.Check{ID: "invoice-check", Title: "Invoice check", Steps: []detective.Step{{ID: "detail", Kind: "query", QueryID: "invoice-detail"}}}
	scenario := detective.Scenario{ID: "invoice-review", Title: "Invoice review", Inputs: []detective.Input{{Name: "invoice_ids", Type: investigation.ValueTypeInteger, Cardinality: "set"}, {Name: "window", Type: investigation.ValueTypeDate, Cardinality: "range"}}, Steps: []detective.ScenarioStep{{ID: "find", Kind: "query", Ref: "old-query", Bindings: []detective.Binding{{Input: "ids", FromInput: "invoice_ids"}, {Input: "from_date", FromInput: "window", Part: "start"}, {Input: "to_date", FromInput: "window", Part: "end"}}}}}
	tests := []struct {
		name, key, rowKind string
		draft              ai.Draft
		prepare            func(*Model)
		assert             func(*testing.T, ai.Draft)
	}{
		{
			name: "add Check step", key: "a", draft: ai.Draft{Kind: ai.KindCheck, Check: &check},
			prepare: func(m *Model) {
				setAIField(m, "ID", "crm")
				setAIField(m, "Kind", "check")
				setAIField(m, "Ref", "validate-customer")
			},
			assert: func(t *testing.T, d ai.Draft) {
				if d.Check == nil || len(d.Check.Steps) != 2 || d.Check.Steps[1].CheckID != "validate-customer" {
					t.Fatalf("new nested Check step lost: %+v", d.Check)
				}
			},
		},
		{
			name: "add per-row Validator", key: "v", rowKind: "ai-check-step", draft: ai.Draft{Kind: ai.KindCheck, Check: &check},
			prepare: func(m *Model) {
				setAIField(m, "ID", "customer-id")
				setAIField(m, "Field", "customer_id")
			},
			assert: func(t *testing.T, d ai.Draft) {
				if d.Check == nil || len(d.Check.Steps[0].Validators) != 1 || d.Check.Steps[0].Validators[0].Field != "customer_id" {
					t.Fatalf("local Validator lost: %+v", d.Check)
				}
			},
		},
		{
			name: "repair Scenario query reference", key: "e", rowKind: "ai-scenario-step", draft: ai.Draft{Kind: ai.KindScenario, Scenario: &scenario},
			prepare: func(m *Model) { setAIField(m, "Ref", "invoice-list") },
			assert: func(t *testing.T, d ai.Draft) {
				if d.Scenario == nil || len(d.Scenario.Steps) != 1 || d.Scenario.Steps[0].Ref != "invoice-list" || formatBindings(d.Scenario.Steps[0].Bindings) != "ids=invoice_ids, from_date=window.start, to_date=window.end" {
					t.Fatalf("Scenario query repair lost: %+v", d.Scenario)
				}
			},
		},
		{
			name: "repair Scenario range binding", key: "e", rowKind: "ai-scenario-step", draft: ai.Draft{Kind: ai.KindScenario, Scenario: &scenario},
			prepare: func(m *Model) {
				setAIField(m, "Bindings", "ids=invoice_ids, begins=window.start, ends=window.end")
			},
			assert: func(t *testing.T, d ai.Draft) {
				if d.Scenario == nil || formatBindings(d.Scenario.Steps[0].Bindings) != "ids=invoice_ids, begins=window.start, ends=window.end" {
					t.Fatalf("Scenario range binding correction lost: %+v", d.Scenario)
				}
			},
		},
		{
			name: "add Scenario Check step", key: "a", draft: ai.Draft{Kind: ai.KindScenario, Scenario: &scenario},
			prepare: func(m *Model) {
				setAIField(m, "ID", "verify")
				setAIField(m, "Ref", "invoice-check")
			},
			assert: func(t *testing.T, d ai.Draft) {
				if d.Scenario == nil || len(d.Scenario.Steps) != 2 || d.Scenario.Steps[1].Ref != "invoice-check" || d.Scenario.Steps[1].Kind != "check" {
					t.Fatalf("Scenario Check step lost: %+v", d.Scenario)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proposal := ai.Proposal{ID: "proposal-1", Digest: "digest-1", Draft: tc.draft}
			service := &aiUIFixture{fixtureService: &fixtureService{state: detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true}}}, page: AIPage{Proposals: []ai.Proposal{proposal}}}
			m := New(service, "checks")
			m.Update(m.Init()())
			m.aiPage, m.aiProposalID, m.screen = service.page, proposal.ID, aiDetail
			m.rebuild()
			if tc.rowKind != "" {
				found := false
				for i, row := range m.rows {
					if row.kind == tc.rowKind {
						m.index, found = i, true
						break
					}
				}
				if !found {
					t.Fatalf("expected selectable %s row", tc.rowKind)
				}
			}
			press(m, tc.key)
			if m.form == nil {
				t.Fatal("nested typed editor did not open")
			}
			tc.prepare(m)
			cmd := press(m, "ctrl+s")
			if cmd == nil {
				t.Fatalf("nested review not dispatched: %s", m.feedback)
			}
			m.Update(cmd())
			if service.reviewed.Type != "draft" || service.reviewed.Draft == nil {
				t.Fatalf("nested review lost: %+v", service.reviewed)
			}
			tc.assert(t, service.reviewed.Draft.Draft)
		})
	}
}

func TestNativeAICaseSemanticReviewAndReadOnlyConfirmation(t *testing.T) {
	invoices, err := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("7"))
	if err != nil {
		t.Fatal(err)
	}
	proposal := ai.Proposal{ID: "case-draft", Digest: "digest-1", Draft: ai.Draft{Kind: ai.KindCase, Case: &ai.CaseEdit{CaseID: "case-1", ExpectedVersion: 3, Title: "Invoice mismatch", Context: map[string]detective.InputValue{"invoice_ids": {Set: &invoices}}, Findings: []detective.Finding{{ID: "missing-payment", Text: "Payment missing", Status: "confirmed", EvidenceRefs: []string{"evidence-1"}}}, Status: "closed", ClosureReason: "No payment"}}}
	service := &aiUIFixture{fixtureService: &fixtureService{state: detective.State{Cases: []detective.Case{{ID: "case-1", Version: 3}}, Capabilities: detective.Capabilities{CanRead: true, CanWrite: true}}}, page: AIPage{Proposals: []ai.Proposal{proposal}}}
	m := New(service, "detective")
	m.Update(m.Init()())
	m.Update(tea.WindowSizeMsg{Width: 48, Height: 12})
	m.aiPage, m.aiProposalID, m.screen = service.page, proposal.ID, aiDetail
	m.rebuild()
	if len(m.rows) < 5 {
		t.Fatalf("Case semantics not in detail: %+v", m.rows)
	}
	press(m, "c")
	if m.form != nil || !strings.Contains(m.feedback, "Review the Case") {
		t.Fatal("unreviewed semantic Case edit reached confirmation")
	}
	press(m, "r")
	if m.form == nil || m.form.kind != "ai-draft-review" {
		t.Fatal("typed Case review unavailable")
	}
	setAIField(m, "Context 1 value", "1, 2, 7")
	setAIField(m, "Finding 1 text", "Payment and CRM records disagree")
	setAIField(m, "Closure reason", "Confirmed reconciliation gap")
	cmd := press(m, "ctrl+s")
	if cmd == nil {
		t.Fatalf("semantic Case review rejected: %s", m.feedback)
	}
	_, refresh := m.Update(cmd())
	if refresh != nil {
		m.Update(refresh())
	}
	if service.reviewed.Draft == nil || service.reviewed.Draft.Draft.Case == nil {
		t.Fatalf("reviewed Case missing: %+v", service.reviewed)
	}
	caseEdit := service.reviewed.Draft.Draft.Case
	if len(caseEdit.Context["invoice_ids"].Set.Values) != 3 || caseEdit.Findings[0].Text != "Payment and CRM records disagree" || caseEdit.Status != "closed" || caseEdit.ClosureReason != "Confirmed reconciliation gap" {
		t.Fatalf("semantic correction lost: %+v", caseEdit)
	}
	derived := ai.Proposal{ID: "case-reviewed", ParentProposalID: proposal.ID, Digest: "digest-2", Draft: service.reviewed.Draft.Draft}
	m.aiPage, m.aiProposalID, m.screen = AIPage{Proposals: []ai.Proposal{derived}}, derived.ID, aiDetail
	m.rebuild()
	press(m, "c")
	if m.form == nil || m.form.kind != "ai-confirm" {
		t.Fatal("reviewed Case did not reach confirmation")
	}
	for _, label := range []string{"Status", "Closure reason", "Context 1 value", "Finding 1 text", "Finding 1 evidence refs"} {
		found := false
		for i := range m.form.fields {
			if m.form.fields[i].label == label && m.form.fields[i].readOnly {
				m.form.index = i
				before := m.form.fields[i].value
				press(m, "x")
				press(m, "backspace")
				if m.form.fields[i].value != before {
					t.Fatalf("confirmation changed reviewed %s", label)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("confirmation omitted %s", label)
		}
	}
	setAIField(m, "Confirmation", "CONFIRM")
	if cmd := press(m, "ctrl+s"); cmd == nil {
		t.Fatalf("reviewed Case confirmation not dispatched: %s", m.feedback)
	} else {
		m.Update(cmd())
	}
	if service.confirmed.ProposalID != derived.ID || service.confirmed.Digest != derived.Digest {
		t.Fatalf("wrong Case confirmed: %+v", service.confirmed)
	}
}

func TestNativeAIRunConfirmationShowsTypedCohort(t *testing.T) {
	invoices, err := apicontract.NewTypedValueSet(investigation.NewIntegerValue("1"), investigation.NewIntegerValue("7"))
	if err != nil {
		t.Fatal(err)
	}
	proposal := ai.Proposal{ID: "run-reviewed", Digest: "digest-1", Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: "invoice-check", CaseID: "case-1", Environment: "qa", Inputs: map[string]detective.InputValue{"invoice_ids": {Set: &invoices}}}}}
	service := &aiUIFixture{fixtureService: &fixtureService{state: detective.State{Capabilities: detective.Capabilities{CanRead: true, CanWrite: true, CanRun: true}}}, page: AIPage{Proposals: []ai.Proposal{proposal}}}
	m := New(service, "checks")
	m.Update(m.Init()())
	m.aiPage, m.aiProposalID, m.screen = service.page, proposal.ID, aiDetail
	m.rebuild()
	press(m, "c")
	if m.form == nil || m.form.kind != "ai-confirm" {
		t.Fatal("run confirmation missing")
	}
	for _, item := range m.form.fields {
		if item.label == "Input invoice_ids" && item.readOnly && strings.Contains(item.value, "7") {
			return
		}
	}
	t.Fatalf("run confirmation omitted typed cohort: %+v", m.form.fields)
}
