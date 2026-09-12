package incidentstore

import (
	"fmt"
	"strings"

	"github.com/datatug/datatug-core/pkg/incidents"
)

// RepositoryRoots resolves every supported StoreLocation to its checked-out
// Git repository root. The caller owns cloning, authentication, and sync.
type RepositoryRoots struct {
	Application string
	Dedicated   map[string]string
	Projects    map[incidents.ProjectRef]string
}

// OpenLocation resolves a store location and opens its repository-backed
// incident store. It keeps location selection separate from persistence so
// project, dedicated, and application repositories cannot silently share a
// fallback directory.
func OpenLocation(location incidents.StoreLocation, roots RepositoryRoots) (*RepositoryStore, error) {
	if err := location.Validate(); err != nil {
		return nil, err
	}
	var root string
	switch location.Kind {
	case incidents.StoreLocationProjectRepository:
		root = roots.Projects[*location.Project]
	case incidents.StoreLocationDedicatedRepository:
		root = roots.Dedicated[location.StoreID]
	case incidents.StoreLocationApplicationRepository:
		root = roots.Application
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("repository root is not configured for incident store %q (%s)", location.StoreID, location.Kind)
	}
	return NewRepositoryStore(location, root)
}
