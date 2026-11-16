package executionstore

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/incidentstore"
)

func TestRegisterProjectRejectsAnInvalidEvidenceIdentityWithoutChangingRoutes(t *testing.T) {
	manager, err := NewManager(incidentstore.RepositoryRoots{}, nil, Options{PrivateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	for _, id := range []string{"", "../foreign", "bad\x00project"} {
		if err = manager.RegisterProject(id, t.TempDir()); err == nil {
			t.Fatalf("invalid registered project %q accepted", id)
		}
		if _, ok := manager.locations[id]; ok {
			t.Fatalf("invalid project %q gained an evidence route", id)
		}
	}
}
