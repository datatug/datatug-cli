package detectiveui

import (
	"context"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/investigation"
	"strings"
	"testing"
	"time"
)

type nonAIService struct {
	fixtureService
}

type mockAIService struct {
	fixtureService
	proposalsErr error
	generateErr  error
	reviewErr    error
	confirmErr   error
	page         AIPage
}

func (m *mockAIService) AIProposals(_ context.Context, _ string) (AIPage, error) {
	return m.page, m.proposalsErr
}

func (m *mockAIService) GenerateAI(_ context.Context, _ AIGenerate) (ai.Response, error) {
	return ai.Response{Proposals: m.page.Proposals}, m.generateErr
}

func (m *mockAIService) ReviewAI(_ context.Context, _ AIReview) (ai.Proposal, error) {
	if len(m.page.Proposals) > 0 {
		return m.page.Proposals[0], m.reviewErr
	}
	return ai.Proposal{}, m.reviewErr
}

func (m *mockAIService) ConfirmAI(_ context.Context, _ ai.ConfirmRequest) (ai.Receipt, error) {
	return ai.Receipt{OperationID: "op-1"}, m.confirmErr
}

func TestCoverage100_AIModel_ServiceAndPaging(t *testing.T) {
	t.Parallel()

	// 1. aiService error when service does not implement AIService (lines 18-19)
	mNonAI := New(&nonAIService{}, "checks")
	if _, err := mNonAI.aiService(); err == nil || !strings.Contains(err.Error(), "does not expose Detective AI") {
		t.Fatalf("expected non-AI service error: %v", err)
	}

	// 2. loadAI error when aiService fails (lines 24-26)
	cmdNil := mNonAI.loadAI(false)
	if cmdNil != nil {
		t.Fatalf("expected nil cmd when aiService fails")
	}
	if !strings.Contains(mNonAI.feedback, "does not expose Detective AI") {
		t.Fatalf("expected feedback set to error: %s", mNonAI.feedback)
	}

	// 3. loadAI with older=true (lines 29-31)
	aiSvc := &mockAIService{
		page: AIPage{
			Proposals:  []ai.Proposal{{ID: "p1"}},
			NextCursor: "cursor-123",
			HasMore:    true,
		},
	}
	mAI := New(aiSvc, "checks")
	mAI.aiPage = aiSvc.page
	cmdOlder := mAI.loadAI(true)
	if cmdOlder == nil {
		t.Fatalf("expected command from loadAI(true)")
	}
	msg := cmdOlder()
	pageMsg, ok := msg.(aiPageMsg)
	if !ok || !pageMsg.older {
		t.Fatalf("expected older aiPageMsg")
	}

	// 4. defaultAICaseID matching case ID (lines 48-50)
	mAI.state = detective.State{
		Cases: []detective.Case{
			{ID: "case-target"},
			{ID: "case-other"},
		},
	}
	mAI.id = "case-target"
	if got := mAI.defaultAICaseID(); got != "case-target" {
		t.Fatalf("expected case-target, got %s", got)
	}

	// 5. currentAIProposal returns nil when not found (lines 62-63)
	mAI.aiProposalID = "non-existent"
	if got := mAI.currentAIProposal(); got != nil {
		t.Fatalf("expected nil proposal, got %+v", got)
	}

	// 6. rebuildAI returns early when current proposal is nil (lines 67-69)
	mAI.rows = nil
	mAI.rebuildAI()
	if len(mAI.rows) != 0 {
		t.Fatalf("expected no rows when proposal is nil")
	}
}

func TestCoverage100_AIModel_RebuildAI_Variations(t *testing.T) {
	t.Parallel()

	// 1. Sources citation (lines 88-89), Check step with kind "check" (lines 101-102),
	// and Explain proposal (lines 152-154)
	pSourcesAndCheck := ai.Proposal{
		ID:        "prop-check",
		ExpiresAt: time.Now(),
		Draft: ai.Draft{
			Kind:  ai.KindCheck,
			Title: "Check with Subcheck Step",
			Sources: []ai.Provenance{
				{SourceID: "doc-1", Start: 0, End: 10, Confidence: 0.95, Quote: "Source Quote"},
			},
			Check: &detective.Check{
				ID:    "chk-1",
				Title: "Check One",
				Steps: []detective.Step{
					{
						ID:       "step-1",
						Kind:     "check",
						CheckID:  "sub-check-id",
						Bindings: []detective.Binding{{Input: "param", FromColumn: "col"}},
						Validators: []detective.Validator{
							{ID: "val-1", Kind: "non-null", Field: "col"},
						},
					},
				},
			},
		},
	}

	pExplain := ai.Proposal{
		ID: "prop-explain",
		Draft: ai.Draft{
			Kind:  ai.KindExplain,
			Title: "Explanation Proposal",
			Explanation: &ai.FailureExplanation{
				RunID:   "run-fail-1",
				Summary: "Query timed out after 30s",
				Basis:   "Logs show gateway timeout",
			},
		},
	}

	aiSvc := &mockAIService{
		page: AIPage{Proposals: []ai.Proposal{pSourcesAndCheck, pExplain}},
	}
	m := New(aiSvc, "checks")
	m.aiPage = aiSvc.page

	// Rebuild with pSourcesAndCheck
	m.aiProposalID = pSourcesAndCheck.ID
	m.rebuildAI()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows from rebuildAI with pSourcesAndCheck")
	}

	// Rebuild with pExplain
	m.rows = nil
	m.aiProposalID = pExplain.ID
	m.rebuildAI()
	if len(m.rows) == 0 {
		t.Fatalf("expected rows from rebuildAI with pExplain")
	}
}

func TestCoverage100_AIModel_aiValueShape_And_ConfirmationFields(t *testing.T) {
	t.Parallel()

	// 1. aiValueShape default fallback (lines 174-175)
	emptyVal := detective.InputValue{}
	typeName, cardinality := aiValueShape(emptyVal)
	if typeName != "string" || cardinality != "scalar" {
		t.Fatalf("expected string/scalar for empty InputValue, got %s/%s", typeName, cardinality)
	}

	// 2. aiConfirmationFields default branch for Query/Validator (lines 230-231)
	pQuery := ai.Proposal{
		ID: "prop-query",
		Draft: ai.Draft{
			Kind:  ai.KindQuery,
			Title: "Query Title",
		},
	}
	fields := aiConfirmationFields(pQuery)
	foundTitle := false
	for _, f := range fields {
		if f.label == "Reviewed title" && f.value == "Query Title" {
			foundTitle = true
			break
		}
	}
	if !foundTitle {
		t.Fatalf("expected Reviewed title field in aiConfirmationFields default branch")
	}
}

func TestCoverage100_AIModel_updateAIKey_Branches(t *testing.T) {
	t.Parallel()

	aiSvc := &mockAIService{
		page: AIPage{
			Proposals: []ai.Proposal{
				{
					ID:    "prop-explain",
					Draft: ai.Draft{Kind: ai.KindExplain, Title: "Explain"},
				},
				{
					ID:    "prop-check",
					Draft: ai.Draft{Kind: ai.KindCheck, Title: "Check"},
				},
			},
			HasMore: true,
		},
	}
	m := New(aiSvc, "checks")
	m.aiPage = aiSvc.page

	// 1. aiList screen:
	m.screen = aiList

	// key "l" with HasMore = true (lines 241-243)
	_, cmdL := m.updateAIKey("l")
	if cmdL == nil {
		t.Fatalf("expected cmd from 'l' when HasMore = true")
	}

	// key "l" with HasMore = false
	m.aiPage.HasMore = false
	_, cmdLNone := m.updateAIKey("l")
	if cmdLNone != nil {
		t.Fatalf("expected nil cmd when HasMore = false")
	}

	// key "enter" on aiList with rows > 0 (lines 245-251)
	m.rows = []row{{id: "prop-explain", label: "Proposal explain"}}
	m.index = 0
	m.updateAIKey("enter")
	if m.screen != aiDetail || m.aiProposalID != "prop-explain" {
		t.Fatalf("expected enter to transition to aiDetail: %v, %s", m.screen, m.aiProposalID)
	}

	// key "enter" on aiList with empty rows
	m.screen = aiList
	m.rows = nil
	m.updateAIKey("enter")
	if m.screen != aiList {
		t.Fatalf("expected aiList to remain when rows empty")
	}

	// unhandled key on aiList (line 253)
	m.updateAIKey("z")

	// 2. aiDetail screen with no current proposal (lines 256-258)
	m.screen = aiDetail
	m.aiProposalID = "non-existent"
	m.updateAIKey("r")

	// 3. aiDetail screen with KindExplain on "r" (lines 278-281)
	m.aiProposalID = "prop-explain"
	m.updateAIKey("r")
	if !strings.Contains(m.feedback, "Failure explanations are review evidence") {
		t.Fatalf("expected explain review feedback, got: %s", m.feedback)
	}

	// 4. aiDetail screen with "e" and empty rows (lines 318-320)
	m.rows = nil
	m.updateAIKey("e")

	// 5. aiDetail screen with unhandled key (line 386)
	m.updateAIKey("z")
}

func TestCoverage100_AIModel_submitAIForm_Errors(t *testing.T) {
	t.Parallel()

	// 1. aiService error in submitAIForm (lines 393-395)
	mNonAI := New(&nonAIService{}, "checks")
	mNonAI.form = &form{kind: "ai-generate", fields: []field{{label: "Prompt", value: "test"}}}
	_, handled, err := mNonAI.submitAIForm()
	if !handled || err == nil || !strings.Contains(err.Error(), "does not expose Detective AI") {
		t.Fatalf("expected aiService error: %v", err)
	}

	// 2. ai-generate prompt empty (lines 399-401)
	aiSvc := &mockAIService{}
	m := New(aiSvc, "checks")
	m.form = &form{kind: "ai-generate", fields: []field{{label: "Prompt", value: ""}}}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "prompt required" {
		t.Fatalf("expected prompt required error: %v", err)
	}

	// 3. ai-generate parseExpectedCounts error (lines 403-405)
	m.form.fields = []field{
		{label: "Prompt", value: "valid prompt"},
		{label: "Expected counts", value: "invalid=notanumber"},
	}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseExpectedCounts error")
	}

	// 4. ai-context-review errors (lines 414-446)
	// a) Kind != KindContext
	m.form = &form{
		kind: "ai-context-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindQuery},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "context proposal required" {
		t.Fatalf("expected context proposal required: %v", err)
	}

	// b) Case ID empty
	m.form.aiProposal.Draft.Kind = ai.KindContext
	m.form.fields = []field{{label: "Case ID", value: ""}}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "case ID required" {
		t.Fatalf("expected case ID required: %v", err)
	}

	// c) Invalid context parameter name
	m.form.aiProposal.Draft.Context = map[string]ai.ExtractedValue{"p1": {}}
	m.form.fields = []field{
		{label: "Case ID", value: "c1"},
		{label: "Context 1 name", value: "Invalid Name With Spaces!"},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "requires a valid name") {
		t.Fatalf("expected valid name error: %v", err)
	}

	// d) Duplicate context name
	m.form.aiProposal.Draft.Context = map[string]ai.ExtractedValue{"p1": {}, "p2": {}}
	m.form.fields = []field{
		{label: "Case ID", value: "c1"},
		{label: "Context 1 name", value: "same_name"},
		{label: "Context 1 type", value: "string"},
		{label: "Context 1 cardinality", value: "scalar"},
		{label: "Context 1 value", value: "v1"},
		{label: "Context 2 name", value: "same_name"},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "duplicate context name") {
		t.Fatalf("expected duplicate context name error: %v", err)
	}

	// e) ParseValue error in context review
	m.form.aiProposal.Draft.Context = map[string]ai.ExtractedValue{"p1": {}}
	m.form.fields = []field{
		{label: "Case ID", value: "c1"},
		{label: "Context 1 name", value: "p1"},
		{label: "Context 1 type", value: "integer"},
		{label: "Context 1 cardinality", value: "scalar"},
		{label: "Context 1 value", value: "not-an-int"},
	}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseValue error for invalid integer")
	}

	// f) Case not in catalog
	m.form.fields[4].value = "123"
	m.state.Cases = nil
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "not in the authorized catalog") {
		t.Fatalf("expected not in authorized catalog error: %v", err)
	}

	// 5. ai-draft-review: KindQuery
	// a) Query draft unavailable (lines 459-461)
	m.form = &form{
		kind: "ai-draft-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindQuery, Query: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "query draft unavailable" {
		t.Fatalf("expected query draft unavailable: %v", err)
	}

	// b) parseInputs error on Query (lines 469-471)
	m.form.aiProposal.Draft.Query = &ai.QueryDraft{}
	m.form.fields = []field{{label: "Inputs", value: "invalid:input:format:extra:extra:extra"}}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseInputs error")
	}

	// 6. ai-draft-review: KindCheck
	// a) Check draft unavailable (lines 476-478)
	m.form = &form{
		kind: "ai-draft-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindCheck, Check: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "check draft unavailable" {
		t.Fatalf("expected check draft unavailable: %v", err)
	}

	// b) parseInputs error on Check (lines 483-485)
	m.form.aiProposal.Draft.Check = &detective.Check{}
	m.form.fields = []field{{label: "Inputs", value: "bad:input:syntax:too:many:colons"}}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseInputs error on Check")
	}

	// 7. ai-draft-review: KindValidator
	// a) Validator draft unavailable (lines 489-491)
	m.form = &form{
		kind: "ai-draft-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindValidator, Validator: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "validator draft unavailable" {
		t.Fatalf("expected validator draft unavailable: %v", err)
	}

	// b) parseValidator error on Validator (lines 496-498)
	m.form.aiProposal.Draft.Validator = &detective.PublishedValidator{}
	m.form.fields = []field{{label: "Kind", value: "unknown-kind"}}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseValidator error on invalid kind")
	}

	// 8. ai-draft-review: KindScenario
	// a) Scenario draft unavailable (lines 502-504)
	m.form = &form{
		kind: "ai-draft-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindScenario, Scenario: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "scenario draft unavailable" {
		t.Fatalf("expected scenario draft unavailable: %v", err)
	}

	// b) parseInputs error on Scenario (lines 509-511)
	m.form.aiProposal.Draft.Scenario = &detective.Scenario{}
	m.form.fields = []field{{label: "Inputs", value: "invalid:input:fmt:extra:parts"}}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseInputs error on Scenario")
	}

	// 9. ai-draft-review: KindCase
	// a) Case draft unavailable (lines 515-517)
	m.form = &form{
		kind: "ai-draft-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindCase, Case: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "case edit unavailable" {
		t.Fatalf("expected case edit unavailable: %v", err)
	}

	// b) Context name empty -> continue (lines 528-529)
	intVal := investigation.NewIntegerValue("1")
	m.form.aiProposal.Draft.Case = &ai.CaseEdit{
		Context: map[string]detective.InputValue{"c1": {Scalar: &intVal}},
	}
	m.form.fields = []field{
		{label: "Context 1 name", value: ""},
		{label: "Findings action", value: "keep"},
	}
	_, _, err = m.submitAIForm()
	if err != nil {
		t.Fatalf("expected success with skipped empty context name, got: %v", err)
	}

	// c) Context invalid name (lines 530-532)
	m.form.fields = []field{
		{label: "Context 1 name", value: "Bad Name!"},
		{label: "Findings action", value: "keep"},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "requires a valid name") {
		t.Fatalf("expected valid name error: %v", err)
	}

	// d) Context duplicate name (lines 533-535)
	m.form.aiProposal.Draft.Case.Context = map[string]detective.InputValue{
		"c1": {Scalar: &intVal},
		"c2": {Scalar: &intVal},
	}
	m.form.fields = []field{
		{label: "Context 1 name", value: "dup_name"},
		{label: "Context 1 type", value: "string"},
		{label: "Context 1 cardinality", value: "scalar"},
		{label: "Context 1 value", value: "v1"},
		{label: "Context 2 name", value: "dup_name"},
		{label: "Findings action", value: "keep"},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "duplicate context name") {
		t.Fatalf("expected duplicate context name error: %v", err)
	}

	// e) Context parseValue error (lines 537-539)
	m.form.fields = []field{
		{label: "Context 1 name", value: "c1"},
		{label: "Context 1 type", value: "integer"},
		{label: "Context 1 cardinality", value: "scalar"},
		{label: "Context 1 value", value: "not-int"},
		{label: "Context 2 name", value: "c2"},
		{label: "Context 2 type", value: "string"},
		{label: "Context 2 cardinality", value: "scalar"},
		{label: "Context 2 value", value: "ok"},
		{label: "Findings action", value: "keep"},
	}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseValue error")
	}

	// f) Finding id empty -> continue (lines 551-553)
	m.form.aiProposal.Draft.Case.Context = nil
	m.form.aiProposal.Draft.Case.Findings = []detective.Finding{{ID: "f1"}}
	m.form.fields = []field{
		{label: "Findings action", value: "replace"},
		{label: "Finding 1 ID", value: ""},
	}
	_, _, err = m.submitAIForm()
	if err != nil {
		t.Fatalf("expected success with empty finding ID skipped, got: %v", err)
	}

	// g) Finding invalid (lines 555-557)
	m.form.fields = []field{
		{label: "Findings action", value: "replace"},
		{label: "Finding 1 ID", value: "bad slug!"},
		{label: "Finding 1 status", value: "invalid-status"},
		{label: "Finding 1 text", value: ""},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "requires a valid ID, status and text") {
		t.Fatalf("expected invalid finding error: %v", err)
	}

	// h) Findings action invalid (lines 560-562)
	m.form.fields = []field{
		{label: "Findings action", value: "invalid-action"},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "findings action must be keep or replace") {
		t.Fatalf("expected invalid findings action error: %v", err)
	}

	// 10. ai-draft-review: KindRun
	// a) Run draft unavailable (lines 565-567)
	m.form = &form{
		kind: "ai-draft-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindRun, Run: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "run draft unavailable" {
		t.Fatalf("expected run draft unavailable: %v", err)
	}

	// b) validateEnvironment error (lines 572-574)
	m.form.aiProposal.Draft.Run = &ai.RunDraft{}
	m.form.fields = []field{
		{label: "Environment", value: ""},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "select a logical environment" {
		t.Fatalf("expected validateEnvironment error, got: %v", err)
	}

	// c) Check not in catalog (lines 576-578)
	m.form.fields = []field{
		{label: "Environment", value: "qa"},
		{label: "Check ID", value: "missing-check"},
	}
	m.state.Checks = nil
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "not in the authorized catalog") {
		t.Fatalf("expected check not in catalog error: %v", err)
	}

	// d) Run inputs: required input missing (lines 583-585)
	m.state.Checks = []detective.Check{
		{
			ID: "check-1",
			Inputs: []detective.Input{
				{Name: "req_param", Required: true, Type: investigation.ValueTypeString, Cardinality: "scalar"},
				{Name: "opt_param", Required: false, Type: investigation.ValueTypeString, Cardinality: "scalar"},
			},
		},
	}
	m.form.fields = []field{
		{label: "Environment", value: "qa"},
		{label: "Check ID", value: "check-1"},
		{label: "Input req_param", value: ""},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("expected required input error: %v", err)
	}

	// e) Run inputs: optional input empty -> continue (lines 586-587)
	// and parseValue error on input (lines 589-591)
	m.form.fields = []field{
		{label: "Environment", value: "qa"},
		{label: "Check ID", value: "check-1"},
		{label: "Input req_param", value: "present"},
		{label: "Input opt_param", value: ""},
	}
	_, _, err = m.submitAIForm()
	if err != nil {
		t.Fatalf("expected success with optional input empty, got: %v", err)
	}

	m.state.Checks[0].Inputs[0].Type = investigation.ValueTypeInteger
	m.form.fields[2].value = "not-integer"
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseValue error on invalid input integer")
	}

	// 11. ai-draft-review: default cannot be confirmed (lines 596-597)
	m.form = &form{
		kind: "ai-draft-review",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: "unsupported-kind"},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "this proposal cannot be confirmed") {
		t.Fatalf("expected cannot be confirmed error: %v", err)
	}

	// 12. ai-step-edit: Check draft nil (lines 609-611), parseStep err (lines 615-617),
	// and step index out of bounds (lines 620-622)
	m.form = &form{
		kind: "ai-step-edit",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindCheck, Check: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "check draft unavailable" {
		t.Fatalf("expected check draft unavailable: %v", err)
	}

	m.form.aiProposal.Draft.Check = &detective.Check{
		Steps: []detective.Step{{ID: "s1"}},
	}
	m.form.fields = []field{{label: "Kind", value: "invalid-kind"}}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseStep error")
	}

	m.form.fields = []field{{label: "Kind", value: "query"}, {label: "ID", value: "step-valid"}, {label: "Ref", value: "q1"}}
	m.form.stepIndex = 99 // out of bounds
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "step unavailable" {
		t.Fatalf("expected step unavailable error: %v", err)
	}

	// 13. ai-validator-edit: Check draft nil (lines 630-632), query step unavailable (lines 636-638),
	// parseValidator err (lines 642-644), validator index out of bounds (lines 647-649)
	m.form = &form{
		kind: "ai-validator-edit",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindCheck, Check: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "check draft unavailable" {
		t.Fatalf("expected check draft unavailable: %v", err)
	}

	m.form.aiProposal.Draft.Check = &detective.Check{
		Steps: []detective.Step{{ID: "s1", Kind: "not-query"}},
	}
	m.form.stepIndex = 0
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "query step unavailable" {
		t.Fatalf("expected query step unavailable: %v", err)
	}

	m.form.aiProposal.Draft.Check.Steps[0].Kind = "query"
	m.form.fields = []field{{label: "Kind", value: "invalid-validator-kind"}}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseValidator error")
	}

	m.form.fields = []field{{label: "ID", value: "v1"}, {label: "Kind", value: "non-null"}, {label: "Scope", value: "row"}, {label: "Field", value: "col"}}
	m.form.validatorIndex = 99 // out of bounds
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "validator unavailable" {
		t.Fatalf("expected validator unavailable error: %v", err)
	}

	// 14. ai-scenario-step-edit: Scenario draft nil (lines 657-659),
	// invalid step ID/kind/ref (lines 663-665), parseBindings err (lines 667-669),
	// scenario step index out of bounds (lines 673-675)
	m.form = &form{
		kind: "ai-scenario-step-edit",
		aiProposal: ai.Proposal{
			Draft: ai.Draft{Kind: ai.KindScenario, Scenario: nil},
		},
	}
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "scenario draft unavailable" {
		t.Fatalf("expected scenario draft unavailable: %v", err)
	}

	m.form.aiProposal.Draft.Scenario = &detective.Scenario{
		Steps: []detective.ScenarioStep{{ID: "step1"}},
	}
	m.form.fields = []field{
		{label: "ID", value: "invalid ID slug!"},
		{label: "Kind", value: "invalid-kind"},
		{label: "Ref", value: ""},
	}
	_, _, err = m.submitAIForm()
	if err == nil || !strings.Contains(err.Error(), "valid Scenario step ID, kind and ref required") {
		t.Fatalf("expected valid Scenario step error: %v", err)
	}

	m.form.fields = []field{
		{label: "ID", value: "step-valid"},
		{label: "Kind", value: "query"},
		{label: "Ref", value: "q1"},
		{label: "Bindings", value: "invalid:bindings:syntax:bad:colon:count"},
	}
	_, _, err = m.submitAIForm()
	if err == nil {
		t.Fatalf("expected parseBindings error")
	}

	m.form.fields[3].value = ""
	m.form.stepIndex = 99 // out of bounds
	_, _, err = m.submitAIForm()
	if err == nil || err.Error() != "scenario step unavailable" {
		t.Fatalf("expected scenario step unavailable error: %v", err)
	}

	// 15. submitAIForm unhandled kind (line 700)
	m.form = &form{kind: "ai-unknown"}
	_, handled, err = m.submitAIForm()
	if handled || err != nil {
		t.Fatalf("expected unhandled return false, nil: %v, %v", handled, err)
	}
}

func TestCoverage100_AIModel_parseExpectedCounts(t *testing.T) {
	t.Parallel()

	// 1. Empty raw -> nil, nil (lines 703-705)
	counts, err := parseExpectedCounts("   ")
	if err != nil || counts != nil {
		t.Fatalf("expected nil, nil for empty string, got: %v, %v", counts, err)
	}

	// 2. Missing '=' or invalid parameter name (lines 709-711)
	_, err = parseExpectedCounts("noequalsign")
	if err == nil || !strings.Contains(err.Error(), "expected counts require name=count") {
		t.Fatalf("expected name=count error: %v", err)
	}

	_, err = parseExpectedCounts("Invalid Name!=5")
	if err == nil || !strings.Contains(err.Error(), "expected counts require name=count") {
		t.Fatalf("expected name=count error: %v", err)
	}

	// 3. Count not a number or out of bounds (lines 713-715)
	_, err = parseExpectedCounts("valid_name=abc")
	if err == nil || !strings.Contains(err.Error(), "must be 1..100") {
		t.Fatalf("expected 1..100 error: %v", err)
	}

	_, err = parseExpectedCounts("valid_name=0")
	if err == nil || !strings.Contains(err.Error(), "must be 1..100") {
		t.Fatalf("expected 1..100 error: %v", err)
	}

	_, err = parseExpectedCounts("valid_name=101")
	if err == nil || !strings.Contains(err.Error(), "must be 1..100") {
		t.Fatalf("expected 1..100 error: %v", err)
	}

	// 4. Valid count
	counts, err = parseExpectedCounts("valid_name=42")
	if err != nil || counts["valid_name"] != 42 {
		t.Fatalf("expected valid count 42: %v, %v", counts, err)
	}
}
