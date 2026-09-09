package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// TestInitCommandAction_CreatesLoadableValidProject is the repro/regression
// test for the audit's "datatug init panics unconditionally" finding
// (datatug/datatug spec/research/2026-09-09-current-state-audit.md,
// pkg/storage/vars.go:19-21 via cmd_init_project.go:112): the handler built
// the right store at line 76 (filestore.NewSingleProjectStore, assigned to
// storage.Current) and then ignored it, calling the permanently-unwired
// storage.NewDatatugStore("") instead, which panics
// ("var 'NewDatatugStore' is not initialized"). Proves `datatug init` now
// produces a project datatug-core's own LoadProject can read back and that
// passes Validate() - the exact two checks `datatug validate` runs.
func TestInitCommandAction_CreatesLoadableValidProject(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "proj")

	cmd := initCommand()
	cmd.SetArgs([]string{"myproj", projectDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	store := filestore.NewProjectStore("myproj", projectDir)
	project, err := store.LoadProject(context.Background())
	if err != nil {
		t.Fatalf("LoadProject after init: %v", err)
	}
	if project.ID != "myproj" {
		t.Errorf("project.ID = %q, want %q", project.ID, "myproj")
	}
	if err := project.Validate(); err != nil {
		t.Errorf("project.Validate() after init: %v", err)
	}
}

// TestInitCommandAction_DefaultsProjectIDFromDir covers the "project ID
// argument omitted" path (initCommandAction reads it via argAt, matching
// urfave/cli/v3's StringArg semantics where a missing positional resolves to
// "") - filestore.NewSingleProjectStore fills in storage.SingleProjectID.
func TestInitCommandAction_DefaultsProjectIDFromDir(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "proj")

	cmd := initCommand()
	cmd.SetArgs([]string{"", projectDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	store, projID := filestore.NewSingleProjectStore(projectDir, "")
	project, err := store.GetProjectStore(projID).LoadProject(context.Background())
	if err != nil {
		t.Fatalf("LoadProject after init: %v", err)
	}
	if err := project.Validate(); err != nil {
		t.Errorf("project.Validate() after init: %v", err)
	}
}
