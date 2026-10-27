package secureread

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// countingSealedBackend is an adapter backend that can be closed. A DALgo DB
// built from it is the sealed validatedDB value, exactly as dalgo2couchdb and
// dalgo2postgrest return their handle: Close is only reachable through dal.As.
type countingSealedBackend struct {
	dal.Backend
	closes int
}

func (b *countingSealedBackend) Close() error {
	b.closes++
	return nil
}

func TestExecutorClosesAnOwnedSourceOnEveryPath(t *testing.T) {
	ref, err := dbcopy.Parse(newSQLiteFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	realDB, err := ref.OpenProtected(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbcopy.CloseSource(realDB) })

	backend := &countingSealedBackend{Backend: dal.BackendOf(realDB)}
	sealed := dal.NewDB(backend)
	if _, visible := any(sealed).(interface{ Close() error }); visible {
		t.Fatal("the test needs the framework's sealed DB value (Close must be hidden)")
	}

	original := openSourceFn
	t.Cleanup(func() { openSourceFn = original })
	openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
		// A fresh owned closer per open, exactly as openSource builds it.
		return sealed, dbcopy.SourceCloser(sealed), nil
	}

	executor := NewExecutor(Session{Unrestricted: true})
	ctx := context.Background()

	if _, err = executor.RunStructured(ctx, "sqlite://whatever.db", productsQuery(), nil); err != nil {
		t.Fatalf("success path: %v", err)
	}
	if backend.closes != 1 {
		t.Fatalf("close after success = %d, want 1", backend.closes)
	}

	missing := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("missing_table", ""))).SelectColumns()
	if _, err = executor.RunStructured(ctx, "sqlite://whatever.db", missing, nil); err == nil {
		t.Fatal("the query was expected to fail after the source was opened")
	}
	if backend.closes != 2 {
		t.Fatalf("close after a query failure = %d, want 2", backend.closes)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = executor.RunStructured(cancelled, "sqlite://whatever.db", productsQuery(), nil)
	if backend.closes != 3 {
		t.Fatalf("close after cancellation = %d, want 3", backend.closes)
	}
}
