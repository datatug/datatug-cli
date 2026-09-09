package commands

import (
	"context"
	"fmt"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// dbModelsSaver is satisfied by datatug-core's filestore project store,
// whose concrete (unexported) type embeds a working DB-models store and so
// has this exported method - datatug.ProjectStore just doesn't declare it.
// Declaring the same method locally lets saveProjectWithDbModels reach it
// through a plain type assertion, without naming or forking that concrete
// type.
type dbModelsSaver interface {
	SaveDbModels(ctx context.Context, dbModels datatug.DbModels) error
}

// saveProjectWithDbModels saves a project and, unlike a plain
// store.SaveProject, also persists project.DbModels.
//
// datatug-core v0.17.0's filestore.SaveProject
// (github.com/datatug/datatug-core/pkg/storage/filestore/store_project_saver.go)
// does not save DB models - the code path is there (a working
// fsDbModelsStore.SaveDbModels is embedded on the project store, per
// db_models_store.go) but SaveProject's parallel-save block for DB models is
// stubbed with a "Saving %v DB models IS NOT IMPLEMENTED YET" log line
// instead of calling it. datatug.ProjectStore also doesn't declare
// SaveDbModels, so it can't be reached through the interface directly. This
// wrapper reaches the store's own already-correct SaveDbModels via a
// structural (dbModelsSaver) type assertion instead of duplicating its
// file-writing logic or forking the model.
//
// Flagged in the PR for datatug-core: SaveProject should call SaveDbModels
// like it calls SaveEntities/SaveEnvironments/saveBoards.
func saveProjectWithDbModels(ctx context.Context, store datatug.ProjectStore, project *datatug.Project) error {
	if err := store.SaveProject(ctx, project); err != nil {
		return err
	}
	if len(project.DbModels) == 0 {
		return nil
	}
	saver, ok := store.(dbModelsSaver)
	if !ok {
		return fmt.Errorf("project store %T does not support saving DB models (datatug-core filestore.SaveProject does not persist them)", store)
	}
	if err := saver.SaveDbModels(ctx, project.DbModels); err != nil {
		return fmt.Errorf("failed to save DB models: %w", err)
	}
	return nil
}
