package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeQueryFile writes a minimal .query.json file at
// <projectDir>/queries/<relPath> with the given declared id — enough for
// ResolveQueryID's directory walk (it never reads query bodies/parameters).
// Includes a title (datatug.QueryDef.Validate() requires one — ProjItemBrief
// isTitleRequired=true) so a *datatug.QueryDefWithFolderPath built from this
// file can pass its own .Validate(), the way GetQuery's real HTTP response
// path (apicore.Execute) always does — see queries_api_test.go's
// "FolderPath is populated and the response validates" case.
func writeQueryFile(t *testing.T, projectDir, relPath, id string) {
	t.Helper()
	full := filepath.Join(projectDir, "queries", relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	content := `{"id":"` + id + `","title":"` + id + `","type":"SQL"}`
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// TestResolveQueryID covers S97's one saved-query id convention: the
// folder-qualified id is canonical; a bare id is accepted only when it
// resolves to exactly one query across every folder.
func TestResolveQueryID(t *testing.T) {
	t.Run("bare id unique resolves to its canonical folder-qualified form", func(t *testing.T) {
		dir := t.TempDir()
		writeQueryFile(t, dir, "customers/customer-invoices.query.json", "customer-invoices")
		got, err := ResolveQueryID(dir, "customer-invoices")
		if err != nil {
			t.Fatalf("ResolveQueryID: %v", err)
		}
		if got != "customers/customer-invoices" {
			t.Errorf("got %q, want %q", got, "customers/customer-invoices")
		}
	})

	t.Run("folder-qualified id resolves as-is", func(t *testing.T) {
		dir := t.TempDir()
		writeQueryFile(t, dir, "customers/customer-invoices.query.json", "customer-invoices")
		got, err := ResolveQueryID(dir, "customers/customer-invoices")
		if err != nil {
			t.Fatalf("ResolveQueryID: %v", err)
		}
		if got != "customers/customer-invoices" {
			t.Errorf("got %q, want %q", got, "customers/customer-invoices")
		}
	})

	t.Run("bare id with no subfolder resolves to itself", func(t *testing.T) {
		dir := t.TempDir()
		writeQueryFile(t, dir, "top-level.query.json", "top-level")
		got, err := ResolveQueryID(dir, "top-level")
		if err != nil {
			t.Fatalf("ResolveQueryID: %v", err)
		}
		if got != "top-level" {
			t.Errorf("got %q, want %q", got, "top-level")
		}
	})

	t.Run("unknown bare id is ErrQueryNotFound (required case)", func(t *testing.T) {
		dir := t.TempDir()
		writeQueryFile(t, dir, "customers/customer-invoices.query.json", "customer-invoices")
		_, err := ResolveQueryID(dir, "no-such-query")
		if !errors.Is(err, ErrQueryNotFound) {
			t.Fatalf("ResolveQueryID error = %v, want ErrQueryNotFound", err)
		}
	})

	t.Run("unknown folder-qualified path is ErrQueryNotFound, never a raw filesystem error", func(t *testing.T) {
		dir := t.TempDir()
		writeQueryFile(t, dir, "customers/customer-invoices.query.json", "customer-invoices")
		_, err := ResolveQueryID(dir, "wrong-folder/customer-invoices")
		if !errors.Is(err, ErrQueryNotFound) {
			t.Fatalf("ResolveQueryID error = %v, want ErrQueryNotFound", err)
		}
	})

	t.Run("ambiguous bare id across folders is ErrAmbiguousQueryID (required case)", func(t *testing.T) {
		dir := t.TempDir()
		writeQueryFile(t, dir, "x/q.query.json", "q")
		writeQueryFile(t, dir, "y/q.query.json", "q")
		_, err := ResolveQueryID(dir, "q")
		if !errors.Is(err, ErrAmbiguousQueryID) {
			t.Fatalf("ResolveQueryID error = %v, want ErrAmbiguousQueryID", err)
		}
		if !strings.Contains(err.Error(), "x/q") || !strings.Contains(err.Error(), "y/q") {
			t.Errorf("error message = %q, want it to name both candidates x/q and y/q", err.Error())
		}
	})

	t.Run("explicit folder-qualified id still resolves even when its bare form is ambiguous elsewhere", func(t *testing.T) {
		dir := t.TempDir()
		writeQueryFile(t, dir, "x/q.query.json", "q")
		writeQueryFile(t, dir, "y/q.query.json", "q")
		got, err := ResolveQueryID(dir, "x/q")
		if err != nil {
			t.Fatalf("ResolveQueryID: %v", err)
		}
		if got != "x/q" {
			t.Errorf("got %q, want %q", got, "x/q")
		}
	})

	t.Run("empty queries directory is ErrQueryNotFound, not a filesystem error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := ResolveQueryID(dir, "anything")
		if !errors.Is(err, ErrQueryNotFound) {
			t.Fatalf("ResolveQueryID error = %v, want ErrQueryNotFound", err)
		}
	})
}
