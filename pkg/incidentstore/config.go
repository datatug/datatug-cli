package incidentstore

import (
	"fmt"
	"strings"

	"github.com/datatug/datatug-core/pkg/incidents"
)

// ConfiguredStore is one trusted repository route shared by incident and
// execution evidence. Repository is a checked-out local Git repository root.
type ConfiguredStore struct {
	StoreID    string                      `yaml:"storeId" json:"storeId"`
	Kind       incidents.StoreLocationKind `yaml:"kind" json:"kind"`
	Project    *incidents.ProjectRef       `yaml:"project,omitempty" json:"project,omitempty"`
	Repository string                      `yaml:"repository" json:"repository"`
}

func (c ConfiguredStore) Location() incidents.StoreLocation {
	return incidents.StoreLocation{StoreID: c.StoreID, Kind: c.Kind, Project: c.Project}
}

func (c ConfiguredStore) Validate() error {
	if err := c.Location().Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Repository) == "" {
		return fmt.Errorf("incident store %q repository is required", c.StoreID)
	}
	return nil
}

// ResolveRouting combines project defaults with explicit dedicated,
// application, or project-repository overrides.
func ResolveRouting(projectPaths map[string]string, configured []ConfiguredStore) (RepositoryRoots, map[string]incidents.StoreLocation, error) {
	roots := RepositoryRoots{Projects: make(map[incidents.ProjectRef]string), Dedicated: make(map[string]string)}
	locations := make(map[string]incidents.StoreLocation, len(projectPaths)+len(configured))
	for projectID, repository := range projectPaths {
		project := incidents.ProjectRef{StoreID: projectID, ProjectID: projectID}
		roots.Projects[project] = repository
		locations[projectID] = incidents.StoreLocation{StoreID: projectID, Kind: incidents.StoreLocationProjectRepository, Project: &project}
	}
	for _, item := range configured {
		if err := item.Validate(); err != nil {
			return RepositoryRoots{}, nil, err
		}
		location := item.Location()
		if existing, duplicate := locations[item.StoreID]; duplicate && (item.Kind != incidents.StoreLocationProjectRepository || existing.Kind != incidents.StoreLocationProjectRepository) {
			return RepositoryRoots{}, nil, fmt.Errorf("incident store %q is configured more than once", item.StoreID)
		}
		switch item.Kind {
		case incidents.StoreLocationProjectRepository:
			roots.Projects[*item.Project] = item.Repository
		case incidents.StoreLocationDedicatedRepository:
			roots.Dedicated[item.StoreID] = item.Repository
		case incidents.StoreLocationApplicationRepository:
			if roots.Application != "" && roots.Application != item.Repository {
				return RepositoryRoots{}, nil, fmt.Errorf("more than one application incident repository is configured")
			}
			roots.Application = item.Repository
		}
		locations[item.StoreID] = location
	}
	return roots, locations, nil
}
