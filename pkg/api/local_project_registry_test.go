package api

import (
	"context"
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestLocalRegistryAuthorityAndDynamicRegistration(t *testing.T) {
	registry, err := localprojects.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "owner", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	ConfigureSecureSession(session, map[string]string{}, Capabilities{AllowWrites: true})
	ConfigureLocalProjectRegistry(registry)
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	request := localprojects.Create{Store: "local", ID: "registry-owned", Title: "New local project", OperationID: "create"}
	project, err := CreateLocalProject(context.Background(), request)
	if err != nil || project.ID != request.ID {
		t.Fatalf("create: %+v %v", project, err)
	}
	if _, ok := ProjectDir(request.ID); !ok {
		t.Fatal("created project cannot be opened")
	}
	paths := SecureConfiguredProjectPaths()
	delete(paths, request.ID)
	if _, ok := ProjectDir(request.ID); !ok {
		t.Fatal("caller mutated guarded registry")
	}
	state, err := GetLocalProjectRegistry()
	if err != nil || !state.CanCreate || !state.CanArchive || len(state.Projects) != 1 {
		t.Fatalf("registry: %+v %v", state, err)
	}
	if _, err = ArchiveLocalProject(context.Background(), localprojects.Archive{Storage: "local", Project: request.ID, Archived: true, OperationID: "archive"}); err != nil {
		t.Fatal(err)
	}
	// Read-only owner may see both lists, but cannot create/archive.
	ConfigureSecureSession(session, registry.Paths(), Capabilities{})
	ConfigureLocalProjectRegistry(registry)
	state, err = GetLocalProjectRegistry()
	if err != nil || state.CanCreate || state.CanArchive || !state.Projects[0].Archived {
		t.Fatalf("read-only registry: %+v %v", state, err)
	}
	if _, err = CreateLocalProject(context.Background(), request); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatal(err)
	}
	// A limited principal never gets all registered project identities or filesystem authority.
	ConfigureSecureSession(secureread.Session{Principal: session.Principal}, registry.Paths(), Capabilities{AllowWrites: true})
	ConfigureLocalProjectRegistry(registry)
	if _, err = GetLocalProjectRegistry(); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatal(err)
	}
	if _, err = ArchiveLocalProject(context.Background(), localprojects.Archive{Storage: "local", Project: request.ID, Archived: false, OperationID: "restore"}); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatal(err)
	}
}
