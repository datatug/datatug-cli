package api

import (
	"context"
	"errors"
	"fmt"
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
)

// This wrapper observes entry to the real provider without replacing its lock,
// admission, filesystem writes, authorization callback or returned result.
type observedFolderStore struct {
	storage.Store
	entered chan struct{}
}

func (s observedFolderStore) GetProjectStore(id string) datatug.ProjectStore {
	return observedFolderProject{ProjectStore: s.Store.GetProjectStore(id), entered: s.entered}
}

type observedFolderProject struct {
	datatug.ProjectStore
	entered chan struct{}
}

func (p observedFolderProject) CreateQueryFolder(ctx context.Context, parent, name string) error {
	return p.ProjectStore.(datatug.QueryFolderDirectories).CreateQueryFolder(ctx, parent, name)
}

func (p observedFolderProject) DeclaredQueryFolders(ctx context.Context) ([]string, error) {
	return p.ProjectStore.(datatug.QueryFolderDirectories).DeclaredQueryFolders(ctx)
}

func (p observedFolderProject) CreateQueryFolderGuarded(ctx context.Context, parent, name string, authorize func() error) error {
	close(p.entered)
	return p.ProjectStore.(datatug.GuardedQueryFolderDirectories).CreateQueryFolderGuarded(ctx, parent, name, authorize)
}

type unguardedFolderProject struct {
	datatug.ProjectStore
	folders datatug.QueryFolderDirectories
	called  *bool
}

func (p unguardedFolderProject) CreateQueryFolder(ctx context.Context, parent, name string) error {
	*p.called = true
	return p.folders.CreateQueryFolder(ctx, parent, name)
}

func (p unguardedFolderProject) DeclaredQueryFolders(ctx context.Context) ([]string, error) {
	return p.folders.DeclaredQueryFolders(ctx)
}

func TestQueryFolderRequiresGuardedProviderRatherThanLegacyAuthoring(t *testing.T) {
	const project = "folder-legacy-capability"
	dir, _ := setupTestGitProject(t, project)
	paths := map[string]string{project: dir}
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
	p := filestore.NewProjectStore(project, dir)
	called := false
	legacy := unguardedFolderProject{ProjectStore: p, folders: p.(datatug.QueryFolderDirectories), called: &called}
	old := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = old })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return legacy }}, nil
	}
	out, err := CreateProjectQueryFolder(context.Background(), QueryFolderCreate{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, Path: "~", Name: "must-not-exist"})
	if out != nil || !errors.Is(err, datatug.ErrProjectUsageIncomplete) || called {
		t.Fatalf("unguarded provider used or acknowledged: out=%v err=%v called=%v", out, err, called)
	}
	if _, err := os.Lstat(filepath.Join(dir, "queries", "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("unguarded provider changed target", err)
	}
}

func TestQueryFolderAuthoritySurvivesNeitherOuterNorProviderWait(t *testing.T) {
	for boundaryIndex, boundary := range []string{"project lock", "provider lock"} {
		drifts := []string{"writes revoked", "unrestricted revoked", "generation", "principal", "missing principal", "cancel", "valid"}
		if boundary == "provider lock" {
			drifts = append(drifts, "branch", "HEAD", "branch read failure", "cancel during branch read")
		}
		for driftIndex, drift := range drifts {
			t.Run(boundary+"/"+drift, func(t *testing.T) {
				project := fmt.Sprintf("folder-authority-%d-%d", boundaryIndex, driftIndex)
				dir, git := setupTestGitProject(t, project)
				branch := git("symbolic-ref", "--short", "HEAD")
				paths := map[string]string{project: dir}
				configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
				filestore.SetProjectPath(project, dir)
				previousStore := storage.NewDatatugStore
				t.Cleanup(func() { storage.NewDatatugStore = previousStore })
				entered := make(chan struct{})
				storage.NewDatatugStore = func(string) (storage.Store, error) {
					s, err := filestore.NewStore("files", paths)
					if err != nil || boundary == "project lock" {
						return s, err
					}
					return observedFolderStore{Store: s, entered: entered}, nil
				}
				lockPath := filepath.Join(dir, ".git", "datatug-project-api.lock")
				if boundary == "provider lock" {
					private := filepath.Join(dir, "queries", ".dt-query-txn")
					if err := os.MkdirAll(private, 0o700); err != nil {
						t.Fatal(err)
					}
					lockPath = filepath.Join(private, "lock")
				} else {
					original := flockTryLockContextSeam
					t.Cleanup(func() { flockTryLockContextSeam = original })
					flockTryLockContextSeam = func(f *flock.Flock, ctx context.Context, retry time.Duration) (bool, error) {
						close(entered)
						return original(f, ctx, retry)
					}
				}
				lock := flock.New(lockPath)
				if err := lock.Lock(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Unlock() }()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if drift == "cancel during branch read" {
					original := localGitStateSeam
					t.Cleanup(func() { localGitStateSeam = original })
					localGitStateSeam = func(ctx context.Context, root string) (gitState, error) {
						state, err := original(ctx, root)
						select {
						case <-entered:
							cancel()
						default:
						}
						return state, err
					}
				}
				type outcome struct {
					folder *datatug.QueriesFolder
					err    error
				}
				finished := make(chan outcome, 1)
				settled := make(chan struct{})
				go func() {
					defer close(settled)
					folder, err := CreateProjectQueryFolder(ctx, QueryFolderCreate{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, Branch: branch, Path: "~", Name: "reports"})
					finished <- outcome{folder, err}
				}()
				defer func() {
					cancel()
					_ = lock.Unlock()
					select {
					case <-settled:
					case <-time.After(5 * time.Second):
						t.Error("request did not settle before fixture cleanup")
					}
				}()
				select {
				case <-entered:
				case got := <-finished:
					t.Fatalf("request finished before held lock boundary: %+v", got)
				case <-time.After(5 * time.Second):
					t.Fatal("request did not reach held lock boundary")
				}
				secureMu.Lock()
				switch drift {
				case "writes revoked":
					capabilities.AllowWrites = false
				case "unrestricted revoked":
					secureSession.Unrestricted = false
				case "generation":
					securityContextID += "replaced"
				case "principal":
					secureSession.Principal = &access.Principal{ID: "bob"}
				case "missing principal":
					secureSession.Principal = nil
				}
				secureMu.Unlock()
				switch drift {
				case "branch":
					git("checkout", "-b", "replaced")
				case "HEAD":
					git("commit", "--allow-empty", "-m", "moved during provider wait")
				case "branch read failure":
					if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("invalid head"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if drift == "cancel" {
					cancel()
				}
				if err := lock.Unlock(); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-finished:
					if drift == "valid" {
						if got.err != nil || got.folder == nil || got.folder.ID != "reports" {
							t.Fatalf("current authority refused: %+v", got)
						}
						for _, name := range []string{"README.md", ".datatug-query-folder.json"} {
							if _, err := os.Stat(filepath.Join(dir, "queries", "reports", name)); err != nil {
								t.Fatal("accepted declaration missing", err)
							}
						}
						return
					}
					want := secureread.ErrAccessDenied
					if drift == "cancel" || drift == "cancel during branch read" {
						want = context.Canceled
					}
					if drift == "branch" || drift == "HEAD" {
						want = dto.ErrBranchHeadConflict
					}
					if got.folder != nil || got.err == nil || drift != "branch read failure" && !errors.Is(got.err, want) {
						t.Fatalf("stale request acknowledged: folder=%v err=%v (%T) want=%v", got.folder, got.err, got.err, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("request did not finish after lock release")
				}
				if _, err := os.Lstat(filepath.Join(dir, "queries", "reports")); !os.IsNotExist(err) {
					t.Fatal("denied request wrote folder/README/marker", err)
				}
			})
		}
	}
}

func TestQueryFolderCancellationBeforeWritePreservesItsCause(t *testing.T) {
	for index, boundary := range []string{"initial Git read", "after real project lock"} {
		t.Run(boundary, func(t *testing.T) {
			project := fmt.Sprintf("folder-cancel-cause-%d", index)
			dir, _ := setupTestGitProject(t, project)
			paths := map[string]string{project: dir}
			configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
			oldStore := storage.NewDatatugStore
			t.Cleanup(func() { storage.NewDatatugStore = oldStore })
			storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if boundary == "initial Git read" {
				cancel()
			} else {
				original := flockTryLockContextSeam
				t.Cleanup(func() { flockTryLockContextSeam = original })
				flockTryLockContextSeam = func(f *flock.Flock, ctx context.Context, retry time.Duration) (bool, error) {
					locked, err := original(f, ctx, retry)
					if locked && err == nil {
						cancel() // The real acquisition can win the cancellation/release race.
					}
					return locked, err
				}
			}
			out, err := CreateProjectQueryFolder(ctx, QueryFolderCreate{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}, Path: "~", Name: "must-not-exist"})
			if out != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost before write: folder=%v err=%v (%T)", out, err, err)
			}
			if _, err := os.Lstat(filepath.Join(dir, "queries", "must-not-exist")); !os.IsNotExist(err) {
				t.Fatal("canceled request wrote folder/README/marker", err)
			}
		})
	}
}
