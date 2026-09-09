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

// TestSaveProjectWithDbModels_PersistsDbModels proves saveProjectWithDbModels
// works around datatug-core v0.17.0's filestore.SaveProject, which does not
// persist project.DbModels (see the comment on saveProjectWithDbModels).
func TestSaveProjectWithDbModels_PersistsDbModels(t *testing.T) {
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

	if err := saveProjectWithDbModels(context.Background(), projectStore, project); err != nil {
		t.Fatalf("saveProjectWithDbModels: %v", err)
	}

	dbModelFile := filepath.Join(dir, "dbmodels", "dbmodel1.dbmodel.json")
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

// TestSaveProjectWithDbModels_NoDbModels proves the wrapper is a no-op extra
// step (beyond the plain SaveProject) when there are no DB models to save.
func TestSaveProjectWithDbModels_NoDbModels(t *testing.T) {
	dir := t.TempDir()
	store, projID := filestore.NewSingleProjectStore(dir, "proj2")
	projectStore := store.GetProjectStore(projID)

	project := &datatug.Project{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: projID}, Access: "private"},
		Created:     &datatug.ProjectCreated{At: time.Now()},
	}

	if err := saveProjectWithDbModels(context.Background(), projectStore, project); err != nil {
		t.Fatalf("saveProjectWithDbModels: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dbmodels")); !os.IsNotExist(err) {
		t.Errorf("expected no dbmodels dir to be created, stat error = %v", err)
	}
}
