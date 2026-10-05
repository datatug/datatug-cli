package api

import (
	"context"
	"fmt"
	"log"

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
	store, err := storeFor(ref.StoreID)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoNilness
	projectStore := store.GetProjectStore(ref.ProjectID)
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
	store, err := storeFor(ref.StoreID)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoNilness
	projectStore := store.GetProjectStore(ref.ProjectID)
	return projectStore.LoadEntities(ctx)
}

// DeleteEntity deletes board
func DeleteEntity(ctx context.Context, ref dto.ProjectItemRef) error {
	if err := validateEntityInput(ref.ProjectID, ref.ID); err != nil {
		return err
	}
	store, err := storeFor(ref.StoreID)
	if err != nil {
		return err
	}
	//goland:noinspection GoNilness
	projectStore := store.GetProjectStore(ref.ProjectID)
	if err = projectStore.DeleteEntity(ctx, ref.ID); err != nil {
		return itemWriteFailed("delete", "entity", dbcopy.SourceIDDisplay(ref.ID), err)
	}
	return nil
}

// SaveEntity saves board
func SaveEntity(ctx context.Context, ref dto.ProjectRef, entity *datatug.Entity) error {
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
	store, err := storeFor(ref.StoreID)
	if err != nil {
		return err
	}
	//goland:noinspection GoNilness
	projectStore := store.GetProjectStore(ref.ProjectID)
	if err = projectStore.SaveEntity(ctx, entity); err != nil {
		return itemWriteFailed("save", "entity", dbcopy.SourceIDDisplay(entity.ID), err)
	}
	return nil
}
