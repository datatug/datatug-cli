package api

import (
	"context"
	"fmt"
	"log"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/strongo/validation"
)

func validateEntityInput(projectID, entityID string) (err error) {
	if err = validateProjectInput(projectID); err != nil {
		return
	}
	if entityID == "" {
		return validation.NewErrRequestIsMissingRequiredField("entityID")
	}
	return
}

// GetEntity returns board by ID
func GetEntity(ctx context.Context, ref dto.ProjectItemRef) (entity *datatug.Entity, err error) {
	if err = validateEntityInput(ref.ProjectID, ref.ID); err != nil {
		return
	}
	store, err := storage.GetStore(ctx, ref.StoreID)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoNilness
	projectStore := store.GetProjectStore(ref.ProjectID)
	return projectStore.LoadEntity(ctx, ref.ID)
}

// GetAllEntities returns all entities
func GetAllEntities(ctx context.Context, ref dto.ProjectRef) (entity datatug.Entities, err error) {
	if err = validateProjectInput(ref.ProjectID); err != nil {
		return
	}
	store, err := storage.GetStore(ctx, ref.StoreID)
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
	store, err := storage.GetStore(ctx, ref.StoreID)
	if err != nil {
		return err
	}
	//goland:noinspection GoNilness
	projectStore := store.GetProjectStore(ref.ProjectID)
	return projectStore.DeleteEntity(ctx, ref.ID)
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
	store, err := storage.GetStore(ctx, ref.StoreID)
	if err != nil {
		return err
	}
	//goland:noinspection GoNilness
	projectStore := store.GetProjectStore(ref.ProjectID)
	return projectStore.SaveEntity(ctx, entity)
}
