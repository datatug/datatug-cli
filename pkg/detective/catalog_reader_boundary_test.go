package detective

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogAdmissionRejectsMissingOrInactiveCaseBeforeLoadingCheck(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.SaveCheck(ctx, Check{ID: "audit-check", Title: "Audit", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadCatalog(ctx, "audit-check", "", "missing-case"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Case admitted for Check run: %v", err)
	}
	c, err := s.SaveCase(ctx, Case{ID: "case-1", Title: "Investigation", Status: "active"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadCatalog(ctx, "audit-check", "", c.ID); err != nil {
		t.Fatalf("active Case failed catalog admission: %v", err)
	}
	c.Status, c.ClosureReason = "closed", "reviewed"
	if _, err := s.SaveCase(ctx, c, "alice", "update-case"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadCatalog(ctx, "audit-check", "", c.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closed Case admitted for new Check run: %v", err)
	}
}

func TestCatalogReaderRejectsTamperedValidatorDefinitions(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.GetPublishedValidator(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing published Validator was not reported: %v", err)
	}
	dir := s.definitionDir("validators")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tampered.json")
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPublishedValidator(ctx, "tampered"); err == nil {
		t.Fatal("malformed Validator definition was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"id":"different"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPublishedValidator(ctx, "tampered"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("renamed Validator definition was accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.projectDir, "queries", "q.query.json"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPublishedValidator(ctx, "tampered"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlinked Validator definition was followed: %v", err)
	}
}

func TestCatalogReaderRejectsTamperedQueryLocalValidators(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, _, err := s.GetQueryValidators(ctx, "../escape"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid query reference admitted: %v", err)
	}
	if _, present, err := s.GetQueryValidators(ctx, "q"); err != nil || present {
		t.Fatalf("absent local Validators reported as present: present=%t err=%v", present, err)
	}
	dir := s.definitionDir("query-validators")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, queryValidatorFileID("q")+".json")
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, present, err := s.GetQueryValidators(ctx, "q"); err == nil || present {
		t.Fatalf("malformed query-local Validators were accepted: present=%t err=%v", present, err)
	}
	if err := os.WriteFile(path, []byte(`{"queryId":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, present, err := s.GetQueryValidators(ctx, "q"); !errors.Is(err, ErrInvalid) || present {
		t.Fatalf("query-local Validator file crossed query boundary: present=%t err=%v", present, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.projectDir, "queries", "q.query.json"), path); err != nil {
		t.Fatal(err)
	}
	if _, present, err := s.GetQueryValidators(ctx, "q"); !errors.Is(err, ErrInvalid) || present {
		t.Fatalf("symlinked query-local Validator file was followed: present=%t err=%v", present, err)
	}
}
