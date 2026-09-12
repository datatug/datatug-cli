package incidentstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/stretchr/testify/require"
)

func TestOpenLocationRoutesAllRepositoryKinds(t *testing.T) {
	project := incidents.ProjectRef{StoreID: "project", ProjectID: "payments", Environment: "prod"}
	roots := RepositoryRoots{
		Application: t.TempDir(),
		Dedicated:   map[string]string{"dedicated": t.TempDir()},
		Projects:    map[incidents.ProjectRef]string{project: t.TempDir()},
	}
	cases := []struct {
		name     string
		location incidents.StoreLocation
		root     string
	}{
		{name: "project", location: incidents.StoreLocation{StoreID: "project", Kind: incidents.StoreLocationProjectRepository, Project: &project}, root: roots.Projects[project]},
		{name: "dedicated", location: incidents.StoreLocation{StoreID: "dedicated", Kind: incidents.StoreLocationDedicatedRepository}, root: roots.Dedicated["dedicated"]},
		{name: "application", location: incidents.StoreLocation{StoreID: "application", Kind: incidents.StoreLocationApplicationRepository}, root: roots.Application},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store, err := OpenLocation(testCase.location, roots)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			mutation := createdMutation(t, "create-1", "INC-1")
			mutation.Incident.StoreID = testCase.location.StoreID
			_, err = store.Append(context.Background(), mutation)
			require.NoError(t, err)
			_, err = os.Stat(filepath.Join(testCase.root, "incidents", "INC-1", "events.jsonl"))
			require.NoError(t, err)
		})
	}
}

func TestOpenLocationRefusesUnconfiguredRepository(t *testing.T) {
	project := incidents.ProjectRef{StoreID: "project", ProjectID: "payments"}
	locations := []incidents.StoreLocation{
		{StoreID: "project", Kind: incidents.StoreLocationProjectRepository, Project: &project},
		{StoreID: "dedicated", Kind: incidents.StoreLocationDedicatedRepository},
		{StoreID: "application", Kind: incidents.StoreLocationApplicationRepository},
	}
	for _, location := range locations {
		_, err := OpenLocation(location, RepositoryRoots{})
		require.ErrorContains(t, err, "not configured")
	}
}
