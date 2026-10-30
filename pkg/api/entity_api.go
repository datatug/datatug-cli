package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sync"

	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

// entityIDField is the field the refusal of an entity ID names.
const entityIDField = "entityID"

// validateEntityInput checks the project and the ID of an entity, which the store
// joins into a folder and a file name: the ID must be a plain name.
func validateEntityInput(projectID, entityID string) (err error) {
	if err = validateProjectInput(projectID); err != nil {
		return
	}
	if entityID == "" {
		return validation.NewErrRequestIsMissingRequiredField(entityIDField)
	}
	return ValidateIdentifier(entityIDField, entityID)
}

// GetEntity returns board by ID
func GetEntity(ctx context.Context, ref dto.ProjectItemRef) (entity *datatug.Entity, err error) {
	if err = validateEntityInput(ref.ProjectID, ref.ID); err != nil {
		return
	}
	projectStore, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	entity, err = projectStore.LoadEntity(ctx, ref.ID)
	if err != nil {
		return nil, itemNotFound("entity", dbcopy.SourceIDDisplay(ref.ID), err)
	}
	return entity, nil
}

// GetAllEntities returns all entities
func GetAllEntities(ctx context.Context, ref dto.ProjectRef) (entity datatug.Entities, err error) {
	if err = validateProjectInput(ref.ProjectID); err != nil {
		return
	}
	projectStore, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	entities, err := projectStore.LoadEntities(ctx)
	if err != nil {
		return nil, itemsNotLoaded("entities", dbcopy.SourceIDDisplay(ref.ProjectID), err)
	}
	return entities, nil
}

// DeleteEntity deletes board
func DeleteEntity(ctx context.Context, ref dto.ProjectItemRef) error {
	if err := validateEntityInput(ref.ProjectID, ref.ID); err != nil {
		return err
	}
	projectStore, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return deleteItem("entity", dbcopy.SourceIDDisplay(ref.ID),
		func() error { _, err := projectStore.LoadEntity(ctx, ref.ID); return err },
		func() error { return projectStore.DeleteEntity(ctx, ref.ID) })
}

// entityWriteMu serialises creates and saves of entities in this process, so two
// creates of one ID cannot both pass the existence check and a save cannot slip
// in between a create's check and its write.
var entityWriteMu sync.Mutex

// SaveEntity saves board
func SaveEntity(ctx context.Context, ref dto.ProjectRef, entity *datatug.Entity) error {
	return writeEntity(ctx, ref, entity, false)
}

// CreateEntity saves a new entity. An entity that is already there under that ID
// is not replaced: the answer says so.
func CreateEntity(ctx context.Context, ref dto.ProjectRef, entity *datatug.Entity) error {
	return writeEntity(ctx, ref, entity, true)
}

func writeEntity(ctx context.Context, ref dto.ProjectRef, entity *datatug.Entity, create bool) error {
	if entity.ID == "" {
		entity.ID = entity.Title
		entity.Title = ""
	} else if entity.Title == entity.ID {
		entity.Title = ""
	}
	if err := validateEntityInput(ref.ProjectID, entity.ID); err != nil {
		return err
	}
	if err := entity.Validate(); err != nil {
		return fmt.Errorf("entity is not valid: %w", err)
	}
	log.Printf("Saving entity: %+v", entity)
	projectStore, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	shown := dbcopy.SourceIDDisplay(entity.ID)
	entityWriteMu.Lock()
	defer entityWriteMu.Unlock()
	if create {
		switch _, err := projectStore.LoadEntity(ctx, entity.ID); {
		case err == nil:
			return itemExists("entity", shown)
		case !errors.Is(err, fs.ErrNotExist) && !record.IsNotFound(err):
			return itemWriteFailed("create", "entity", shown, err)
		}
	}
	if err = projectStore.SaveEntity(ctx, entity); err != nil {
		return itemWriteFailed("save", "entity", shown, err)
	}
	return nil
}
