package api

import (
	"context"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/strongo/validation"
)

// CreateFolder creates a new folder for queries
func CreateFolder(ctx context.Context, request dto.CreateFolder) (folder *datatug.Folder, err error) {
	if err = request.ProjectRef.Validate(); err != nil {
		return nil, err
	}
	store, err := storage.GetProjectStore(ctx, request.StoreID, request.ProjectID)
	if err != nil {
		return nil, err
	}
	folder = &datatug.Folder{
		Name: request.Name,
		Note: request.Note,
	}
	return folder, store.SaveFolder(ctx, request.Path, folder)
}

// DeleteFolder deletes queries folder
func DeleteFolder(ctx context.Context, ref dto.ProjectItemRef) error {
	if ref.ProjectID == "" {
		return validation.NewErrRequestIsMissingRequiredField("projectID")
	}
	store, err := storage.GetProjectStore(ctx, ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return store.DeleteFolder(ctx, ref.ID)
}
