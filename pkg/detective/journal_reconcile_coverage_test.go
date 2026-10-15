package detective

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Not parallel: manipulates filesystem paths in store private dir.
func TestCoverage100_JournalReconcile_AllBranches(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 1. case "query": return nil
	dir := s.privateItemDir("ai-confirm-operations")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "op-query.json"), []byte(`{"kind":"query"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePendingOther(ctx, "cases", "c1"); err != nil {
		t.Fatalf("expected nil for query: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "op-query.json"))

	// 2. case "run": bad json -> ErrInvalid
	if err := os.WriteFile(filepath.Join(dir, "op-run-bad.json"), []byte(`{"kind":"run","produced":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePendingOther(ctx, "cases", "c1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad run op: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "op-run-bad.json"))

	// 3. case "run": finishAIRun called and op.Done == true
	runOp := `{"kind":"run","done":false,"produced":true,"draft":{"caseId":"c1"},"receipt":{"resourceId":"c1"}}`
	if err := os.WriteFile(filepath.Join(dir, "op-run.json"), []byte(runOp), 0600); err != nil {
		t.Fatal(err)
	}
	_ = s.reconcilePendingOther(ctx, "cases", "c1")
	runOpDone := `{"kind":"run","done":true,"produced":true,"draft":{"caseId":"c1"}}`
	_ = os.WriteFile(filepath.Join(dir, "op-run.json"), []byte(runOpDone), 0600)
	_ = s.reconcilePendingOther(ctx, "cases", "c1")
	_ = os.Remove(filepath.Join(dir, "op-run.json"))

	// 4. case ai.KindCheck ("draft-check"): bad json -> ErrInvalid
	if err := os.WriteFile(filepath.Join(dir, "op-check-bad.json"), []byte(`{"kind":"draft-check","done":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePendingOther(ctx, "cases", "c1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad check op: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "op-check-bad.json"))

	// 5. case ai.KindCheck ("draft-check"): finishAICatalog called and op.Done == true
	checkOp := `{"kind":"draft-check","done":false,"receipt":{"resourceId":"chk1"}}`
	if err := os.WriteFile(filepath.Join(dir, "op-check.json"), []byte(checkOp), 0600); err != nil {
		t.Fatal(err)
	}
	_ = s.reconcilePendingOther(ctx, "checks", "chk1")
	checkOpDone := `{"kind":"draft-check","done":true,"receipt":{"resourceId":"chk1"}}`
	_ = os.WriteFile(filepath.Join(dir, "op-check.json"), []byte(checkOpDone), 0600)
	_ = s.reconcilePendingOther(ctx, "checks", "chk1")
	_ = os.Remove(filepath.Join(dir, "op-check.json"))

	// 6. case "case": bad json -> ErrInvalid, finishAICase, and op.Done == true
	if err := os.WriteFile(filepath.Join(dir, "op-case-bad.json"), []byte(`{"kind":"case","done":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePendingOther(ctx, "cases", "c1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad case op: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "op-case-bad.json"))

	caseOp := `{"kind":"case","done":false,"after":{"id":"c1"}}`
	if err := os.WriteFile(filepath.Join(dir, "op-case.json"), []byte(caseOp), 0600); err != nil {
		t.Fatal(err)
	}
	_ = s.reconcilePendingOther(ctx, "cases", "c1")
	caseOpDone := `{"kind":"case","done":true,"after":{"id":"c1"}}`
	_ = os.WriteFile(filepath.Join(dir, "op-case.json"), []byte(caseOpDone), 0600)
	_ = s.reconcilePendingOther(ctx, "cases", "c1")
	_ = os.Remove(filepath.Join(dir, "op-case.json"))

	// 7. incident-operations: bad json -> ErrInvalid
	incDir := s.privateItemDir("incident-operations")
	if err := os.MkdirAll(incDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "inc-bad.json"), []byte(`{"done":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePendingOther(ctx, "cases", "c1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad incident op: %v", err)
	}
	_ = os.Remove(filepath.Join(incDir, "inc-bad.json"))

	// 8. incident-operations: finishIncidentOperation called and op.Done == true
	incOp := `{"done":false,"case":{"id":"c1"}}`
	if err := os.WriteFile(filepath.Join(incDir, "inc-op.json"), []byte(incOp), 0600); err != nil {
		t.Fatal(err)
	}
	_ = s.reconcilePendingOther(ctx, "cases", "c1")
	incOpDone := `{"done":true,"case":{"id":"c1"}}`
	_ = os.WriteFile(filepath.Join(incDir, "inc-op.json"), []byte(incOpDone), 0600)
	_ = s.reconcilePendingOther(ctx, "cases", "c1")
	_ = os.Remove(filepath.Join(incDir, "inc-op.json"))

	// 9. knowledge-operations: bad json -> ErrInvalid
	knDir := s.privateItemDir("knowledge-operations")
	if err := os.MkdirAll(knDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knDir, "kn-bad.json"), []byte(`{"done":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcilePendingOther(ctx, "knowledge-proposals", "kp1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad knowledge op: %v", err)
	}
	_ = os.Remove(filepath.Join(knDir, "kn-bad.json"))

	// 10. knowledge-operations: finishKnowledgeOperation called and op.Done == true
	knOp := `{"done":false,"response":{"proposal":{"id":"kp1"}}}`
	if err := os.WriteFile(filepath.Join(knDir, "kn-op.json"), []byte(knOp), 0600); err != nil {
		t.Fatal(err)
	}
	_ = s.reconcilePendingOther(ctx, "knowledge-proposals", "kp1")
	knOpDone := `{"done":true,"response":{"proposal":{"id":"kp1"}}}`
	_ = os.WriteFile(filepath.Join(knDir, "kn-op.json"), []byte(knOpDone), 0600)
	_ = s.reconcilePendingOther(ctx, "knowledge-proposals", "kp1")
	_ = os.Remove(filepath.Join(knDir, "kn-op.json"))

	// 11. visitJournal: ensureDirIfExists fails when file exists where dir expected
	fileDir := s.privateItemDir("file-kind")
	if err := os.WriteFile(fileDir, []byte("regular file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.visitJournal("file-kind", func(string, []byte) error { return nil }); err == nil {
		t.Fatalf("expected error when private dir is a file")
	}
	_ = os.Remove(fileDir)

	// 12. visitJournal: os.Open error (unreadable directory)
	unreadableDir := s.privateItemDir("unreadable-kind")
	if err := os.MkdirAll(unreadableDir, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(unreadableDir, 0700); _ = os.RemoveAll(unreadableDir) }()
	_ = s.visitJournal("unreadable-kind", func(string, []byte) error { return nil })

	// 13. visitJournal: journalReaddirnames error
	origReaddir := journalReaddirnames
	defer func() { journalReaddirnames = origReaddir }()
	journalReaddirnames = func(f *os.File, n int) ([]string, error) {
		return nil, errors.New("simulated readdirnames error")
	}
	if err := s.visitJournal("ai-confirm-operations", func(string, []byte) error { return nil }); err == nil {
		t.Fatalf("expected error from journalReaddirnames")
	}
	journalReaddirnames = origReaddir
}
