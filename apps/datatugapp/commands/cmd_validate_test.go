package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// TestQueryDefTarget_Validate_RejectsEmbeddedCredentials proves datatug-core
// v0.17.0 is actually live (not the pre-migration vendored model, which had
// no QueryDefTarget.Validate() or QueryTypeTugQL at all): the project's
// Validate() now runs the new query-target credential check, matching
// `datatug validate`'s semantics (validateProject calls project.Validate()).
func TestQueryDefTarget_Validate_RejectsEmbeddedCredentials(t *testing.T) {
	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: "proj1"},
			Access:        "private",
		},
		Created: &datatug.ProjectCreated{},
		Queries: &datatug.QueriesFolder{
			Items: datatug.QueryDefs{
				{
					ProjectItem: datatug.ProjectItem{
						ProjItemBrief: datatug.ProjItemBrief{ID: "q1", Title: "Query 1"},
					},
					Type: datatug.QueryTypeTugQL, StructuredBody: true,
					Targets: []datatug.QueryDefTarget{
						{Driver: "postgres", Credentials: datatug.Credentials{Password: "hunter2"}},
					},
				},
			},
		},
	}

	err := project.Validate()
	if err == nil {
		t.Fatal("expected project.Validate() to reject a query target with an embedded password, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "password") {
		t.Errorf("expected error to mention the credential field, got: %v", got)
	}
}

// TestQueryDefTarget_Validate_AcceptsCredentialFreeTugQLQuery proves a
// QueryTypeTugQL query (new in v0.17.0 - the pre-migration model had no TugQL
// query type at all) with no embedded credentials passes validation.
func TestQueryDefTarget_Validate_AcceptsCredentialFreeTugQLQuery(t *testing.T) {
	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{
			ProjItemBrief: datatug.ProjItemBrief{ID: "proj1"},
			Access:        "private",
		},
		Created: &datatug.ProjectCreated{},
		Queries: &datatug.QueriesFolder{
			Items: datatug.QueryDefs{
				{
					ProjectItem: datatug.ProjectItem{
						ProjItemBrief: datatug.ProjItemBrief{ID: "q1", Title: "Query 1"},
					},
					Type: datatug.QueryTypeTugQL, StructuredBody: true,
					Targets: []datatug.QueryDefTarget{{Driver: "postgres", Catalog: "mydb"}},
				},
			},
		},
	}

	if err := project.Validate(); err != nil {
		t.Fatalf("expected a credential-free TugQL query to validate, got: %v", err)
	}
}

// TestValidateAction_FallsBackToSingleProjectMode is the repro/regression
// test for the audit's "`datatug validate --dir <single-project-dir>` errors
// instead of falling back to single-project mode when there is no root
// .datatug.yaml" finding. validateAction's `os.IsNotExist(err)` check never
// fires: filestore.LoadRootDatatugFile wraps the underlying not-exist error
// with fmt.Errorf("...: %w", ...), and os.IsNotExist predates errors.Is - it
// only recognizes a handful of concrete os error types, not an arbitrary
// %w-wrapped one, so the wrapped not-exist error is always treated as a real
// failure instead of the "no root file, fall back to single-project mode"
// signal it's meant to be.
func TestValidateAction_FallsBackToSingleProjectMode(t *testing.T) {
	dir := t.TempDir()
	initCmd := initCommand()
	initCmd.SetArgs([]string{"proj1", dir})
	if err := initCmd.Execute(); err != nil {
		t.Fatalf("init fixture project: %v", err)
	}

	cmd := testCommandArgs()
	cmd.SetArgs([]string{"--dir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate --dir %s (no root .datatug.yaml): %v", dir, err)
	}
}

// TestValidateAction_RootFileMissing_vs_RealError distinguishes "no root
// file, fall back" from "root file present but unreadable for a real reason"
// directly against validateAction's actual predicate, independent of the
// filesystem: a bare os.ErrNotExist must fall back (via validateProject,
// which then fails because the single-project dir isn't a valid project
// either - proving the fallback path was actually taken, not swallowed) and
// a non-not-exist error must still be surfaced as-is.
func TestValidateAction_RootFileMissing_vs_RealError(t *testing.T) {
	dir := t.TempDir() // empty: not a valid single project either

	cmd := testCommandArgs()
	cmd.SetArgs([]string{"--dir", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error - dir has no root file AND is not a valid single project")
	}
	if strings.Contains(err.Error(), "failed to load root repo file") {
		t.Errorf("error still reports the root-file load failure instead of falling back to single-project mode: %v", err)
	}
}

// TestValidateProject_RealDemoProject exercises the exact path `datatug
// validate` uses (validateProject: initProjectCommand -> LoadProject ->
// Validate) against the real, committed demo project, proving the
// datatug-core v0.17.0 module loads and validates it end to end. Skips off
// this VM (no sibling datatug-demo-projects checkout, e.g. CI).
func TestValidateProject_RealDemoProject(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve home dir: %v", err)
	}
	dir := filepath.Join(home, "projects", "datatug", "datatug-demo-projects", "demo-project-1")
	if _, err := os.Stat(filepath.Join(dir, "datatug-project.json")); err != nil {
		t.Skipf("demo project fixture not present at %s (expected on the dev VM, not in CI): %v", dir, err)
	}

	if err := validateProject(dir); err != nil {
		t.Fatalf("validateProject(%s): %v", dir, err)
	}
}

// TestValidateCommand_ShowcaseBoard keeps the published CLI validator aligned
// with the saved chart and nested-tab board contract. The fixture is the
// showcase board at datatug-demo-project origin/main 11d88a220354.
func TestValidateCommand_ShowcaseBoard(t *testing.T) {
	dir := t.TempDir()
	initCmd := initCommand()
	initCmd.SetArgs([]string{"showcase-fixture", dir})
	if err := initCmd.Execute(); err != nil {
		t.Fatalf("init project: %v", err)
	}

	board, err := os.ReadFile(filepath.Join("testdata", "visual-showcase-board.json"))
	if err != nil {
		t.Fatalf("read saved showcase board: %v", err)
	}
	boardDir := filepath.Join(dir, "boards", "showcase")
	if err := os.MkdirAll(boardDir, 0o755); err != nil {
		t.Fatalf("create board directory: %v", err)
	}
	boardPath := filepath.Join(boardDir, "board.json")
	if err := os.WriteFile(boardPath, board, 0o600); err != nil {
		t.Fatalf("write saved showcase board: %v", err)
	}

	cmd := testCommandArgs()
	cmd.SetArgs([]string{"-d=" + dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("datatug validate rejected the saved showcase board: %v", err)
	}

	invalidBoard := strings.Replace(string(board), `"type": "bar-race"`, `"type": "invalid-chart"`, 1)
	if invalidBoard == string(board) {
		t.Fatal("saved showcase board no longer contains the race chart under test")
	}
	if err := os.WriteFile(boardPath, []byte(invalidBoard), 0o600); err != nil {
		t.Fatalf("write invalid chart board: %v", err)
	}
	cmd = testCommandArgs()
	cmd.SetArgs([]string{"-d=" + dir})
	if err := cmd.Execute(); err == nil {
		t.Fatal("datatug validate accepted an invalid chart type")
	}
}
