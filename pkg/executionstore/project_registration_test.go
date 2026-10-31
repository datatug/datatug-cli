package executionstore

import (
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-core/pkg/incidents"
)

func TestRegisterCreatedProjectPreservesExistingRoutesAndPrivateBoundaries(t *testing.T) {
	manager, err := NewManager(incidentstore.RepositoryRoots{}, nil, Options{PrivateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	dir := t.TempDir()
	if err = manager.RegisterProject("new-project", dir); err != nil {
		t.Fatal(err)
	}
	if err = manager.RegisterProject("new-project", dir); err != nil {
		t.Fatal(err)
	}
	if err = manager.RegisterProject("new-project", t.TempDir()); err == nil {
		t.Fatal("route silently changed")
	}
	ref := incidents.ProjectRef{StoreID: "new-project", ProjectID: "new-project"}
	if manager.roots.Projects[ref] != dir {
		t.Fatal("new project path unavailable")
	}
	if _, err = manager.ProjectStore("new-project"); err != nil {
		t.Fatal(err)
	}
	if err = manager.RegisterProject("private", manager.options.PrivateDir); err == nil {
		t.Fatal("private snapshot dir admitted as a project")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				_, _ = manager.location("new-project")
			}
		}()
	}
	if err = manager.RegisterProject("another", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
