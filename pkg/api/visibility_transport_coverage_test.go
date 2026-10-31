package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/projectvisibility"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestLocalCreateRechecksAuthorityAtEveryPostPublicationBoundary(t *testing.T) {
	for _, stage := range []string{"missing published path", "revoked before evidence", "revoked before attachment"} {
		t.Run(stage, func(t *testing.T) {
			registry, err := localprojects.Open(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, map[string]string{}, Capabilities{AllowWrites: true})
			ConfigureLocalProjectRegistry(registry)
			previousPaths, previousRegister := localRegistryPaths, registerCreatedProjectEvidence
			t.Cleanup(func() { localRegistryPaths, registerCreatedProjectEvidence = previousPaths, previousRegister })
			registerCalls := 0
			localRegistryPaths = func(r *localprojects.Registry) map[string]string {
				paths := r.Paths()
				switch stage {
				case "missing published path":
					return map[string]string{}
				case "revoked before evidence":
					ConfigureLocalProjectRegistry(nil)
				}
				return paths
			}
			registerCreatedProjectEvidence = func(string, string) error {
				registerCalls++
				if stage == "revoked before attachment" {
					ConfigureLocalProjectRegistry(nil)
				}
				return nil
			}
			_, err = CreateLocalProject(context.Background(), localprojects.Create{Store: "local", ID: "post-publish", Title: "Native project", OperationID: "publish"})
			if err == nil {
				t.Fatal("post-publication failure was acknowledged")
			}
			if stage != "missing published path" && !errors.Is(err, secureread.ErrAccessDenied) {
				t.Fatalf("revocation lost: %v", err)
			}
			if stage != "revoked before attachment" && registerCalls != 0 {
				t.Fatal("evidence registered after failed path/authority guard")
			}
			if _, ok := ProjectDir("post-publish"); ok {
				t.Fatal("unproved project attached to API scope")
			}
			projects, listErr := registry.List()
			if listErr != nil || len(projects) != 1 {
				t.Fatalf("recoverable filesystem receipt lost: %+v %v", projects, listErr)
			}
		})
	}
}

func TestConnectionVisibilityCodecRefusesPartialOrUnreadableProjection(t *testing.T) {
	root := visibilityAPIProject(t, "connections")
	if err := filterLocalConnectionVisibility(root, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := LocalConnectionCatalog{Connections: []LocalCatalogConnection{{}}}
	if err := filterLocalConnectionVisibility(root, &catalog); err == nil {
		t.Fatal("corrupt overlay admitted connection discovery")
	}
	previous := marshalLocalConnectionVisibility
	t.Cleanup(func() { marshalLocalConnectionVisibility = previous })
	failure := errors.New("projection codec failure")
	marshalLocalConnectionVisibility = func(any) ([]byte, error) { return nil, failure }
	if body, err := catalog.MarshalJSON(); !errors.Is(err, failure) || body != nil {
		t.Fatalf("partial codec output: %q %v", body, err)
	}
	marshalLocalConnectionVisibility = func(any) ([]byte, error) { return []byte(`{`), nil }
	if body, err := catalog.MarshalJSON(); err == nil || body != nil {
		t.Fatalf("malformed projection returned: %q %v", body, err)
	}
	marshalLocalConnectionVisibility = func(any) ([]byte, error) {
		return []byte(`{"connections":[null,{"id":"archived"}],"format":"test"}`), nil
	}
	catalog.ArchivedItems = map[string]bool{"archived": true}
	body, err := catalog.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err = json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	rows := response["connections"].([]any)
	if rows[0] != nil || rows[1].(map[string]any)["archived"] != true {
		t.Fatalf("defensive projection changed resolver rows: %s", body)
	}
}

func TestLocalCreateRefusesEvidenceRouteCollisionAfterFilesystemPublication(t *testing.T) {
	const id = "creation-collision"
	registry, err := localprojects.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, map[string]string{}, Capabilities{AllowWrites: true})
	ConfigureLocalProjectRegistry(registry)
	if err = ConfigureExecutionEvidence(map[string]string{id: visibilityAPIProject(t, id)}, nil, executionstore.Options{PrivateDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseExecutionEvidence() })
	_, err = CreateLocalProject(context.Background(), localprojects.Create{Store: "local", ID: id, Title: "New project", OperationID: "create-collision"})
	if err == nil {
		t.Fatal("creation replaced an already configured execution route")
	}
	if _, ok := ProjectDir(id); ok {
		t.Fatal("failed evidence registration attached the new project to API scope")
	}
	projects, listErr := registry.List()
	if listErr != nil || len(projects) != 1 || projects[0].ID != id {
		t.Fatalf("filesystem receipt was lost instead of remaining recoverable: %+v %v", projects, listErr)
	}
}

type uncertainLifecycleStore struct{ *lifecycleTestStore }

func (s uncertainLifecycleStore) ChangeProjectLifecycle(context.Context, datatug.ProjectLifecycleScope, datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
	return datatug.ProjectLifecycleAck{}, projectvisibility.ErrOutcomeUncertain
}

func TestLocalLifecycleRejectsMalformedScopeUnsupportedProviderAndUncertainAck(t *testing.T) {
	selection, provider := lifecycleAPISetup(t, true)
	for _, alter := range []func(*LocalLifecycleSelection){
		func(s *LocalLifecycleSelection) { s.Environment = ".." },
		func(s *LocalLifecycleSelection) { s.ProjectAPI = "remote" },
		func(s *LocalLifecycleSelection) { s.Branch = "branch\nother" },
	} {
		bad := selection
		alter(&bad)
		if _, err := LocalProjectLifecycleCatalog(context.Background(), bad, false); !errors.Is(err, datatug.ErrProjectLifecycleInvalid) {
			t.Fatalf("malformed scope admitted: %v", err)
		}
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return mockProjectStore{} }}, nil
	}
	if _, err := LocalProjectLifecycleCatalog(context.Background(), selection, false); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatalf("unsupported provider gained archive capability: %v", err)
	}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return uncertainLifecycleStore{provider} }}, nil
	}
	if _, err := LocalProjectLifecycleChange(context.Background(), lifecycleWrite(selection)); !errors.Is(err, ErrProjectMutationOutcomeUncertain) || !errors.Is(err, projectvisibility.ErrOutcomeUncertain) {
		t.Fatalf("uncertain mutation was acknowledged as settled: %v", err)
	}
}

func TestMeaningsCatalogRefusesInvalidSemanticSources(t *testing.T) {
	dir, git := setupTestGitProject(t, "meaning-invalid-source")
	configureBranchReadTest(t, "meaning-invalid-source", dir)
	if err := os.MkdirAll(filepath.Join(dir, "queries"), 0700); err != nil {
		t.Fatal(err)
	}
	model := strings.Replace(localMeaningSemanticFixture, `"version":1`, `"version":2`, 1)
	if err := os.WriteFile(filepath.Join(dir, "queries", "bad.query.json"), []byte(`{"id":"bad","semanticModel":`+model+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := LocalProjectMeaningsCatalog(context.Background(), dto.ProjectRef{StoreID: "local", ProjectID: "meaning-invalid-source"}, git("symbolic-ref", "--short", "HEAD"))
	if err == nil || !strings.Contains(err.Error(), "query semantic model") {
		t.Fatalf("invalid semantic model did not reach the owning catalog validator: %v", err)
	}
}

func TestLocalLifecycleNativeFactoryProjectsCurrentKnownDefinitions(t *testing.T) {
	root := visibilityAPIProject(t, "p")
	configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, map[string]string{"p": root}, Capabilities{AllowWrites: true, EvidencePrivateDir: t.TempDir()})
	previousStore, previousFactory := storage.NewDatatugStore, visibilityInventoryFactory
	t.Cleanup(func() { storage.NewDatatugStore = previousStore; ConfigureLocalVisibilityInventory(previousFactory) })
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{"p": root}) }
	target := datatug.ProjectObjectRef{Kind: "check", ID: "current-native"}
	calls := 0
	ConfigureLocalVisibilityInventory(func(context.Context, datatug.ProjectLifecycleScope) (projectvisibility.Runtime, error) {
		return projectvisibility.Runtime{Authorize: func(context.Context, datatug.ProjectLifecycleScope, bool) error { return nil }, Inventory: func(context.Context, datatug.ProjectLifecycleScope) ([]projectvisibility.Target, error) {
			calls++
			return []projectvisibility.Target{{Item: datatug.ProjectUsageItem{Ref: target, Title: "Known native check"}, Revision: "1"}}, nil
		}}, nil
	})
	catalog, err := LocalProjectLifecycleCatalog(context.Background(), LocalLifecycleSelection{ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: "p"}}, false)
	if err != nil || !catalog.ArchiveSupported || catalog.Complete || calls == 0 {
		t.Fatalf("current native definitions were not projected safely: %+v calls=%d err=%v", catalog, calls, err)
	}
	found := false
	for _, item := range catalog.Items {
		if item.Ref == target {
			found = true
		}
	}
	if !found {
		t.Fatal("known owning definition missing from lifecycle discovery")
	}
}

func visibilityAPIProject(t *testing.T, id string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, storage.ProjectSummaryFileName), []byte(`{"id":"`+id+`","title":"Local fixture","access":"private"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLocalRegistryRefusesMissingAuthorityAndConfirmsCreationCapability(t *testing.T) {
	ConfigureSecureSession(secureread.Session{}, map[string]string{}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	if _, err := GetLocalProjectRegistry(); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatalf("missing principal admitted: %v", err)
	}
	configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, map[string]string{}, Capabilities{AllowWrites: true})
	ConfigureLocalProjectRegistry(nil)
	if _, err := GetLocalProjectRegistry(); !errors.Is(err, localprojects.ErrRootRequired) {
		t.Fatalf("missing root silently enabled creation: %v", err)
	}
	if CanCreateLocalProject() {
		t.Fatal("unconfigured creation root advertised as writable")
	}
	if _, err := CreateLocalProject(context.Background(), localprojects.Create{}); !errors.Is(err, localprojects.ErrRootRequired) {
		t.Fatalf("creation bypassed missing root: %v", err)
	}
	registry, err := localprojects.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ConfigureLocalProjectRegistry(registry)
	if !CanCreateLocalProject() {
		t.Fatal("operator-rooted writable owner cannot create")
	}
	if _, err := CreateLocalProject(context.Background(), localprojects.Create{}); !errors.Is(err, localprojects.ErrInvalid) {
		t.Fatalf("invalid command admitted: %v", err)
	}
	if err := CloseExecutionEvidence(); err != nil {
		t.Fatal(err)
	}
	request := localprojects.Create{Store: "local", ID: "api-coverage-project", Title: "Local creation", OperationID: "create"}
	created, err := CreateLocalProject(context.Background(), request)
	if err != nil || created.ID != request.ID {
		t.Fatalf("valid native creation failed: %+v %v", created, err)
	}
	if path, ok := ProjectDir(request.ID); !ok || path == "" {
		t.Fatal("created project did not gain an authorized route")
	}
	authorize := registryWriteAuthority(registry)
	ConfigureLocalProjectRegistry(nil)
	if err := authorize(); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatalf("old registry retained write authority: %v", err)
	}
}

func TestGetProjectsHidesArchivedEntriesAndRedactsUnreadableRegistry(t *testing.T) {
	active, hidden := visibilityAPIProject(t, "active"), visibilityAPIProject(t, "hidden")
	paths := map[string]string{"active": active, "hidden": hidden}
	registry, err := localprojects.Open("", paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.SetArchived(context.Background(), localprojects.Archive{Storage: "local", Project: "hidden", Archived: true, OperationID: "archive"}); err != nil {
		t.Fatal(err)
	}
	configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
	ConfigureLocalProjectRegistry(registry)
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	a, b := datatug.ProjectBrief{}, datatug.ProjectBrief{}
	a.ID, b.ID = "active", "hidden"
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectsFunc: func(context.Context) ([]datatug.ProjectBrief, error) { return []datatug.ProjectBrief{a, b}, nil }}, nil
	}
	projects, err := GetProjects(context.Background(), "files")
	if err != nil || len(projects) != 1 || projects[0].ID != "active" {
		t.Fatalf("archive leaked into active project discovery: %+v %v", projects, err)
	}
	if err = os.WriteFile(filepath.Join(hidden, ".datatug-project-state.json"), []byte(`broken private registry`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = GetProjects(context.Background(), "files"); err == nil || err.Error() != projectsNotListed || strings.Contains(err.Error(), hidden) {
		t.Fatalf("private registry error was disclosed: %v", err)
	}
}

func TestMeaningVisibilityUsesOnlyReadableRegisteredDefinitions(t *testing.T) {
	root := visibilityAPIProject(t, "meaning-project")
	configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, map[string]string{"meaning-project": root}, Capabilities{AllowWrites: true})
	if _, err := LocalProjectMeaningVisibilityItems("unknown"); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatalf("unregistered meanings disclosed: %v", err)
	}
	body := `{"version":1,"entities":[{"documentId":"project","origin":"project","conceptId":"person","name":"Person","fields":[{"documentId":"project","conceptId":"person-id","name":"ID"}]}]}`
	if err := os.WriteFile(filepath.Join(root, localMeaningsFile), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := LocalProjectMeaningVisibilityItems("meaning-project")
	if err != nil || len(items) != 2 || items[0].Ref.ID != "project/person" || items[1].Ref.ID != "project/person-id" {
		t.Fatalf("canonical meanings inventory lost: %+v %v", items, err)
	}
	if err = os.WriteFile(filepath.Join(root, localMeaningsFile), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if rows, err := LocalProjectMeaningVisibilityItems("meaning-project"); err == nil || rows != nil {
		t.Fatalf("corrupt meanings returned partial rows: %+v %v", rows, err)
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if rows, err := LocalProjectMeaningVisibilityItems("meaning-project"); err == nil || rows != nil {
		t.Fatalf("missing registered root returned definitions: %+v %v", rows, err)
	}
}

func TestLocalLifecycleNativeFactoryFailuresNeverGrantFallbackAuthority(t *testing.T) {
	for _, kind := range []string{"factory", "removed registration", "invalid private root", "unresolved private root", "inventory"} {
		t.Run(kind, func(t *testing.T) {
			root := visibilityAPIProject(t, "p")
			private := t.TempDir()
			if kind == "invalid private root" {
				private = filepath.Join(private, "file")
				if err := os.WriteFile(private, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, map[string]string{"p": root}, Capabilities{AllowWrites: true, EvidencePrivateDir: private})
			previousStore, previousFactory := storage.NewDatatugStore, visibilityInventoryFactory
			t.Cleanup(func() { storage.NewDatatugStore = previousStore; ConfigureLocalVisibilityInventory(previousFactory) })
			storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{"p": root}) }
			want := errors.New("owning native inventory unavailable")
			if kind == "unresolved private root" {
				previous := resolveLifecyclePrivateDir
				resolveLifecyclePrivateDir = func(string) (string, error) { return "", want }
				t.Cleanup(func() { resolveLifecyclePrivateDir = previous })
			}
			ConfigureLocalVisibilityInventory(func(context.Context, datatug.ProjectLifecycleScope) (projectvisibility.Runtime, error) {
				if kind == "factory" {
					return projectvisibility.Runtime{}, want
				}
				if kind == "removed registration" {
					configureWriteSession(t, secureread.SessionOptions{As: "coverage-owner", NoPolicies: true}, map[string]string{}, Capabilities{AllowWrites: true})
				}
				return projectvisibility.Runtime{Authorize: func(context.Context, datatug.ProjectLifecycleScope, bool) error { return nil }, Inventory: func(context.Context, datatug.ProjectLifecycleScope) ([]projectvisibility.Target, error) {
					return nil, want
				}}, nil
			})
			selection := LocalLifecycleSelection{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"}}
			catalog, err := LocalProjectLifecycleCatalog(context.Background(), selection, false)
			if err == nil || len(catalog.Items) != 0 || catalog.Complete {
				t.Fatalf("failed owning factory granted fallback catalog: %+v %v", catalog, err)
			}
			if (kind == "factory" || kind == "inventory" || kind == "unresolved private root") && !errors.Is(err, want) {
				t.Fatalf("owning error lost: %v", err)
			}
			if kind == "removed registration" && !errors.Is(err, secureread.ErrAccessDenied) {
				t.Fatalf("revoked project attached to new scope: %v", err)
			}
		})
	}
}
