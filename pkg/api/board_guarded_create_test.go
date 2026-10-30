package api

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

// Opt-in fake, separate from mockProjectStore so unsupported providers remain
// genuinely unsupported. It never calls the legacy SaveBoard method.
type guardedBoardTestProject struct {
	datatug.ProjectStore
	create func(context.Context, *datatug.Board) error
}

type guardedStageProject struct {
	datatug.ProjectStore
	staged func()
}

func (p guardedStageProject) CreateBoardGuarded(ctx context.Context, board *datatug.Board, authorize func() error) error {
	checks := 0
	return p.ProjectStore.(datatug.GuardedBoardCreator).CreateBoardGuarded(ctx, board, func() error {
		checks++
		if checks == 2 {
			p.staged()
		}
		if authorize != nil {
			return authorize()
		}
		return nil
	})
}

// This worker writes through the real legacy filestore in a different process:
// it cannot share the API mutex or native project lock with the guarded caller.
func TestBoardCreateCompetingProcess(t *testing.T) {
	dir := os.Getenv("DATATUG_TEST_BOARD_WINNER_DIR")
	if dir == "" {
		return
	}
	store, err := filestore.NewStore("files", map[string]string{"board-winner": dir})
	require.NoError(t, err)
	board := nativeBoardDefinition()
	board.Title = "Existing concurrently saved Board"
	require.NoError(t, store.GetProjectStore("board-winner").SaveBoard(context.Background(), &board))
}

func TestBoardCreatePreservesAnotherProcessWinnerAfterStaging(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "trusted legacy"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			const project = "board-winner"
			dir, git := setupTestGitProject(t, project)
			branch := git("symbolic-ref", "--short", "HEAD")
			paths := map[string]string{project: dir}
			configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
			previous := storage.NewDatatugStore
			t.Cleanup(func() { storage.NewDatatugStore = previous })
			filename := filepath.Join(dir, "boards", "new-board", "board.json")
			var winner []byte
			staged := false
			storage.NewDatatugStore = func(string) (storage.Store, error) {
				s, err := filestore.NewStore("files", paths)
				return mockStore{getProjectStoreFunc: func(id string) datatug.ProjectStore {
					return guardedStageProject{ProjectStore: s.GetProjectStore(id), staged: func() {
						staged = true
						files, err := filepath.Glob(filepath.Join(dir, "boards", "new-board", ".datatug-create-*.tmp"))
						require.NoError(t, err)
						require.Len(t, files, 1, "the competing process must win after actual provider staging")
						cmd := exec.Command(os.Args[0], "-test.run=^TestBoardCreateCompetingProcess$")
						cmd.Env = append(os.Environ(), "DATATUG_TEST_BOARD_WINNER_DIR="+dir)
						out, err := cmd.CombinedOutput()
						require.NoError(t, err, "%s", out)
						winner, err = os.ReadFile(filename)
						require.NoError(t, err)
					}}
				}}, err
			}
			ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}
			var board *datatug.Board
			var err error
			if native {
				board, err = CreateLocalProjectBoard(context.Background(), ref, branch, nativeBoardDefinition())
			} else {
				board, err = CreateBoard(context.Background(), ref, nativeBoardDefinition())
			}
			require.True(t, staged)
			require.Nil(t, board)
			require.Error(t, err)
			actual, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, winner, actual, "create must preserve every byte written by the winning process")
			reloadedStore, err := filestore.NewStore("files", paths)
			require.NoError(t, err)
			reloaded, err := reloadedStore.GetProjectStore(project).LoadBoard(context.Background(), "new-board")
			require.NoError(t, err)
			require.Equal(t, "Existing concurrently saved Board", reloaded.Title)
			files, err := filepath.Glob(filepath.Join(dir, "boards", "new-board", ".datatug-create-*.tmp"))
			require.NoError(t, err)
			require.Empty(t, files)
		})
	}
}

func TestNativeBoardCreateRechecksCurrentAdmissionAfterProviderStaging(t *testing.T) {
	for _, drift := range []string{"writes", "generation", "principal", "restricted", "cancel", "branch", "HEAD", "Git error"} {
		t.Run(drift, func(t *testing.T) {
			const project = "board-stage-authority"
			dir, git := setupTestGitProject(t, project)
			branch := git("symbolic-ref", "--short", "HEAD")
			paths := map[string]string{project: dir}
			configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			previous, originalGit := storage.NewDatatugStore, localGitStateSeam
			t.Cleanup(func() { storage.NewDatatugStore, localGitStateSeam = previous, originalGit })
			staged := false
			storage.NewDatatugStore = func(string) (storage.Store, error) {
				s, err := filestore.NewStore("files", paths)
				return mockStore{getProjectStoreFunc: func(id string) datatug.ProjectStore {
					return guardedStageProject{ProjectStore: s.GetProjectStore(id), staged: func() {
						staged = true
						files, err := filepath.Glob(filepath.Join(dir, "boards", "new-board", ".datatug-create-*.tmp"))
						require.NoError(t, err)
						require.Len(t, files, 1)
						secureMu.Lock()
						switch drift {
						case "writes":
							capabilities.AllowWrites = false
						case "generation":
							securityContextID += "-replacement"
						case "principal":
							secureSession.Principal = &access.Principal{ID: "bob"}
						case "restricted":
							secureSession.Unrestricted = false
						}
						secureMu.Unlock()
						switch drift {
						case "cancel":
							cancel()
						case "branch":
							git("switch", "-q", "-c", "different")
						case "HEAD":
							git("commit", "-q", "--allow-empty", "-m", "changed")
						case "Git error":
							localGitStateSeam = func(context.Context, string) (gitState, error) { return gitState{}, errors.New("cannot verify Git") }
						}
					}}
				}}, err
			}
			board, err := CreateLocalProjectBoard(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, branch, nativeBoardDefinition())
			require.True(t, staged)
			require.Nil(t, board)
			require.Error(t, err)
			require.NoDirExists(t, filepath.Join(dir, "boards", "new-board"), "no target or private stage may remain after refused admission")
		})
	}
}

func TestBoardCreateUsesOnlyGuardedProviderAndPreservesErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{{"success", nil}, {"collision", os.ErrExist}, {"failure", errors.New("provider cannot publish")}} {
		t.Run(tc.name, func(t *testing.T) {
			failure := tc.failure
			serveProjects(t, "p1")
			previous := storage.NewDatatugStore
			t.Cleanup(func() { storage.NewDatatugStore = previous })
			creates, saves := 0, 0
			storage.NewDatatugStore = func(string) (storage.Store, error) {
				return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
					return guardedBoardTestProject{ProjectStore: mockProjectStore{
						loadBoardFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Board, error) {
							return nil, os.ErrNotExist
						},
						saveBoardFunc: func(context.Context, *datatug.Board) error { saves++; return nil },
					}, create: func(context.Context, *datatug.Board) error { creates++; return failure }}
				}}, nil
			}
			board, err := CreateBoard(context.Background(), dto.ProjectRef{ProjectID: "p1"}, nativeBoardDefinition())
			require.Equal(t, 1, creates)
			require.Zero(t, saves)
			if failure == nil {
				require.NoError(t, err)
				require.NotNil(t, board)
			} else {
				require.Error(t, err)
				require.Nil(t, board)
			}
		})
	}
}

func (p guardedBoardTestProject) CreateBoardGuarded(ctx context.Context, board *datatug.Board, authorize func() error) error {
	if authorize != nil {
		if err := authorize(); err != nil {
			return err
		}
	}
	if p.create != nil {
		return p.create(ctx, board)
	}
	return nil
}

func TestCreateBoardNeverFallsBackToLegacySave(t *testing.T) {
	serveProjects(t, "p1")
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	saves, loads := 0, 0
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			return mockProjectStore{
				loadBoardFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Board, error) {
					loads++
					return nil, os.ErrNotExist
				},
				saveBoardFunc: func(context.Context, *datatug.Board) error { saves++; return nil },
			}
		}}, nil
	}
	board, err := CreateBoard(context.Background(), dto.ProjectRef{ProjectID: "p1"}, nativeBoardDefinition())
	require.Nil(t, board)
	require.ErrorIs(t, err, dto.ErrUnsupportedCapability)
	require.Zero(t, saves, "an unsupported provider must never receive SaveBoard")
	require.Zero(t, loads, "an unsupported provider must be refused before legacy storage I/O")
}
