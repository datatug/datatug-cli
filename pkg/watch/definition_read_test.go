package watch

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func TestReadDefinitionInTransactionRechecksAuthorityScopeAndVisibility(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	saved := create(t, s, a, definition("private-reuse"))
	authorize := s.cloud.options.Authorize
	calls := 0
	s.cloud.options.Authorize = func(ctx context.Context, tx dal.ReadTransaction) error { calls++; return authorize(ctx, tx) }
	db := s.cloud.db.(*contractFirestoreDB)
	read := func(who Actor, id string) (Definition, error) {
		var result Definition
		err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
			var err error
			result, err = s.ReadDefinitionInTransaction(ctx, tx, who, id)
			if tx.(*contractFirestoreTX).wrote {
				t.Fatal("read wrote native state")
			}
			return err
		})
		return result, err
	}
	got, err := read(a, saved.ID)
	if err != nil || got.ID != saved.ID || got.Version != saved.Version || calls != 1 {
		t.Fatalf("native read: %v calls=%d", err, calls)
	}
	if _, err = read(actor("bob"), saved.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private definition disclosed: %v", err)
	}
	wrong := a
	wrong.Scope.Project = "another-project"
	if _, err = read(wrong, saved.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-project read admitted: %v", err)
	}
	if _, err = read(a, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing definition: %v", err)
	}
	s.cloud.options.Authorize = func(context.Context, dal.ReadTransaction) error { return errWatchInjected }
	if _, err = read(a, saved.ID); !errors.Is(err, errWatchInjected) {
		t.Fatalf("revoked authority admitted: %v", err)
	}
}

func TestReadDefinitionInTransactionRejectsInvalidCorruptAndCanceledReads(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	s := stores[0]
	ctx := context.Background()
	a := actor("alice")
	saved := create(t, s, a, definition("corrupt-reuse"))
	db := s.cloud.db.(*contractFirestoreDB)
	cloudCorruptRow(t, s, definitions, a.Scope, saved.ID, func(row map[string]any) { row["payload"] = "{" })
	if err := db.RunReadonlyTransaction(ctx, func(ctx context.Context, tx dal.ReadTransaction) error {
		got, err := s.ReadDefinitionInTransaction(ctx, tx, a, saved.ID)
		if err != nil && got.ID != "" {
			t.Fatal("failed read returned partial private definition")
		}
		return err
	}); err == nil {
		t.Fatal("corrupt native payload admitted")
	}
	if _, err := s.ReadDefinitionInTransaction(ctx, nil, a, saved.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil transaction admitted")
	}
	var absent *Store
	if _, err := absent.ReadDefinitionInTransaction(ctx, nil, a, saved.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil store admitted")
	}
	if _, err := (&Store{}).ReadDefinitionInTransaction(ctx, &contractFirestoreTX{}, a, saved.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-native store admitted")
	}
	if _, err := s.ReadDefinitionInTransaction(ctx, &contractFirestoreTX{}, a, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty id admitted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ReadDefinitionInTransaction(canceled, &contractFirestoreTX{}, a, saved.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read admitted: %v", err)
	}
}
