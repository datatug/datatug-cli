package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

// The fake capability runs the real planner at the write boundary. It is not a
// claim that the stock filesystem provider can inventory retained history.
type lifecycleTestStore struct {
	datatug.ProjectStore
	snapshot datatug.ProjectUsageSnapshot
	scope    datatug.ProjectLifecycleScope
	writes   int
	badAck   bool
}

func (s *lifecycleTestStore) ReviewProjectLifecycle(_ context.Context, scope datatug.ProjectLifecycleScope, target datatug.ProjectObjectRef) (datatug.ProjectLifecycleReview, error) {
	s.scope = scope
	return datatug.ReviewProjectLifecycle(s.snapshot, target, func(datatug.ProjectObjectRef) bool { return true })
}
func (s *lifecycleTestStore) ChangeProjectLifecycle(_ context.Context, scope datatug.ProjectLifecycleScope, write datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
	s.scope = scope
	if _, err := datatug.PlanProjectLifecycle(s.snapshot, write); err != nil {
		return datatug.ProjectLifecycleAck{}, err
	}
	s.writes++
	ack := datatug.ProjectLifecycleAck{OperationID: write.OperationID, Revision: "r2", Change: write.Change}
	if s.badAck {
		ack.OperationID = "different-operation"
	}
	return ack, nil
}
func (s *lifecycleTestStore) ListProjectLifecycle(_ context.Context, scope datatug.ProjectLifecycleScope, archived bool) (datatug.ProjectLifecycleCatalog, error) {
	s.scope = scope
	out := datatug.ProjectLifecycleCatalog{Revision: s.snapshot.Revision, Complete: s.snapshot.Complete, Items: []datatug.ProjectUsageItem{}}
	for _, item := range s.snapshot.Objects {
		if item.Archived == archived {
			out.Items = append(out.Items, datatug.ProjectUsageItem{Ref: item.Ref, Title: item.Title, Archived: item.Archived})
		}
	}
	return out, nil
}
func lifecycleAPISetup(t *testing.T, writable bool) (LocalLifecycleSelection, *lifecycleTestStore) {
	t.Helper()
	originalGuard := lifecycleBranchGuard
	t.Cleanup(func() { lifecycleBranchGuard = originalGuard })
	lifecycleBranchGuard = func(ctx context.Context, ref dto.ProjectRef, branch string, mutating bool, fn func(*os.Root, gitState) error) error {
		return fn(nil, gitState{})
	}
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, map[string]string{"p": t.TempDir()}, Capabilities{AllowWrites: writable})
	provider := &lifecycleTestStore{snapshot: datatug.ProjectUsageSnapshot{Revision: "r1", Complete: true, Objects: []datatug.ProjectObjectUsage{{Ref: datatug.ProjectObjectRef{Kind: "query", ID: "q"}, ContentRevision: "q1"}}}}
	original := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = original })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return provider }}, nil
	}
	return LocalLifecycleSelection{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "p"}, Branch: "feature/x", ProjectAPI: "local"}, provider
}
func lifecycleWrite(selection LocalLifecycleSelection) LocalLifecycleWrite {
	return LocalLifecycleWrite{LocalLifecycleSelection: selection, ProjectLifecycleWrite: datatug.ProjectLifecycleWrite{OperationID: "operation-1", ExpectedRevision: "r1", Change: datatug.ProjectLifecycleChange{Object: datatug.ProjectObjectRef{Kind: "query", ID: "q"}, Action: "archive"}}}
}
func TestLocalProjectLifecycleBindsScopeAndKeepsDeleteReferenceSafe(t *testing.T) {
	selection, provider := lifecycleAPISetup(t, true)
	review, err := LocalProjectLifecycleReview(context.Background(), selection, datatug.ProjectObjectRef{Kind: "query", ID: "q"})
	if err != nil || !review.CanArchive {
		t.Fatalf("review: %+v %v", review, err)
	}
	if provider.scope.ActorID == "" || provider.scope.Storage != LocalStoreID || provider.scope.Project != "p" || provider.scope.Branch != "" {
		t.Fatalf("scope lost: %+v", provider.scope)
	}
	// Visibility preserves references. Destructive Delete still checks the
	// fresh retained graph at the provider boundary.
	provider.snapshot.Objects = append(provider.snapshot.Objects, datatug.ProjectObjectUsage{Ref: datatug.ProjectObjectRef{Kind: "board", ID: "new-board"}, ContentRevision: "b1", References: []datatug.ProjectObjectRef{{Kind: "query", ID: "q"}}})
	request := lifecycleWrite(selection)
	request.Change.Action = "delete"
	_, err = LocalProjectLifecycleChange(context.Background(), request)
	if !errors.Is(err, datatug.ErrProjectObjectReferenced) || provider.writes != 0 {
		t.Fatalf("new reference bypassed: %v, writes %d", err, provider.writes)
	}
}
func TestLocalProjectLifecycleKeepsReadOnlyCapabilitiesDisabled(t *testing.T) {
	selection, provider := lifecycleAPISetup(t, false)
	review, err := LocalProjectLifecycleReview(context.Background(), selection, datatug.ProjectObjectRef{Kind: "query", ID: "q"})
	if err != nil || review.CanArchive || review.CanDelete || review.CanRestore {
		t.Fatalf("read-only review: %+v %v", review, err)
	}
	_, err = LocalProjectLifecycleChange(context.Background(), lifecycleWrite(selection))
	if !errors.Is(err, secureread.ErrAccessDenied) || provider.writes != 0 {
		t.Fatalf("read-only write: %v", err)
	}
}
func TestLocalProjectLifecycleRejectsUnconfirmedReceipt(t *testing.T) {
	selection, provider := lifecycleAPISetup(t, true)
	provider.badAck = true
	_, err := LocalProjectLifecycleChange(context.Background(), lifecycleWrite(selection))
	if !errors.Is(err, ErrProjectMutationOutcomeUncertain) || provider.writes != 1 {
		t.Fatalf("receipt mismatch: %v", err)
	}
}
func TestLocalProjectLifecycleFilesystemVisibilityPreservesExecutionDefinition(t *testing.T) {
	project, queriesDir := servedWritableProject(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, true)
	dir := filepath.Dir(queriesDir)
	if err := os.MkdirAll(filepath.Join(dir, "queries"), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte(`{"title":"Kept query","queryType":"sql","text":"select 1"}`)
	path := filepath.Join(dir, "queries", "q.query.json")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	selection := LocalLifecycleSelection{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: project}}
	review, err := LocalProjectLifecycleReview(context.Background(), selection, datatug.ProjectObjectRef{Kind: "query", ID: "q"})
	if err != nil || !review.CanArchive || !review.ArchiveSupported || review.Complete || review.CanDelete {
		t.Fatalf("visibility unavailable: %+v %v", review, err)
	}
	request := lifecycleWrite(selection)
	request.ExpectedRevision = review.Revision
	if _, err = LocalProjectLifecycleChange(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("definition changed: %s %v", after, err)
	}
	request.Change.Action = "delete"
	if _, err = LocalProjectLifecycleChange(context.Background(), request); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatalf("delete granted without graph: %v", err)
	}
	catalog, err := LocalProjectLifecycleCatalog(context.Background(), selection, true)
	if err != nil || len(catalog.Items) != 1 || !catalog.Items[0].Archived {
		t.Fatalf("archive missing: %+v %v", catalog, err)
	}
	request.OperationID = "restore"
	request.ExpectedRevision = catalog.Revision
	request.Change.Action = "restore"
	if _, err = LocalProjectLifecycleChange(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}
func TestLocalProjectLifecycleRejectsInvalidScopeAndCommands(t *testing.T) {
	selection, provider := lifecycleAPISetup(t, true)
	for _, change := range []func(*LocalLifecycleWrite){
		func(w *LocalLifecycleWrite) { w.OperationID = "" }, func(w *LocalLifecycleWrite) { w.ExpectedRevision = "" },
		func(w *LocalLifecycleWrite) { w.Change.Action = "purge" }, func(w *LocalLifecycleWrite) { w.Change.Object.ID = "../outside" },
		func(w *LocalLifecycleWrite) { w.Change.Object.Kind = "run" }, func(w *LocalLifecycleWrite) { w.ProjectAPI = "cloud" },
	} {
		request := lifecycleWrite(selection)
		change(&request)
		if _, err := LocalProjectLifecycleChange(context.Background(), request); !errors.Is(err, datatug.ErrProjectLifecycleInvalid) {
			t.Fatalf("invalid command accepted: %+v %v", request, err)
		}
	}
	if provider.writes != 0 {
		t.Fatal("invalid request reached persistence")
	}
}
