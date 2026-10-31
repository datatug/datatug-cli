package api

import (
	"context"
	"errors"

	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

var localProjectRegistry *localprojects.Registry

type LocalProjectRegistryState struct {
	CanCreate  bool                    `json:"canCreate"`
	CanArchive bool                    `json:"canArchive"`
	Projects   []localprojects.Project `json:"projects"`
}

func ConfigureLocalProjectRegistry(registry *localprojects.Registry) {
	secureMu.Lock()
	defer secureMu.Unlock()
	localProjectRegistry = registry
}

// SecureConfiguredProjectPaths returns a snapshot so live registry additions
// never race a filestore map iteration or expand a restricted principal's scope.
func SecureConfiguredProjectPaths() map[string]string {
	secureMu.RLock()
	defer secureMu.RUnlock()
	out := make(map[string]string, len(projectDirs))
	for id, dir := range projectDirs {
		out[id] = dir
	}
	return out
}

func registryAuthority(write bool) (*localprojects.Registry, bool, error) {
	if err := RequireLocalReadPrincipal(); err != nil {
		return nil, false, err
	}
	secureMu.RLock()
	registry, owner, writable := localProjectRegistry, secureSession.LocalProjectOwner, capabilities.AllowWrites
	secureMu.RUnlock()
	if !owner || write && !writable {
		return nil, false, secureread.ErrAccessDenied
	}
	if registry == nil {
		return nil, writable, localprojects.ErrRootRequired
	}
	return registry, writable, nil
}

func GetLocalProjectRegistry() (LocalProjectRegistryState, error) {
	registry, writable, err := registryAuthority(false)
	if err != nil {
		return LocalProjectRegistryState{}, err
	}
	projects, err := registry.List()
	return LocalProjectRegistryState{CanCreate: writable && registry.CanCreate(), CanArchive: writable, Projects: projects}, err
}

func CanCreateLocalProject() bool {
	registry, _, err := registryAuthority(true)
	return err == nil && registry.CanCreate()
}

func CreateLocalProject(ctx context.Context, request localprojects.Create) (localprojects.Project, error) {
	registry, _, err := registryAuthority(true)
	if err != nil {
		return localprojects.Project{}, err
	}
	project, err := registry.Create(ctx, request, registryWriteAuthority(registry))
	if err != nil {
		return localprojects.Project{}, err
	}
	paths := registry.Paths()
	path, exists := paths[project.ID]
	if !exists {
		return localprojects.Project{}, errors.New("created project is unavailable")
	}
	if err = registryWriteAuthority(registry)(); err != nil {
		return localprojects.Project{}, err
	}
	if err = registerExecutionEvidenceProject(project.ID, path); err != nil {
		return localprojects.Project{}, err
	}
	secureMu.Lock()
	// ConfigureSecureSession may have changed while the filesystem command was
	// pending. Never attach a created project to another principal's new session.
	if localProjectRegistry != registry || !secureSession.LocalProjectOwner || !capabilities.AllowWrites {
		secureMu.Unlock()
		return localprojects.Project{}, secureread.ErrAccessDenied
	}
	projectDirs[project.ID] = path
	secureMu.Unlock()
	if previous := filestore.GetProjectPath(project.ID); previous == "" || previous == path {
		filestore.SetProjectPath(project.ID, path)
	}
	return project, nil
}

func ArchiveLocalProject(ctx context.Context, request localprojects.Archive) (localprojects.ArchiveResult, error) {
	registry, _, err := registryAuthority(true)
	if err != nil {
		return localprojects.ArchiveResult{}, err
	}
	return registry.SetArchived(ctx, request, registryWriteAuthority(registry))
}

func registryWriteAuthority(registry *localprojects.Registry) func() error {
	return func() error {
		secureMu.RLock()
		defer secureMu.RUnlock()
		if localProjectRegistry != registry || !secureSession.LocalProjectOwner || !capabilities.AllowWrites {
			return secureread.ErrAccessDenied
		}
		return nil
	}
}
