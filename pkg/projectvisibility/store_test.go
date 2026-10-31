package projectvisibility

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

type nativeFixture struct {
	archived   bool
	revision   string
	restores   int
	operations map[string]bool
	denied     bool
}

func nativeStoreFixture(t *testing.T) (func() *Store, *nativeFixture, datatug.ProjectLifecycleScope) {
	t.Helper()
	root, private := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "queries"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "queries", "kept.query.json"), []byte(`{"title":"Kept query"}`), 0600); err != nil {
		t.Fatal(err)
	}
	state := &nativeFixture{archived: true, revision: "1", operations: map[string]bool{}}
	scope := datatug.ProjectLifecycleScope{ActorID: "owner", Storage: "local", Project: "project"}
	native := Runtime{
		Inventory: func(context.Context, datatug.ProjectLifecycleScope) ([]Target, error) {
			return []Target{{Item: datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "session", ID: "session"}, Title: "Private session", Archived: state.archived}, Revision: state.revision}}, nil
		},
		Restore: func(_ context.Context, _ datatug.ProjectLifecycleScope, target Target, operation string) error {
			if state.operations[operation] {
				return nil
			}
			if !state.archived || target.Revision != state.revision {
				return datatug.ErrProjectLifecycleConflict
			}
			state.archived = false
			state.revision = "2"
			state.restores++
			state.operations[operation] = true
			return nil
		},
		Authorize: func(_ context.Context, _ datatug.ProjectLifecycleScope, write bool) error {
			if state.denied && write {
				return os.ErrPermission
			}
			return nil
		},
	}
	create := func() *Store {
		provider := filestore.NewProjectStoreWithVisibilityItems(scope.Project, root, func(ctx context.Context, scope datatug.ProjectLifecycleScope) ([]datatug.ProjectUsageItem, error) {
			items, err := native.Inventory(ctx, scope)
			out := []datatug.ProjectUsageItem{}
			for _, item := range items {
				item.Item.Archived = false
				out = append(out, item.Item)
			}
			return out, err
		}).(datatug.ProjectLifecycleStore)
		store, err := NewStore(provider, scope, private, native)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	t.Cleanup(func() { fault = nil })
	return create, state, scope
}
func restoreWrite(t *testing.T, store *Store, scope datatug.ProjectLifecycleScope) datatug.ProjectLifecycleWrite {
	t.Helper()
	ref := datatug.ProjectObjectRef{Kind: "session", ID: "session"}
	review, err := store.ReviewProjectLifecycle(context.Background(), scope, ref)
	if err != nil || !review.CanRestore {
		t.Fatalf("native archive not restorable: %+v %v", review, err)
	}
	return datatug.ProjectLifecycleWrite{OperationID: "restore-session", ExpectedRevision: review.Revision, Change: datatug.ProjectLifecycleChange{Object: ref, Action: "restore"}}
}
func TestNativeRestoreRecoversEveryCrossStoreInterruption(t *testing.T) {
	for _, stage := range []string{"accepted", "native-restored", "metadata-restored"} {
		t.Run(stage, func(t *testing.T) {
			create, state, scope := nativeStoreFixture(t)
			store := create()
			write := restoreWrite(t, store, scope)
			fired := false
			fault = func(current string) error {
				if current == stage && !fired {
					fired = true
					return errors.New("injected interruption")
				}
				return nil
			}
			if _, err := store.ChangeProjectLifecycle(context.Background(), scope, write); !errors.Is(err, ErrOutcomeUncertain) {
				t.Fatalf("interrupted restore acknowledged: %v", err)
			}
			fault = nil
			restarted := create()
			ack, err := restarted.ChangeProjectLifecycle(context.Background(), scope, write)
			if err != nil {
				t.Fatal(err)
			}
			if state.archived || state.revision != "2" || state.restores != 1 {
				t.Fatalf("native restore was lost or repeated: %+v", state)
			}
			catalog, err := restarted.ListProjectLifecycle(context.Background(), scope, true)
			if err != nil || catalog.Revision != ack.Revision {
				t.Fatalf("receipt does not bind resulting state: %+v %v", catalog, err)
			}
			replay, err := create().ChangeProjectLifecycle(context.Background(), scope, write)
			if err != nil || replay != ack {
				t.Fatalf("durable receipt replay differs: %+v %v", replay, err)
			}
		})
	}
}
func TestNativeRestoreRejectsStaleReviewPayloadRebindingAndRevokedActor(t *testing.T) {
	create, state, scope := nativeStoreFixture(t)
	store := create()
	write := restoreWrite(t, store, scope)
	stale := write
	stale.ExpectedRevision = "stale"
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, stale); !errors.Is(err, datatug.ErrProjectLifecycleConflict) || state.restores != 0 {
		t.Fatal("stale review changed native state", err)
	}
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, write); err != nil {
		t.Fatal(err)
	}
	rebound := write
	rebound.Change.Action = "archive"
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, rebound); !errors.Is(err, datatug.ErrProjectLifecycleConflict) {
		t.Fatal("operation payload rebound", err)
	}
	state.denied = true
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, write); !errors.Is(err, os.ErrPermission) {
		t.Fatal("receipt replay bypassed current write authority", err)
	}
}
func TestConcurrentVisibilityWritesKeepReviewCAS(t *testing.T) {
	create, state, scope := nativeStoreFixture(t)
	state.archived = false
	store := create()
	catalog, err := store.ListProjectLifecycle(context.Background(), scope, true)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, operation := range []string{"first", "second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := create().ChangeProjectLifecycle(context.Background(), scope, datatug.ProjectLifecycleWrite{OperationID: id, ExpectedRevision: catalog.Revision, Change: datatug.ProjectLifecycleChange{Object: datatug.ProjectObjectRef{Kind: "query", ID: "kept"}, Action: "archive"}})
			results <- err
		}(operation)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, datatug.ErrProjectLifecycleConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent review CAS success=%d conflict=%d", success, conflict)
	}
}

func TestPendingNativeRestoreCannotUndoANewerArchive(t *testing.T) {
	create, state, scope := nativeStoreFixture(t)
	store := create()
	restore := restoreWrite(t, store, scope)
	fault = func(stage string) error {
		if stage == "native-restored" {
			return errors.New("crash after native receipt")
		}
		return nil
	}
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, restore); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatal(err)
	}
	fault = nil
	current, err := create().ListProjectLifecycle(context.Background(), scope, true)
	if err != nil {
		t.Fatal(err)
	}
	archive := datatug.ProjectLifecycleWrite{OperationID: "newer-archive", ExpectedRevision: current.Revision, Change: datatug.ProjectLifecycleChange{Object: restore.Change.Object, Action: "archive"}}
	if _, err = create().ChangeProjectLifecycle(context.Background(), scope, archive); err != nil {
		t.Fatal(err)
	}
	if _, err = create().ChangeProjectLifecycle(context.Background(), scope, restore); err != nil {
		t.Fatal(err)
	}
	after, err := create().ReviewProjectLifecycle(context.Background(), scope, restore.Change.Object)
	if err != nil || !after.Target.Archived || !after.CanRestore || state.restores != 1 {
		t.Fatalf("old native receipt undid newer archive: %+v %v", after, err)
	}
}
