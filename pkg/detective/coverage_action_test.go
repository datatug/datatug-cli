package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Action_Helpers(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)

	// 1. defaultCaseContextDigest
	// a) nil values
	dig, err := defaultCaseContextDigest(nil)
	if err != nil || len(dig) != 64 {
		t.Fatalf("expected 64 hex char digest for nil: %v, %s", err, dig)
	}

	// 2. ActionLinkStatus
	// a) ActionBinding error (project mismatch)
	badReq := ActionRequest{Project: "other-proj", Type: "save-check"}
	if _, err := store.ActionLinkStatus(ctx, "op-1", "alice", badReq); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from ActionLinkStatus on binding error: %v", err)
	}

	// b) safeReadFile error (not exist)
	req := ActionRequest{
		Project: store.projectID,
		Type:    "save-check",
		Check:   &Check{ID: "chk-1", Title: "Title"},
	}
	if _, err := store.ActionLinkStatus(ctx, "missing-op", "alice", req); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist: %v", err)
	}

	// c) Corrupt JSON or CheckOperationReplay mismatch or !op.Done -> ErrConflict
	opsDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(opsDir, 0700)
	_ = os.WriteFile(filepath.Join(opsDir, "corrupt-op.json"), []byte("{"), 0600)
	if _, err := store.ActionLinkStatus(ctx, "corrupt-op", "alice", req); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on corrupt op: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "corrupt-op.json"))

	// !op.Done -> ErrConflict
	binding, err := store.ActionBinding(req, "alice")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := core.NewOperationReceipt("not-done-op", binding, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_ = writeItem(opsDir, "not-done-op", actionOperation{Receipt: receipt, Done: false})
	if _, err := store.ActionLinkStatus(ctx, "not-done-op", "alice", req); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on !op.Done: %v", err)
	}

	// Happy path for ActionLinkStatus
	_ = writeItem(opsDir, "done-op", actionOperation{Receipt: receipt, Done: true, CaseLinkStatus: "linked"})
	status, err := store.ActionLinkStatus(ctx, "done-op", "alice", req)
	if err != nil || status != "linked" {
		t.Fatalf("expected linked status: %v, %s", err, status)
	}

	// 3. actionResourceID
	if id := actionResourceID(ActionRequest{Type: "save-check", Check: nil}); id != "" {
		t.Fatalf("expected empty for nil check, got %s", id)
	}
	if id := actionResourceID(ActionRequest{Type: "save-validator", Validator: nil}); id != "" {
		t.Fatalf("expected empty for nil validator, got %s", id)
	}
	if id := actionResourceID(ActionRequest{Type: "save-query-validators", QueryValidators: nil}); id != "" {
		t.Fatalf("expected empty for nil query validators, got %s", id)
	}
	if id := actionResourceID(ActionRequest{Type: "save-scenario", Scenario: nil}); id != "" {
		t.Fatalf("expected empty for nil scenario, got %s", id)
	}
	if id := actionResourceID(ActionRequest{Type: "save-scenario", Scenario: &Scenario{ID: "scen-id"}}); id != "scen-id" {
		t.Fatalf("expected scen-id, got %s", id)
	}
	if id := actionResourceID(ActionRequest{Type: "create-case", Case: nil}); id != "" {
		t.Fatalf("expected empty for nil case, got %s", id)
	}
	if id := actionResourceID(ActionRequest{Type: "update-case", Case: nil}); id != "" {
		t.Fatalf("expected empty for nil case, got %s", id)
	}
	if id := actionResourceID(ActionRequest{Type: "unknown-type"}); id != "" {
		t.Fatalf("expected empty for unknown type, got %s", id)
	}

	// 4. actionResourceTarget
	col, res := actionResourceTarget(ActionRequest{Type: "save-scenario", Scenario: &Scenario{ID: "scen-tgt"}})
	if col != "scenarios" || res != "scen-tgt" {
		t.Fatalf("expected scenarios / scen-tgt, got %s / %s", col, res)
	}
	col, res = actionResourceTarget(ActionRequest{Type: "unknown-type"})
	if col != "" || res != "" {
		t.Fatalf("expected empty target for unknown, got %s / %s", col, res)
	}

	// 5. operationTarget
	col, res = operationTarget(actionOperation{Kind: "run", CaseID: "case-123"})
	if col != "cases" || res != "case-123" {
		t.Fatalf("expected cases / case-123, got %s / %s", col, res)
	}
	col, res = operationTarget(actionOperation{Kind: "checks", ID: "chk-123"})
	if col != "checks" || res != "chk-123" {
		t.Fatalf("expected checks / chk-123, got %s / %s", col, res)
	}

	// 6. ActionBinding validation
	if _, err := store.ActionBinding(ActionRequest{}, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on empty request: %v", err)
	}

	// 7. actionResponseVersion
	if v := actionResponseVersion(ActionResponse{Scenario: &Scenario{Version: 42}}); v != 42 {
		t.Fatalf("expected 42, got %d", v)
	}
	if v := actionResponseVersion(ActionResponse{}); v != 0 {
		t.Fatalf("expected 0, got %d", v)
	}

	// 8. sha256Sum
	sum := sha256Sum("hello")
	expected := sha256.Sum256([]byte("hello"))
	if hex.EncodeToString(sum) != hex.EncodeToString(expected[:]) {
		t.Fatalf("sha256Sum mismatch")
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Action_ReconcilePending(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)

	// 1. collection == "" or resourceID == "" -> returns nil
	if err := store.reconcilePendingActions(ctx, "", "id", "except"); err != nil {
		t.Fatalf("expected nil for empty collection: %v", err)
	}
	if err := store.reconcilePendingActions(ctx, "checks", "", "except"); err != nil {
		t.Fatalf("expected nil for empty resourceID: %v", err)
	}

	// 2. reconcilePendingOther error
	opsDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(opsDir, 0700)
	aiOpsDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(aiOpsDir, 0700)
	_ = os.WriteFile(filepath.Join(aiOpsDir, "corrupt.json"), []byte("{"), 0600)
	if err := store.reconcilePendingActions(ctx, "checks", "chk-1", "except"); err == nil {
		t.Fatalf("expected error from reconcilePendingOther")
	}
	_ = os.Remove(filepath.Join(aiOpsDir, "corrupt.json"))

	// 3. safeReadFile error (directory in action-operations)
	_ = os.MkdirAll(filepath.Join(opsDir, "dir-op.json"), 0700)
	if err := store.reconcilePendingActions(ctx, "checks", "chk-1", "except"); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(opsDir, "dir-op.json"))

	// 4. Corrupt JSON in action-operations -> ErrInvalid
	_ = os.WriteFile(filepath.Join(opsDir, "corrupt.json"), []byte("{"), 0600)
	if err := store.reconcilePendingActions(ctx, "checks", "chk-1", "except"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on corrupt action: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "corrupt.json"))

	// 5. Non-.json file and id == exceptID are skipped
	_ = os.WriteFile(filepath.Join(opsDir, "ignore.txt"), []byte("ignored"), 0600)
	_ = os.WriteFile(filepath.Join(opsDir, "except-op.json"), []byte("{}"), 0600)

	// 6. !op.Done && op.Produced && kind == collection && targetID == resourceID
	// triggers finishAction
	check := Check{ID: "chk-rec", Title: "Check Rec", Version: 1}
	checkJSON, _ := json.Marshal(check)
	req := ActionRequest{Project: store.projectID, Type: "save-check", Check: &check}
	binding, _ := store.ActionBinding(req, "alice")
	receipt, _ := core.NewOperationReceipt("op-rec", binding, 1, time.Now().UTC())

	pendingOp := actionOperation{
		Receipt:  receipt,
		Kind:     "checks",
		ID:       "chk-rec",
		After:    checkJSON,
		Audit:    AuditEvent{ID: "op-rec", Action: "save-check"},
		Produced: true,
		Done:     false,
	}
	_ = writeItem(opsDir, "op-rec", pendingOp)

	// Cause finishAction to fail (corrupt audit)
	auditPath := filepath.Join(store.privateDir, "audit.jsonl")
	_ = os.WriteFile(auditPath, []byte("{\n"), 0600)
	if err := store.reconcilePendingActions(ctx, "checks", "chk-rec", "except-op"); err == nil {
		t.Fatalf("expected finishAction error when audit is corrupt")
	}
	_ = os.Remove(auditPath)

	// Happy path for finishAction during reconcile
	if err := store.reconcilePendingActions(ctx, "checks", "chk-rec", "except-op"); err != nil {
		t.Fatalf("expected success reconciling pending action: %v", err)
	}

	_ = os.Remove(filepath.Join(opsDir, "ignore.txt"))
	_ = os.Remove(filepath.Join(opsDir, "except-op.json"))
	_ = os.Remove(filepath.Join(opsDir, "op-rec.json"))
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Action_ExecuteAction_Mutations(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)

	check := Check{
		ID:    "chk-mut",
		Title: "Check Mutation",
		Steps: []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)

	req := ActionRequest{
		Project: store.projectID,
		Type:    "save-check",
		Check:   &check,
	}

	// 1. Invalid operationID
	if _, err := store.ExecuteAction(ctx, "../bad", "alice", req, nil, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on bad op ID: %v", err)
	}

	// 2. ActionBinding error
	badReq := req
	badReq.Project = "wrong"
	if _, err := store.ExecuteAction(ctx, "op-mut-1", "alice", badReq, nil, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on bad project: %v", err)
	}

	// 3. reconcilePendingActions error
	aiOpsDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(aiOpsDir, 0700)
	_ = os.WriteFile(filepath.Join(aiOpsDir, "corrupt.json"), []byte("{"), 0600)
	if _, err := store.ExecuteAction(ctx, "op-mut-1", "alice", req, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(aiOpsDir, "corrupt.json"))

	// 4. safeReadFile error other than ErrNotExist (directory)
	opsDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(opsDir, 0700)
	_ = os.MkdirAll(filepath.Join(opsDir, "dir-op.json"), 0700)
	if _, err := store.ExecuteAction(ctx, "dir-op", "alice", req, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(opsDir, "dir-op.json"))

	// 5. Second lock paths in ExecuteAction:
	// a) Existing op in second lock: replay mismatch -> ErrConflict
	binding, _ := store.ActionBinding(req, "alice")
	receipt, _ := core.NewOperationReceipt("op-2nd-lock", binding, 1, time.Now().UTC())
	badReceipt := receipt
	badReceipt.PayloadDigest = strings.Repeat("f", 64)
	_ = writeItem(opsDir, "op-2nd-lock", actionOperation{Receipt: badReceipt})
	if _, err := store.ExecuteAction(ctx, "op-2nd-lock", "alice", req, nil, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on replay mismatch in second lock: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "op-2nd-lock.json"))

	// b) Existing op !op.Produced -> ErrOutcomeUncertain
	_ = writeItem(opsDir, "op-unc", actionOperation{Receipt: receipt, Produced: false})
	if _, err := store.ExecuteAction(ctx, "op-unc", "alice", req, nil, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "op-unc.json"))

	// c) Existing op Produced == true, Done == false -> finishAction
	_ = writeItem(opsDir, "op-fin", actionOperation{
		Receipt:  receipt,
		Kind:     "checks",
		ID:       check.ID,
		After:    json.RawMessage(`{"id":"chk-mut","title":"Check Mutation","version":1,"steps":[{"id":"s1","kind":"query","queryId":"valid-q"}]}`),
		Audit:    AuditEvent{ID: "op-fin", Action: "save-check"},
		Produced: true,
		Done:     false,
	})
	resp, err := store.ExecuteAction(ctx, "op-fin", "alice", req, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("expected success finishing action: %v, %+v", err, resp)
	}
	_ = os.Remove(filepath.Join(opsDir, "op-fin.json"))

	// d) prepErr != nil in prepareAction (e.g. check without steps)
	checkBad := Check{ID: "chk-bad-step", Title: "Bad"}
	reqBad := ActionRequest{Project: store.projectID, Type: "save-check", Check: &checkBad}
	if _, err := store.ExecuteAction(ctx, "op-bad-prep", "alice", reqBad, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected prepareAction error")
	}

	// e) writeItem error on action-operations (chmod 0500)
	chkWerr := Check{
		ID:      "chk-werr",
		Title:   "Check Werr",
		Version: 0,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	reqWerr := ActionRequest{
		Project: store.projectID,
		Type:    "save-check",
		Check:   &chkWerr,
	}
	if err := os.Chmod(opsDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecuteAction(ctx, "op-werr", "alice", reqWerr, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected writeItem error on read-only opsDir")
	}
	_ = os.Chmod(opsDir, 0700)

	// f) Happy path: execute new save-check
	chkHappy := Check{
		ID:      "chk-happy",
		Title:   "Check Happy",
		Version: 0,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	reqHappy := ActionRequest{
		Project: store.projectID,
		Type:    "save-check",
		Check:   &chkHappy,
	}
	resp, err = store.ExecuteAction(ctx, "op-happy", "alice", reqHappy, nil, nil, nil, nil)
	if err != nil || resp.Check == nil || resp.Check.ID != "chk-happy" {
		t.Fatalf("expected success executing action: %v, %+v", err, resp)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Action_ExecuteRunAction_Errors(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)

	// Valid check and query setup
	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	check := Check{
		ID:      "chk-run-err",
		Title:   "Check Run Err",
		Version: 1,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	_ = writeItem(checksDir, check.ID, check)

	req := ActionRequest{
		Project:     store.projectID,
		Type:        "run-check",
		CheckID:     check.ID,
		Environment: "prod",
	}

	// 1. executeRunAction parameter validation
	// runner == nil
	if _, err := store.executeRunAction(ctx, "r-1", "alice", req, core.OperationBinding{}, nil, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil runner: %v", err)
	}
	// req.Environment == ""
	emptyEnvReq := req
	emptyEnvReq.Environment = ""
	dummyRunner := func(ctx context.Context, queryID, env string, inputs map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	if _, err := store.executeRunAction(ctx, "r-1", "alice", emptyEnvReq, core.OperationBinding{}, dummyRunner, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty env: %v", err)
	}
	// result != nil && req.Type != "run-query-validators"
	res := apicontract.Result{}
	if _, err := store.executeRunAction(ctx, "r-1", "alice", req, core.OperationBinding{}, dummyRunner, nil, &res, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when result is provided for run-check: %v", err)
	}

	// 2. core.NewOperationReceipt error (invalid binding)
	binding, _ := store.ActionBinding(req, "alice")
	badBinding := binding
	badBinding.PayloadDigest = "bad"
	if _, err := store.executeRunAction(ctx, "r-1", "alice", req, badBinding, dummyRunner, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on bad binding digest: %v", err)
	}

	opsDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(opsDir, 0700)

	// 3. Existing op in first lock:
	// a) replay mismatch -> ErrConflict
	badReceiptBinding := binding
	badReceiptBinding.PayloadDigest = strings.Repeat("a", 64)
	receiptBad, _ := core.NewOperationReceipt("r-conflict", badReceiptBinding, 0, time.Now().UTC())
	_ = writeItem(opsDir, "r-conflict", actionOperation{Receipt: receiptBad})
	if _, err := store.executeRunAction(ctx, "r-conflict", "alice", req, binding, dummyRunner, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on replay mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "r-conflict.json"))

	// b) replay matches -> ErrOutcomeUncertain
	receiptOk, _ := core.NewOperationReceipt("r-unc", binding, 0, time.Now().UTC())
	_ = writeItem(opsDir, "r-unc", actionOperation{Receipt: receiptOk})
	if _, err := store.executeRunAction(ctx, "r-unc", "alice", req, binding, dummyRunner, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "r-unc.json"))

	// c) safeReadFile error (directory)
	_ = os.MkdirAll(filepath.Join(opsDir, "r-dir.json"), 0700)
	if _, err := store.executeRunAction(ctx, "r-dir", "alice", req, binding, dummyRunner, nil, nil, nil); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(opsDir, "r-dir.json"))

	// 4. Case context errors:
	// a) CaseID not found
	caseReq := req
	caseReq.CaseID = "missing-case"
	caseBinding, _ := store.ActionBinding(caseReq, "alice")
	if _, err := store.executeRunAction(ctx, "r-case-missing", "alice", caseReq, caseBinding, dummyRunner, nil, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}

	// b) Case status != "active" -> ErrInvalid
	casesDir := store.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)
	_ = writeItem(casesDir, "closed-case", Case{ID: "closed-case", Title: "Closed", Status: "closed", Version: 1})
	caseReq.CaseID = "closed-case"
	caseBinding, _ = store.ActionBinding(caseReq, "alice")
	if _, err := store.executeRunAction(ctx, "r-case-closed", "alice", caseReq, caseBinding, dummyRunner, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for closed case: %v", err)
	}
	_ = os.Remove(filepath.Join(casesDir, "closed-case.json"))

	// c) caseContextDigest error
	_ = writeItem(casesDir, "active-case", Case{
		ID:      "active-case",
		Title:   "Active",
		Status:  "active",
		Version: 1,
		Context: map[string]InputValue{"k": {Scalar: ptr(investigation.NewStringValue("v"))}},
	})
	caseContextDigest = func(values map[string]InputValue) (string, error) {
		return "", errors.New("digest fail")
	}
	caseReq.CaseID = "active-case"
	caseBinding, _ = store.ActionBinding(caseReq, "alice")
	if _, err := store.executeRunAction(ctx, "r-digest-fail", "alice", caseReq, caseBinding, dummyRunner, nil, nil, nil); err == nil {
		t.Fatalf("expected caseContextDigest error")
	}
	caseContextDigest = defaultCaseContextDigest

	// 5. writeItem error on action-operations (chmod 0500)
	if err := os.Chmod(opsDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := store.executeRunAction(ctx, "r-werr", "alice", req, binding, dummyRunner, nil, nil, nil); err == nil {
		t.Fatalf("expected writeItem error on read-only opsDir")
	}
	_ = os.Chmod(opsDir, 0700)

	// 6. Evaluate error (via EvaluateQueryValidatorsForResult with invalid result)
	qvReq := ActionRequest{
		Project:     store.projectID,
		Type:        "run-query-validators",
		QueryID:     "valid-q",
		Environment: "default",
	}
	qvBinding, bErr := store.ActionBinding(qvReq, "alice")
	if bErr != nil {
		t.Fatal(bErr)
	}
	emptyResult := apicontract.Result{}
	if _, err := store.executeRunAction(ctx, "r-run-fail", "alice", qvReq, qvBinding, dummyRunner, nil, &emptyResult, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from EvaluateQueryValidatorsForResult: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "r-run-fail.json"))

	// 7. Second lock safeReadFile error
	// Create happy op in first lock, then before second lock, corrupt or chmod
	// We can test the second lock by putting a corrupt file in action-operations
	// or calling second lock directly through a runner that modifies opsDir
	tamperRunner := func(ctx context.Context, queryID, env string, inputs map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		// Corrupt the operation file before second lock
		_ = os.WriteFile(filepath.Join(opsDir, "r-tamper.json"), []byte("{"), 0600)
		return apicontract.Result{}, nil
	}
	if _, err := store.executeRunAction(ctx, "r-tamper", "alice", req, binding, tamperRunner, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on tampered op in second lock: %v", err)
	}
	_ = os.Remove(filepath.Join(opsDir, "r-tamper.json"))
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Action_FinishAction_And_Linking(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)

	// 1. !op.Produced or CheckOperationReplay error -> ErrInvalid
	badOp := actionOperation{Produced: false}
	if err := store.finishAction(ctx, "op-bad", &badOp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for !op.Produced: %v", err)
	}

	// 2. Kind == "run" validation
	receipt := core.OperationReceipt{
		ID:            "r-fin",
		Project:       store.projectID,
		Actor:         "alice",
		Action:        "run-check",
		ResourceID:    "chk-1",
		PayloadDigest: strings.Repeat("a", 64),
		At:            time.Now().UTC(),
	}
	runOp := actionOperation{
		Receipt:  receipt,
		Kind:     "run",
		ID:       "run-id-1",
		Produced: true,
	}
	// Missing Run or ID mismatch -> ErrInvalid
	if err := store.finishAction(ctx, "r-fin", &runOp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on nil run: %v", err)
	}

	run := CheckRun{ID: "run-id-1", CheckID: "chk-1", Environment: "prod", Outcome: "pass"}
	audit := AuditEvent{ID: "run-audit-1", Project: store.projectID, Action: "run-check"}
	runOp.Response.Run = &run
	runOp.RunAudit = &audit

	// 3. Case linking branches:
	// a) Case unavailable (missing case) -> "case-unavailable"
	runOpCase := runOp
	runOpCase.CaseID = "missing-case"
	if err := store.finishAction(ctx, "r-case-unavail", &runOpCase); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runOpCase.CaseLinkStatus != "case-unavailable" {
		t.Fatalf("expected case-unavailable, got %s", runOpCase.CaseLinkStatus)
	}

	// b) Case archived -> "case-archived"
	casesDir := store.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)
	_ = writeItem(casesDir, "archived-case", Case{ID: "archived-case", Title: "Archived", Status: "archived", Version: 1})
	runOpArch := runOp
	runOpArch.CaseID = "archived-case"
	if err := store.finishAction(ctx, "r-case-arch", &runOpArch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runOpArch.CaseLinkStatus != "case-archived" {
		t.Fatalf("expected case-archived, got %s", runOpArch.CaseLinkStatus)
	}

	// c) Case context changed -> "case-changed"
	_ = writeItem(casesDir, "active-case", Case{
		ID:      "active-case",
		Title:   "Active",
		Status:  "active",
		Version: 1,
		Context: map[string]InputValue{"k": {Scalar: ptr(investigation.NewStringValue("new-val"))}},
	})
	runOpChanged := runOp
	runOpChanged.CaseID = "active-case"
	runOpChanged.CaseContextDigest = strings.Repeat("0", 64) // mismatch
	if err := store.finishAction(ctx, "r-case-changed", &runOpChanged); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runOpChanged.CaseLinkStatus != "case-changed" {
		t.Fatalf("expected case-changed, got %s", runOpChanged.CaseLinkStatus)
	}

	// 4. finishActionFile errors:
	// safeReadFile directory error
	_ = os.MkdirAll(filepath.Join(casesDir, "dir-case.json"), 0700)
	if err := store.finishActionFile(casesDir, "dir-case", nil, Case{}); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(casesDir, "dir-case.json"))

	// !jsonSame(current, before) -> ErrConflict
	_ = writeItem(casesDir, "case-conflict", Case{ID: "case-conflict", Title: "Curr", Version: 5})
	if err := store.finishActionFile(casesDir, "case-conflict", []byte(`{"version":1}`), Case{ID: "case-conflict", Title: "Next", Version: 6}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict: %v", err)
	}
	_ = os.Remove(filepath.Join(casesDir, "case-conflict.json"))

	// 5. appendAuditOnce errors:
	if err := store.appendAuditOnce(ctx, AuditEvent{ID: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty audit ID: %v", err)
	}
	failAuditStore := &failingAuditStore{failCase: true}
	storeFail := *store
	storeFail.audit = failAuditStore
	if err := storeFail.appendAuditOnce(ctx, AuditEvent{ID: "audit-fail", Action: "test"}); err == nil {
		t.Fatalf("expected audit error")
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Action_PrepareAction_AllKinds(t *testing.T) {
	ctx := context.Background()
	_ = ctx
	store := testStore(t)
	actor := "alice"

	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)

	// 1. save-scenario in prepareAction:
	// a) req.Scenario == nil -> ErrInvalid
	if _, err := store.prepareAction(ActionRequest{Type: "save-scenario", Scenario: nil}, actor, "op-scen-nil", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on nil scenario: %v", err)
	}

	// b) Happy path for save-scenario
	scen := Scenario{
		ID:      "scen-prep",
		Title:   "Scenario Prep",
		Version: 0,
		Steps:   []ScenarioStep{{ID: "s1", Kind: "query", Ref: "valid-q"}},
	}
	op, err := store.prepareAction(ActionRequest{Type: "save-scenario", Scenario: &scen}, actor, "op-scen-ok", nil)
	if err != nil || op.Response.Scenario == nil || op.Response.Scenario.ID != "scen-prep" {
		t.Fatalf("expected success saving scenario in prepareAction: %v, %+v", err, op)
	}

	// 2. save-query-validators in prepareAction:
	// a) req.QueryValidators == nil -> ErrInvalid
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: nil}, actor, "op-qv-nil", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on nil query validators: %v", err)
	}

	// b) ValidateQueryValidators error (empty validators)
	qvBad := QueryValidators{QueryID: "q1"}
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvBad}, actor, "op-qv-bad", nil); err == nil {
		t.Fatalf("expected ValidateQueryValidators error")
	}

	// c) resolveQueryRef error (missing query)
	qvValid := QueryValidators{
		QueryID: "missing-q",
		Validators: []Validator{{
			ID: "v1", Kind: "non-null", Scope: "row", Severity: "error", OnUnavailable: "fail", Field: "f1",
		}},
	}
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvValid}, actor, "op-qv-res-err", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing query: %v", err)
	}

	// Save query for valid resolution
	queriesDir = filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)
	qvValid.QueryID = "valid-q"

	// d) Existing query-validators: version mismatch -> ErrConflict
	qvDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvDir, 0700)
	_ = writeItem(qvDir, queryValidatorFileID("valid-q"), QueryValidators{
		QueryID: "valid-q",
		Version: 2,
		Validators: []Validator{{
			ID: "v1", Kind: "non-null", Scope: "row", Severity: "error", OnUnavailable: "fail", Field: "f1",
		}},
	})
	qvMismatch := qvValid
	qvMismatch.Version = 1
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvMismatch}, actor, "op-qv-mismatch", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on version mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(qvDir, queryValidatorFileID("valid-q")+".json"))

	// e) New query-validators: version != 0 -> ErrConflict
	qvNewBadVer := qvValid
	qvNewBadVer.Version = 1
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvNewBadVer}, actor, "op-qv-ver1", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new with version != 0: %v", err)
	}

	// f) Happy path for save-query-validators
	qvValid.Version = 0
	op, err = store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvValid}, actor, "op-qv-ok", nil)
	if err != nil || op.Response.QueryValidators == nil || op.Response.QueryValidators.Version != 1 {
		t.Fatalf("expected success in save-query-validators: %v, %+v", err, op)
	}

	// 3. Cases in prepareAction:
	casesDir := store.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)

	// a) create-case: req.Case == nil -> ErrInvalid
	if _, err := store.prepareAction(ActionRequest{Type: "create-case", Case: nil}, actor, "op-cc-nil", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid: %v", err)
	}

	// b) create-case: req.Case.IncidentRefs != 0 -> ErrInvalid
	caseWithInc := Case{ID: "c-inc", Title: "Inc", IncidentRefs: []string{"inc-1"}}
	if _, err := store.prepareAction(ActionRequest{Type: "create-case", Case: &caseWithInc}, actor, "op-cc-inc", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid: %v", err)
	}

	// c) create-case: case already exists -> ErrConflict
	_ = writeItem(casesDir, "c-exist", Case{ID: "c-exist", Title: "Exist", Status: "active", Version: 1})
	caseExist := Case{ID: "c-exist", Title: "Exist"}
	if _, err := store.prepareAction(ActionRequest{Type: "create-case", Case: &caseExist}, actor, "op-cc-exist", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on existing case: %v", err)
	}

	// d) create-case: capacity exceeded -> ErrCapacity
	for i := 0; i < maxItems-1; i++ {
		id := fmt.Sprintf("case-cap-%03d", i)
		_ = writeItem(casesDir, id, Case{ID: id, Title: id, Status: "active", Version: 1})
	}
	caseOverflow := Case{ID: "case-overflow", Title: "Overflow"}
	if _, err := store.prepareAction(ActionRequest{Type: "create-case", Case: &caseOverflow}, actor, "op-cc-cap", nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity: %v", err)
	}
	for i := 0; i < maxItems-1; i++ {
		_ = os.Remove(filepath.Join(casesDir, fmt.Sprintf("case-cap-%03d.json", i)))
	}

	// e) update-case: IncidentRefs changed -> ErrInvalid
	caseUpdateBadInc := Case{ID: "c-exist", Title: "Exist", Version: 1, IncidentRefs: []string{"different-inc"}}
	if _, err := store.prepareAction(ActionRequest{Type: "update-case", Case: &caseUpdateBadInc}, actor, "op-uc-inc", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on changed IncidentRefs: %v", err)
	}

	// f) apply-scenario in prepareAction (exercises sha256Sum!)
	scenDir := store.definitionDir("scenarios")
	_ = os.MkdirAll(scenDir, 0700)
	_ = writeItem(scenDir, "scen-apply", Scenario{
		ID:      "scen-apply",
		Title:   "Apply",
		Version: 1,
		Steps:   []ScenarioStep{{ID: "s1", Kind: "query", Ref: "valid-q"}},
	})
	op, err = store.prepareAction(ActionRequest{
		Type:       "apply-scenario",
		CaseID:     "c-exist",
		ScenarioID: "scen-apply",
	}, actor, "op-apply-scen", nil)
	if err != nil || op.Response.Case == nil {
		t.Fatalf("expected success applying scenario: %v, %+v", err, op)
	}

	// g) append-event with link-evidence
	runsDir := store.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)
	_ = writeItem(runsDir, "run-ev", CheckRun{ID: "run-ev", CheckID: "chk-1", Environment: "prod", Outcome: "pass"})
	cohortDir := store.privateItemDir("run-cohorts")
	_ = os.MkdirAll(cohortDir, 0700)
	dig, _ := defaultCaseContextDigest(nil)
	_ = writeItem(cohortDir, "run-ev", runCohortProof{RunID: "run-ev", CaseID: "c-exist", ContextDigest: dig})

	ev := CaseEvent{Type: "link-evidence", Ref: "run-ev"}
	op, err = store.prepareAction(ActionRequest{
		Type:   "append-event",
		CaseID: "c-exist",
		Event:  &ev,
	}, actor, "op-append-ev", nil)
	if err != nil || op.Response.Case == nil {
		t.Fatalf("expected success appending event: %v, %+v", err, op)
	}

	// h) Unknown action type -> ErrInvalid
	if _, err := store.prepareAction(ActionRequest{Type: "unsupported-action"}, actor, "op-unknown", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for unknown action: %v", err)
	}
}

// Not parallel: tests filesystem modifications and package-level seams.
func TestCoverage100_Action_DeepBranches_Part1(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	actor := "alice"

	// 1. reconcilePendingActions:
	// a) ensureDirIfExists error when action-operations is a file
	opsPath := store.privateItemDir("action-operations")
	_ = os.RemoveAll(opsPath)
	_ = os.WriteFile(opsPath, []byte("file"), 0600)
	if err := store.reconcilePendingActions(ctx, "checks", "c1", ""); err == nil {
		t.Fatalf("expected ensureDirIfExists error when dir is file")
	}
	_ = os.Remove(opsPath)

	// b) os.Open error when action-operations has no read perms (chmod 0000)
	_ = os.MkdirAll(opsPath, 0000)
	if err := store.reconcilePendingActions(ctx, "checks", "c1", ""); err == nil {
		t.Fatalf("expected os.Open error on unreadable dir")
	}
	_ = os.Chmod(opsPath, 0700)

	// c) actionReaddirnames error seam (line 159)
	oldReaddir := actionReaddirnames
	actionReaddirnames = func(f *os.File, n int) ([]string, error) {
		return nil, errors.New("simulated readdir error")
	}
	if err := store.reconcilePendingActions(ctx, "checks", "c1", ""); err == nil {
		t.Fatalf("expected actionReaddirnames error")
	}
	actionReaddirnames = oldReaddir

	// 2. ExecuteAction:
	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)

	chk := Check{
		ID:      "chk-rep",
		Title:   "Check Rep",
		Version: 0,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	req := ActionRequest{
		Project: store.projectID,
		Type:    "save-check",
		Check:   &chk,
	}
	binding, err := store.ActionBinding(req, actor)
	if err != nil {
		t.Fatal(err)
	}

	// a) safeReadFile error other than ErrNotExist (e.g. dir instead of file)
	_ = os.MkdirAll(filepath.Join(opsPath, "op-dir.json"), 0700)
	if _, err := store.ExecuteAction(ctx, "op-dir", actor, req, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(opsPath, "op-dir.json"))

	// b) Existing op with corrupt JSON -> ErrConflict
	_ = os.WriteFile(filepath.Join(opsPath, "op-corrupt.json"), []byte("{corrupt"), 0600)
	if _, err := store.ExecuteAction(ctx, "op-corrupt", actor, req, nil, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on corrupt existing op: %v", err)
	}
	_ = os.Remove(filepath.Join(opsPath, "op-corrupt.json"))

	// c) Existing op with replay check failure -> ErrConflict
	unmatchedReceipt := core.OperationReceipt{
		ID:            "op-unmatched",
		Project:       "other-proj",
		Actor:         actor,
		Action:        req.Type,
		ResourceID:    "chk-rep",
		PayloadDigest: binding.PayloadDigest,
		At:            time.Now().UTC(),
	}
	_ = writeItem(opsPath, "op-unmatched", actionOperation{Receipt: unmatchedReceipt})
	if _, err := store.ExecuteAction(ctx, "op-unmatched", actor, req, nil, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on replay failure: %v", err)
	}
	_ = os.Remove(filepath.Join(opsPath, "op-unmatched.json"))

	// d) Existing op not produced (!op.Produced) -> ErrOutcomeUncertain
	matchedReceipt := core.OperationReceipt{
		ID:            "op-unprod",
		Project:       binding.Project,
		Actor:         binding.Actor,
		Action:        binding.Action,
		ResourceID:    binding.ResourceID,
		PayloadDigest: binding.PayloadDigest,
		At:            time.Now().UTC(),
	}
	_ = writeItem(opsPath, "op-unprod", actionOperation{Receipt: matchedReceipt, Produced: false})
	if _, err := store.ExecuteAction(ctx, "op-unprod", actor, req, nil, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain on unproduced op: %v", err)
	}
	_ = os.Remove(filepath.Join(opsPath, "op-unprod.json"))

	// e) Existing op produced -> finishAction (replay)
	_ = writeItem(opsPath, "op-replay", actionOperation{
		Receipt:  matchedReceipt,
		Produced: true,
		Kind:     "checks",
		ID:       "chk-rep",
		Response: ActionResponse{Check: &chk},
	})
	if resp, err := store.ExecuteAction(ctx, "op-replay", actor, req, nil, nil, nil, nil); err != nil || resp.Check == nil {
		t.Fatalf("expected replay success: %v, %+v", err, resp)
	}
	_ = os.Remove(filepath.Join(opsPath, "op-replay.json"))

	// f) actionNewOperationReceipt error seam
	oldNewReceipt := actionNewOperationReceipt
	actionNewOperationReceipt = func(id string, b core.OperationBinding, v int64, at time.Time) (core.OperationReceipt, error) {
		return core.OperationReceipt{}, errors.New("simulated receipt error")
	}
	if _, err := store.ExecuteAction(ctx, "op-receipt-fail", actor, req, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected receipt error from seam")
	}
	actionNewOperationReceipt = oldNewReceipt

	// g) actionSafeReadFile seam in second lock (lines 263-272):
	oldSafeRead := actionSafeReadFile
	// i) safeReadFile error other than ErrNotExist (line 272)
	actionSafeReadFile = func(dir, id string) ([]byte, error) {
		return nil, errors.New("simulated read error")
	}
	if _, err := store.ExecuteAction(ctx, "op-lock2-err", actor, req, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected safeReadFile error in second lock")
	}

	// ii) safeReadFile returns corrupt data (line 263-264)
	actionSafeReadFile = func(dir, id string) ([]byte, error) {
		return []byte("{corrupt"), nil
	}
	if _, err := store.ExecuteAction(ctx, "op-lock2-corrupt", actor, req, nil, nil, nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict in second lock: %v", err)
	}

	// iii) safeReadFile returns !Produced (lines 266-267)
	unprodData, _ := json.Marshal(actionOperation{Receipt: matchedReceipt, Produced: false})
	actionSafeReadFile = func(dir, id string) ([]byte, error) {
		return unprodData, nil
	}
	if _, err := store.ExecuteAction(ctx, "op-lock2-unprod", actor, req, nil, nil, nil, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain in second lock: %v", err)
	}

	// iv) safeReadFile returns Produced -> finishAction replay (line 269)
	prodData, _ := json.Marshal(actionOperation{
		Receipt:  matchedReceipt,
		Produced: true,
		Kind:     "checks",
		ID:       "chk-rep",
		Response: ActionResponse{Check: &chk},
	})
	actionSafeReadFile = func(dir, id string) ([]byte, error) {
		return prodData, nil
	}
	if resp, err := store.ExecuteAction(ctx, "op-lock2-prod", actor, req, nil, nil, nil, nil); err != nil || resp.Check == nil {
		t.Fatalf("expected replay success in second lock: %v, %+v", err, resp)
	}
	actionSafeReadFile = oldSafeRead

	// 3. executeRunAction:
	runCheck := Check{
		ID:      "chk-run-more",
		Title:   "Run More",
		Version: 1,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	_ = writeItem(checksDir, runCheck.ID, runCheck)
	runReq := ActionRequest{
		Project:     store.projectID,
		Type:        "run-check",
		CheckID:     runCheck.ID,
		Environment: "default",
	}
	runBinding, err := store.ActionBinding(runReq, actor)
	if err != nil {
		t.Fatal(err)
	}
	dummyRunner := func(ctx context.Context, queryID, env string, inputs map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	_ = dummyRunner

	// a) executeRunAction: safeReadFile error in second lock (dir instead of file)
	runnerCreateDir := func(ctx context.Context, queryID, env string, inputs map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		_ = os.Remove(filepath.Join(opsPath, "r-lock2-dir.json"))
		_ = os.MkdirAll(filepath.Join(opsPath, "r-lock2-dir.json"), 0700)
		return apicontract.Result{}, nil
	}
	if _, err := store.executeRunAction(ctx, "r-lock2-dir", actor, runReq, runBinding, runnerCreateDir, nil, nil, nil); err == nil {
		t.Fatalf("expected safeReadFile error in second lock")
	}
	_ = os.RemoveAll(filepath.Join(opsPath, "r-lock2-dir.json"))

	// b) executeRunAction: writeItem error in second lock (chmod opsPath 0500)
	runnerChmod := func(ctx context.Context, queryID, env string, inputs map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		_ = os.Chmod(opsPath, 0500)
		return apicontract.Result{}, nil
	}
	if _, err := store.executeRunAction(ctx, "r-lock2-werr", actor, runReq, runBinding, runnerChmod, nil, nil, nil); err == nil {
		t.Fatalf("expected writeItem error in second lock")
	}
	_ = os.Chmod(opsPath, 0700)
	_ = os.Remove(filepath.Join(opsPath, "r-lock2-werr.json"))

	// 4. finishActionFile json.Marshal error with unsupported type
	if err := store.finishActionFile(store.definitionDir("checks"), "c1", nil, make(chan int)); err == nil {
		t.Fatalf("expected json.Marshal error for channel")
	}
}

// Not parallel: tests filesystem modifications and package-level seams.
func TestCoverage100_Action_DeepBranches_Part2(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	opsPath := store.privateItemDir("action-operations")
	casesDir := store.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)
	runsDir := store.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)
	cohortsDir := store.privateItemDir("run-cohorts")
	_ = os.MkdirAll(cohortsDir, 0700)

	id := "run-act-finish"
	baseReceipt := core.OperationReceipt{
		ID:            id,
		Project:       store.projectID,
		Actor:         "alice",
		Action:        "run-check",
		ResourceID:    "chk-1",
		PayloadDigest: strings.Repeat("c", 64),
		At:            time.Now().UTC(),
	}
	baseRun := CheckRun{
		ID:          id,
		CheckID:     "chk-1",
		Environment: "default",
		Outcome:     "pass",
	}
	baseAudit := AuditEvent{
		ID:      "audit-" + id,
		Project: store.projectID,
		Actor:   "alice",
		Action:  "run-check",
		At:      time.Now().UTC(),
	}

	makeOp := func(subID string) actionOperation {
		rcpt := baseReceipt
		rcpt.ID = subID
		r := baseRun
		r.ID = subID
		a := baseAudit
		a.ID = "audit-" + subID
		return actionOperation{
			ID:       subID,
			Kind:     "run",
			Produced: true,
			Receipt:  rcpt,
			Response: ActionResponse{Run: &r},
			RunAudit: &a,
		}
	}

	// 1. saveRunCohortProof error (chmod cohortsDir 0500)
	op1 := makeOp("run-finish-1")
	op1.CaseID = "c1"
	op1.CaseContextDigest = "dig"
	_ = os.Chmod(cohortsDir, 0500)
	if err := store.finishAction(ctx, op1.ID, &op1); err == nil {
		t.Fatalf("expected saveRunCohortProof error on read-only cohortsDir")
	}
	_ = os.Chmod(cohortsDir, 0700)

	// 2. finishActionFile error on runs (chmod runsDir 0500)
	op2 := makeOp("run-finish-2")
	_ = os.Chmod(runsDir, 0500)
	if err := store.finishAction(ctx, op2.ID, &op2); err == nil {
		t.Fatalf("expected finishActionFile error on read-only runsDir")
	}
	_ = os.Chmod(runsDir, 0700)

	// 3. appendAuditOnce error (failing audit store)
	op3 := makeOp("run-finish-3")
	storeFailAudit := *store
	storeFailAudit.audit = &failingAuditStore{failRun: true}
	if err := storeFailAudit.finishAction(ctx, op3.ID, &op3); err == nil {
		t.Fatalf("expected appendAuditOnce error")
	}

	// 4. getCase error other than ErrNotFound (directory in casesDir)
	op4 := makeOp("run-finish-4")
	op4.CaseID = "c-dir-err"
	_ = os.MkdirAll(filepath.Join(casesDir, "c-dir-err.json"), 0700)
	if err := store.finishAction(ctx, op4.ID, &op4); err == nil {
		t.Fatalf("expected getCase error on directory")
	}
	_ = os.RemoveAll(filepath.Join(casesDir, "c-dir-err.json"))

	// 5. caseContextDigest error (via seam)
	op5 := makeOp("run-finish-5")
	op5.CaseID = "c-digest-err"
	op5.CaseContextDigest = strings.Repeat("f", 64)
	_ = writeItem(casesDir, "c-digest-err", Case{ID: "c-digest-err", Title: "Title", Status: "active", Version: 1})
	oldDigest := caseContextDigest
	caseContextDigest = func(values map[string]InputValue) (string, error) {
		return "", errors.New("simulated digest error")
	}
	if err := store.finishAction(ctx, op5.ID, &op5); err == nil || !strings.Contains(err.Error(), "simulated digest error") {
		t.Fatalf("expected simulated digest error: %v", err)
	}
	caseContextDigest = oldDigest
	_ = os.Remove(filepath.Join(casesDir, "c-digest-err.json"))

	// 6. authorizeCaseRunLink error other than ErrInvalid (unreadable cohort file)
	op6 := makeOp("run-finish-6")
	op6.CaseID = "c-auth-err"
	_ = writeItem(casesDir, "c-auth-err", Case{ID: "c-auth-err", Title: "Title", Status: "active", Version: 1})
	op6.Response.Run.CaseID = "c-auth-err"
	cohortFile := filepath.Join(cohortsDir, op6.ID+".json")
	_ = os.WriteFile(cohortFile, []byte("{}"), 0600)
	_ = os.Chmod(cohortFile, 0000)
	if err := store.finishAction(ctx, op6.ID, &op6); err == nil {
		t.Fatalf("expected authorizeCaseRunLink error on unreadable cohort file")
	}
	_ = os.Chmod(cohortFile, 0600)
	_ = os.Remove(cohortFile)
	_ = os.Remove(filepath.Join(casesDir, "c-auth-err.json"))

	// 7. authorizeCaseRunLink returns ErrInvalid -> CaseLinkStatus = "case-changed"
	op7 := makeOp("run-finish-7")
	op7.CaseID = "c-changed"
	_ = writeItem(casesDir, "c-changed", Case{ID: "c-changed", Title: "Title", Status: "active", Version: 1})
	op7.Response.Run.CaseID = "other-case"
	if err := store.finishAction(ctx, op7.ID, &op7); err != nil || op7.CaseLinkStatus != "case-changed" {
		t.Fatalf("expected case-changed status: %v, %s", err, op7.CaseLinkStatus)
	}
	_ = os.Remove(filepath.Join(casesDir, "c-changed.json"))

	// 8. core.AttachRun error -> CaseLinkStatus = "link-unavailable"
	op8 := makeOp("run-finish-8")
	op8.CaseID = "c-attach-err"
	_ = writeItem(casesDir, "c-attach-err", Case{ID: "c-attach-err", Title: "Title", Status: "active", Version: 1})
	op8.Response.Run.CaseID = ""
	if err := store.finishAction(ctx, op8.ID, &op8); err != nil || op8.CaseLinkStatus != "link-unavailable" {
		t.Fatalf("expected link-unavailable status: %v, %s", err, op8.CaseLinkStatus)
	}
	_ = os.Remove(filepath.Join(casesDir, "c-attach-err.json"))

	// 9. compactCaseTimeline error: len(Timeline) > 400 and chmod case-timeline 0500
	op9 := makeOp("run-finish-9")
	op9.CaseID = "c-compact-err"
	timeline := make([]CaseEvent, 401)
	for i := range timeline {
		timeline[i] = CaseEvent{ID: fmt.Sprintf("ev-%d", i), Type: "note", At: time.Now().UTC()}
	}
	_ = writeItem(casesDir, "c-compact-err", Case{
		ID:       "c-compact-err",
		Title:    "Title",
		Status:   "active",
		Version:  1,
		Timeline: timeline,
	})
	op9.Response.Run.CaseID = "c-compact-err"
	dig, _ := defaultCaseContextDigest(nil)
	_ = writeItem(cohortsDir, op9.ID, runCohortProof{RunID: op9.ID, CaseID: "c-compact-err", ContextDigest: dig})
	timelineDir := store.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	_ = os.Chmod(timelineDir, 0500)
	if err := store.finishAction(ctx, op9.ID, &op9); err == nil {
		t.Fatalf("expected compactCaseTimeline error on read-only timelineDir")
	}
	_ = os.Chmod(timelineDir, 0700)
	_ = os.Remove(filepath.Join(cohortsDir, op9.ID+".json"))
	_ = os.Remove(filepath.Join(casesDir, "c-compact-err.json"))

	// 10. writeItem error on action-operations at line 489
	op10 := makeOp("run-finish-10")
	op10.CaseID = "c-werr-489"
	_ = writeItem(casesDir, "c-werr-489", Case{ID: "c-werr-489", Title: "Title", Status: "active", Version: 1})
	op10.Response.Run.CaseID = ""
	_ = os.Chmod(opsPath, 0500)
	if err := store.finishAction(ctx, op10.ID, &op10); err == nil {
		t.Fatalf("expected writeItem error on action-operations at line 489")
	}
	_ = os.Chmod(opsPath, 0700)
	_ = os.Remove(filepath.Join(casesDir, "c-werr-489.json"))

	// 11. finishActionFile error at line 505 (chmod checksDir 0500)
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	op11 := actionOperation{
		ID:       "chk-mut-fail",
		Kind:     "checks",
		Produced: true,
		Receipt: core.OperationReceipt{
			ID:            "chk-mut-fail",
			Project:       store.projectID,
			Actor:         "alice",
			Action:        "save-check",
			ResourceID:    "chk-mut-fail",
			PayloadDigest: strings.Repeat("d", 64),
			At:            time.Now().UTC(),
		},
		After: []byte(`{"id":"chk-mut-fail","title":"T"}`),
	}
	_ = os.Chmod(checksDir, 0500)
	if err := store.finishAction(ctx, "chk-mut-fail", &op11); err == nil {
		t.Fatalf("expected finishActionFile error on read-only checksDir")
	}
	_ = os.Chmod(checksDir, 0700)

	// 12. writeItem error on action-operations at line 513 -> ErrOutcomeUncertain
	op12 := actionOperation{
		ID:       "chk-unc-fail",
		Kind:     "checks",
		Produced: true,
		Receipt: core.OperationReceipt{
			ID:            "chk-unc-fail",
			Project:       store.projectID,
			Actor:         "alice",
			Action:        "save-check",
			ResourceID:    "chk-unc-fail",
			PayloadDigest: strings.Repeat("e", 64),
			At:            time.Now().UTC(),
		},
	}
	_ = os.Chmod(opsPath, 0500)
	if err := store.finishAction(ctx, "chk-unc-fail", &op12); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from line 513: %v", err)
	}
	_ = os.Chmod(opsPath, 0700)
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Action_DeepBranches_Part3(t *testing.T) {
	ctx := context.Background()
	_ = ctx
	store := testStore(t)
	actor := "alice"

	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "q1.query.json"), []byte(`{"id":"q1"}`), 0600)
	_ = os.WriteFile(filepath.Join(queriesDir, "q2.query.json"), []byte(`{"id":"q2"}`), 0600)

	qvDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvDir, 0700)
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	validatorsDir := store.definitionDir("validators")
	_ = os.MkdirAll(validatorsDir, 0700)
	casesDir := store.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)
	runsDir := store.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)

	// 1. save-query-validators:
	// a) update existing query-validators (Before != nil, item.Version++)
	qvExisting := QueryValidators{
		QueryID: "q1",
		Version: 1,
		Validators: []Validator{{
			ID: "v1", Kind: "non-null", Scope: "row", Severity: "error", OnUnavailable: "fail", Field: "f1",
		}},
	}
	_ = writeItem(qvDir, queryValidatorFileID("q1"), qvExisting)
	// Write check in checksDir and another local in qvDir to exercise loop bodies cm[v.ID] = v and qm[v.QueryID] = v
	_ = writeItem(checksDir, "chk-in-dir", Check{
		ID:      "chk-in-dir",
		Title:   "In Dir",
		Version: 1,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "q1"}},
	})
	_ = writeItem(qvDir, queryValidatorFileID("q2"), QueryValidators{
		QueryID: "q2",
		Version: 1,
		Validators: []Validator{{
			ID: "v2", Kind: "non-null", Scope: "row", Severity: "error", OnUnavailable: "fail", Field: "f2",
		}},
	})
	qvUpdate := qvExisting
	op, err := store.prepareAction(ActionRequest{
		Type:            "save-query-validators",
		QueryValidators: &qvUpdate,
	}, actor, "op-qv-up", nil)
	if err != nil || op.Response.QueryValidators == nil || op.Response.QueryValidators.Version != 2 || op.Before == nil {
		t.Fatalf("expected success updating query-validators: %v, %+v", err, op)
	}

	// b) safeReadFile error other than ErrNotExist (dir instead of file)
	_ = os.MkdirAll(filepath.Join(qvDir, queryValidatorFileID("q-dir-err")+".json"), 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "q-dir-err.query.json"), []byte(`{"id":"q-dir-err"}`), 0600)
	qvDirErr := QueryValidators{
		QueryID: "q-dir-err",
		Validators: []Validator{{
			ID: "v1", Kind: "non-null", Scope: "row", Severity: "error", OnUnavailable: "fail", Field: "f1",
		}},
	}
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvDirErr}, actor, "op-qv-dir-err", nil); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(qvDir, queryValidatorFileID("q-dir-err")+".json"))

	// c) readItems[Check] error
	_ = os.WriteFile(filepath.Join(checksDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvUpdate}, actor, "op-qv-chk-err", nil); err == nil {
		t.Fatalf("expected readItems[Check] error")
	}
	_ = os.Remove(filepath.Join(checksDir, "bad..slug.json"))

	// d) readItems[PublishedValidator] error
	_ = os.WriteFile(filepath.Join(validatorsDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvUpdate}, actor, "op-qv-val-err", nil); err == nil {
		t.Fatalf("expected readItems[PublishedValidator] error")
	}
	_ = os.Remove(filepath.Join(validatorsDir, "bad..slug.json"))

	// e) readItems[QueryValidators] error
	_ = os.WriteFile(filepath.Join(qvDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvUpdate}, actor, "op-qv-loc-err", nil); err == nil {
		t.Fatalf("expected readItems[QueryValidators] error")
	}
	_ = os.Remove(filepath.Join(qvDir, "bad..slug.json"))

	// f) validateGraph error (check references nonexistent published validator or check)
	_ = writeItem(checksDir, "chk-bad-graph", Check{
		ID:      "chk-bad-graph",
		Title:   "Bad Graph",
		Version: 1,
		Steps:   []Step{{ID: "s1", Kind: "check", CheckID: "nonexistent-chk"}},
	})
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvUpdate}, actor, "op-qv-bad-graph", nil); err == nil {
		t.Fatalf("expected validateGraph error")
	}
	_ = os.Remove(filepath.Join(checksDir, "chk-bad-graph.json"))

	// g) ensureQueryRefs error (check references nonexistent query)
	_ = writeItem(checksDir, "chk-missing-q", Check{
		ID:      "chk-missing-q",
		Title:   "Missing Q",
		Version: 1,
		Steps:   []Step{{ID: "s1", Kind: "query", QueryID: "missing-query-id"}},
	})
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvUpdate}, actor, "op-qv-miss-q", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from ensureQueryRefs: %v", err)
	}
	_ = os.Remove(filepath.Join(checksDir, "chk-missing-q.json"))

	// 2. Cases and Scenarios in prepareAction:
	// a) getCase error for update-case when case not found
	caseNotFound := Case{ID: "c-not-found", Title: "Not Found"}
	if _, err := store.prepareAction(ActionRequest{Type: "update-case", Case: &caseNotFound}, actor, "op-uc-404", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for nonexistent case: %v", err)
	}

	// b) create-case getCase error other than ErrNotFound (dir in casesDir)
	_ = os.MkdirAll(filepath.Join(casesDir, "c-dir-case.json"), 0700)
	cDirCase := Case{ID: "c-dir-case", Title: "Dir Case"}
	if _, err := store.prepareAction(ActionRequest{Type: "create-case", Case: &cDirCase}, actor, "op-cc-dir", nil); err == nil {
		t.Fatalf("expected getCase error on directory")
	}
	_ = os.RemoveAll(filepath.Join(casesDir, "c-dir-case.json"))

	// c) create-case readItems[Case] error
	_ = os.WriteFile(filepath.Join(casesDir, "bad..slug.json"), []byte("{}"), 0600)
	cGood := Case{ID: "c-good", Title: "Good Case"}
	if _, err := store.prepareAction(ActionRequest{Type: "create-case", Case: &cGood}, actor, "op-cc-read-err", nil); err == nil {
		t.Fatalf("expected readItems[Case] error in create-case")
	}
	_ = os.Remove(filepath.Join(casesDir, "bad..slug.json"))

	// d) update-case with req.Case == nil -> ErrInvalid
	_ = writeItem(casesDir, "c-base", Case{ID: "c-base", Title: "Base", Status: "active", Version: 1})
	if _, err := store.prepareAction(ActionRequest{Type: "update-case", CaseID: "c-base", Case: nil}, actor, "op-uc-nil", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Case in update-case: %v", err)
	}

	// e) apply-scenario: safeReadFile error other than ErrNotExist (dir in scenariosDir)
	scenDir := store.definitionDir("scenarios")
	_ = os.MkdirAll(scenDir, 0700)
	_ = os.MkdirAll(filepath.Join(scenDir, "scen-dir.json"), 0700)
	if _, err := store.prepareAction(ActionRequest{Type: "apply-scenario", CaseID: "c-base", ScenarioID: "scen-dir"}, actor, "op-as-dir", nil); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(scenDir, "scen-dir.json"))

	// f) apply-scenario: corrupt JSON
	_ = os.WriteFile(filepath.Join(scenDir, "scen-corrupt.json"), []byte("{corrupt"), 0600)
	if _, err := store.prepareAction(ActionRequest{Type: "apply-scenario", CaseID: "c-base", ScenarioID: "scen-corrupt"}, actor, "op-as-corrupt", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on corrupt scenario JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(scenDir, "scen-corrupt.json"))

	// 3. record-scenario-step:
	// Setup case with ScenarioApplication having query step and check step
	appliedScen := Scenario{
		ID:    "scen-app",
		Title: "Applied Scenario",
		Steps: []ScenarioStep{
			{ID: "step-q", Kind: "query", Ref: "q1"},
			{ID: "step-chk", Kind: "check", Ref: "chk-in-dir"},
		},
	}
	cWithApp := Case{
		ID:      "c-app",
		Title:   "Case With App",
		Status:  "active",
		Version: 1,
		ScenarioApplications: []ScenarioApplication{
			{
				ID: "app-other", // Exercises loop continue when application.ID != req.ApplicationID
			},
			{
				ID:       "app-1",
				Snapshot: appliedScen,
				Steps: []ScenarioStepReceipt{
					{StepID: "step-q", Status: "pending"},
					{StepID: "step-chk", Status: "pending"},
				},
			},
		},
		RunRefs: []string{"run-already-linked"},
	}
	_ = writeItem(casesDir, "c-app", cWithApp)

	// a) proof != nil, but application.ID loop continues over app-other:
	proof := ScenarioQueryProof{
		Execution:       apicontract.ExecutionRef{StoreID: "p", ProjectID: store.projectID, ExecutionID: "exec-1"},
		QueryID:         "q1",
		Environment:     "default",
		ExecutionStatus: "completed",
	}
	op, err = store.prepareAction(ActionRequest{
		Type:          "record-scenario-step",
		CaseID:        "c-app",
		ApplicationID: "app-1",
		StepID:        "step-q",
	}, actor, "op-rss-proof", &proof)
	if err != nil || op.Response.Case == nil {
		t.Fatalf("expected success with proof and multi-app: %v, %+v", err, op)
	}

	// Update cWithApp so step-q is completed for testing step-chk
	cWithApp.ScenarioApplications[1].Steps[0].Status = "completed"
	_ = writeItem(casesDir, "c-app", cWithApp)

	// b) proof == nil:
	// i) req.StepStatus != "completed" (e.g. "skipped") -> run is nil
	op, err = store.prepareAction(ActionRequest{
		Type:          "record-scenario-step",
		CaseID:        "c-app",
		ApplicationID: "app-1",
		StepID:        "step-chk",
		StepStatus:    "skipped",
	}, actor, "op-rss-skip", nil)
	if err != nil || op.Response.Case == nil {
		t.Fatalf("expected success recording skipped step: %v, %+v", err, op)
	}

	// ii) req.StepStatus == "completed", but getRun fails (run not found)
	if _, err := store.prepareAction(ActionRequest{
		Type:          "record-scenario-step",
		CaseID:        "c-app",
		ApplicationID: "app-1",
		StepID:        "step-chk",
		StepStatus:    "completed",
		RunID:         "missing-run",
	}, actor, "op-rss-norun", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing run: %v", err)
	}

	// iii) req.StepStatus == "completed", authorizeCaseRunLink fails (unauthorized)
	_ = writeItem(runsDir, "run-other-case", CheckRun{
		ID:          "run-other-case",
		CheckID:     "chk-in-dir",
		CaseID:      "different-case",
		Environment: "default",
		Outcome:     "pass",
	})
	if _, err := store.prepareAction(ActionRequest{
		Type:          "record-scenario-step",
		CaseID:        "c-app",
		ApplicationID: "app-1",
		StepID:        "step-chk",
		StepStatus:    "completed",
		RunID:         "run-other-case",
	}, actor, "op-rss-auth-fail", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from authorizeCaseRunLink: %v", err)
	}

	// iv) req.StepStatus == "completed", linked = false (run not in old.RunRefs)
	// Standalone run (CaseID == "") authorizes link, but RecordScenarioStep fails because !runLinked
	_ = writeItem(runsDir, "run-valid-step", CheckRun{
		ID:              "run-valid-step",
		CheckID:         "chk-in-dir",
		Environment:     "default",
		ExecutionStatus: "completed",
		Outcome:         "pass",
	})
	if _, err := store.prepareAction(ActionRequest{
		Type:          "record-scenario-step",
		CaseID:        "c-app",
		ApplicationID: "app-1",
		StepID:        "step-chk",
		StepStatus:    "completed",
		RunID:         "run-valid-step",
	}, actor, "op-rss-unlinked", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when linked is false: %v", err)
	}

	// v) req.StepStatus == "completed", linked = true (run in old.RunRefs)
	dig, _ := defaultCaseContextDigest(nil)
	cohortsDir := store.privateItemDir("run-cohorts")
	_ = writeItem(cohortsDir, "run-already-linked", runCohortProof{
		RunID:         "run-already-linked",
		CaseID:        "c-app",
		ContextDigest: dig,
	})
	_ = writeItem(runsDir, "run-already-linked", CheckRun{
		ID:              "run-already-linked",
		Kind:            "check",
		CheckID:         "chk-in-dir",
		CaseID:          "c-app",
		Environment:     "default",
		ExecutionStatus: "completed",
		Outcome:         "pass",
	})
	op, err = store.prepareAction(ActionRequest{
		Type:          "record-scenario-step",
		CaseID:        "c-app",
		ApplicationID: "app-1",
		StepID:        "step-chk",
		StepStatus:    "completed",
		RunID:         "run-already-linked",
	}, actor, "op-rss-linked", nil)
	if err != nil || op.Response.Case == nil {
		t.Fatalf("expected success recording completed step with linked=true: %v, %+v", err, op)
	}

	// 4. append-event:
	// a) req.Event == nil -> ErrInvalid
	if _, err := store.prepareAction(ActionRequest{Type: "append-event", CaseID: "c-app", Event: nil}, actor, "op-ae-nil", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Event: %v", err)
	}

	// b) link-evidence with missing run -> getRun error
	evMiss := CaseEvent{Type: "link-evidence", Ref: "nonexistent-run"}
	if _, err := store.prepareAction(ActionRequest{Type: "append-event", CaseID: "c-app", Event: &evMiss}, actor, "op-ae-norun", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing run in link-evidence: %v", err)
	}

	// c) link-evidence with unauthorized run -> authorizeCaseRunLink error
	evUnauth := CaseEvent{Type: "link-evidence", Ref: "run-other-case"}
	if _, err := store.prepareAction(ActionRequest{Type: "append-event", CaseID: "c-app", Event: &evUnauth}, actor, "op-ae-unauth", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for unauth run in link-evidence: %v", err)
	}

	// d) core.AppendCaseEvent error (e.g. invalid event Type)
	evBadType := CaseEvent{Type: "unsupported-event-type"}
	if _, err := store.prepareAction(ActionRequest{Type: "append-event", CaseID: "c-app", Event: &evBadType}, actor, "op-ae-bad-type", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad event type: %v", err)
	}

	// e) compactCaseTimeline error in prepareAction
	cBigTimeline := Case{
		ID:       "c-big-timeline",
		Title:    "Big Timeline",
		Status:   "active",
		Version:  1,
		Timeline: make([]CaseEvent, 401),
	}
	for i := range cBigTimeline.Timeline {
		cBigTimeline.Timeline[i] = CaseEvent{ID: fmt.Sprintf("ev-%d", i), Type: "note", At: time.Now().UTC()}
	}
	_ = writeItem(casesDir, "c-big-timeline", cBigTimeline)
	timelineDir := store.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	_ = os.Chmod(timelineDir, 0500)
	evNote := CaseEvent{Type: "note", Message: "hello"}
	if _, err := store.prepareAction(ActionRequest{Type: "append-event", CaseID: "c-big-timeline", Event: &evNote}, actor, "op-ae-compact-err", nil); err == nil {
		t.Fatalf("expected compactCaseTimeline error on read-only timelineDir")
	}
	_ = os.Chmod(timelineDir, 0700)

	// 5. save-query-validators: len(locals) >= maxItems -> ErrCapacity
	qvCapDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvCapDir, 0700)
	existing, _ := os.ReadDir(qvCapDir)
	needed := maxItems - len(existing)
	var qvCreated []string
	validVals := []Validator{{ID: "present", Scope: "row", Kind: "non-null", Severity: "error", Field: "invoice_id"}}
	for i := 0; i < needed; i++ {
		name := fmt.Sprintf("qv-cap-%04d", i)
		_ = writeItem(qvCapDir, name, QueryValidators{QueryID: name, Version: 1, Validators: validVals})
		qvCreated = append(qvCreated, name)
	}
	qvNew := QueryValidators{QueryID: "invoice-list", Version: 0, Validators: validVals}
	if _, err := store.prepareAction(ActionRequest{Type: "save-query-validators", QueryValidators: &qvNew}, actor, "op-qv-cap", nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity for save-query-validators: %v", err)
	}
	for _, name := range qvCreated {
		_ = os.Remove(filepath.Join(qvCapDir, name+".json"))
	}
}
