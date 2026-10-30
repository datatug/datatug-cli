package detective

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	knowledge "github.com/datatug/datatug-core/pkg/detectiveknowledge"
)

func TestLocalKnowledgeReviewHandoffRestartAndReplay(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	item := Case{ID: "case-1", Title: "Invoice issue", Evidence: []EvidenceRef{{ID: "source-note", Kind: "external", Ref: "reviewed invoice sample"}}, Findings: []Finding{{ID: "customer-absent", Text: "CRM customer missing", Status: "confirmed", EvidenceRefs: []string{"source-note"}}}}
	if _, err := s.SaveCase(ctx, item, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	draft := knowledge.Proposal{ID: "missing-customer-rule", CaseID: "case-1", Kind: "invariant", Title: "Invoice customer exists", Statement: "Each invoice customer must exist in CRM.", EvidenceRefs: []string{"source-note"}, FindingRefs: []string{"customer-absent"}, ClaimStatus: "confirmed"}
	request := knowledge.Action{Storage: "local", Project: "p", OperationID: "knowledge-draft", Type: "save-draft", Proposal: &draft}
	first, err := s.KnowledgeAction(ctx, request, "alice")
	if err != nil || first.Proposal == nil || first.Proposal.Version != 1 {
		t.Fatalf("save confirmed draft: %+v, %v", first, err)
	}
	if _, err := s.KnowledgeAction(ctx, request, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-actor replay: %v", err)
	}
	changed := request
	altered := draft
	altered.Statement = "changed claim"
	changed.Proposal = &altered
	if _, err := s.KnowledgeAction(ctx, changed, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("payload-changed replay: %v", err)
	}
	other, err := NewLocalStore("p", s.projectDir, filepath.Dir(filepath.Dir(s.privateDir)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalStore("other-project", s.projectDir, filepath.Dir(filepath.Dir(s.privateDir))); !errors.Is(err, ErrConflict) {
		t.Fatalf("project alias shared private knowledge: %v", err)
	}
	page, err := other.KnowledgePage(ctx, "")
	if err != nil || len(page.Proposals) != 1 || page.Proposals[0].ID != draft.ID {
		t.Fatalf("cold knowledge page: %+v, %v", page, err)
	}
	replayed, err := other.KnowledgeAction(ctx, request, "alice")
	if err != nil || replayed.Receipt != first.Receipt || replayed.Proposal.Version != first.Proposal.Version {
		t.Fatalf("cold replay: %+v, %v", replayed, err)
	}
	bad := knowledge.Action{Storage: "local", Project: "p", OperationID: "unsupported-evidence", Type: "save-draft", Proposal: &knowledge.Proposal{ID: "foreign", CaseID: "case-1", Kind: "invariant", Title: "Foreign", Statement: "Unsupported", EvidenceRefs: []string{"foreign-note"}}}
	if _, err := other.KnowledgeAction(ctx, bad, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign evidence accepted: %v", err)
	}
	unsupported := draft
	unsupported.ID = "unconfirmed"
	unsupported.FindingRefs = nil
	if _, err := other.KnowledgeAction(ctx, knowledge.Action{Storage: "local", Project: "p", OperationID: "unconfirmed-draft", Type: "save-draft", Proposal: &unsupported}, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unconfirmed evidence accepted: %v", err)
	}
	review := knowledge.Action{Storage: "local", Project: "p", OperationID: "knowledge-review", Type: "review", ProposalID: draft.ID, IfMatch: 1, ReviewNote: "Compared to confirmed Case finding"}
	reviewed, err := other.KnowledgeAction(ctx, review, "alice")
	if err != nil || reviewed.Proposal == nil || reviewed.Proposal.Status != "reviewed" || reviewed.Proposal.Version != 2 {
		t.Fatalf("review: %+v, %v", reviewed, err)
	}
	if _, err := other.KnowledgeAction(ctx, knowledge.Action{Storage: "local", Project: "p", OperationID: "stale-review", Type: "review", ProposalID: draft.ID, IfMatch: 1}, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale review accepted: %v", err)
	}
	target := &knowledge.Target{System: "meaninggraph", Repository: "datatug/meaninggraph", ResourceID: "invoice.customer", ExpectedRevision: "mg-r17"}
	export := knowledge.Action{Storage: "local", Project: "p", OperationID: "knowledge-export", Type: "export", ProposalID: draft.ID, IfMatch: 2, Target: target}
	handoff, err := other.KnowledgeAction(ctx, export, "alice")
	if err != nil || handoff.Export == nil || handoff.Export.Target.ExpectedRevision != "mg-r17" || handoff.Export.ProposalVersion != 2 || handoff.Proposal.Version != 3 || len(handoff.Proposal.Handoffs) != 1 {
		t.Fatalf("target review handoff: %+v, %v", handoff, err)
	}
	page, err = s.KnowledgePage(ctx, "")
	if err != nil || len(page.Proposals) != 1 || len(page.Proposals[0].Handoffs) != 1 {
		t.Fatalf("durable handoff: %+v, %v", page, err)
	}
	changedTarget := export
	changedTarget.Target = &knowledge.Target{System: "modelspec", Repository: "datatug/modelspec", ResourceID: "invoice", ExpectedRevision: "ms-r8"}
	if _, err := s.KnowledgeAction(ctx, changedTarget, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed-target replay accepted: %v", err)
	}
}

func TestLocalKnowledgeInterruptedAuditReplaysAcceptedMutation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Investigation", Evidence: []EvidenceRef{{ID: "source-note", Kind: "external", Ref: "reviewed source"}}}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	s.audit = failingAudit{}
	request := knowledge.Action{Storage: "local", Project: "p", OperationID: "interrupted-draft", Type: "save-draft", Proposal: &knowledge.Proposal{ID: "invoice-rule", CaseID: "case-1", Kind: "invariant", Title: "Invoice rule", Statement: "Invoice customer exists.", EvidenceRefs: []string{"source-note"}}}
	if _, err := s.KnowledgeAction(ctx, request, "alice"); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("audit failure must report uncertainty: %v", err)
	}
	restarted, err := NewLocalStore("p", s.projectDir, filepath.Dir(filepath.Dir(s.privateDir)))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.KnowledgeAction(ctx, request, "alice")
	if err != nil || recovered.Proposal == nil || recovered.Proposal.Version != 1 {
		t.Fatalf("accepted mutation recovery: %+v, %v", recovered, err)
	}
	wasRecorded, err := restarted.auditRecorded(AuditEvent{ID: request.OperationID, Action: "knowledge:save-draft", Project: "p", Actor: "alice"})
	if err != nil || !wasRecorded {
		t.Fatalf("recovered audit missing: %v, %v", wasRecorded, err)
	}
	replayed, err := restarted.KnowledgeAction(ctx, request, "alice")
	if err != nil || replayed.Receipt != recovered.Receipt {
		t.Fatalf("completed replay: %+v, %v", replayed, err)
	}
}

func TestLocalKnowledgePageIsBoundedAndCursorStable(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Investigate", Evidence: []EvidenceRef{{ID: "source-note", Kind: "external", Ref: "reviewed source"}}}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	for i := range 51 {
		id := fmt.Sprintf("proposal-%02d", i)
		proposal := knowledge.Proposal{ID: id, CaseID: "case-1", Kind: "invariant", Title: id, Statement: "Private hypothesis", EvidenceRefs: []string{"source-note"}}
		if _, err := s.KnowledgeAction(ctx, knowledge.Action{Storage: "local", Project: "p", OperationID: "save-" + id, Type: "save-draft", Proposal: &proposal}, "alice"); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	first, err := s.KnowledgePage(ctx, "")
	if err != nil || len(first.Proposals) != 50 || !first.HasMore || first.NextCursor != "proposal-49" {
		t.Fatalf("first page: %d %v %+v", len(first.Proposals), err, first)
	}
	second, err := s.KnowledgePage(ctx, first.NextCursor)
	if err != nil || len(second.Proposals) != 1 || second.Proposals[0].ID != "proposal-50" || second.HasMore {
		t.Fatalf("second page: %+v, %v", second, err)
	}
}
