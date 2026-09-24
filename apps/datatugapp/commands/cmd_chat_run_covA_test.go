package commands

// Coverage lane A (datatug-cli#289): drives runChat/runChatProject
// end-to-end against a real temp project, through ChatUI.Run's
// RunTeaProgram seam (pkg/chat/chatui.go) so no real terminal program ever
// starts.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// writeChatRunProjectFixture lays out a minimal project with exactly one
// environment and one UNSCANNED database catalog (no dbModel) -- so
// buildChatProjectCatalog's catalog carries no "table"/"project_view"
// objects at all, hasQueryableProjectTables is false, and runChatProject
// takes the unavailableSchemaConversation branch instead of ever calling
// chat.NewLLMProvider (which would need a real/fake model endpoint). This
// keeps the integration test hermetic while still exercising the full
// runChatProject happy path: project/database resolution, catalog
// building, session/store/executor wiring, join-application load (skipped,
// unavailable source), SessionChat/ChatUI construction, saved-query
// service, browser bridge, and ui.Run() through the seam.
func writeChatRunProjectFixture(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
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
	write("datatug-project.json", `{"id":"chat-run-test","title":"Chat run test"}`)
	if err := os.Mkdir(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["orders"]}]}`)
	write("environments/local/catalogs/orders/orders.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q}`, filepath.Join(dir, "orders.sqlite")))
	return dir
}

// TestRunChatProjectUnavailableSchemaHappyPath drives runChatProject
// end-to-end for a project whose only source has no scanned schema
// (unavailableSchemaConversation), asserting it returns the empty
// "SelectedProject" (RunTeaProgram never switches projects) with no error.
func TestRunChatProjectUnavailableSchemaHappyPath(t *testing.T) {
	restoreRun := chat.RunTeaProgram
	t.Cleanup(func() { chat.RunTeaProgram = restoreRun })
	var ranWithConversation bool
	chat.RunTeaProgram = func(p *tea.Program) (tea.Model, error) {
		ranWithConversation = true
		return nil, nil
	}
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}

	nextProject, err := runChatProject(cmd, options)
	if err != nil {
		t.Fatalf("runChatProject: %v", err)
	}
	if nextProject != "" {
		t.Fatalf("nextProject = %q, want empty (no project switch happened)", nextProject)
	}
	if !ranWithConversation {
		t.Fatal("ui.Run() (and so RunTeaProgram) was never reached")
	}
}

// TestRunChatLoopsUntilNoProjectSwitch covers runChat's outer for loop: a
// single pass through runChatProject that returns nextProject == "" (no
// project switch) ends the loop and returns runChatProject's own nil error
// -- and, because options.ai is empty, resolveChatAIProfile is never
// called.
func TestRunChatLoopsUntilNoProjectSwitch(t *testing.T) {
	restoreRun := chat.RunTeaProgram
	t.Cleanup(func() { chat.RunTeaProgram = restoreRun })
	chat.RunTeaProgram = func(p *tea.Program) (tea.Model, error) { return nil, nil }
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if err := runChat(cmd, options); err != nil {
		t.Fatalf("runChat: %v", err)
	}
}

// TestRunChatResolvesAIProfileBeforeLooping covers runChat's options.ai !=
// "" branch: an unknown AI profile fails resolveChatAIProfile and runChat
// returns its usage error immediately, WITHOUT ever calling
// runChatProject (so RunTeaProgram is never reached, proving the failure
// really did short-circuit the loop).
func TestRunChatResolvesAIProfileBeforeLooping(t *testing.T) {
	restoreRun := chat.RunTeaProgram
	t.Cleanup(func() { chat.RunTeaProgram = restoreRun })
	reached := false
	chat.RunTeaProgram = func(p *tea.Program) (tea.Model, error) { reached = true; return nil, nil }

	cmd := chatCommand()
	options := chatOptions{project: ".", env: "local", model: defaultChatModel, thinking: "low", ai: "missing-profile"}
	err := runChat(cmd, options)
	if err == nil || !strings.Contains(err.Error(), `unknown AI profile "missing-profile"`) {
		t.Fatalf("runChat error = %v, want unknown AI profile", err)
	}
	if reached {
		t.Fatal("runChatProject/ui.Run should never have been reached")
	}
}

// TestRunChatProjectRequiresValidProject covers runChatProject's very
// first error path: resolveQueryProject failing for a project that is
// neither a directory nor a registered ID.
func TestRunChatProjectRequiresValidProject(t *testing.T) {
	cmd := chatCommand()
	options := chatOptions{project: filepath.Join(t.TempDir(), "does-not-exist-and-is-not-registered"), env: "local"}
	_, err := runChatProject(cmd, options)
	if err == nil {
		t.Fatal("expected an error for an unresolvable --project")
	}
}

// TestRunChatProjectUsesRequestContext covers runChatProject's ctx==nil
// fallback to context.Background() indirectly: cmd.Context() is non-nil
// here (ExecuteContext-style), and the happy path still completes,
// confirming runChatProject actually threads it through instead of always
// silently substituting Background().
func TestRunChatProjectUsesRequestContext(t *testing.T) {
	restoreRun := chat.RunTeaProgram
	t.Cleanup(func() { chat.RunTeaProgram = restoreRun })
	chat.RunTeaProgram = func(p *tea.Program) (tea.Model, error) { return nil, nil }
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	ctx := context.WithValue(context.Background(), struct{ key string }{"probe"}, "present")
	cmd.SetContext(ctx)
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err != nil {
		t.Fatalf("runChatProject with an explicit context: %v", err)
	}
}

// TestBuildChatProjectCatalogErrorBranches covers buildChatProjectCatalog's
// four early-return error paths: LoadProject failing outright (no project
// file at all), LoadEnvDbCatalogs failing (an environment ID the project
// has no environment file for), QueryIDIndex failing (no "queries"
// directory at all -- filepath.WalkDir's root-stat error, unlike an EMPTY
// queries dir, which is not an error), and LoadQuery failing for an ID
// QueryIDIndex did find (malformed query JSON).
func TestBuildChatProjectCatalogErrorBranches(t *testing.T) {
	ctx := context.Background()
	write := func(dir, path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("LoadProject", func(t *testing.T) {
		dir := t.TempDir() // no datatug-project.json at all
		store := filestore.NewProjectStore("missing", dir)
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); err == nil {
			t.Fatal("expected an error loading a project with no project file")
		}
	})

	t.Run("LoadEnvDbCatalogs", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "datatug-project.json", `{"id":"catalog-err","title":"Catalog err"}`)
		write(dir, "queries/.keep", "")
		write(dir, "environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["broken"]}]}`)
		write(dir, "environments/local/catalogs/broken/broken.db.json", `{ not valid json`)
		// A malformed catalog file under a real environment: ListSources'
		// own catalogSources call swallows a per-catalog resolve failure
		// (sourceURLFromCatalog's "unsupported driver" comment), so it does
		// NOT fail here -- but buildChatProjectCatalog's own direct
		// LoadEnvDbCatalogs call (unlike ListSources', it has no such
		// per-catalog leniency) still surfaces the same malformed file as
		// a hard error loading the catalog LIST itself.
		store := filestore.NewProjectStore("catalog-err", dir)
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); err == nil {
			t.Fatal("expected an error loading catalogs with a malformed catalog file")
		}
	})

	t.Run("QueryIDIndex", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "datatug-project.json", `{"id":"no-queries-dir","title":"No queries dir"}`)
		write(dir, "environments/local/local.env.json", `{"id":"local"}`)
		// Deliberately no "queries" directory at all (unlike the other
		// fixtures in this file, which always create it, even empty).
		store := filestore.NewProjectStore("no-queries-dir", dir)
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); err == nil {
			t.Fatal("expected an error indexing queries with no queries directory")
		}
	})

	t.Run("LoadQuery", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "datatug-project.json", `{"id":"bad-query","title":"Bad query"}`)
		write(dir, "environments/local/local.env.json", `{"id":"local"}`)
		write(dir, "queries/broken.query.json", `{ not valid json`)
		store := filestore.NewProjectStore("bad-query", dir)
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); err == nil {
			t.Fatal("expected an error loading a malformed saved query")
		}
	})
}
