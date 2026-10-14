package detective

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalVisitorRejectsSubstitutedPrivateEntries(t *testing.T) {
	s := testStore(t)
	visited := 0
	if err := s.visitJournal("missing-operations", func(string, []byte) error { visited++; return nil }); err != nil || visited != 0 {
		t.Fatalf("missing journal was not empty: visits=%d err=%v", visited, err)
	}
	dir := s.privateItemDir("tampered-operations")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.projectDir, "queries", "q.query.json"), filepath.Join(dir, "operation-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.visitJournal("tampered-operations", func(string, []byte) error { visited++; return nil }); !errors.Is(err, ErrInvalid) || visited != 0 {
		t.Fatalf("private journal followed substituted operation: visits=%d err=%v", visited, err)
	}
	if err := os.Remove(filepath.Join(dir, "operation-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "operation-1.json"), []byte(`{"kind":"case"}`), 0600); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop at reviewed operation")
	if err := s.visitJournal("tampered-operations", func(id string, data []byte) error {
		visited++
		if id != "operation-1" || len(data) == 0 {
			t.Fatalf("journal delivered unexpected entry %q", id)
		}
		return stop
	}); !errors.Is(err, stop) || visited != 1 {
		t.Fatalf("journal did not propagate visitor failure: visits=%d err=%v", visited, err)
	}
}

func TestReconcileRejectsMalformedOrUnknownPrivateOperationKind(t *testing.T) {
	s := testStore(t)
	dir := s.privateItemDir("ai-confirm-operations")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "operation-1.json")
	for _, content := range []string{"{", `{"kind":"unrecognized"}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.reconcilePendingOther(context.Background(), "cases", "case-1"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed operation %q admitted: %v", content, err)
		}
	}
}
