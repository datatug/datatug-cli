package detective

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	knowledge "github.com/datatug/datatug-core/pkg/detectiveknowledge"
)

// Not parallel: tests all branch paths of knowledge operations and proposals.
func TestCoverage100_Knowledge_AllBranches(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	kpDir := s.privateItemDir("knowledge-proposals")
	koDir := s.privateItemDir("knowledge-operations")
	_ = os.MkdirAll(kpDir, 0700)
	_ = os.MkdirAll(koDir, 0700)

	// 1. KnowledgePage validation & errors
	if _, err := s.KnowledgePage(ctx, "invalid slug!"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad cursor slug")
	}
	// readItems error (corrupt file in knowledge-proposals)
	if err := os.WriteFile(filepath.Join(kpDir, "bad.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgePage(ctx, ""); err == nil {
		t.Fatalf("expected error from readItems with corrupt json")
	}
	_ = os.Remove(filepath.Join(kpDir, "bad.json"))

	// 2. KnowledgeAction input validation
	baseProposal := knowledge.Proposal{
		ID:           "prop-1",
		CaseID:       "case-1",
		Kind:         "invariant",
		Title:        "Valid Title",
		Statement:    "Valid Statement",
		EvidenceRefs: []string{"ev-1"},
		ClaimStatus:  "hypothesis",
	}
	validAction := knowledge.Action{
		Project:     s.projectID,
		Storage:     "local",
		OperationID: "op-k-1",
		Type:        "save-draft",
		Proposal:    &baseProposal,
	}

	// invalid actions
	badActions := []knowledge.Action{
		func() knowledge.Action { a := validAction; a.Project = "other"; return a }(),
		func() knowledge.Action { a := validAction; a.Storage = "cloud"; return a }(),
		func() knowledge.Action { a := validAction; a.SpaceID = "non-empty"; return a }(),
		func() knowledge.Action { a := validAction; a.OperationID = "invalid slug!"; return a }(),
		func() knowledge.Action { a := validAction; a.Type = "unknown"; return a }(),
	}
	for i, bad := range badActions {
		if _, err := s.KnowledgeAction(ctx, bad, "alice"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("badActions[%d]: expected ErrInvalid, got %v", i, err)
		}
	}
	if _, err := s.KnowledgeAction(ctx, validAction, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty actor")
	}

	// BindOperation failure (e.g. invalid proposal ID when proposal is nil)
	badActionNoID := knowledge.Action{
		Project:     s.projectID,
		Storage:     "local",
		OperationID: "op-k-1",
		Type:        "review",
		ProposalID:  "", // empty proposal ID
	}
	if _, err := s.KnowledgeAction(ctx, badActionNoID, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for missing proposal ID")
	}

	// reconcilePendingActions error
	actDir := s.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	if err := os.WriteFile(filepath.Join(actDir, "bad-action.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeAction(ctx, validAction, "alice"); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "bad-action.json"))

	// safeReadFile error on operationID (directory in place of file)
	_ = os.MkdirAll(filepath.Join(koDir, "dir-op.json"), 0700)
	dirAction := validAction
	dirAction.OperationID = "dir-op"
	if _, err := s.KnowledgeAction(ctx, dirAction, "alice"); err == nil {
		t.Fatalf("expected error reading directory operation")
	}
	_ = os.RemoveAll(filepath.Join(koDir, "dir-op.json"))

	// replay with knowledgeFacts error
	// Create an operation record that references a case that does not exist
	binding, err := core.BindOperation(s.projectID, "alice", "knowledge:save-draft", "prop-1", validAction)
	if err != nil {
		t.Fatal(err)
	}
	rcpt, err := core.NewOperationReceipt("op-replay-err", binding, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	replayAction := validAction
	replayAction.OperationID = "op-replay-err"
	bndReplayErr, _ := core.BindOperation(s.projectID, "alice", "knowledge:save-draft", "prop-1", replayAction)
	rcptReplayErr, _ := core.NewOperationReceipt("op-replay-err", bndReplayErr, 1, time.Now().UTC())
	replayFactsErrOp := knowledgeOperation{
		Receipt:  rcptReplayErr,
		Response: knowledge.Response{Proposal: &knowledge.Proposal{ID: "prop-1", CaseID: "nonexistent-case", Title: "T"}},
		Audit:    AuditEvent{ID: "op-replay-err", Project: s.projectID, Actor: "alice", Action: "knowledge:save-draft"},
		Done:     true,
	}
	if err := writeItem(koDir, "op-replay-err", replayFactsErrOp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeAction(ctx, replayAction, "alice"); err == nil {
		t.Fatalf("expected error from knowledgeFacts on replay")
	}
	_ = os.Remove(filepath.Join(koDir, "op-replay-err.json"))

	// Setup a real case for valid knowledge proposal actions
	c, err := s.SaveCase(ctx, Case{
		ID:    "case-1",
		Title: "Case One",
		Evidence: []EvidenceRef{
			{ID: "ev-1", Kind: "external", Ref: "ref-1"},
		},
		Findings: []Finding{
			{ID: "f-1", Text: "finding 1", Status: "confirmed", EvidenceRefs: []string{"ev-1"}},
		},
	}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}

	// BindOperation error with empty project ID
	emptyProjStore := &LocalStore{projectID: ""}
	if _, err := emptyProjStore.KnowledgeAction(ctx, knowledge.Action{
		Project:     "",
		Storage:     "local",
		OperationID: "op-empty-p",
		Type:        "save-draft",
		Proposal:    &baseProposal,
	}, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty project ID: %v", err)
	}

	// corrupt proposal file on disk in KnowledgeAction
	if err := os.WriteFile(filepath.Join(kpDir, "prop-corrupt.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	corruptProp := baseProposal
	corruptProp.ID = "prop-corrupt"
	corruptAction := validAction
	corruptAction.Proposal = &corruptProp
	if _, err := s.KnowledgeAction(ctx, corruptAction, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for corrupt proposal file on disk: %v", err)
	}
	_ = os.Remove(filepath.Join(kpDir, "prop-corrupt.json"))

	// safeReadFile error on proposal (directory in place of proposal file)
	_ = os.MkdirAll(filepath.Join(kpDir, "prop-dir.json"), 0700)
	dirProp := baseProposal
	dirProp.ID = "prop-dir"
	propDirAction := validAction
	propDirAction.Proposal = &dirProp
	if _, err := s.KnowledgeAction(ctx, propDirAction, "alice"); err == nil {
		t.Fatalf("expected error reading directory proposal")
	}
	_ = os.RemoveAll(filepath.Join(kpDir, "prop-dir.json"))

	// replay with ValidateReferences error
	// Proposal references finding that doesn't exist
	replayValErrOp := knowledgeOperation{
		Receipt: rcpt,
		Response: knowledge.Response{Proposal: &knowledge.Proposal{
			ID:          "prop-1",
			CaseID:      c.ID,
			Kind:        "invariant",
			Title:       "T",
			Statement:   "S",
			ClaimStatus: "confirmed",
			FindingRefs: []string{"nonexistent-finding"},
		}},
		Audit: AuditEvent{ID: "op-val-err", Project: s.projectID, Actor: "alice", Action: "knowledge:save-draft"},
		Done:  true,
	}
	valErrAction := validAction
	valErrAction.OperationID = "op-val-err"
	bndValErr, _ := core.BindOperation(s.projectID, "alice", "knowledge:save-draft", "prop-1", valErrAction)
	rcptValErr, _ := core.NewOperationReceipt("op-val-err", bndValErr, 1, time.Now().UTC())
	replayValErrOp.Receipt = rcptValErr
	if err := writeItem(koDir, "op-val-err", replayValErrOp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeAction(ctx, valErrAction, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from ValidateReferences on replay: %v", err)
	}
	_ = os.Remove(filepath.Join(koDir, "op-val-err.json"))

	// replay with !operation.Done calling finishKnowledgeOperation
	resumeProp := knowledge.Proposal{
		ID:           "prop-resume",
		CaseID:       c.ID,
		Kind:         "invariant",
		Title:        "T",
		Statement:    "S",
		ClaimStatus:  "confirmed",
		EvidenceRefs: []string{"ev-1"},
		FindingRefs:  []string{"f-1"},
	}
	finishAction := validAction
	finishAction.OperationID = "op-finish-err"
	finishAction.Proposal = &resumeProp
	bndFinish, _ := core.BindOperation(s.projectID, "alice", "knowledge:save-draft", "prop-resume", finishAction)
	rcptFinish, _ := core.NewOperationReceipt("op-finish-err", bndFinish, 1, time.Now().UTC())
	replayFinishOp := knowledgeOperation{
		Receipt:  rcptFinish,
		Response: knowledge.Response{Proposal: &resumeProp},
		Previous: nil,
		Audit:    AuditEvent{ID: "op-finish-err", Project: s.projectID, Actor: "alice", Action: "knowledge:save-draft"},
		Done:     false,
	}
	// Store current proposal on disk that conflicts with Previous (current != nil and current != Previous)
	if err := writeItem(kpDir, "prop-resume", knowledge.Proposal{ID: "prop-resume", Version: 99}); err != nil {
		t.Fatal(err)
	}
	if err := writeItem(koDir, "op-finish-err", replayFinishOp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeAction(ctx, finishAction, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict from finishKnowledgeOperation on replay: %v", err)
	}
	_ = os.Remove(filepath.Join(kpDir, "prop-resume.json"))
	_ = os.Remove(filepath.Join(koDir, "op-finish-err.json"))

	// NewOperationReceipt error with next.Version overflow (< 0)
	storedMaxVerProp := baseProposal
	storedMaxVerProp.ID = "prop-maxver"
	storedMaxVerProp.Version = 9223372036854775807
	storedMaxVerProp.Status = "draft"
	if err := writeItem(kpDir, "prop-maxver", storedMaxVerProp); err != nil {
		t.Fatal(err)
	}
	draftMaxProp := baseProposal
	draftMaxProp.ID = "prop-maxver"
	draftMaxProp.Version = 0
	draftMaxProp.Status = ""
	maxAction := validAction
	maxAction.OperationID = "op-maxver"
	maxAction.Proposal = &draftMaxProp
	maxAction.IfMatch = 9223372036854775807
	if _, err := s.KnowledgeAction(ctx, maxAction, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from NewOperationReceipt with overflow version: %v", err)
	}
	_ = os.Remove(filepath.Join(kpDir, "prop-maxver.json"))

	// finishKnowledgeOperation: writeItem error on knowledge-proposals
	finishWriteErrOp := knowledgeOperation{
		Receipt: rcpt,
		Response: knowledge.Response{Proposal: &knowledge.Proposal{
			ID:           "prop-finish-err",
			CaseID:       c.ID,
			Kind:         "invariant",
			Title:        "T",
			Statement:    "S",
			ClaimStatus:  "confirmed",
			EvidenceRefs: []string{"ev-1"},
			FindingRefs:  []string{"f-1"},
		}},
		Audit: AuditEvent{ID: "op-finish-werr", Project: s.projectID, Actor: "alice", Action: "knowledge:save-draft"},
	}
	if err := os.Chmod(kpDir, 0500); err != nil {
		t.Fatal(err)
	}
	if err := s.finishKnowledgeOperation(ctx, "op-finish-werr", &finishWriteErrOp); err == nil {
		t.Fatalf("expected error from writeItem to read-only knowledge-proposals in finishKnowledgeOperation")
	}
	_ = os.Chmod(kpDir, 0700)

	// finishKnowledgeOperation & auditRecorded: os.Open error on unreadable audit file
	audPath := filepath.Join(s.privateDir, "audit.jsonl")
	_ = os.WriteFile(audPath, []byte("{}"), 0600)
	_ = os.Chmod(audPath, 0000)
	finishAuditErrOp := knowledgeOperation{
		Receipt: rcpt,
		Response: knowledge.Response{Proposal: &knowledge.Proposal{
			ID:           "prop-aud-err",
			CaseID:       c.ID,
			Kind:         "invariant",
			Title:        "T",
			Statement:    "S",
			ClaimStatus:  "confirmed",
			EvidenceRefs: []string{"ev-1"},
			FindingRefs:  []string{"f-1"},
		}},
		Audit: AuditEvent{ID: "op-aud-err", Project: s.projectID, Actor: "alice", Action: "knowledge:save-draft"},
	}
	_ = writeItem(kpDir, "prop-aud-err", *finishAuditErrOp.Response.Proposal)
	if err := s.finishKnowledgeOperation(ctx, "op-aud-err", &finishAuditErrOp); err == nil {
		t.Fatalf("expected error from auditRecorded with unreadable audit file: %v", err)
	}
	_ = os.Chmod(audPath, 0600)
	_ = os.Remove(audPath)
	_ = os.Remove(filepath.Join(kpDir, "prop-aud-err.json"))

	// writeItem error on knowledge-operations via chmod 0500
	if err := os.Chmod(koDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeAction(ctx, validAction, "alice"); err == nil {
		t.Fatalf("expected error from writeItem to read-only knowledge-operations")
	}
	_ = os.Chmod(koDir, 0700)

	// readItems error when old == nil and exceeding capacity
	// 1) readItems error (> maxItems)
	for i := 0; i < maxItems+1; i++ {
		capID := "kp-" + strings.Repeat("a", 3) + string(rune('a'+i/26)) + string(rune('a'+i%26))
		_ = writeItem(kpDir, capID, knowledge.Proposal{ID: capID})
	}
	if _, err := s.KnowledgeAction(ctx, validAction, "alice"); err == nil {
		t.Fatalf("expected error from readItems exceeding maxItems")
	}
	// 2) exactly maxItems -> ErrCapacity
	lastID := "kp-" + strings.Repeat("a", 3) + string(rune('a'+maxItems/26)) + string(rune('a'+maxItems%26))
	_ = os.Remove(filepath.Join(kpDir, lastID+".json"))
	if _, err := s.KnowledgeAction(ctx, validAction, "alice"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity when items == maxItems: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		capID := "kp-" + strings.Repeat("a", 3) + string(rune('a'+i/26)) + string(rune('a'+i%26))
		_ = os.Remove(filepath.Join(kpDir, capID+".json"))
	}

	// review, reject, export when old == nil -> ErrNotFound
	reviewMissingAction := knowledge.Action{
		Project:     s.projectID,
		Storage:     "local",
		OperationID: "op-rev-missing",
		Type:        "review",
		ProposalID:  "missing-prop",
		IfMatch:     1,
		ReviewNote:  "note",
	}
	if _, err := s.KnowledgeAction(ctx, reviewMissingAction, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing proposal review: %v", err)
	}

	rejectMissingAction := knowledge.Action{
		Project:     s.projectID,
		Storage:     "local",
		OperationID: "op-rej-missing",
		Type:        "reject",
		ProposalID:  "missing-prop",
		IfMatch:     1,
		ReviewNote:  "note",
	}
	if _, err := s.KnowledgeAction(ctx, rejectMissingAction, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing proposal reject: %v", err)
	}

	exportMissingAction := knowledge.Action{
		Project:     s.projectID,
		Storage:     "local",
		OperationID: "op-exp-missing",
		Type:        "export",
		ProposalID:  "missing-prop",
		IfMatch:     1,
		Target:      &knowledge.Target{System: "meaninggraph", Repository: "org/repo", ResourceID: "res", ExpectedRevision: "r1"},
	}
	if _, err := s.KnowledgeAction(ctx, exportMissingAction, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing proposal export: %v", err)
	}

	// writeItem error on knowledge-operations
	koDirBak := koDir + ".bak"
	if err := os.Rename(koDir, koDirBak); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(koDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeAction(ctx, validAction, "alice"); err == nil {
		t.Fatalf("expected error from writeItem to blocked knowledge-operations")
	}
	_ = os.Remove(koDir)
	_ = os.Rename(koDirBak, koDir)

	// 3. finishKnowledgeOperation branches
	// next == nil -> ErrInvalid
	if err := s.finishKnowledgeOperation(ctx, "op1", &knowledgeOperation{Response: knowledge.Response{}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil proposal in finish")
	}

	// corrupt proposal in finishKnowledgeOperation
	if err := os.WriteFile(filepath.Join(kpDir, "prop-bad-fin.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	opFinBad := &knowledgeOperation{
		Response: knowledge.Response{Proposal: &knowledge.Proposal{ID: "prop-bad-fin"}},
	}
	if err := s.finishKnowledgeOperation(ctx, "op1", opFinBad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for corrupt json in finishKnowledgeOperation")
	}
	_ = os.Remove(filepath.Join(kpDir, "prop-bad-fin.json"))

	// safeReadFile directory in finishKnowledgeOperation
	_ = os.MkdirAll(filepath.Join(kpDir, "prop-dir-fin.json"), 0700)
	opFinDir := &knowledgeOperation{
		Response: knowledge.Response{Proposal: &knowledge.Proposal{ID: "prop-dir-fin"}},
	}
	if err := s.finishKnowledgeOperation(ctx, "op1", opFinDir); err == nil {
		t.Fatalf("expected error reading directory in finishKnowledgeOperation")
	}
	_ = os.RemoveAll(filepath.Join(kpDir, "prop-dir-fin.json"))

	// writeItem to knowledge-proposals fails
	kpDirBak := kpDir + ".bak"
	_ = os.Rename(kpDir, kpDirBak)
	if err := os.WriteFile(kpDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	opFinWriteFail := &knowledgeOperation{
		Response: knowledge.Response{Proposal: &knowledge.Proposal{ID: "prop-wf"}},
		Previous: nil,
	}
	if err := s.finishKnowledgeOperation(ctx, "op1", opFinWriteFail); err == nil {
		t.Fatalf("expected error writing to blocked knowledge-proposals")
	}
	_ = os.Remove(kpDir)
	_ = os.Rename(kpDirBak, kpDir)

	// writeItem to knowledge-operations fails -> ErrOutcomeUncertain
	_ = os.Rename(koDir, koDirBak)
	if err := os.WriteFile(koDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	opFinUnc := &knowledgeOperation{
		Response: knowledge.Response{Proposal: &knowledge.Proposal{ID: "prop-unc"}},
		Previous: &knowledge.Proposal{ID: "prop-unc"},
		Audit:    AuditEvent{ID: "op-unc", Action: "knowledge:save-draft", Project: s.projectID, Actor: "alice", At: time.Now().UTC()},
	}
	_ = writeItem(kpDir, "prop-unc", knowledge.Proposal{ID: "prop-unc"})
	if err := s.finishKnowledgeOperation(ctx, "op-unc", opFinUnc); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from blocked koDir: %v", err)
	}
	_ = os.Remove(koDir)
	_ = os.Rename(koDirBak, koDir)
	_ = os.Remove(filepath.Join(kpDir, "prop-unc.json"))

	// 4. auditRecorded error branches
	auditPath := s.audit.(localJSONLAudit).path
	auditBak := auditPath + ".bak"
	_ = os.Rename(auditPath, auditBak)
	// audit.path is a directory -> ensureAbsentOrRegular fails
	_ = os.MkdirAll(auditPath, 0700)
	if _, err := s.auditRecorded(AuditEvent{ID: "test"}); err == nil {
		t.Fatalf("expected error from auditRecorded with dir audit file")
	}
	_ = os.RemoveAll(auditPath)
	_ = os.Rename(auditBak, auditPath)

	// 5. knowledgeError tests
	if err := knowledgeError(knowledge.ErrConflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict")
	}
	if err := knowledgeError(knowledge.ErrCapacity); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity")
	}
	if err := knowledgeError(errors.New("other")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid")
	}

	// 6. knowledgeFacts branches
	// Duplicate or invalid evidence slug in Case
	caseBadEv, err := s.SaveCase(ctx, Case{ID: "case-bad-ev", Title: "Bad Evidence Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	caseBadEv.Evidence = []EvidenceRef{
		{ID: "ev-dup", Kind: "external", Ref: "r1"},
		{ID: "ev-dup", Kind: "external", Ref: "r2"},
	}
	_ = writeItem(s.privateItemDir("cases"), caseBadEv.ID, caseBadEv)
	if _, err := s.knowledgeFacts(knowledge.Proposal{CaseID: caseBadEv.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for duplicate evidence in case")
	}

	// Case with valid run reference: evidence-run1
	// Save a run in store
	runItem := CheckRun{
		ID:              "run-fact-1",
		Kind:            "check",
		CheckID:         "check-1",
		CaseID:          c.ID,
		Actor:           "alice",
		ExecutionStatus: "completed",
		Outcome:         "passed",
		StartedAt:       time.Now().UTC(),
		DurationMs:      10,
	}
	if err := s.SaveRun(ctx, runItem); err != nil {
		t.Fatal(err)
	}
	caseWithRun := c
	caseWithRun.RunRefs = []string{runItem.ID}
	_ = writeItem(s.privateItemDir("cases"), caseWithRun.ID, caseWithRun)

	propWithRun := knowledge.Proposal{
		ID:           "prop-run",
		CaseID:       c.ID,
		EvidenceRefs: []string{"evidence-" + runItem.ID},
	}
	facts, err := s.knowledgeFacts(propWithRun)
	if err != nil {
		t.Fatalf("expected knowledgeFacts to succeed for valid run: %v", err)
	}
	if !containsString(facts.EvidenceIDs, "evidence-"+runItem.ID) {
		t.Fatalf("expected evidence-run-fact-1 in facts: %+v", facts)
	}

	// Case with incomplete run status -> ErrInvalid
	incompleteRun := runItem
	incompleteRun.ID = "run-fact-2"
	incompleteRun.ExecutionStatus = "running"
	if err := s.SaveRun(ctx, incompleteRun); err != nil {
		t.Fatal(err)
	}
	caseWithRun.RunRefs = append(caseWithRun.RunRefs, incompleteRun.ID)
	_ = writeItem(s.privateItemDir("cases"), caseWithRun.ID, caseWithRun)
	propIncomplete := knowledge.Proposal{
		CaseID:       c.ID,
		EvidenceRefs: []string{"evidence-" + incompleteRun.ID},
	}
	if _, err := s.knowledgeFacts(propIncomplete); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for incomplete run in knowledgeFacts")
	}

	// containsString returns false
	if containsString([]string{"a", "b"}, "c") {
		t.Fatalf("expected false")
	}
}
