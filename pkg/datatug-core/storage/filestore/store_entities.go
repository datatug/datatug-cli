package filestore

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/filetug/pkg/fsutils"
	"github.com/strongo/validation"
)

var _ datatug.EntitiesStore = (*fsEntitiesStore)(nil)

func newFsEntitiesStore(projectPath string) fsEntitiesStore {
	return fsEntitiesStore{
		fsProjectItemsStore: newFileProjectItemsStore[datatug.Entities, *datatug.Entity, datatug.Entity](
			path.Join(projectPath, storage.EntitiesFolder), storage.EntityFileSuffix,
		),
	}
}

type fsEntitiesStore struct {
	fsProjectItemsStore[datatug.Entities, *datatug.Entity, datatug.Entity]
}

// entityDirPath returns the per-entity directory: <entitiesDir>/<id>.
func (s fsEntitiesStore) entityDirPath(id string) string {
	return path.Join(s.dirPath, id)
}

// entityFilePath returns the canonical entity file path:
// <entitiesDir>/<id>/<id>.entity.json.
func (s fsEntitiesStore) entityFilePath(id string) string {
	return path.Join(s.entityDirPath(id), storage.JsonFileName(id, s.itemFileSuffix))
}

func (s fsEntitiesStore) LoadEntity(_ context.Context, id string, o ...datatug.StoreOption) (*datatug.Entity, error) {
	_ = datatug.GetStoreOptions(o...)
	entity := new(datatug.Entity)
	if err := fsutils.ReadJSONFile(s.entityFilePath(id), true, entity); err != nil {
		return entity, fmt.Errorf("failed to load entity[%s] from project: %w", id, err)
	}
	entity.SetID(id)
	return entity, nil
}

func (s fsEntitiesStore) LoadEntities(ctx context.Context, o ...datatug.StoreOption) (datatug.Entities, error) {
	_ = datatug.GetStoreOptions(o...)
	dirEntries, err := os.ReadDir(s.dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entities datatug.Entities
	for _, de := range dirEntries {
		if !de.IsDir() {
			continue
		}
		id := de.Name()
		if _, statErr := os.Stat(s.entityFilePath(id)); statErr != nil {
			continue
		}
		entity, loadErr := s.LoadEntity(ctx, id, o...)
		if loadErr != nil {
			return nil, loadErr
		}
		entities = append(entities, entity)
	}
	sort.Slice(entities, func(i, j int) bool {
		return strings.Compare(entities[i].GetID(), entities[j].GetID()) < 0
	})
	return entities, nil
}

func (s fsEntitiesStore) DeleteEntity(_ context.Context, id string) error {
	filePath := s.entityFilePath(id)
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Remove(filePath)
}

func (s fsEntitiesStore) SaveEntities(ctx context.Context, entities datatug.Entities) (err error) {
	return saveItems(s.dirPath, len(entities), func(i int) func() error {
		return func() error {
			return s.SaveEntity(ctx, entities[i])
		}
	})
}

func (s fsEntitiesStore) SaveEntity(_ context.Context, entity *datatug.Entity) (err error) {
	if entity == nil {
		return validation.NewErrRequestIsMissingRequiredField("entity")
	}
	if entity.ID == "" {
		return validation.NewErrBadRequestFieldValue("entity", validation.NewErrRecordIsMissingRequiredField("GetID").Error())
	}
	if len(entity.Fields) == 0 && entity.Fields != nil {
		entity.Fields = nil
	}
	if err = saveJSONFile(s.entityDirPath(entity.ID), s.itemFileName(entity.ID), entity); err != nil {
		return fmt.Errorf("failed to save entity file: %w", err)
	}
	return nil
}
