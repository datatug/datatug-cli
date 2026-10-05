package api

import (
	"context"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

// A folder's path and name are folders and a folder name under the project, joined
// into a path by the store, so every entry that takes one checks it (and the project)
// before the store is asked, and none says what the store said when it fails.

// CreateFolder creates a new folder for queries
func CreateFolder(ctx context.Context, request dto.CreateFolder) (folder *datatug.Folder, err error) {
	if err = request.ProjectRef.Validate(); err != nil {
		return nil, err
	}
	if err = ValidateProjectIdentifier("project", request.ProjectID); err != nil {
		return nil, err
	}
	// The path is the folders the new folder goes into: none, for the root.
	if request.Path != "" {
		if err = ValidatePathIdentifier("path", request.Path); err != nil {
			return nil, err
		}
	}
	if err = ValidateIdentifier("name", request.Name); err != nil {
		return nil, err
	}
	// The project comes from the body here, not from a query that a route resolved the
	// store of (see ResolveStoreID), so a project this process does not serve is refused
	// here, before a project store is asked for it, with the same text. The route is
	// answered by apicore, which gives a status of 400 to a bad request only.
	if _, err = servedProjectDir(request.ProjectID); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	store, err := projectStoreForID(request.StoreID, request.ProjectID)
	if err != nil {
		return nil, err
	}
	folder = &datatug.Folder{
		Name: request.Name,
		Note: request.Note,
	}
	if err = store.SaveFolder(ctx, request.Path, folder); err != nil {
		shown := dbcopy.QueryIDDisplay(strings.TrimPrefix(request.Path+"/"+request.Name, "/"))
		return nil, itemWriteFailed("create", "folder", shown, err)
	}
	return folder, nil
}

// DeleteFolder deletes queries folder
func DeleteFolder(ctx context.Context, ref dto.ProjectItemRef) error {
	if ref.ProjectID == "" {
		return validation.NewErrRequestIsMissingRequiredField("projectID")
	}
	if err := validateProjectPathItem(ref.ProjectID, "folderID", ref.ID); err != nil {
		return err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	if err = store.DeleteFolder(ctx, ref.ID); err != nil {
		return itemWriteFailed("delete", "folder", dbcopy.QueryIDDisplay(ref.ID), err)
	}
	return nil
}
