package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

// Observer only: the project remains the real owning filestore.
type observedBoardStore struct {
	storage.Store
	entered chan struct{}
}

func (s observedBoardStore) GetProjectStore(id string) datatug.ProjectStore {
	close(s.entered)
	return s.Store.GetProjectStore(id)
}
func nativeBoardDefinition() datatug.Board {
	return datatug.Board{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "new-board", Title: "Native board", Folder: "~"}}}
}

func TestNativeBoardCreateRejectsMalformedIdentifiersBeforeOpeningAProvider(t *testing.T) {
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	opened := 0
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		opened++
		return nil, errors.New("unexpected provider resolution")
	}
	for _, tc := range []struct {
		name, projectID, boardID, field string
	}{
		{"project traversal", "../outside", "new-board", "project"},
		{"board traversal", "project", "../outside", boardIDField},
		{"missing board ID", "project", "", boardIDField},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := nativeBoardDefinition()
			definition.ID = tc.boardID
			board, err := CreateLocalProjectBoard(context.Background(), dto.ProjectRef{
				StoreID: LocalStoreID, ProjectID: tc.projectID,
			}, "main", definition)
			require.Nil(t, board)
			unsafe := tc.boardID
			if tc.field == "project" {
				unsafe = tc.projectID
			}
			assertRefusal(t, tc.name, tc.field, unsafe, err)
			require.Zero(t, opened, "malformed identifiers must be refused before any provider resolution")
		})
	}
}

func TestNativeBoardCreateRechecksAuthorityAfterEveryOwnedWait(t *testing.T) {
	for _, boundary := range []string{"project lock", "board mutex"} {
		for _, drift := range []string{"writes", "unrestricted", "generation", "principal", "missing principal", "cancel", "valid"} {
			t.Run(boundary+"/"+drift, func(t *testing.T) {
				const project = "board-authority"
				dir, git := setupTestGitProject(t, project)
				branch := git("symbolic-ref", "--short", "HEAD")
				paths := map[string]string{project: dir}
				configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
				previous := storage.NewDatatugStore
				entered := make(chan struct{})
				storage.NewDatatugStore = func(string) (storage.Store, error) {
					s, err := filestore.NewStore("files", paths)
					if boundary == "board mutex" && err == nil {
						return observedBoardStore{Store: s, entered: entered}, nil
					}
					return s, err
				}
				t.Cleanup(func() { storage.NewDatatugStore = previous })
				unlock := func() {}
				if boundary == "project lock" {
					lock := flock.New(filepath.Join(dir, ".git", "datatug-project-api.lock"))
					require.NoError(t, lock.Lock())
					unlock = func() { require.NoError(t, lock.Unlock()) }
					original := flockTryLockContextSeam
					flockTryLockContextSeam = func(lock *flock.Flock, ctx context.Context, retry time.Duration) (bool, error) {
						close(entered)
						return original(lock, ctx, retry)
					}
					t.Cleanup(func() { flockTryLockContextSeam = original })
				} else {
					createBoardMu.Lock()
					unlock = createBoardMu.Unlock
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type result struct {
					board *datatug.Board
					err   error
				}
				done := make(chan result, 1)
				settled := make(chan struct{})
				go func() {
					defer close(settled)
					board, err := CreateLocalProjectBoard(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, branch, nativeBoardDefinition())
					done <- result{board, err}
				}()
				released := false
				defer func() {
					if !released {
						cancel()
						unlock()
					}
					select {
					case <-settled:
					case <-time.After(5 * time.Second):
						t.Error("board request did not settle")
					}
				}()
				select {
				case <-entered:
				case got := <-done:
					t.Fatalf("finished before owned wait: board=%v err=%v", got.board, got.err)
				case <-time.After(5 * time.Second):
					t.Fatal("did not reach owned wait")
				}
				secureMu.Lock()
				switch drift {
				case "writes":
					capabilities.AllowWrites = false
				case "unrestricted":
					secureSession.LocalProjectOwner = false
				case "generation":
					securityContextID += "changed"
				case "principal":
					secureSession.Principal = &access.Principal{ID: "bob"}
				case "missing principal":
					secureSession.Principal = nil
				}
				secureMu.Unlock()
				if drift == "cancel" {
					cancel()
				}
				unlock()
				released = true
				var got result
				select {
				case got = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("did not settle after releasing wait")
				}
				filename := filepath.Join(dir, "boards", "new-board", "board.json")
				if drift == "valid" {
					require.NoError(t, got.err)
					require.NotNil(t, got.board)
					require.FileExists(t, filename)
				} else {
					require.Nil(t, got.board)
					require.Error(t, got.err)
					if drift == "cancel" {
						require.ErrorIs(t, got.err, context.Canceled)
					}
					require.NoFileExists(t, filename)
					require.NoDirExists(t, filepath.Dir(filename))
				}
			})
		}
	}
}

type boardReadHookStore struct {
	storage.Store
	hook func()
}

func (s boardReadHookStore) GetProjectStore(id string) datatug.ProjectStore {
	return boardReadHookProject{ProjectStore: s.Store.GetProjectStore(id), hook: s.hook}
}

type boardReadHookProject struct {
	datatug.ProjectStore
	hook func()
}

func (p boardReadHookProject) CreateBoardGuarded(ctx context.Context, board *datatug.Board, authorize func() error) error {
	return p.ProjectStore.(datatug.GuardedBoardCreator).CreateBoardGuarded(ctx, board, authorize)
}

func (p boardReadHookProject) LoadBoard(ctx context.Context, id string, opts ...datatug.StoreOption) (*datatug.Board, error) {
	board, err := p.ProjectStore.LoadBoard(ctx, id, opts...)
	p.hook()
	return board, err
}

func TestNativeBoardCreateRefusesDriftDuringOwningExistenceRead(t *testing.T) {
	for _, drift := range []string{"writes", "branch", "HEAD", "read failure", "cancel", "cancel during final Git read", "principal during final Git read", "canceled Git read failure"} {
		t.Run(drift, func(t *testing.T) {
			const project = "board-read-fence"
			dir, git := setupTestGitProject(t, project)
			branch := git("symbolic-ref", "--short", "HEAD")
			paths := map[string]string{project: dir}
			configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			previous := storage.NewDatatugStore
			t.Cleanup(func() { storage.NewDatatugStore = previous })
			originalGit := localGitStateSeam
			t.Cleanup(func() { localGitStateSeam = originalGit })
			hook := func() {
				switch drift {
				case "writes":
					secureMu.Lock()
					capabilities.AllowWrites = false
					secureMu.Unlock()
				case "branch":
					git("switch", "-q", "-c", "different")
				case "HEAD":
					git("commit", "-q", "--allow-empty", "-m", "changed")
				case "read failure":
					localGitStateSeam = func(context.Context, string) (gitState, error) {
						return gitState{}, errors.New("cannot verify Git state")
					}
				case "cancel":
					cancel()
				case "canceled Git read failure":
					localGitStateSeam = func(context.Context, string) (gitState, error) {
						cancel()
						return gitState{}, dto.ErrUnsupportedCapability
					}
				case "cancel during final Git read", "principal during final Git read":
					localGitStateSeam = func(ctx context.Context, root string) (gitState, error) {
						state, err := originalGit(ctx, root)
						if drift == "cancel during final Git read" {
							cancel()
						} else {
							secureMu.Lock()
							secureSession.Principal = &access.Principal{ID: "bob"}
							secureMu.Unlock()
						}
						return state, err
					}
				}
			}
			storage.NewDatatugStore = func(string) (storage.Store, error) {
				s, err := filestore.NewStore("files", paths)
				return boardReadHookStore{Store: s, hook: hook}, err
			}
			board, err := CreateLocalProjectBoard(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, branch, nativeBoardDefinition())
			require.Nil(t, board)
			require.Error(t, err)
			if drift == "cancel during final Git read" || drift == "canceled Git read failure" {
				require.ErrorIs(t, err, context.Canceled)
			}
			if drift == "principal during final Git read" {
				require.ErrorIs(t, err, secureread.ErrAccessDenied)
			}
			require.NoDirExists(t, filepath.Join(dir, "boards", "new-board"))
		})
	}
}

func TestNativeBoardCreateDoesNotAcknowledgePostWriteBranchDrift(t *testing.T) {
	const project = "board-post-write"
	dir, git := setupTestGitProject(t, project)
	paths := map[string]string{project: dir}
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		s, err := filestore.NewStore("files", paths)
		return boardWriteHookStore{Store: s, hook: func() { git("commit", "-q", "--allow-empty", "-m", "after-write") }}, err
	}
	board, err := CreateLocalProjectBoard(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, "", nativeBoardDefinition())
	require.Nil(t, board)
	require.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	_, err = os.ReadFile(filepath.Join(dir, "boards", "new-board", "board.json"))
	require.NoError(t, err, "durable write is not canceled or replayed by a lost acknowledgement")
}

type boardWriteHookStore struct {
	storage.Store
	hook func()
}

func (s boardWriteHookStore) GetProjectStore(id string) datatug.ProjectStore {
	return boardWriteHookProject{ProjectStore: s.Store.GetProjectStore(id), hook: s.hook}
}

type boardWriteHookProject struct {
	datatug.ProjectStore
	hook func()
}

func (p boardWriteHookProject) CreateBoardGuarded(ctx context.Context, board *datatug.Board, authorize func() error) error {
	if err := p.ProjectStore.(datatug.GuardedBoardCreator).CreateBoardGuarded(ctx, board, authorize); err != nil {
		return err
	}
	p.hook()
	return nil
}

func (p boardWriteHookProject) SaveBoard(ctx context.Context, b *datatug.Board) error {
	if err := p.ProjectStore.SaveBoard(ctx, b); err != nil {
		return err
	}
	p.hook()
	return nil
}

func TestNativeBoardCreateRequiresAnIdentifiedUnrestrictedWritableSession(t *testing.T) {
	for _, kind := range []string{"unbound", "readonly", "restricted", "missing principal", "missing principal ID"} {
		t.Run(kind, func(t *testing.T) {
			dir, _ := setupTestGitProject(t, "board-admission")
			paths := map[string]string{"board-admission": dir}
			session := secureread.Session{Principal: &access.Principal{ID: "operator"}, DatabaseProfile: secureread.DatabasePolicyFreeReads, LocalProjectOwner: true}
			caps := Capabilities{AllowWrites: true}
			switch kind {
			case "unbound":
				session = secureread.Session{}
				paths = nil
			case "readonly":
				caps.AllowWrites = false
			case "restricted":
				session.LocalProjectOwner = false
			case "missing principal":
				session.Principal = nil
			case "missing principal ID":
				session.Principal = &access.Principal{}
			}
			ConfigureSecureSession(session, paths, caps)
			t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
			board, err := CreateLocalProjectBoard(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "board-admission"}, "", nativeBoardDefinition())
			require.Nil(t, board)
			require.ErrorIs(t, err, secureread.ErrAccessDenied)
			require.NoDirExists(t, filepath.Join(dir, "boards"))
		})
	}
}

func TestNativeBoardCreatePreservesExistingCorruptAndRacingTargets(t *testing.T) {
	for _, target := range []string{"existing", "corrupt", "racing non-directory"} {
		t.Run(target, func(t *testing.T) {
			const project = "board-target"
			dir, _ := setupTestGitProject(t, project)
			paths := map[string]string{project: dir}
			configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
			previous := storage.NewDatatugStore
			t.Cleanup(func() { storage.NewDatatugStore = previous })
			store, err := filestore.NewStore("files", paths)
			require.NoError(t, err)
			storage.NewDatatugStore = func(string) (storage.Store, error) { return store, nil }
			ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}
			board := nativeBoardDefinition()
			file := filepath.Join(dir, "boards", board.ID, "board.json")
			if target != "racing non-directory" {
				_, err = CreateLocalProjectBoard(context.Background(), ref, "", board)
				require.NoError(t, err)
				if target == "corrupt" {
					require.NoError(t, os.WriteFile(file, []byte(`{"id":"new-board",broken`), 0o600))
				}
			} else {
				storage.NewDatatugStore = func(string) (storage.Store, error) {
					return boardReadHookStore{Store: store, hook: func() {
						require.NoError(t, os.MkdirAll(filepath.Join(dir, "boards"), 0o700))
						require.NoError(t, os.WriteFile(filepath.Dir(file), []byte("competing target"), 0o600))
					}}, nil
				}
			}
			var before []byte
			if target != "racing non-directory" {
				before, err = os.ReadFile(file)
				require.NoError(t, err)
			}
			board.Title = "Must not replace"
			created, err := CreateLocalProjectBoard(context.Background(), ref, "", board)
			require.Nil(t, created)
			require.Error(t, err)
			if target == "racing non-directory" {
				after, err := os.ReadFile(filepath.Dir(file))
				require.NoError(t, err)
				require.Equal(t, "competing target", string(after))
			} else {
				after, err := os.ReadFile(file)
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
		})
	}
}
