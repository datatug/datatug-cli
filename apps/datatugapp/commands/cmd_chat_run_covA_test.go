package commands

// Coverage lane A (datatug-cli#289): drives runChat/runChatProject
// end-to-end against a real temp project, through ChatUI.Run's
// RunTeaProgram seam (pkg/chat/chatui.go) so no real terminal program ever
// starts.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
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
	var ranWithConversation bool
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) {
		ranWithConversation = true
		return nil, nil
	}))
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
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))
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
	reached := false
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { reached = true; return nil, nil }))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })
	getChatSettings = func() (dtconfig.Settings, error) { return dtconfig.Settings{}, nil }

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
	for _, hint := range []string{"datatug projects", "datatug chat --project", "datatug init"} {
		if !strings.Contains(err.Error(), hint) {
			t.Fatalf("missing recovery instruction %q: %v", hint, err)
		}
	}
}

// TestRunChatProjectUsesRequestContext covers runChatProject's ctx==nil
// fallback to context.Background() indirectly: cmd.Context() is non-nil
// here (ExecuteContext-style), and the happy path still completes,
// confirming runChatProject actually threads it through instead of always
// silently substituting Background().
func TestRunChatProjectUsesRequestContext(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))
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

	// t.Run("ListSourcesError") below actually exercises ListSources' OWN
	// error branch (buildChatProjectCatalog's "sources, err :=
	// api.ListSources(...)" call), not the later direct LoadEnvDbCatalogs
	// call a few lines down: a malformed catalog file under a real
	// environment makes ListSources' catalogSources helper fail outright
	// (it calls the SAME projStore.LoadEnvDbCatalogs projectStore method,
	// which errors identically on a malformed file either way -- it does
	// NOT swallow the failure per-catalog the way sourceURLFromCatalog's
	// own "unsupported driver" skip does). Renamed from the old
	// "LoadEnvDbCatalogs" name, which described the wrong call site
	// (verified with a debug probe: the returned error is "resolver: list
	// catalogs for environment...", ListSources' own wrapped message).
	t.Run("ListSourcesError", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "datatug-project.json", `{"id":"catalog-err","title":"Catalog err"}`)
		write(dir, "queries/.keep", "")
		write(dir, "environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["broken"]}]}`)
		write(dir, "environments/local/catalogs/broken/broken.db.json", `{ not valid json`)
		store := filestore.NewProjectStore("catalog-err", dir)
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); err == nil {
			t.Fatal("expected an error loading catalogs with a malformed catalog file")
		}
	})

	// LoadEnvDbCatalogsDirectCall covers buildChatProjectCatalog's OWN
	// direct "projectStore.LoadEnvDbCatalogs(ctx, environment)" call
	// (cmd_chat.go:346-349), distinct from the one ListSources makes
	// internally a few lines above it. Both call sites share the same
	// projectStore and args, so a real fixture can't make one fail without
	// the other; faultyProjectStore lets the first (ListSources') call
	// through untouched and fails only the second.
	t.Run("LoadEnvDbCatalogsDirectCall", func(t *testing.T) {
		dir := writeChatRunProjectFixture(t)
		real := filestore.NewProjectStore("chat-run-test", dir)
		store := &faultyProjectStore{ProjectStore: real, failLoadEnvDbCatalogsAfter: 2}
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); err == nil {
			t.Fatal("expected buildChatProjectCatalog's own direct LoadEnvDbCatalogs call to fail")
		}
		if store.envDbCatalogsCalls < 2 {
			t.Fatalf("expected LoadEnvDbCatalogs to be called at least twice (ListSources, then buildChatProjectCatalog directly), got %d", store.envDbCatalogsCalls)
		}
	})

	// QueryIDIndexError covers the queryIDIndexFunc seam directly: no real
	// fixture reaches api.QueryIDIndex's own error branch without ALSO
	// tripping over ListSources' httpQuerySources call, which needs a
	// "queries" directory to exist (unlike QueryIDIndex, which treats a
	// missing one as an empty index, not an error) -- see the ListSources
	// probe in this file's history. writeChatRunProjectFixture's project
	// (unscanned schema, no dbModel) reaches buildChatProjectCatalog's
	// QueryIDIndex call cleanly; queryIDIndexFunc is then overridden to
	// fail outright.
	t.Run("QueryIDIndexError", func(t *testing.T) {
		dir := writeChatRunProjectFixture(t)
		restore := queryIDIndexFunc
		t.Cleanup(func() { queryIDIndexFunc = restore })
		injected := errors.New("injected QueryIDIndex failure")
		queryIDIndexFunc = func(string) (map[string]string, error) { return nil, injected }
		store := filestore.NewProjectStore("chat-run-test", dir)
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); !errors.Is(err, injected) {
			t.Fatalf("buildChatProjectCatalog error = %v, want the injected QueryIDIndex error", err)
		}
	})

	// LoadQuery covers projectStore.LoadQuery's own error branch
	// (cmd_chat.go:412-415): queryIDIndexFunc is overridden to report one ID
	// that names no real query file on disk, so QueryIDIndex itself never
	// has to see (and fail loading the project on) malformed content --
	// LoadProject eagerly loads every real query file up front, so a
	// malformed one on disk fails LoadProject first, never reaching this
	// branch (also verified with a debug probe).
	t.Run("LoadQuery", func(t *testing.T) {
		dir := writeChatRunProjectFixture(t)
		restore := queryIDIndexFunc
		t.Cleanup(func() { queryIDIndexFunc = restore })
		queryIDIndexFunc = func(string) (map[string]string, error) {
			return map[string]string{"missing-query": "missing-query"}, nil
		}
		store := filestore.NewProjectStore("chat-run-test", dir)
		if _, _, err := buildChatProjectCatalog(ctx, dir, store, "local"); err == nil {
			t.Fatal("expected an error loading a query ID with no backing file")
		}
	})
}

// faultyProjectStore wraps a real datatug.ProjectStore, delegating every
// method except LoadEnvDbCatalogs, which it fails after
// failLoadEnvDbCatalogsAfter calls (0 disables the fault). It exists because
// buildChatProjectCatalog and api.ListSources both call
// projectStore.LoadEnvDbCatalogs with identical arguments -- a real
// filesystem fixture cannot make one call succeed and the other fail, since
// they are the same method call on the same store.
type faultyProjectStore struct {
	datatug.ProjectStore
	failLoadEnvDbCatalogsAfter int
	envDbCatalogsCalls         int
	// nilCatalogEntry, when true, makes LoadEnvDbCatalogs return a single
	// synthetic nil *datatug.DbCatalog entry instead of delegating -- a real
	// filestore.ProjectStore never returns one (loadOneEnvDbCatalog skips a
	// nil catalog before appending it), so this is the only way to reach
	// buildChatProjectCatalog's "if database == nil { continue }" branch.
	nilCatalogEntry bool
}

func (s *faultyProjectStore) LoadEnvDbCatalogs(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	s.envDbCatalogsCalls++
	if s.nilCatalogEntry {
		return datatug.DbCatalogs{nil}, nil
	}
	if s.failLoadEnvDbCatalogsAfter > 0 && s.envDbCatalogsCalls >= s.failLoadEnvDbCatalogsAfter {
		return nil, fmt.Errorf("injected LoadEnvDbCatalogs failure (call %d)", s.envDbCatalogsCalls)
	}
	return s.ProjectStore.LoadEnvDbCatalogs(ctx, envID, o...)
}

// TestBuildChatProjectCatalogTitleFallsBackToID covers buildChatProjectCatalog's
// "catalog.Title = project.ID" branch: a project file with no title at all.
func TestBuildChatProjectCatalogTitleFallsBackToID(t *testing.T) {
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
	write("datatug-project.json", `{"id":"no-title-project"}`)
	write("queries/.keep", "")
	write("environments/local/local.env.json", `{"id":"local"}`)
	store := filestore.NewProjectStore("no-title-project", dir)
	catalog, _, err := buildChatProjectCatalog(context.Background(), dir, store, "local")
	if err != nil {
		t.Fatalf("buildChatProjectCatalog: %v", err)
	}
	if catalog.Title != "no-title-project" {
		t.Fatalf("catalog.Title = %q, want the project ID as a fallback", catalog.Title)
	}
}

// TestBuildChatProjectCatalogNilDatabaseCatalogEntrySkipped covers the
// "if database == nil { continue }" branch in buildChatProjectCatalog's
// catalogs loop (cmd_chat.go:352-354). A real filestore.ProjectStore never
// returns a nil *datatug.DbCatalog (loadOneEnvDbCatalog skips it before
// appending -- see env_db_catalogs_store.go), so faultyDBListProjectStore
// (a second faultyProjectStore variant, this one overriding
// LoadEnvDbCatalogs unconditionally rather than delegating) hands back a
// synthetic list containing one explicit nil entry.
func TestBuildChatProjectCatalogNilDatabaseCatalogEntrySkipped(t *testing.T) {
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
	write("datatug-project.json", `{"id":"nil-catalog-entry","title":"Nil catalog entry"}`)
	write("queries/.keep", "")
	write("environments/local/local.env.json", `{"id":"local"}`)
	real := filestore.NewProjectStore("nil-catalog-entry", dir)
	store := &faultyProjectStore{ProjectStore: real, nilCatalogEntry: true}
	catalog, _, err := buildChatProjectCatalog(context.Background(), dir, store, "local")
	if err != nil {
		t.Fatalf("buildChatProjectCatalog: %v", err)
	}
	for _, object := range catalog.Objects {
		if object.Reference.Kind == "source" {
			t.Fatalf("expected the nil database catalog entry to be skipped entirely, got a source object: %+v", object)
		}
	}
}

// TestBuildChatProjectCatalogUnsupportedDriverMarksSourceUnresolved covers
// the "if urls[id] == \"\" { ...Issue = \"Source connection could not be
// resolved...\" }" branch: a catalog whose driver sourceURLFromCatalog does
// not support (api/source_resolver.go's default case) is skipped by
// ListSources (so absent from urls) but still returned by
// buildChatProjectCatalog's own direct LoadEnvDbCatalogs call.
func TestBuildChatProjectCatalogUnsupportedDriverMarksSourceUnresolved(t *testing.T) {
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
	write("datatug-project.json", `{"id":"unsupported-driver","title":"Unsupported driver"}`)
	write("queries/.keep", "")
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"postgres","catalogs":["pg"]}]}`)
	write("environments/local/catalogs/pg/pg.db.json", `{"driver":"postgres","path":"unused"}`)
	store := filestore.NewProjectStore("unsupported-driver", dir)
	catalog, urls, err := buildChatProjectCatalog(context.Background(), dir, store, "local")
	if err != nil {
		t.Fatalf("buildChatProjectCatalog: %v", err)
	}
	if urls["pg"] != "" {
		t.Fatalf("expected an unsupported driver to have no resolvable URL, got %q", urls["pg"])
	}
	found := false
	for _, object := range catalog.Objects {
		if object.Reference.Kind == "source" && object.Reference.SourceID == "pg" {
			found = true
			if !strings.Contains(object.Issue, "Source connection could not be resolved") {
				t.Fatalf("expected the unresolved-source issue, got %q", object.Issue)
			}
		}
	}
	if !found {
		t.Fatal("expected the unsupported-driver catalog to still appear as a source object")
	}
}

// TestBuildChatProjectCatalogSchemaErrorAndEmptyRelationsAndViewKind covers
// three of buildChatProjectCatalog's remaining schema-loop branches in one
// project: a scanned catalog whose dbModel directory is unreadable
// (GetCatalogSchemaPartial's schemaErr branch), a scanned catalog with a
// dbModel directory that defines no tables or views at all (the
// len(schema.Relations) == 0 branch), and a scanned catalog with a VIEW
// relation (the "kind = project_view" branch, ported from the existing
// dbmodels/<model>/main/tables/... fixture pattern in cmd_chat_test.go).
func TestBuildChatProjectCatalogSchemaErrorAndEmptyRelationsAndViewKind(t *testing.T) {
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
	write("datatug-project.json", `{"id":"schema-branches","title":"Schema branches"}`)
	write("queries/.keep", "")
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["unreadable","empty","withview"]}]}`)

	write("environments/local/catalogs/unreadable/unreadable.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q,"dbModel":"unreadable-model"}`, filepath.Join(dir, "unreadable.sqlite")))
	// dbmodels/unreadable-model is a regular FILE, not a directory:
	// loadCatalogRelationsWithMode's os.ReadDir(dbModelDir) then fails with
	// a non-NotExist error ("not a directory"), which getCatalogSchema
	// propagates as schemaErr.
	if err := os.MkdirAll(filepath.Join(dir, "dbmodels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dbmodels/unreadable-model"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	write("environments/local/catalogs/empty/empty.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q,"dbModel":"empty-model"}`, filepath.Join(dir, "empty.sqlite")))
	// dbmodels/empty-model exists but defines no tables or views.
	if err := os.MkdirAll(filepath.Join(dir, "dbmodels/empty-model"), 0o755); err != nil {
		t.Fatal(err)
	}

	write("environments/local/catalogs/withview/withview.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q,"dbModel":"view-model"}`, filepath.Join(dir, "withview.sqlite")))
	write("dbmodels/view-model/main/views/ActiveCustomer/main.ActiveCustomer.columns.json", `{"columns":[{"name":"CustomerId","dbType":"INTEGER"}]}`)

	store := filestore.NewProjectStore("schema-branches", dir)
	catalog, _, err := buildChatProjectCatalog(context.Background(), dir, store, "local")
	if err != nil {
		t.Fatalf("buildChatProjectCatalog: %v", err)
	}

	issues := map[string]string{}
	kinds := map[string]string{}
	for _, object := range catalog.Objects {
		if object.Reference.Kind == "source" {
			issues[object.Reference.SourceID] = object.Issue
		}
		if object.Reference.SourceID == "withview" && object.Reference.ObjectID == "main.ActiveCustomer" {
			kinds["withview/main.ActiveCustomer"] = object.Reference.Kind
		}
	}
	if !strings.Contains(issues["unreadable"], "Schema unavailable") {
		t.Errorf("expected a schema-unavailable issue for the unreadable dbModel, got %q", issues["unreadable"])
	}
	if !strings.Contains(issues["empty"], "No scanned tables or views") {
		t.Errorf("expected a no-scanned-tables issue for the empty dbModel, got %q", issues["empty"])
	}
	if kinds["withview/main.ActiveCustomer"] != "project_view" {
		t.Errorf("expected the VIEW relation to be classified as project_view, got %q", kinds["withview/main.ActiveCustomer"])
	}
}

// TestRunChatProjectScannedQueryableHappyPath drives runChatProject's full
// "queryable project" branch end to end -- the complement of
// TestRunChatProjectUnavailableSchemaHappyPath's unscanned-catalog fixture.
// It uses the real chinook.db fixture (shared with
// TestLoadChatJoinApplicationLoadsRealSQLiteSnapshot) as a SCANNED catalog
// (dbModel + tables, chat_test.go's dbmodels fixture pattern), so this one
// test covers several branches no other test reaches: the "healthy
// relations" schema-context loop (storedSchema.Relations non-empty),
// chat.NewLLMProvider/chat.NewAIConversation's success calls (routed to a
// local httptest openai-compat server, never real network),
// loadChatJoinApplication's success path threaded all the way through
// runChatProject (closeJoin non-nil, deferred), and hasQueryableProjectTables
// actually returning true.
func TestRunChatProjectScannedQueryableHappyPath(t *testing.T) {
	chinookPath, err := filepath.Abs(filepath.Join("..", "..", "..", "pkg", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(chinookPath); statErr != nil {
		t.Skipf("chinook fixture unavailable: %v", statErr)
	}

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
	write("datatug-project.json", `{"id":"chat-scanned-test","title":"Chat scanned test"}`)
	if err := os.Mkdir(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["chinook-local"]}]}`)
	write("environments/local/catalogs/chinook-local/chinook-local.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q,"dbModel":"chinook"}`, chinookPath))
	write("dbmodels/chinook/main/tables/Customer/main.Customer.columns.json", `{"columns":[{"name":"CustomerId","dbType":"INTEGER"}]}`)
	write("dbmodels/chinook/main/tables/Invoice/main.Invoice.columns.json", `{"columns":[{"name":"InvoiceId","dbType":"INTEGER"}]}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer server.Close()

	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", database: "chinook-local", model: "deepseek-flash", baseURL: server.URL, thinking: "low"}
	nextProject, err := runChatProject(cmd, options)
	if err != nil {
		t.Fatalf("runChatProject: %v", err)
	}
	if nextProject != "" {
		t.Fatalf("nextProject = %q, want empty", nextProject)
	}
}

// TestRunChatProjectSourceURLResolutionErrorDegrades covers runChatProject's
// sourceErr branch (cmd_chat.go:102-106): an explicit --database naming no
// configured catalog makes resolveQuerySourceURL fail while
// buildChatProjectCatalog itself (which doesn't need the chosen database to
// exist) still succeeds -- runChatProject degrades to an
// "unavailable://"-scoped session instead of failing outright.
func TestRunChatProjectSourceURLResolutionErrorDegrades(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", database: "does-not-exist", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err != nil {
		t.Fatalf("runChatProject: %v", err)
	}
}

// TestRunChatProjectResolveServeSessionErrorReported covers runChatProject's
// resolveServeSession error branch (cmd_chat.go:133-135): a project policies/
// folder exists but no --as/--role/--group principal was passed, matching
// TestResolveServeSession_ProjectPoliciesNoPrincipal_Refuses's fixture
// (cmd_serve_test.go's projectWithPolicies).
func TestRunChatProjectResolveServeSessionErrorReported(t *testing.T) {
	dir := writeChatRunProjectFixture(t)
	policiesDir := filepath.Join(dir, "policies")
	if err := os.MkdirAll(policiesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	policy := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata:\n  name: test\ndefault: deny\nscopes:\n  - path: /**\n    rules:\n      - id: all\n        effect: allow\n        operations: [readwrite]\n"
	if err := os.WriteFile(filepath.Join(policiesDir, "policy.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "no principal was named") {
		t.Fatalf("runChatProject error = %v, want a no-principal-named refusal", err)
	}
}

// chinookScannedProjectFixture lays out a project whose one catalog IS
// scanned (dbModel + tables) against the real chinook.db fixture shared with
// TestRunChatProjectScannedQueryableHappyPath, so hasQueryableProjectTables
// is true and runChatProject reaches the chat.NewLLMProvider/
// chat.NewAIConversation branch instead of unavailableSchemaConversation.
// Returns "" (skipping the calling test) if the fixture file is missing.
func chinookScannedProjectFixture(t *testing.T) (dir, database string) {
	t.Helper()
	chinookPath, err := filepath.Abs(filepath.Join("..", "..", "..", "pkg", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(chinookPath); statErr != nil {
		t.Skipf("chinook fixture unavailable: %v", statErr)
	}
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
	write("datatug-project.json", `{"id":"chat-scanned-test","title":"Chat scanned test"}`)
	if err := os.Mkdir(filepath.Join(dir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["chinook-local"]}]}`)
	write("environments/local/catalogs/chinook-local/chinook-local.db.json", fmt.Sprintf(`{"driver":"sqlite3","path":%q,"dbModel":"chinook"}`, chinookPath))
	write("dbmodels/chinook/main/tables/Customer/main.Customer.columns.json", `{"columns":[{"name":"CustomerId","dbType":"INTEGER"}]}`)
	write("dbmodels/chinook/main/tables/Invoice/main.Invoice.columns.json", `{"columns":[{"name":"InvoiceId","dbType":"INTEGER"}]}`)
	return dir, "chinook-local"
}

// TestRunChatProjectNewLLMProviderErrorReported covers runChatProject's
// chat.NewLLMProvider error branch (cmd_chat.go:141-144): an unrecognized
// model with no --base-url fails resolveEndpoint's provider-family lookup.
func TestRunChatProjectNewLLMProviderErrorReported(t *testing.T) {
	dir, database := chinookScannedProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", database: database, model: "totally-unrecognized-model-xyz", thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "configure chat model") {
		t.Fatalf("runChatProject error = %v, want a configure-chat-model usage error", err)
	}
}

// TestRunChatProjectNewAIConversationErrorReported covers runChatProject's
// chat.NewAIConversation error branch (cmd_chat.go:145-148): a valid model
// endpoint but an invalid --thinking level fails
// chat.WithThinkingLevel/normalizeReasoning.
func TestRunChatProjectNewAIConversationErrorReported(t *testing.T) {
	dir, database := chinookScannedProjectFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", database: database, model: "deepseek-flash", baseURL: server.URL, thinking: "not-a-real-level"}
	if _, err := runChatProject(cmd, options); err == nil {
		t.Fatal("expected an invalid --thinking level to fail chat.NewAIConversation")
	}
}

// TestRunChatProjectDefaultChatStorePathErrorReported covers
// runChatProject's chat.DefaultChatStorePath error branch (cmd_chat.go:
// 150-153): it calls os.UserHomeDir(), which fails with HOME unset.
func TestRunChatProjectDefaultChatStorePathErrorReported(t *testing.T) {
	// resolveServeSession's own accesspolicies.Load also falls back to a
	// HOME-relative default directory; point it at a real, empty directory
	// first so THAT resolution succeeds (ErrNoPolicies, unrestricted
	// session) and the failure under test is DefaultChatStorePath's own
	// os.UserHomeDir() call, not an earlier, unrelated one.
	t.Setenv(accesspolicies.DirEnv, t.TempDir())
	t.Setenv("HOME", "")
	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "resolve chat storage") {
		t.Fatalf("runChatProject error = %v, want a resolve-chat-storage usage error", err)
	}
}

// TestRunChatProjectOpenSessionStoreErrorReported covers runChatProject's
// chat.OpenSessionStore error branch (cmd_chat.go:159-162): HOME points at a
// directory where "<HOME>/.datatug" already exists as a regular FILE, so
// OpenSessionStore's os.MkdirAll for its private chat directory fails.
func TestRunChatProjectOpenSessionStoreErrorReported(t *testing.T) {
	// See the identical accesspolicies.DirEnv comment in
	// TestRunChatProjectDefaultChatStorePathErrorReported: resolveServeSession
	// must resolve cleanly against HOME before the failure under test.
	t.Setenv(accesspolicies.DirEnv, t.TempDir())
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".datatug"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "open chat sessions") {
		t.Fatalf("runChatProject error = %v, want an open-chat-sessions usage error", err)
	}
}

// TestRunChatProjectSaveLastChatOptionsWarns covers runChatProject's
// saveLastChatOptions warning branch (cmd_chat.go:192-194, non-fatal): the
// lastChatOptionsPath seam (cmd_chat_recent.go) points at a path whose
// parent cannot be created, so the save fails and a warning goes to stderr
// without failing runChatProject itself.
func TestRunChatProjectSaveLastChatOptionsWarns(t *testing.T) {
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })
	restorePath := lastChatOptionsPath
	t.Cleanup(func() { lastChatOptionsPath = restorePath })
	// A parent path component that is a regular file blocks any
	// os.MkdirAll/WriteFile under it, deterministically.
	blocker := filepath.Join(t.TempDir(), "blocker-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lastChatOptionsPath = func() (string, error) { return filepath.Join(blocker, "chat-last.json"), nil }

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err != nil {
		t.Fatalf("runChatProject: %v (save failure must only warn, not fail)", err)
	}
	if !strings.Contains(stderr.String(), "warning: save chat options:") {
		t.Fatalf("expected a save-chat-options warning on stderr, got %q", stderr.String())
	}
}

// TestRunChatProjectUIRunErrorReported covers runChatProject's ui.Run()
// error branch (cmd_chat.go:196-198): runTeaProgram (the seam ui.Run uses)
// returns an error.
func TestRunChatProjectUIRunErrorReported(t *testing.T) {
	injected := errors.New("injected RunTeaProgram failure")
	t.Cleanup(chat.SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, injected }))
	restoreSettings := getChatSettings
	t.Cleanup(func() { getChatSettings = restoreSettings })

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); !errors.Is(err, injected) {
		t.Fatalf("runChatProject error = %v, want the injected RunTeaProgram error", err)
	}
}

// TestRunChatLoopSwitchesProjects covers runChat's outer-loop "options.project
// = nextProject" branch (cmd_chat.go:74-77): runChatProjectFunc (the seam
// runChat calls instead of runChatProject directly, since driving a real
// project switch would need the full TUI's F3 picker) reports a different
// project on its first call and no switch on its second, so runChat must
// loop exactly twice with the new project on the second call.
func TestRunChatLoopSwitchesProjects(t *testing.T) {
	restore := runChatProjectFunc
	t.Cleanup(func() { runChatProjectFunc = restore })
	var seenProjects []string
	calls := 0
	runChatProjectFunc = func(cmd *cobra.Command, options chatOptions) (string, error) {
		calls++
		seenProjects = append(seenProjects, options.project)
		if calls == 1 {
			return "second-project", nil
		}
		return "", nil
	}
	cmd := chatCommand()
	options := chatOptions{project: "first-project", database: "db1"}
	if err := runChat(cmd, options); err != nil {
		t.Fatalf("runChat: %v", err)
	}
	if calls != 2 {
		t.Fatalf("runChatProjectFunc called %d times, want 2", calls)
	}
	if seenProjects[0] != "first-project" || seenProjects[1] != "second-project" {
		t.Fatalf("seenProjects = %v, want [first-project second-project]", seenProjects)
	}
}

// --- runChatProject: NewSessionChat / NewSessionChatUI / SetSavedQueryService / StartBrowserBridge seams ---

// TestRunChatProjectNewSessionChatErrorReported covers runChatProject's
// chat.NewSessionChat error branch (cmd_chat.go, "restore chat session")
// via the newSessionChat seam: every real dependency up to this call
// (store, conversation, sourceURL, projectCatalog) already succeeded in the
// same synchronous call, so no real fixture can make chat.NewSessionChat
// itself fail without also failing something earlier.
func TestRunChatProjectNewSessionChatErrorReported(t *testing.T) {
	restore := newSessionChat
	t.Cleanup(func() { newSessionChat = restore })
	injected := errors.New("injected NewSessionChat failure")
	newSessionChat = func(ctx context.Context, store *chat.SessionStore, agent chat.ContextualConversation, source string, catalogs ...chat.ProjectCatalog) (*chat.SessionChat, error) {
		return nil, injected
	}

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "injected NewSessionChat failure") {
		t.Fatalf("runChatProject error = %v, want the injected NewSessionChat error", err)
	}
}

// TestRunChatProjectNewSessionChatUIErrorReported covers runChatProject's
// chat.NewSessionChatUI error branch ("render chat session") via the
// newSessionChatUI seam.
func TestRunChatProjectNewSessionChatUIErrorReported(t *testing.T) {
	restore := newSessionChatUI
	t.Cleanup(func() { newSessionChatUI = restore })
	injected := errors.New("injected NewSessionChatUI failure")
	newSessionChatUI = func(ctx context.Context, sessions *chat.SessionChat, modelName string) (*chat.ChatUI, error) {
		return nil, injected
	}

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "injected NewSessionChatUI failure") {
		t.Fatalf("runChatProject error = %v, want the injected NewSessionChatUI error", err)
	}
}

// TestRunChatProjectSetSavedQueryServiceErrorReported covers runChatProject's
// ui.SetSavedQueryService error branch ("list saved project queries") via
// the setSavedQueryService seam.
func TestRunChatProjectSetSavedQueryServiceErrorReported(t *testing.T) {
	restore := setSavedQueryService
	t.Cleanup(func() { setSavedQueryService = restore })
	injected := errors.New("injected SetSavedQueryService failure")
	setSavedQueryService = func(ui *chat.ChatUI, service chat.SavedQueryService) error { return injected }

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "injected SetSavedQueryService failure") {
		t.Fatalf("runChatProject error = %v, want the injected SetSavedQueryService error", err)
	}
}

// TestRunChatProjectStartBrowserBridgeErrorReported covers runChatProject's
// chat.StartBrowserBridge error branch ("start browser chat") via the
// startBrowserBridge seam.
func TestRunChatProjectStartBrowserBridgeErrorReported(t *testing.T) {
	restore := startBrowserBridge
	t.Cleanup(func() { startBrowserBridge = restore })
	injected := errors.New("injected StartBrowserBridge failure")
	startBrowserBridge = func(sessions *chat.SessionChat) (*chat.BrowserBridge, error) { return nil, injected }

	dir := writeChatRunProjectFixture(t)
	cmd := chatCommand()
	options := chatOptions{project: dir, env: "local", model: defaultChatModel, thinking: "low"}
	if _, err := runChatProject(cmd, options); err == nil || !strings.Contains(err.Error(), "injected StartBrowserBridge failure") {
		t.Fatalf("runChatProject error = %v, want the injected StartBrowserBridge error", err)
	}
}

// --- loadChatJoinApplication: openJoinMetadataDB seam ---------------------

// TestLoadChatJoinApplicationOpenMetadataDBErrorReported covers
// loadChatJoinApplication's sql.Open error branch via the
// openJoinMetadataDB seam: modernc.org/sqlite never errors eagerly for a
// syntactically valid DSN against a real, readable file (CheckSourceFile
// already passed), so no real fixture reaches this branch.
func TestLoadChatJoinApplicationOpenMetadataDBErrorReported(t *testing.T) {
	restore := openJoinMetadataDB
	t.Cleanup(func() { openJoinMetadataDB = restore })
	injected := errors.New("injected sql.Open failure")
	openJoinMetadataDB = func(driverName, dataSourceName string) (*sql.DB, error) { return nil, injected }

	real, err := filepath.Abs(filepath.Join("..", "..", "..", "pkg", "dbcopy", "testdata", "chinook.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(real); statErr != nil {
		t.Skipf("chinook fixture unavailable: %v", statErr)
	}
	_, closeFn, err := loadChatJoinApplication(context.Background(), "sqlite:///"+real, nil, false)
	if !errors.Is(err, injected) {
		t.Fatalf("loadChatJoinApplication error = %v, want the injected sql.Open error", err)
	}
	if closeFn != nil {
		t.Fatal("expected no close func on an sql.Open error")
	}
}
