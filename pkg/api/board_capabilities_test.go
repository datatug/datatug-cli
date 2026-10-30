package api

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

func TestGuardedBoardCapabilityRequiresCurrentNativeAuthorityAndExactScope(t *testing.T) {
	for _, kind := range []string{"valid", "readonly", "restricted", "no principal", "no principal ID", "unbound", "unserved", "foreign store", "wrong branch", "canceled", "detached", "Git unavailable", "missing provider", "typed nil provider", "custom provider", "resolver failure"} {
		t.Run(kind, func(t *testing.T) {
			const project = "board-capability"
			dir, git := setupTestGitProject(t, project)
			branch := git("symbolic-ref", "--short", "HEAD")
			paths := map[string]string{project: dir}
			configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
			previous := storage.NewDatatugStore
			t.Cleanup(func() { storage.NewDatatugStore = previous })
			resolutions := 0
			storage.NewDatatugStore = func(string) (storage.Store, error) {
				resolutions++
				if kind == "resolver failure" {
					return nil, errors.New("provider unavailable")
				}
				s, err := filestore.NewStore("files", paths)
				switch kind {
				case "missing provider":
					return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return nil }}, nil
				case "typed nil provider":
					return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return (*guardedBoardTestProject)(nil) }}, nil
				case "custom provider":
					return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return mockProjectStore{} }}, nil
				}
				return s, err
			}
			ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "readonly":
				secureMu.Lock()
				capabilities.AllowWrites = false
				secureMu.Unlock()
			case "restricted":
				secureMu.Lock()
				secureSession.LocalProjectOwner = false
				secureMu.Unlock()
			case "no principal":
				secureMu.Lock()
				secureSession.Principal = nil
				secureMu.Unlock()
			case "no principal ID":
				secureMu.Lock()
				secureSession.Principal = &access.Principal{}
				secureMu.Unlock()
			case "unbound":
				secureMu.Lock()
				securityContextID = ""
				secureMu.Unlock()
			case "unserved":
				ref.ProjectID = "unserved-project"
			case "foreign store":
				ref.StoreID = "custom"
			case "wrong branch":
				branch = "different"
			case "canceled":
				cancel()
			case "detached":
				git("checkout", "--detach", "-q")
			case "Git unavailable":
				original := localGitStateSeam
				t.Cleanup(func() { localGitStateSeam = original })
				localGitStateSeam = func(context.Context, string) (gitState, error) { return gitState{}, errors.New("cannot verify branch") }
			}
			supported := LocalGuardedBoardCreateSupported(ctx, ref, branch)
			require.Equal(t, kind == "valid", supported)
			require.NoDirExists(t, dir+"/boards", "discovery must not create a Board")
			if kind == "readonly" || kind == "restricted" || kind == "no principal" || kind == "no principal ID" || kind == "unbound" || kind == "canceled" || kind == "unserved" || kind == "foreign store" || kind == "wrong branch" {
				require.Zero(t, resolutions, "refuse before provider discovery")
			}
		})
	}
}

func TestGuardedBoardCapabilityCannotSurviveAuthorityOrGitDrift(t *testing.T) {
	for _, boundary := range []string{"provider", "final Git read"} {
		for _, drift := range []string{"writes", "generation", "principal", "restricted", "cancel", "branch", "HEAD", "Git error"} {
			t.Run(boundary+"/"+drift, func(t *testing.T) {
				const project = "board-capability-drift"
				dir, git := setupTestGitProject(t, project)
				branch := git("symbolic-ref", "--short", "HEAD")
				paths := map[string]string{project: dir}
				configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				originalGit, originalStore := localGitStateSeam, storage.NewDatatugStore
				t.Cleanup(func() { localGitStateSeam, storage.NewDatatugStore = originalGit, originalStore })
				changed := false
				change := func() {
					changed = true
					secureMu.Lock()
					switch drift {
					case "writes":
						capabilities.AllowWrites = false
					case "generation":
						securityContextID += "-replacement"
					case "principal":
						secureSession.Principal = &access.Principal{ID: "bob"}
					case "restricted":
						secureSession.LocalProjectOwner = false
					}
					secureMu.Unlock()
					switch drift {
					case "cancel":
						cancel()
					case "branch":
						git("switch", "-q", "-c", "different")
					case "HEAD":
						git("commit", "-q", "--allow-empty", "-m", "changed")
					}
				}
				gitReads := 0
				localGitStateSeam = func(ctx context.Context, root string) (gitState, error) {
					gitReads++
					if boundary == "final Git read" && gitReads == 3 {
						change()
					}
					state, err := originalGit(ctx, root)
					if drift == "Git error" && changed {
						return gitState{}, errors.New("Git no longer readable")
					}
					return state, err
				}
				storage.NewDatatugStore = func(string) (storage.Store, error) {
					s, err := filestore.NewStore("files", paths)
					if boundary == "provider" {
						change()
					}
					return s, err
				}
				require.False(t, LocalGuardedBoardCreateSupported(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, branch))
				require.True(t, changed, "test must reach the intended async boundary")
				require.NoDirExists(t, dir+"/boards")
			})
		}
	}
}

func TestCreateBoardFullValidationPrecedesProviderResolution(t *testing.T) {
	serveProjects(t, "p1")
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		t.Fatal("invalid Board reached provider")
		return nil, os.ErrPermission
	}
	board := nativeBoardDefinition()
	board.Title = ""
	created, err := CreateBoard(context.Background(), dto.ProjectRef{ProjectID: "p1"}, board)
	require.Nil(t, created)
	require.Error(t, err)
}
