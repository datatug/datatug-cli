package firestoreschema

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/firestore"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/schemers"
	"google.golang.org/api/iterator"
)

var errFakeGetClient = errors.New("fake getClient error")

// fakeCollectionsProvider satisfies firestoreCollectionsProvider in tests.
// Its Collections method must not be called because the firestoreCollections
// seam is always overridden in tests.
type fakeCollectionsProvider struct{}

func (fakeCollectionsProvider) Collections(_ context.Context) *firestore.CollectionIterator {
	panic("fakeCollectionsProvider.Collections should not be called in tests")
}

// nilClient returns a nil *firestore.Client (no live connection needed).
func nilClient(_ context.Context) (*firestore.Client, error) {
	return (*firestore.Client)(nil), nil
}

// TestNewProvider ensures the constructor builds a non-nil provider.
func TestNewProvider(t *testing.T) {
	p := NewProvider(nilClient)
	if p == nil {
		t.Fatal("NewProvider returned nil")
	}
}

// TestGetCollection_getClientError covers the error-return path at lines 32-34.
func TestGetCollection_getClientError(t *testing.T) {
	p := NewProvider(func(_ context.Context) (*firestore.Client, error) {
		return nil, errFakeGetClient
	})
	collRef := dal.NewRootCollectionRef("users", "")
	_, err := p.GetCollection(context.Background(), &collRef)
	if !errors.Is(err, errFakeGetClient) {
		t.Fatalf("expected errFakeGetClient, got %v", err)
	}
}

// TestGetCollections_getClientError covers the error-return path at lines 66-68.
func TestGetCollections_getClientError(t *testing.T) {
	p := NewProvider(func(_ context.Context) (*firestore.Client, error) {
		return nil, errFakeGetClient
	})
	_, err := p.GetCollections(context.Background(), nil)
	if !errors.Is(err, errFakeGetClient) {
		t.Fatalf("expected errFakeGetClient, got %v", err)
	}
}

// withSeams sets up the iteration seams and restores them after the test.
func withSeams(t *testing.T, refs []*firestore.CollectionRef, customDocSeam func(*firestore.Client, string) firestoreCollectionsProvider) func() {
	t.Helper()

	origDoc := firestoreDoc
	origCollections := firestoreCollections
	origNext := iterCollectionNext
	origClose := closeFirestoreClient

	call := 0
	iterCollectionNext = func(_ *firestore.CollectionIterator) (*firestore.CollectionRef, error) {
		if call < len(refs) {
			r := refs[call]
			call++
			return r, nil
		}
		return nil, iterator.Done
	}
	firestoreCollections = func(_ context.Context, _ firestoreCollectionsProvider) *firestore.CollectionIterator {
		return nil // iter is never used directly; iterCollectionNext seam handles it
	}
	closeFirestoreClient = func(_ *firestore.Client) error { return nil }
	if customDocSeam != nil {
		firestoreDoc = customDocSeam
	}

	return func() {
		firestoreDoc = origDoc
		firestoreCollections = origCollections
		iterCollectionNext = origNext
		closeFirestoreClient = origClose
	}
}

// makeCollectionRef builds a *firestore.CollectionRef via zero-value struct.
// We only need its Path and ID fields to be set for the test assertions.
func makeCollectionRef(path, id string) *firestore.CollectionRef {
	return &firestore.CollectionRef{
		Path: path,
		ID:   id,
	}
}

// TestGetCollection_rootCollection_found covers the else-branch (no "/" in path)
// and the "found" return path (lines 40-41, 44, 46-59).
func TestGetCollection_rootCollection_found(t *testing.T) {
	collRef := dal.NewRootCollectionRef("users", "")
	refs := []*firestore.CollectionRef{
		makeCollectionRef("users", "users"),
	}
	restore := withSeams(t, refs, nil)
	defer restore()

	p := NewProvider(nilClient)
	got, err := p.GetCollection(context.Background(), &collRef)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := &schemers.Collection{ID: "users"}
	if got == nil || got.ID != want.ID {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// TestGetCollection_rootCollection_notFound covers the iteration-done (no match) path.
func TestGetCollection_rootCollection_notFound(t *testing.T) {
	collRef := dal.NewRootCollectionRef("users", "")
	refs := []*firestore.CollectionRef{
		makeCollectionRef("orders", "orders"),
	}
	restore := withSeams(t, refs, nil)
	defer restore()

	p := NewProvider(nilClient)
	got, err := p.GetCollection(context.Background(), &collRef)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil collection, got %v", got)
	}
}

// TestGetCollection_iterError covers the non-Done error return path (lines 52-53).
func TestGetCollection_iterError(t *testing.T) {
	collRef := dal.NewRootCollectionRef("users", "")
	errIter := errors.New("iter error")

	origNext := iterCollectionNext
	origColl := firestoreCollections
	origDoc := firestoreDoc
	defer func() {
		iterCollectionNext = origNext
		firestoreCollections = origColl
		firestoreDoc = origDoc
	}()

	iterCollectionNext = func(_ *firestore.CollectionIterator) (*firestore.CollectionRef, error) {
		return nil, errIter
	}
	firestoreCollections = func(_ context.Context, _ firestoreCollectionsProvider) *firestore.CollectionIterator {
		return nil
	}

	p := NewProvider(nilClient)
	_, err := p.GetCollection(context.Background(), &collRef)
	if !errors.Is(err, errIter) {
		t.Fatalf("expected errIter, got %v", err)
	}
}

// TestGetCollection_nestedCollection covers the "contains /" branch (lines 38-39),
// exercising the firestoreDoc seam.
func TestGetCollection_nestedCollection_found(t *testing.T) {
	parentKey := dal.NewKeyWithID("projects", "proj1")
	collRef := dal.NewCollectionRef("tasks", "", parentKey)

	docSeamCalled := false
	docSeam := func(_ *firestore.Client, _ string) firestoreCollectionsProvider {
		docSeamCalled = true
		return fakeCollectionsProvider{}
	}

	refs := []*firestore.CollectionRef{
		makeCollectionRef("tasks", "tasks"),
	}
	restore := withSeams(t, refs, docSeam)
	defer restore()

	p := NewProvider(nilClient)
	got, err := p.GetCollection(context.Background(), &collRef)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !docSeamCalled {
		t.Fatal("firestoreDoc seam was not called")
	}
	if got == nil || got.ID != "tasks" {
		t.Fatalf("expected collection tasks, got %v", got)
	}
}

// TestGetCollections_noParent_empty covers parentKey==nil branch with empty results.
func TestGetCollections_noParent_empty(t *testing.T) {
	origNext := iterCollectionNext
	origColl := firestoreCollections
	origClose := closeFirestoreClient
	defer func() {
		iterCollectionNext = origNext
		firestoreCollections = origColl
		closeFirestoreClient = origClose
	}()

	firestoreCollections = func(_ context.Context, _ firestoreCollectionsProvider) *firestore.CollectionIterator {
		return nil
	}
	iterCollectionNext = func(_ *firestore.CollectionIterator) (*firestore.CollectionRef, error) {
		return nil, iterator.Done
	}
	closeFirestoreClient = func(_ *firestore.Client) error { return nil }

	p := NewProvider(nilClient)
	cols, err := p.GetCollections(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cols) != 0 {
		t.Fatalf("expected empty, got %v", cols)
	}
}

// TestGetCollections_noParent_withResults covers the append path (lines 91-93).
func TestGetCollections_noParent_withResults(t *testing.T) {
	refs := []*firestore.CollectionRef{
		makeCollectionRef("users", "users"),
		makeCollectionRef("orders", "orders"),
	}
	restore := withSeams(t, refs, nil)
	defer restore()

	p := NewProvider(nilClient)
	cols, err := p.GetCollections(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(cols))
	}
}

// TestGetCollections_withParent covers parentKey!=nil branch (lines 77-78).
func TestGetCollections_withParent(t *testing.T) {
	parentKey := dal.NewKeyWithID("projects", "proj1")

	docSeamCalled := false
	docSeam := func(_ *firestore.Client, _ string) firestoreCollectionsProvider {
		docSeamCalled = true
		return fakeCollectionsProvider{}
	}

	refs := []*firestore.CollectionRef{
		makeCollectionRef("tasks", "tasks"),
	}
	restore := withSeams(t, refs, docSeam)
	defer restore()

	p := NewProvider(nilClient)
	cols, err := p.GetCollections(context.Background(), parentKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !docSeamCalled {
		t.Fatal("firestoreDoc seam was not called")
	}
	if len(cols) != 1 || cols[0].ID != "tasks" {
		t.Fatalf("expected [tasks], got %v", cols)
	}
}

// TestGetCollections_iterError covers the non-Done error return path in GetCollections.
func TestGetCollections_iterError(t *testing.T) {
	errIter := errors.New("iter error in GetCollections")

	origNext := iterCollectionNext
	origColl := firestoreCollections
	origClose := closeFirestoreClient
	defer func() {
		iterCollectionNext = origNext
		firestoreCollections = origColl
		closeFirestoreClient = origClose
	}()

	firestoreCollections = func(_ context.Context, _ firestoreCollectionsProvider) *firestore.CollectionIterator {
		return nil
	}
	iterCollectionNext = func(_ *firestore.CollectionIterator) (*firestore.CollectionRef, error) {
		return nil, errIter
	}
	closeFirestoreClient = func(_ *firestore.Client) error { return nil }

	p := NewProvider(nilClient)
	_, err := p.GetCollections(context.Background(), nil)
	if !errors.Is(err, errIter) {
		t.Fatalf("expected errIter, got %v", err)
	}
}
