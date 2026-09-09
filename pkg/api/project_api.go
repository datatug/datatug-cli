package api

import (
	"context"
	"fmt"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

func validateProjectInput(projectID string) (err error) {
	if projectID == "" {
		return validation.NewErrRequestIsMissingRequiredField("projectID")
	}
	return nil
}

// GetProjects return all projects
func GetProjects(ctx context.Context, storeID string) ([]datatug.ProjectBrief, error) {
	dal, err := storage.NewDatatugStore(storeID)
	if err != nil {
		return nil, err
	}
	return dal.GetProjects(ctx)
}

// GetProjectSummary returns project summary
func GetProjectSummary(ctx context.Context, ref dto.ProjectRef) (projSummary *datatug.ProjectSummary, err error) {
	if ref.ProjectID == "" {
		return nil, validation.NewErrRequestIsMissingRequiredField("id")
	}
	store, err := storage.GetStore(ctx, ref.StoreID)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoNilness
	project := store.GetProjectStore(ref.ProjectID)
	projectFile, err := project.LoadProjectFile(ctx)
	if err != nil {
		return projSummary, fmt.Errorf("failed to load project file: %w", err)
	}
	return &datatug.ProjectSummary{ProjectFile: projectFile}, err
}

// CreateProject create a new DataTug project using requested store
func CreateProject(ctx context.Context, request dto.CreateProjectRequest) (*datatug.ProjectSummary, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	store, err := storage.GetStore(ctx, request.StoreID)
	if err != nil {
		return nil, fmt.Errorf("failed to get store by ID=%v: %w", request.StoreID, err)
	}
	if store == nil {
		return nil, fmt.Errorf("no store returned by storage.GetStore(id=%v)", request.StoreID)
	}
	return store.CreateProject(ctx, request)
}

// GetProjectFull returns full project metadata
func GetProjectFull(ctx context.Context, ref dto.ProjectRef) (*datatug.Project, error) {
	store, err := storage.GetStore(ctx, ref.StoreID)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoNilness
	project := store.GetProjectStore(ref.ProjectID)
	return project.LoadProject(ctx)
}
