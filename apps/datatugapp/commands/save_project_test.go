package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// TestSaveProject_PersistsDbModels proves datatug-core v0.21.0's plain
// filestore.SaveProject now persists project.DbModels itself (PR #306) -
// this repo used to need a local saveProjectWithDbModels workaround for
// v0.17.0..v0.20.0, where SaveProject's DB-models save block was a stub;
// that workaround is gone as of the v0.21.0 bump, and every former call site
// now calls store.SaveProject directly. A brand-new DB model defaults to the
// nested <dbModelsDir>/<id>/<id>.dbmodel.json layout (v0.21.0's dual-layout
// support, same rule entities already had from v0.20.0).
func TestSaveProject_PersistsDbModels(t *testing.T) {
	dir := t.TempDir()
	store, projID := filestore.NewSingleProjectStore(dir, "proj1")
	projectStore := store.GetProjectStore(projID)

	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: projID}, Access: "private"},
		Created:     &datatug.ProjectCreated{At: time.Now()},
		DbModels: datatug.DbModels{
			&datatug.DbModel{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dbmodel1"}}},
		},
	}

	if err := projectStore.SaveProject(context.Background(), project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	dbModelFile := filepath.Join(dir, "dbmodels", "dbmodel1", "dbmodel1.dbmodel.json")
	data, err := os.ReadFile(dbModelFile)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", dbModelFile, err)
	}
	var saved datatug.DbModel
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to decode saved db model: %v", err)
	}
	if saved.ID != "dbmodel1" {
		t.Errorf("saved db model ID = %q, want %q", saved.ID, "dbmodel1")
	}
}

// TestSaveProject_NoDbModels proves SaveProject is a no-op for DB models
// (no dbmodels dir created) when there are none to save.
func TestSaveProject_NoDbModels(t *testing.T) {
	dir := t.TempDir()
	store, projID := filestore.NewSingleProjectStore(dir, "proj2")
	projectStore := store.GetProjectStore(projID)

	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: projID}, Access: "private"},
		Created:     &datatug.ProjectCreated{At: time.Now()},
	}

	if err := projectStore.SaveProject(context.Background(), project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dbmodels")); !os.IsNotExist(err) {
		t.Errorf("expected no dbmodels dir to be created, stat error = %v", err)
	}
}
