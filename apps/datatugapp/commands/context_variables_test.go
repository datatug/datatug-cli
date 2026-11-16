package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/querybody"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// myDataProject writes a stand-in for the datatug/my-data project (backstage
// spec/features/my-data-project) holding its "My projects" and "My user
// record" queries verbatim. It declares no environment and no database: the
// Firestore source those queries are meant for is configured nowhere, so a
// run that got past the refusal could not reach it either.
func myDataProject(t *testing.T) string {
	t.Helper()
	tempDatatugHome(t)
	dir := filepath.Join(t.TempDir(), "my-data")
	query := func(id, title, text string) *datatug.QueryDef {
		return &datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id, Title: title}},
			Type:        datatug.QueryTypeTugQL,
			Text:        text,
		}
	}
	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{Access: "public", ProjItemBrief: datatug.ProjItemBrief{ID: "my-data", Title: "My DataTug Data"}},
		Created:     &datatug.ProjectCreated{At: time.Now()},
		Queries: &datatug.QueriesFolder{Items: datatug.QueryDefs{
			query("my-projects", "My projects", "FROM spaces/{SneatUserPersonalSpaceID}/ext/datatug/projects\nORDER BY title"),
			query("my-user-record", "My user record", "FROM users\nWHERE __key__ = @SneatUserID"),
		}},
	}
	if err := filestore.NewProjectStore("my-data", dir).SaveProject(context.Background(), project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	return dir
}

const myProjectsRefusal = "query uses context variable SneatUserPersonalSpaceID, which only the DataTug web app binds for a signed-in user: DataTug Core and the CLI refuse to run it rather than run it with an empty value\n"

// AC core-refuses-context-variables: `datatug validate` passes on the project,
// and running "My projects" exits 2 naming SneatUserPersonalSpaceID. The
// refusal is the only output: no access session was resolved, no progress
// line printed, and no source opened.
func TestMyDataProjectValidatesAndRefusesToRunMyProjects(t *testing.T) {
	dir := myDataProject(t)
	if err := validateProject(dir); err != nil {
		t.Fatalf("datatug validate: %v", err)
	}

	stdout, stderr, code := runQuery(t, "", "--project", dir, "--query", "my-projects")
	if code != exitCodeUsage {
		t.Fatalf("exit = %d, want %d; stderr = %q", code, exitCodeUsage, stderr)
	}
	if stderr != myProjectsRefusal {
		t.Fatalf("stderr = %q, want only the refusal %q", stderr, myProjectsRefusal)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing", stdout)
	}
}

// AC reserved-names: a query declaring a parameter named SneatUserID fails
// `datatug validate`, naming the reserved variable.
func TestMyDataProjectReservedParameterFailsValidate(t *testing.T) {
	dir := myDataProject(t)
	file := filepath.Join(dir, storage.QueriesFolder, "my-user-record.query.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var q map[string]any
	if err = json.Unmarshal(data, &q); err != nil {
		t.Fatal(err)
	}
	q["parameters"] = []any{map[string]any{"id": "SneatUserID", "type": "string"}}
	if data, err = json.Marshal(q); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = validateProject(dir)
	if err == nil || !strings.Contains(err.Error(), "SneatUserID is a reserved context variable name") {
		t.Fatalf("expected datatug validate to fail naming SneatUserID, got: %v", err)
	}
}

// An ad-hoc TugQL document, or a --from collection path, that uses a context
// variable exits 2 before its source is opened: the sqlite file named by
// --db does not exist, and opening it would fail with a database exit code.
func TestAdHocQueryRefusesContextVariables(t *testing.T) {
	document, err := querybody.Structured(&datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: "FROM users\nWHERE __key__ = @SneatUserID"})
	if err != nil {
		t.Fatal(err)
	}
	db := "sqlite://" + filepath.Join(t.TempDir(), "missing.sqlite")
	for name, run := range map[string]struct {
		stdin, variable string
		argv            []string
	}{
		"file":  {stdin: document, variable: "SneatUserID", argv: []string{"--db", db, "--file", "-", "--no-policies"}},
		"from":  {variable: "SneatUserPersonalSpaceID", argv: []string{"--db", db, "--from", "spaces/{SneatUserPersonalSpaceID}/ext/datatug/projects", "--no-policies"}},
		"plain": {variable: "", argv: []string{"--db", db, "--from", "projects", "--no-policies"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runQuery(t, run.stdin, run.argv...)
			if run.variable == "" {
				if code == exitCodeUsage || strings.Contains(stderr, "context variable") {
					t.Fatalf("a query without context variables was refused: exit %d, %q", code, stderr)
				}
				return
			}
			if code != exitCodeUsage || !strings.Contains(stderr, "context variable "+run.variable+",") {
				t.Fatalf("exit = %d, stderr = %q; want exit %d naming %s", code, stderr, exitCodeUsage, run.variable)
			}
		})
	}
}

// Chat runs and parameter lookups refuse a saved query using a context
// variable before resolving its source (the project has no environment).
func TestChatSavedQueriesRefuseContextVariables(t *testing.T) {
	dir := myDataProject(t)
	store, id := filestore.NewSingleProjectStore(dir, "my-data")
	service := chatSavedQueries{projectDir: dir, store: store.GetProjectStore(id), projectID: id, executor: secureread.NewExecutor(secureread.Session{Unrestricted: true})}
	ctx := context.Background()
	if _, err := service.Run(ctx, "my-projects"); !errors.Is(err, datatug.ErrContextVariableNotBound) {
		t.Fatalf("Run: %v, want a context-variable refusal", err)
	}
	if _, err := service.LookupParameter(ctx, "my-user-record", "SneatUserID"); !errors.Is(err, datatug.ErrContextVariableNotBound) {
		t.Fatalf("LookupParameter: %v, want a context-variable refusal", err)
	}
}
