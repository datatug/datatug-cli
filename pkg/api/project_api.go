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
	// storage.NewDatatugStore, not storage.GetStore/GetProjectStore: the
	// latter resolve through a package-private `stores` map or a store
	// stashed on ctx via storage.ContextWithDatatugStore — datatug serve
	// (ServeHTTP) wires neither, only storage.NewDatatugStore (the
	// factory storage/vars.go's own TODO calls out as GetStore's
	// replacement). GetProjects/ExecuteSelect/RunQuery already go through
	// NewDatatugStore; project_summary/project_full/create_project did not,
	// so every one of those requests failed with "no store configured for
	// id=..." once the request even reached this far (previously masked by
	// the nil apicore.GetAuthTokenFromHttpRequest panic — see auth_hook.go).
	store, err := storage.NewDatatugStore(ref.StoreID)
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
	// See the NewDatatugStore comment in GetProjectSummary above.
	store, err := storage.NewDatatugStore(request.StoreID)
	if err != nil {
		return nil, fmt.Errorf("failed to get store by ID=%v: %w", request.StoreID, err)
	}
	if store == nil {
		return nil, fmt.Errorf("no store returned by storage.NewDatatugStore(id=%v)", request.StoreID)
	}
	return store.CreateProject(ctx, request)
}

// GetProjectFull returns full project metadata
func GetProjectFull(ctx context.Context, ref dto.ProjectRef) (*datatug.Project, error) {
	// See the NewDatatugStore comment in GetProjectSummary above.
	store, err := storage.NewDatatugStore(ref.StoreID)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoNilness
	project := store.GetProjectStore(ref.ProjectID)
	return project.LoadProject(ctx)
}
