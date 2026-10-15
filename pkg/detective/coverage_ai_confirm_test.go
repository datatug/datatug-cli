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
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

// Not parallel: mutates detective store and private journal directories.
func TestCoverage100_AIConfirm_AllBranches(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	// 1. AIReceipt invalid args
	if _, _, err := (LocalAIStore{}).AIReceipt(ctx, "op1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Store")
	}
	if _, _, err := local.AIReceipt(ctx, "invalid slug!"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slug")
	}

	// 2. AIReceipt file not exist
	if _, found, err := local.AIReceipt(ctx, "nonexistent"); err != nil || found {
		t.Fatalf("expected not found: %v, %v", found, err)
	}

	// 3. AIReceipt safeReadFile error (directory in place of file)
	opDir := store.privateItemDir("ai-confirm-operations")
	if err := os.MkdirAll(filepath.Join(opDir, "isdir.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "isdir"); err == nil {
		t.Fatalf("expected error reading directory as file")
	}
	_ = os.RemoveAll(filepath.Join(opDir, "isdir.json"))

	// 4. AIReceipt invalid json header
	if err := os.WriteFile(filepath.Join(opDir, "bad-header.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "bad-header"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for bad header: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "bad-header.json"))

	// 5. AIReceipt kind == "query" (bad unmarshal, scope mismatch, query.Done=false, query.Done=true)
	// Scope mismatch / bad unmarshal:
	if err := os.WriteFile(filepath.Join(opDir, "q-mismatch.json"), []byte(`{"kind":"query","receipt":{"scopeKey":"other"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "q-mismatch"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for scope mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "q-mismatch.json"))

	// query.Done = true:
	qDone := `{"kind":"query","done":true,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"q1"}}`
	if err := os.WriteFile(filepath.Join(opDir, "q-done.json"), []byte(qDone), 0600); err != nil {
		t.Fatal(err)
	}
	if r, found, err := local.AIReceipt(ctx, "q-done"); err != nil || !found || r.ResourceID != "q1" {
		t.Fatalf("expected query receipt: %+v, %v, %v", r, found, err)
	}
	_ = os.Remove(filepath.Join(opDir, "q-done.json"))

	// query.Done = false (finishAIQuery error)
	qNotDone := `{"kind":"query","done":false,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"q1"}}`
	if err := os.WriteFile(filepath.Join(opDir, "q-notdone.json"), []byte(qNotDone), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "q-notdone"); err == nil {
		t.Fatalf("expected error finishing incomplete query")
	}
	_ = os.Remove(filepath.Join(opDir, "q-notdone.json"))

	// 6. AIReceipt kind == "run" (scope mismatch, not produced, done=true, done=false)
	runMismatch := `{"kind":"run","receipt":{"scopeKey":"other"}}`
	if err := os.WriteFile(filepath.Join(opDir, "run-mismatch.json"), []byte(runMismatch), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "run-mismatch"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "run-mismatch.json"))

	runUncertain := `{"kind":"run","produced":false,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice"}}`
	if err := os.WriteFile(filepath.Join(opDir, "run-uncertain.json"), []byte(runUncertain), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "run-uncertain"); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "run-uncertain.json"))

	runDone := `{"kind":"run","produced":true,"done":true,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"r1"}}`
	if err := os.WriteFile(filepath.Join(opDir, "run-done.json"), []byte(runDone), 0600); err != nil {
		t.Fatal(err)
	}
	if r, found, err := local.AIReceipt(ctx, "run-done"); err != nil || !found || r.ResourceID != "r1" {
		t.Fatalf("expected run receipt: %+v, %v, %v", r, found, err)
	}
	_ = os.Remove(filepath.Join(opDir, "run-done.json"))

	runNotDone := `{"kind":"run","produced":true,"done":false,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"r1"}}`
	if err := os.WriteFile(filepath.Join(opDir, "run-notdone.json"), []byte(runNotDone), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "run-notdone"); err == nil {
		t.Fatalf("expected error from incomplete run finish")
	}
	_ = os.Remove(filepath.Join(opDir, "run-notdone.json"))

	// 7. AIReceipt kind == "draft-check" (catalog) (scope mismatch, done=true, done=false)
	catMismatch := `{"kind":"draft-check","receipt":{"scopeKey":"other"}}`
	if err := os.WriteFile(filepath.Join(opDir, "cat-mismatch.json"), []byte(catMismatch), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "cat-mismatch"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "cat-mismatch.json"))

	catDone := `{"kind":"draft-check","done":true,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"c1"}}`
	if err := os.WriteFile(filepath.Join(opDir, "cat-done.json"), []byte(catDone), 0600); err != nil {
		t.Fatal(err)
	}
	if r, found, err := local.AIReceipt(ctx, "cat-done"); err != nil || !found || r.ResourceID != "c1" {
		t.Fatalf("expected catalog receipt: %+v, %v, %v", r, found, err)
	}
	_ = os.Remove(filepath.Join(opDir, "cat-done.json"))

	catNotDone := `{"kind":"draft-check","done":false,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"c1"}}`
	if err := os.WriteFile(filepath.Join(opDir, "cat-notdone.json"), []byte(catNotDone), 0600); err != nil {
		t.Fatal(err)
	}
	if r, found, err := local.AIReceipt(ctx, "cat-notdone"); err != nil || !found || r.ResourceID != "c1" {
		t.Fatalf("expected cat-notdone to finish: %+v, %v, %v", r, found, err)
	}
	_ = os.Remove(filepath.Join(opDir, "cat-notdone.json"))

	catNotDoneErr := `{"kind":"draft-check","done":false,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"invalid-id!"}}`
	if err := os.WriteFile(filepath.Join(opDir, "cat-notdone-err.json"), []byte(catNotDoneErr), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "cat-notdone-err"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from bad resourceId in finishAICatalog: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "cat-notdone-err.json"))

	// 8. AIReceipt kind == "case" (scope mismatch, done=false)
	caseMismatch := `{"kind":"case","receipt":{"scopeKey":"other"}}`
	if err := os.WriteFile(filepath.Join(opDir, "case-mismatch.json"), []byte(caseMismatch), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "case-mismatch"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "case-mismatch.json"))

	caseNotDone := `{"kind":"case","done":false,"receipt":{"scopeKey":"local:p","project":"p","actor":"alice","resourceId":"c-notdone"}}`
	if err := os.WriteFile(filepath.Join(opDir, "case-notdone.json"), []byte(caseNotDone), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.AIReceipt(ctx, "case-notdone"); err == nil {
		t.Fatalf("expected error from incomplete case finish")
	}
	_ = os.Remove(filepath.Join(opDir, "case-notdone.json"))
}

// Not parallel: tests ConfirmCase input validation and editing variations.
func TestCoverage100_AIConfirm_ConfirmCaseVariations(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	created, err := store.SaveCase(ctx, Case{ID: "case-var", Title: "Initial Title"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}

	validCmd := func() ai.Command {
		proposal := ai.Proposal{
			ID:    "reviewed-var",
			Scope: scope,
			Draft: ai.Draft{
				Kind: ai.KindCase,
				Case: &ai.CaseEdit{
					CaseID:          created.ID,
					ExpectedVersion: created.Version,
					Title:           "New Title",
					Status:          "active",
					ClosureReason:   "resolved",
					Findings:        []Finding{{ID: "f1", Text: "found", Status: "hypothesis"}},
				},
			},
		}
		binding := core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-edit-case",
			ResourceID:    proposal.ID,
			PayloadDigest: strings.Repeat("a", 64),
		}
		return ai.Command{
			Scope:       scope,
			OperationID: "op-var-1",
			Proposal:    proposal,
			Binding:     binding,
		}
	}

	// Validation failures
	badCmds := []ai.Command{
		func() ai.Command { c := validCmd(); c.Scope.Key = "other"; return c }(),
		func() ai.Command { c := validCmd(); c.Proposal.Scope.Key = "other"; return c }(),
		func() ai.Command { c := validCmd(); c.Proposal.Draft.Kind = "other"; return c }(),
		func() ai.Command { c := validCmd(); c.Proposal.Draft.Case = nil; return c }(),
		func() ai.Command { c := validCmd(); c.OperationID = "invalid slug!"; return c }(),
		func() ai.Command { c := validCmd(); c.Proposal.ID = "invalid slug!"; return c }(),
		func() ai.Command { c := validCmd(); c.Binding.Project = "other"; return c }(),
		func() ai.Command { c := validCmd(); c.Binding.Actor = "other"; return c }(),
		func() ai.Command { c := validCmd(); c.Binding.Action = "other"; return c }(),
		func() ai.Command { c := validCmd(); c.Binding.ResourceID = "other"; return c }(),
		func() ai.Command { c := validCmd(); c.Binding.PayloadDigest = "short"; return c }(),
	}

	for i, c := range badCmds {
		if _, err := local.ConfirmCase(ctx, c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("badCmd[%d]: expected ErrInvalid, got %v", i, err)
		}
	}

	// Nil Store
	if _, err := (LocalAIStore{}).ConfirmCase(ctx, validCmd()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Store")
	}

	// Successful confirm with Title, Status, ClosureReason, Findings
	cmd := validCmd()
	receipt, err := local.ConfirmCase(ctx, cmd)
	if err != nil {
		t.Fatalf("ConfirmCase failed: %v", err)
	}
	if receipt.ResourceID != created.ID {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}

	// Verify updated case fields
	updated, err := store.GetCase(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "New Title" || updated.Status != "active" || updated.ClosureReason != "resolved" || len(updated.Findings) != 1 {
		t.Fatalf("unexpected updated case: %+v", updated)
	}

	// Version mismatch (old.Version != edit.ExpectedVersion)
	staleCmd := validCmd()
	staleCmd.OperationID = "op-var-2"
	staleCmd.Proposal.ID = "reviewed-var-2"
	staleCmd.Binding.ResourceID = "reviewed-var-2"
	staleCmd.Proposal.Draft.Case.ExpectedVersion = 999
	if _, err := local.ConfirmCase(ctx, staleCmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for version mismatch: %v", err)
	}

	// Prior operation with mismatched receipt
	opDir := store.privateItemDir("ai-confirm-operations")
	if err := os.WriteFile(filepath.Join(opDir, "op-prior-bad.json"), []byte(`{"kind":"case","receipt":{"operationId":"mismatched"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	priorBadCmd := validCmd()
	priorBadCmd.OperationID = "op-prior-bad"
	if _, err := local.ConfirmCase(ctx, priorBadCmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for bad prior operation: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "op-prior-bad.json"))

	// finishAICase conflict: current != After and current != Before
	opFinish := &aiCaseOperation{
		Before: Case{ID: created.ID, Version: 1},
		After:  Case{ID: created.ID, Version: 2, Title: "Expected"},
		Audit:  AuditEvent{ID: "op-finish", Action: "ai-confirm-edit-case", At: time.Now().UTC()},
	}
	// Case on disk has Version 2 Title "New Title", so current != Before and current != After
	if err := local.finishAICase(ctx, "op-finish", opFinish); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when current case diverged: %v", err)
	}

	// finishAICase current == Before, but writing to cases fails due to >1MB size
	opWriteFail := &aiCaseOperation{
		Before: updated,
		After: Case{
			ID:       updated.ID,
			Version:  updated.Version + 1,
			Title:    "Huge Case",
			Findings: []Finding{{ID: "f-huge", Text: strings.Repeat("x", 1<<20), Status: "hypothesis"}},
		},
		Audit: AuditEvent{ID: "op-wf", Action: "ai-confirm-edit-case", At: time.Now().UTC()},
	}
	// current == Before, but writeItem to cases fails
	if err := local.finishAICase(ctx, "op-wf", opWriteFail); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid writing huge case to cases dir: %v", err)
	}

	// finishAICase writeItem to ai-confirm-operations fails -> ErrOutcomeUncertain
	opDirBak := opDir + ".bak"
	if err := os.Rename(opDir, opDirBak); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	opUncertain := &aiCaseOperation{
		Before: updated,
		After:  updated,
		Audit:  AuditEvent{ID: "op-unc", Action: "ai-confirm-edit-case", At: time.Now().UTC()},
	}
	if err := local.finishAICase(ctx, "op-unc", opUncertain); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain: %v", err)
	}
	_ = os.Remove(opDir)
	_ = os.Rename(opDirBak, opDir)

	// finishAICase auditRecorded error: corrupt audit file
	auditPath := store.audit.(localJSONLAudit).path
	auditBak := auditPath + ".bak"
	_ = os.Rename(auditPath, auditBak)
	if err := os.WriteFile(auditPath, []byte("{\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := local.finishAICase(ctx, "op-audit-corrupt", opUncertain); err == nil {
		t.Fatalf("expected error from corrupt audit file")
	}
	_ = os.Remove(auditPath)
	_ = os.Rename(auditBak, auditPath)

	// ConfirmCase: reconcilePendingActions error
	actDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	if err := os.WriteFile(filepath.Join(actDir, "bad-op.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	recCmd := validCmd()
	recCmd.OperationID = "op-rec-err"
	if _, err := local.ConfirmCase(ctx, recCmd); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "bad-op.json"))

	// ConfirmCase: safeReadFile error (directory instead of file, with CaseID="" to bypass reconcile)
	dirOpPath := filepath.Join(opDir, "op-dir.json")
	if err := os.MkdirAll(dirOpPath, 0700); err != nil {
		t.Fatal(err)
	}
	dirCmd := validCmd()
	dirCmd.OperationID = "op-dir"
	dirCmd.Proposal.Draft.Case.CaseID = ""
	if _, err := local.ConfirmCase(ctx, dirCmd); err == nil {
		t.Fatalf("expected error reading directory")
	}
	_ = os.RemoveAll(dirOpPath)

	// ConfirmCase: verifyAIBasisLocked error
	basisCmd := validCmd()
	basisCmd.OperationID = "op-basis"
	basisCmd.Proposal.Basis.CatalogRevision = "rev-xyz"
	if _, err := local.ConfirmCase(ctx, basisCmd); !errors.Is(err, ai.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized from verifyAIBasisLocked: %v", err)
	}

	// ConfirmCase: getCase error (case does not exist)
	noCaseCmd := validCmd()
	noCaseCmd.OperationID = "op-nocase"
	noCaseCmd.Proposal.Draft.Case.CaseID = "nonexistent-case"
	if _, err := local.ConfirmCase(ctx, noCaseCmd); err == nil {
		t.Fatalf("expected error from missing case")
	}

	// ConfirmCase: core.UpdateCase error (e.g. invalid transition to archived)
	archCmd := validCmd()
	archCmd.OperationID = "op-arch"
	archCmd.Proposal.Draft.Case.ExpectedVersion = updated.Version
	archCmd.Proposal.Draft.Case.Status = "archived"
	if _, err := local.ConfirmCase(ctx, archCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for invalid status transition: %v", err)
	}

	// ConfirmCase: compactCaseTimeline error
	// Create a case with >400 events so compactCaseTimeline runs
	bigCase, err := store.SaveCase(ctx, Case{ID: "big-case", Title: "Big Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 410; i++ {
		bigCase.Timeline = append(bigCase.Timeline, CaseEvent{ID: "evt", Type: "note"})
	}
	if err := writeItem(store.privateItemDir("cases"), bigCase.ID, bigCase); err != nil {
		t.Fatal(err)
	}
	// Block case-timeline dir
	timelineDir := store.privateItemDir("case-timeline")
	timelineBak := timelineDir + ".bak"
	_ = os.Rename(timelineDir, timelineBak)
	if err := os.WriteFile(timelineDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	compactCmd := validCmd()
	compactCmd.OperationID = "op-compact"
	compactCmd.Proposal.Draft.Case.CaseID = bigCase.ID
	compactCmd.Proposal.Draft.Case.ExpectedVersion = bigCase.Version
	compactCmd.Proposal.Draft.Case.Title = "Updated Big Case"
	if _, err := local.ConfirmCase(ctx, compactCmd); err == nil {
		t.Fatalf("expected error from compactCaseTimeline")
	}
	_ = os.Remove(timelineDir)
	_ = os.Rename(timelineBak, timelineDir)

	// ConfirmCase: writeItem to ai-confirm-operations fails due to >1MB findings
	writeOpCmd := validCmd()
	writeOpCmd.OperationID = "op-write-fail"
	writeOpCmd.Proposal.Draft.Case.ExpectedVersion = updated.Version
	writeOpCmd.Proposal.Draft.Case.Findings = []Finding{{ID: "f-huge", Text: strings.Repeat("x", 1<<20), Status: "hypothesis"}}
	if _, err := local.ConfirmCase(ctx, writeOpCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from writeItem to ai-confirm-operations: %v", err)
	}

	// ConfirmCase: retry when prior exists and !prior.Done
	// We set prior.After.ID = bigCase.ID and command CaseID = updated.ID so reconcilePendingOther skips it.
	// 1. finishAICase fails:
	priorErrOp := aiCaseOperation{
		Receipt: ai.Receipt{
			OperationID:   "op-resume-err",
			ScopeKey:      scope.Key,
			Project:       scope.Project,
			Actor:         scope.Actor,
			ProposalID:    "reviewed-resume-err",
			PayloadDigest: strings.Repeat("c", 64),
			ResourceID:    bigCase.ID,
		},
		Before: Case{ID: bigCase.ID, Version: 999}, // Version mismatch triggers ErrConflict
		After:  Case{ID: bigCase.ID, Version: 1000},
		Audit:  AuditEvent{ID: "op-resume-err", Action: "ai-confirm-edit-case", At: time.Now().UTC()},
		Done:   false,
	}
	if err := writeItem(opDir, "op-resume-err", priorErrOp); err != nil {
		t.Fatal(err)
	}
	resumeErrCmd := validCmd()
	resumeErrCmd.OperationID = "op-resume-err"
	resumeErrCmd.Proposal.ID = "reviewed-resume-err"
	resumeErrCmd.Binding.ResourceID = "reviewed-resume-err"
	resumeErrCmd.Binding.PayloadDigest = strings.Repeat("c", 64)
	if _, err := local.ConfirmCase(ctx, resumeErrCmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict from finishAICase retry: %v", err)
	}
	_ = os.Remove(filepath.Join(opDir, "op-resume-err.json"))

	// 2. finishAICase succeeds:
	priorOkOp := aiCaseOperation{
		Receipt: ai.Receipt{
			OperationID:   "op-resume-ok",
			ScopeKey:      scope.Key,
			Project:       scope.Project,
			Actor:         scope.Actor,
			ProposalID:    "reviewed-resume-ok",
			PayloadDigest: strings.Repeat("d", 64),
			ResourceID:    bigCase.ID,
		},
		Before: bigCase,
		After:  bigCase,
		Audit:  AuditEvent{ID: "op-resume-ok", Action: "ai-confirm-edit-case", At: time.Now().UTC()},
		Done:   false,
	}
	if err := writeItem(opDir, "op-resume-ok", priorOkOp); err != nil {
		t.Fatal(err)
	}
	resumeOkCmd := validCmd()
	resumeOkCmd.OperationID = "op-resume-ok"
	resumeOkCmd.Proposal.ID = "reviewed-resume-ok"
	resumeOkCmd.Binding.ResourceID = "reviewed-resume-ok"
	resumeOkCmd.Binding.PayloadDigest = strings.Repeat("d", 64)
	rcpt, err := local.ConfirmCase(ctx, resumeOkCmd)
	if err != nil {
		t.Fatalf("expected resume to succeed: %v", err)
	}
	if rcpt.OperationID != "op-resume-ok" {
		t.Fatalf("unexpected receipt: %+v", rcpt)
	}
	_ = os.Remove(filepath.Join(opDir, "op-resume-ok.json"))
}
