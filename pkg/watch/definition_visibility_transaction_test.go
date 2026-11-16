package watch

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func TestListDefinitionsInTransactionRefusesInvalidAndUnreadablePrivateInventory(t *testing.T) {
	stores, _, _ := firestoreContractFixture(t)
	store := stores[0]
	actor := actor("alice")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if items, err := store.ListDefinitionsInTransaction(ctx, &contractFirestoreTX{}, actor); !errors.Is(err, context.Canceled) || items != nil {
		t.Fatalf("canceled inventory disclosed rows: %+v %v", items, err)
	}
	wrong := actor
	wrong.Scope.Project = "another-project"
	if items, err := store.ListDefinitionsInTransaction(context.Background(), &contractFirestoreTX{}, wrong); !errors.Is(err, ErrForbidden) || items != nil {
		t.Fatalf("foreign project inventory disclosed rows: %+v %v", items, err)
	}
	if items, err := store.ListDefinitionsInTransaction(context.Background(), nil, actor); !errors.Is(err, ErrInvalid) || items != nil {
		t.Fatalf("missing transaction admitted: %+v %v", items, err)
	}
	saved := create(t, store, actor, definition("corrupt-visibility"))
	cloudCorruptRow(t, store, definitions, actor.Scope, saved.ID, func(row map[string]any) { row["payload"] = "{" })
	var disclosed []Definition
	err := store.cloud.db.RunReadonlyTransaction(context.Background(), func(ctx context.Context, tx dal.ReadTransaction) error {
		var err error
		disclosed, err = store.ListDefinitionsInTransaction(ctx, tx, actor)
		return err
	})
	if err == nil || disclosed != nil {
		t.Fatalf("corrupt private inventory returned partial rows: %+v %v", disclosed, err)
	}
}
