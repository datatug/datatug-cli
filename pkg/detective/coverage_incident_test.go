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
)

// Not parallel: tests all branch paths of incident operations.
func TestCoverage100_Incident_AllBranches(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	incDir := s.privateItemDir("incident-operations")
	if err := os.MkdirAll(incDir, 0700); err != nil {
		t.Fatal(err)
	}

	// 1. RecordIncidentCreated validation & errors
	badBinding := core.OperationBinding{Action: "incident:link"}
	if err := s.RecordIncidentCreated(ctx, "inv!", badBinding, "ref1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slug")
	}
	if err := s.RecordIncidentCreated(ctx, "op1", badBinding, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty ref")
	}
	if err := s.RecordIncidentCreated(ctx, "op1", badBinding, "ref1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for non-create action")
	}

	validCreateBinding, err := core.BindOperation("p", "alice", "incident:create", "c1", struct{ IfMatch int64 }{0})
	if err != nil {
		t.Fatal(err)
	}

	// safeReadFile error (file does not exist)
	if err := s.RecordIncidentCreated(ctx, "nonexistent", validCreateBinding, "ref1"); err == nil {
		t.Fatalf("expected error for nonexistent operation")
	}

	// write bad json
	if err := os.WriteFile(filepath.Join(incDir, "bad-json.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIncidentCreated(ctx, "bad-json", validCreateBinding, "ref1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for bad json")
	}
	_ = os.Remove(filepath.Join(incDir, "bad-json.json"))

	// CreatedRef matching exact retry returns nil
	receipt, err := core.NewOperationReceipt("op-rec-ok", validCreateBinding, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	recordedOp := IncidentOperation{Receipt: receipt, CreatedRef: "ref-exact"}
	if err := writeItem(incDir, "op-rec-ok", recordedOp); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIncidentCreated(ctx, "op-rec-ok", validCreateBinding, "ref-exact"); err != nil {
		t.Fatalf("expected nil for exact retry, got: %v", err)
	}
	_ = os.Remove(filepath.Join(incDir, "op-rec-ok.json"))

	// 2. BeginIncidentOperation validation
	if _, _, err := s.BeginIncidentOperation(ctx, "bad!", validCreateBinding, "c1", 0, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid")
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op1", validCreateBinding, "bad!", 0, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid")
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op1", validCreateBinding, "c1", -1, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid")
	}

	// reconcilePendingActions error
	actDir := s.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	if err := os.WriteFile(filepath.Join(actDir, "corrupt.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op1", validCreateBinding, "c1", 0, true); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "corrupt.json"))

	// replay conflict when operation exists
	badReplay := `{"receipt":{"operationId":"mismatch"}}`
	if err := os.WriteFile(filepath.Join(incDir, "bad-replay.json"), []byte(badReplay), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "bad-replay", validCreateBinding, "c1", 0, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for bad replay: %v", err)
	}
	_ = os.Remove(filepath.Join(incDir, "bad-replay.json"))

	// safeReadFile error (directory in place of file)
	_ = os.MkdirAll(filepath.Join(incDir, "dir-op.json"), 0700)
	if _, _, err := s.BeginIncidentOperation(ctx, "dir-op", validCreateBinding, "c1", 0, true); err == nil {
		t.Fatalf("expected error reading directory")
	}
	_ = os.RemoveAll(filepath.Join(incDir, "dir-op.json"))

	// 1) readItems error (> maxItems items in incident-operations)
	for i := 0; i < maxItems+1; i++ {
		capID := "cap-" + strings.Repeat("a", 3) + string(rune('a'+i/26)) + string(rune('a'+i%26))
		_ = writeItem(incDir, capID, IncidentOperation{Receipt: core.OperationReceipt{ID: capID}})
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op-cap-err", validCreateBinding, "c-cap", 0, true); err == nil {
		t.Fatalf("expected error from readItems when exceeding maxItems")
	}
	// 2) exactly maxItems -> ErrCapacity
	lastID := "cap-" + strings.Repeat("a", 3) + string(rune('a'+maxItems/26)) + string(rune('a'+maxItems%26))
	_ = os.Remove(filepath.Join(incDir, lastID+".json"))
	if _, _, err := s.BeginIncidentOperation(ctx, "op-cap", validCreateBinding, "c-cap", 0, true); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity: %v", err)
	}
	// clean up capacity items
	for i := 0; i < maxItems; i++ {
		capID := "cap-" + strings.Repeat("a", 3) + string(rune('a'+i/26)) + string(rune('a'+i%26))
		_ = os.Remove(filepath.Join(incDir, capID+".json"))
	}

	// create == true, but case already exists or ifMatch != 0
	existingCase, err := s.SaveCase(ctx, Case{ID: "c-exist", Title: "Existing"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op-exist", validCreateBinding, existingCase.ID, 0, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when case exists for create")
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op-ifmatch", validCreateBinding, "c-nonexist", 1, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when ifMatch != 0 for create")
	}

	// create == false, but version != ifMatch or case not found
	linkBinding, err := core.BindOperation("p", "alice", "incident:link", existingCase.ID, struct{ Ref string }{"ref"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op-link-badmatch", linkBinding, existingCase.ID, 999, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for version mismatch")
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op-link-nocase", linkBinding, "c-notfound", 1, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for case not found")
	}

	// core.NewOperationReceipt error in BeginIncidentOperation
	badHexBinding := validCreateBinding
	badHexBinding.PayloadDigest = strings.Repeat("z", 64)
	if _, _, err := s.BeginIncidentOperation(ctx, "op-bad-hex", badHexBinding, "c-new", 0, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad hex in binding digest: %v", err)
	}

	// writeItem error for BeginIncidentOperation via read-only dir
	if err := os.Chmod(incDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginIncidentOperation(ctx, "op-ro-fail", validCreateBinding, "c-ro", 0, true); err == nil {
		t.Fatalf("expected error from writeItem in read-only dir")
	}
	_ = os.Chmod(incDir, 0700)

	// 3. CompleteIncidentOperation validation
	if _, err := s.CompleteIncidentOperation(ctx, "bad!", validCreateBinding, "c1", "ref", "alice", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid")
	}
	if _, err := s.CompleteIncidentOperation(ctx, "op1", validCreateBinding, "bad!", "ref", "alice", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid")
	}
	if _, err := s.CompleteIncidentOperation(ctx, "op1", validCreateBinding, "c1", "", "alice", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid")
	}

	// safeReadFile error (nonexistent operation)
	if _, err := s.CompleteIncidentOperation(ctx, "nonexistent", validCreateBinding, "c1", "ref", "alice", 0, nil); err == nil {
		t.Fatalf("expected error for nonexistent operation")
	}

	// replay check conflict
	if err := os.WriteFile(filepath.Join(incDir, "bad-complete-replay.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteIncidentOperation(ctx, "bad-complete-replay", validCreateBinding, "c1", "ref", "alice", 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for bad complete replay")
	}
	_ = os.Remove(filepath.Join(incDir, "bad-complete-replay.json"))

	// intent.Done == true, but intent.Case == nil -> ErrInvalid
	doneNoCase := IncidentOperation{Receipt: receipt, Done: true, Case: nil}
	if err := writeItem(incDir, "op-done-nocase", doneNoCase); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteIncidentOperation(ctx, "op-done-nocase", validCreateBinding, "c1", "ref", "alice", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for done without case")
	}

	// incident:create with CreatedRef mismatch
	createRefMismatch := IncidentOperation{Receipt: receipt, CreatedRef: "ref-A"}
	if err := writeItem(incDir, "op-ref-mismatch", createRefMismatch); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteIncidentOperation(ctx, "op-ref-mismatch", validCreateBinding, "c1", "ref-B", "alice", 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for ref mismatch")
	}

	// intent.Case != nil with Case.ID mismatch or incidentRef not found
	caseWithRef := Case{ID: "c1", IncidentRefs: []string{"ref-A"}}
	resumingMismatch := IncidentOperation{Receipt: receipt, CreatedRef: "ref-A", Case: &caseWithRef}
	if err := writeItem(incDir, "op-res-mismatch", resumingMismatch); err != nil {
		t.Fatal(err)
	}
	// Case.ID mismatch:
	if _, err := s.CompleteIncidentOperation(ctx, "op-res-mismatch", validCreateBinding, "c-diff", "ref-A", "alice", 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for Case.ID mismatch")
	}
	// Ref not found in Case.IncidentRefs:
	if _, err := s.CompleteIncidentOperation(ctx, "op-res-mismatch", validCreateBinding, "c1", "ref-other", "alice", 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when ref not in Case.IncidentRefs")
	}

	// intent.Case != nil success resume (calls finishIncidentOperation)
	// We need case c1 to not exist or match Before=0
	opResumeOk := IncidentOperation{
		Receipt:    receipt,
		CreatedRef: "ref-A",
		Case:       &Case{ID: "c-resume", Title: "Resume Case", Version: 1, IncidentRefs: []string{"ref-A"}, Status: "active"},
		Audit:      &AuditEvent{ID: "op-res-ok", Action: "incident:create", Project: "p", Actor: "alice", At: time.Now().UTC()},
		Before:     0,
	}
	if err := writeItem(incDir, "op-res-ok", opResumeOk); err != nil {
		t.Fatal(err)
	}
	bndResume, err := core.BindOperation("p", "alice", "incident:create", "c-resume", struct{ IfMatch int64 }{0})
	if err != nil {
		t.Fatal(err)
	}
	// update receipt in stored op to match binding
	rcptResume, _ := core.NewOperationReceipt("op-res-ok", bndResume, 0, time.Now().UTC())
	opResumeOk.Receipt = rcptResume
	_ = writeItem(incDir, "op-res-ok", opResumeOk)
	resumedCase, err := s.CompleteIncidentOperation(ctx, "op-res-ok", bndResume, "c-resume", "ref-A", "alice", 0, nil)
	if err != nil {
		t.Fatalf("expected resume to succeed: %v", err)
	}
	if resumedCase.ID != "c-resume" {
		t.Fatalf("unexpected resumed case: %+v", resumedCase)
	}

	// create != nil:
	// 1) case already exists -> ErrConflict
	bndCreateExist, err := core.BindOperation("p", "alice", "incident:create", existingCase.ID, struct{ IfMatch int64 }{0})
	if err != nil {
		t.Fatal(err)
	}
	rcptExist, _ := core.NewOperationReceipt("op-cre-exist", bndCreateExist, 0, time.Now().UTC())
	opCreExist := IncidentOperation{Receipt: rcptExist, CreatedRef: "ref-exist"}
	_ = writeItem(incDir, "op-cre-exist", opCreExist)
	if _, err := s.CompleteIncidentOperation(ctx, "op-cre-exist", bndCreateExist, existingCase.ID, "ref-exist", "alice", 0, &Case{Title: "Candidate"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when case already exists for create")
	}

	// 2) success creating a new case via create != nil
	newCaseID := "c-new-created"
	bndNewCreate, err := core.BindOperation("p", "alice", "incident:create", newCaseID, struct{ IfMatch int64 }{0})
	if err != nil {
		t.Fatal(err)
	}
	rcptNewCreate, _ := core.NewOperationReceipt("op-new-cre", bndNewCreate, 0, time.Now().UTC())
	opNewCre := IncidentOperation{Receipt: rcptNewCreate, CreatedRef: "ref-new"}
	_ = writeItem(incDir, "op-new-cre", opNewCre)
	newCreatedCase, err := s.CompleteIncidentOperation(ctx, "op-new-cre", bndNewCreate, newCaseID, "ref-new", "alice", 0, &Case{Title: "Fresh Incident Case"})
	if err != nil {
		t.Fatalf("expected success creating new case: %v", err)
	}
	if newCreatedCase.ID != newCaseID || !containsIncidentRef(newCreatedCase.IncidentRefs, "ref-new") {
		t.Fatalf("unexpected created case: %+v", newCreatedCase)
	}

	// create == nil:
	// 1) case not found -> error
	bndMissingCase, err := core.BindOperation("p", "alice", "incident:link", "c-missing-404", struct{ Ref string }{"ref"})
	if err != nil {
		t.Fatal(err)
	}
	rcptMissing, _ := core.NewOperationReceipt("op-missing", bndMissingCase, 0, time.Now().UTC())
	_ = writeItem(incDir, "op-missing", IncidentOperation{Receipt: rcptMissing})
	if _, err := s.CompleteIncidentOperation(ctx, "op-missing", bndMissingCase, "c-missing-404", "ref", "alice", 1, nil); err == nil {
		t.Fatalf("expected error when case does not exist for update")
	}

	// 2) UpdateCase failure (e.g. invalid actor or transition)
	// core.UpdateCase fails if actor == ""
	bndActorFail, err := core.BindOperation("p", "alice", "incident:link", existingCase.ID, struct{ Ref string }{"ref"})
	if err != nil {
		t.Fatal(err)
	}
	rcptActorFail, _ := core.NewOperationReceipt("op-actor-fail", bndActorFail, 0, time.Now().UTC())
	_ = writeItem(incDir, "op-actor-fail", IncidentOperation{Receipt: rcptActorFail})
	if _, err := s.CompleteIncidentOperation(ctx, "op-actor-fail", bndActorFail, existingCase.ID, "ref", "", existingCase.Version, nil); err == nil {
		t.Fatalf("expected error from core.UpdateCase when actor is empty")
	}

	// 3) compactCaseTimeline error
	timelineDir := s.privateItemDir("case-timeline")
	timelineBak := timelineDir + ".bak"
	_ = os.Rename(timelineDir, timelineBak)
	if err := os.WriteFile(timelineDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	// Case with >400 events
	bigIncCase, err := s.SaveCase(ctx, Case{ID: "big-inc-case", Title: "Big Incident Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 410; i++ {
		bigIncCase.Timeline = append(bigIncCase.Timeline, CaseEvent{ID: "evt", Type: "note"})
	}
	_ = writeItem(s.privateItemDir("cases"), bigIncCase.ID, bigIncCase)
	bndTimelineFail, err := core.BindOperation("p", "alice", "incident:link", bigIncCase.ID, struct{ Ref string }{"ref"})
	if err != nil {
		t.Fatal(err)
	}
	rcptTimelineFail, _ := core.NewOperationReceipt("op-timeline-fail", bndTimelineFail, 0, time.Now().UTC())
	_ = writeItem(incDir, "op-timeline-fail", IncidentOperation{Receipt: rcptTimelineFail})
	if _, err := s.CompleteIncidentOperation(ctx, "op-timeline-fail", bndTimelineFail, bigIncCase.ID, "ref", "alice", bigIncCase.Version, nil); err == nil {
		t.Fatalf("expected error from compactCaseTimeline")
	}
	_ = os.Remove(timelineDir)
	_ = os.Rename(timelineBak, timelineDir)

	// 4) writeItem to incident-operations fails due to 1MB findings
	hugeCaseCandidate := &Case{Title: "Huge Candidate", Findings: []Finding{{ID: "f-big", Text: strings.Repeat("x", 1<<20), Status: "hypothesis"}}}
	bndCreHuge, _ := core.BindOperation("p", "alice", "incident:create", "c-cre-huge", struct{ IfMatch int64 }{0})
	rcptCreHuge, _ := core.NewOperationReceipt("op-cre-huge", bndCreHuge, 0, time.Now().UTC())
	_ = writeItem(incDir, "op-cre-huge", IncidentOperation{Receipt: rcptCreHuge, CreatedRef: "ref-huge"})
	if _, err := s.CompleteIncidentOperation(ctx, "op-cre-huge", bndCreHuge, "c-cre-huge", "ref-huge", "alice", 0, hugeCaseCandidate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from writeItem in CompleteIncidentOperation: %v", err)
	}
	_ = os.Remove(filepath.Join(incDir, "op-cre-huge.json"))

	// 4. finishIncidentOperation branches
	// nil Case or nil Audit -> ErrInvalid
	if err := s.finishIncidentOperation(ctx, "op-nil", &IncidentOperation{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Case")
	}
	if err := s.finishIncidentOperation(ctx, "op-nil", &IncidentOperation{Case: &existingCase, Audit: nil}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Audit")
	}

	// current case version mismatch (conflict)
	opConflict := &IncidentOperation{
		Case:   &Case{ID: existingCase.ID, Version: 99},
		Audit:  &AuditEvent{ID: "op-conf", Action: "incident:link", Project: "p", Actor: "alice", At: time.Now().UTC()},
		Before: 50, // current.Version is 1, so != 50
	}
	if err := s.finishIncidentOperation(ctx, "op-conf", opConflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for version mismatch in finishIncidentOperation: %v", err)
	}

	// writeItem to cases fails in finishIncidentOperation due to 1MB findings
	opCaseWriteFail := &IncidentOperation{
		Case: &Case{
			ID:       existingCase.ID,
			Version:  existingCase.Version + 1,
			Title:    "Huge Finish Case",
			Findings: []Finding{{ID: "f-fin-big", Text: strings.Repeat("x", 1<<20), Status: "hypothesis"}},
		},
		Audit:  &AuditEvent{ID: "op-cwf", Action: "incident:link", Project: "p", Actor: "alice", At: time.Now().UTC()},
		Before: existingCase.Version,
	}
	if err := s.finishIncidentOperation(ctx, "op-cwf", opCaseWriteFail); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid writing case to cases dir: %v", err)
	}

	// auditRecorded error (corrupt audit file)
	auditPath := s.audit.(localJSONLAudit).path
	auditBak := auditPath + ".bak"
	_ = os.Rename(auditPath, auditBak)
	if err := os.WriteFile(auditPath, []byte("{\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opAuditCorrupt := &IncidentOperation{
		Case:   &existingCase,
		Audit:  &AuditEvent{ID: "op-ac", Action: "incident:link", Project: "p", Actor: "alice", At: time.Now().UTC()},
		Before: existingCase.Version,
	}
	if err := s.finishIncidentOperation(ctx, "op-ac", opAuditCorrupt); err == nil {
		t.Fatalf("expected error from corrupt audit file")
	}
	_ = os.Remove(auditPath)
	_ = os.Rename(auditBak, auditPath)

	// writeItem to incident-operations fails -> ErrOutcomeUncertain
	incDirBak := incDir + ".bak"
	_ = os.Rename(incDir, incDirBak)
	if err := os.WriteFile(incDir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	opUnc := &IncidentOperation{
		Case:   &existingCase,
		Audit:  &AuditEvent{ID: "op-unc", Action: "incident:link", Project: "p", Actor: "alice", At: time.Now().UTC()},
		Before: existingCase.Version,
	}
	if err := s.finishIncidentOperation(ctx, "op-unc", opUnc); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from blocked incDir: %v", err)
	}
	_ = os.Remove(incDir)
	_ = os.Rename(incDirBak, incDir)

	// finishIncidentOperation getCase error when Before != 0
	opNotFound := &IncidentOperation{
		Case:   &Case{ID: "case-not-found-123"},
		Audit:  &AuditEvent{ID: "op-nf", Action: "incident:link", Project: "p", Actor: "alice", At: time.Now().UTC()},
		Before: 1,
	}
	if err := s.finishIncidentOperation(ctx, "op-nf", opNotFound); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from finishIncidentOperation: %v", err)
	}

	// 5. containsIncidentRef returns false
	if containsIncidentRef([]string{"a", "b"}, "c") {
		t.Fatalf("expected false for missing ref")
	}
}
