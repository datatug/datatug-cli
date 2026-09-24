package commands

// Coverage lane A2 (datatug-cli#289): additional cmd_chat.go branches lane
// A's report flagged as gaps -- chatCommand's "warning: load previous chat
// options" path, loadChatJoinApplication's dbcopy.Parse/CheckSourceFile/
// refresh error branches, and projectSchemaContext's remaining skip/
// truncation branches.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/chat"
)

// TestChatCommandWarnsOnInvalidLastOptionsThenRuns covers chatCommand's
// RunE "warning: load previous chat options" branch: applyLastChatOptions
// fails on malformed JSON at lastChatOptionsPath, RunE prints a warning to
// stderr instead of failing outright, and still proceeds into runChat.
func TestChatCommandWarnsOnInvalidLastOptionsThenRuns(t *testing.T) {
	restorePath := lastChatOptionsPath
	t.Cleanup(func() { lastChatOptionsPath = restorePath })
	badPath := filepath.Join(t.TempDir(), "chat-last.json")
	if err := os.WriteFile(badPath, []byte(`{ not valid json`), 0o600); err != nil {
		t.Fatal(err)
	}
	lastChatOptionsPath = func() (string, error) { return badPath, nil }

	restoreRun := chat.RunTeaProgram
	t.Cleanup(func() { chat.RunTeaProgram = restoreRun })
	chat.RunTeaProgram = func(p *tea.Program) (tea.Model, error) { return nil, nil }
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	for name, value := range map[string]string{
		"project": dir, "env": "local", "model": defaultChatModel, "thinking": "low",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(stderr.String(), "warning: load previous chat options:") {
		t.Fatalf("expected a warning on stderr, got %q", stderr.String())
	}
}

// --- loadChatJoinApplication ------------------------------------------

func TestLoadChatJoinApplicationParseError(t *testing.T) {
	_, closeFn, err := loadChatJoinApplication(context.Background(), "://not-a-valid-url", nil, false)
	if err == nil {
		t.Fatal("expected dbcopy.Parse to fail for a malformed source URL")
	}
	if closeFn != nil {
		t.Fatal("expected no close func on a Parse error")
	}
}

func TestLoadChatJoinApplicationMissingSourceFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.sqlite")
	_, closeFn, err := loadChatJoinApplication(context.Background(), "sqlite:///"+missing, nil, false)
	if err == nil {
		t.Fatal("expected CheckSourceFile to fail for a missing sqlite file")
	}
	if closeFn != nil {
		t.Fatal("expected no close func when the source file is missing")
	}
}

func TestLoadChatJoinApplicationRefreshError(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.sqlite")
	if err := os.WriteFile(garbage, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, closeFn, err := loadChatJoinApplication(context.Background(), "sqlite:///"+garbage, nil, false)
	if err == nil {
		t.Fatal("expected the initial snapshot refresh to fail against a non-sqlite file")
	}
	if closeFn != nil {
		t.Fatal("expected loadChatJoinApplication to close the metadata DB itself on a refresh error, not hand back a close func")
	}
}

// --- projectSchemaContext ------------------------------------------------

func TestProjectSchemaContextSkipsSelectedSourceAndUnresolvedURLs(t *testing.T) {
	catalog := chat.ProjectCatalog{Objects: []chat.ProjectObject{
		{Reference: chat.ContextReference{Kind: "table", SourceID: "broken", ObjectID: "main.SameSource"}},
		{Reference: chat.ContextReference{Kind: "table", SourceID: "no-url", ObjectID: "main.NoURL"}},
		{Reference: chat.ContextReference{Kind: "table", SourceID: "unavailable-src", ObjectID: "main.Unavailable"}},
		{Reference: chat.ContextReference{Kind: "table", SourceID: "healthy", ObjectID: "main.Invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}},
	}}
	urls := map[string]string{
		"broken":          "sqlite:///broken.db",
		"unavailable-src": "unavailable://unavailable-src",
		"healthy":         "sqlite:///healthy.db",
		// "no-url" deliberately absent.
	}
	text := projectSchemaContext(catalog, urls, "broken")
	if strings.Contains(text, "main.SameSource") {
		t.Fatalf("expected the selected source's own table to be skipped:\n%s", text)
	}
	if strings.Contains(text, "main.NoURL") {
		t.Fatalf("expected a table with no resolvable URL to be skipped:\n%s", text)
	}
	if strings.Contains(text, "main.Unavailable") {
		t.Fatalf("expected a table on an unavailable:// source to be skipped:\n%s", text)
	}
	if !strings.Contains(text, "main.Invoice") {
		t.Fatalf("expected the healthy table to be listed:\n%s", text)
	}
}

func TestProjectSchemaContextTruncatesAtByteLimit(t *testing.T) {
	var objects []chat.ProjectObject
	urls := map[string]string{}
	for i := 0; i < 200; i++ {
		id := "src" + strconv.Itoa(i)
		urls[id] = "sqlite:///" + id + ".db"
		objects = append(objects, chat.ProjectObject{
			Reference: chat.ContextReference{Kind: "table", SourceID: id, ObjectID: fmt.Sprintf("main.Table%d", i)},
			Columns:   []string{"ColumnA", "ColumnB", "ColumnC"},
			ColumnTypes: map[string]string{
				"ColumnA": "INTEGER", "ColumnB": "TEXT", "ColumnC": "TEXT",
			},
		})
	}
	catalog := chat.ProjectCatalog{Objects: objects}
	text := projectSchemaContext(catalog, urls, "selected")
	if len(text) >= 200*80 {
		t.Fatalf("expected the 8000-byte cap to truncate output, got %d bytes", len(text))
	}
	if !strings.Contains(text, "main.Table0") {
		t.Fatalf("expected at least the first table to be listed:\n%s", text)
	}
}

// --- runChatProject early error branches ---------------------------------

// TestRunChatProjectNoDatabaseCatalogsConfigured covers runChatProject's
// resolveQueryDatabase error branch: an environment with zero configured
// database catalogs and no explicit --database.
func TestRunChatProjectNoDatabaseCatalogsConfigured(t *testing.T) {
	dir := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("datatug-project.json", `{"id":"no-catalogs","title":"No catalogs"}`)
	if err := os.Mkdir(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("environments/local/local.env.json", `{"id":"local"}`)

	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	_, err := runChatProject(cmd, options)
	if err == nil || !strings.Contains(err.Error(), "no database catalogs configured") {
		t.Fatalf("runChatProject error = %v, want a 'no database catalogs configured' usage error", err)
	}
}

// TestRunChatProjectCatalogBuildFailure covers runChatProject's
// buildChatProjectCatalog error branch ("load project explorer"): a valid,
// unambiguous database resolves, but the project has no queries/ directory
// at all, so buildChatProjectCatalog's own QueryIDIndex call fails.
func TestRunChatProjectCatalogBuildFailure(t *testing.T) {
	dir := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("datatug-project.json", `{"id":"no-queries-dir","title":"No queries dir"}`)
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["orders"]}]}`)
	write("environments/local/catalogs/orders/orders.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q}`, filepath.Join(dir, "orders.sqlite")))
	// Deliberately no "queries" directory.

	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	_, err := runChatProject(cmd, options)
	if err == nil || !strings.Contains(err.Error(), "load project explorer") {
		t.Fatalf("runChatProject error = %v, want a 'load project explorer' usage error", err)
	}
}
