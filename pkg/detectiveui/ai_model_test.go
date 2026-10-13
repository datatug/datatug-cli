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
	s.page.Proposals[0] = ai.Proposal{ID: "reviewed-1", Digest: "digest-2", Draft: ai.Draft{Kind: ai.KindCase, Title: "Reviewed Case", Case: &ai.CaseEdit{CaseID: "case-1", ExpectedVersion: 2}}}
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
	m.form.fields[0].value = "CONFIRM"
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
