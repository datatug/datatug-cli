package projectvisibility

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
)

type providerHooks struct {
	datatug.ProjectLifecycleStore
	list   func(context.Context, datatug.ProjectLifecycleScope, bool) (datatug.ProjectLifecycleCatalog, error)
	change func(context.Context, datatug.ProjectLifecycleScope, datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error)
}

func (p providerHooks) ListProjectLifecycle(ctx context.Context, scope datatug.ProjectLifecycleScope, all bool) (datatug.ProjectLifecycleCatalog, error) {
	if p.list != nil {
		return p.list(ctx, scope, all)
	}
	return p.ProjectLifecycleStore.ListProjectLifecycle(ctx, scope, all)
}
func (p providerHooks) ChangeProjectLifecycle(ctx context.Context, scope datatug.ProjectLifecycleScope, write datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
	if p.change != nil {
		return p.change(ctx, scope, write)
	}
	return p.ProjectLifecycleStore.ChangeProjectLifecycle(ctx, scope, write)
}

func TestVisibilityAdmissionRejectsInvalidMountsAndScope(t *testing.T) {
	create, _, scope := nativeStoreFixture(t)
	store := create()
	for _, modify := range []func(*Store){func(s *Store) { s.provider = nil }, func(s *Store) { s.scope.ActorID = "" }, func(s *Store) { s.runtime.Inventory = nil }, func(s *Store) { s.runtime.Authorize = nil }} {
		copy := *store
		modify(&copy)
		if _, err := NewStore(copy.provider, copy.scope, t.TempDir(), store.binding, copy.runtime); err == nil {
			t.Fatal("invalid authority accepted")
		}
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(store.provider, scope, file, store.binding, store.runtime); err == nil {
		t.Fatal("non-directory private mount accepted")
	}
	if _, err := NewStore(store.provider, scope, t.TempDir(), filepath.Join(t.TempDir(), "missing"), store.runtime); err == nil {
		t.Fatal("missing registered project root accepted")
	}
	wrong := scope
	wrong.ActorID = "other-owner"
	if _, err := store.ListProjectLifecycle(context.Background(), wrong, true); !errors.Is(err, datatug.ErrProjectLifecycleInvalid) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ReviewProjectLifecycle(cancelled, scope, datatug.ProjectObjectRef{Kind: "session", ID: "session"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	bad := datatug.ProjectLifecycleWrite{}
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, bad); !errors.Is(err, datatug.ErrProjectLifecycleInvalid) {
		t.Fatal(err)
	}
	write := restoreWrite(t, store, scope)
	write.Change.Action = "delete"
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, write); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatal("delete bypassed owning reference provider", err)
	}
}

func TestVisibilityCatalogFailsClosedForNativeInventoryAndReadFailures(t *testing.T) {
	for _, kind := range []string{"provider", "native", "over-capacity", "invalid-ref", "restricted", "history", "history-kind", "revision", "duplicate", "authority"} {
		t.Run(kind, func(t *testing.T) {
			create, _, scope := nativeStoreFixture(t)
			store := create()
			original := store.runtime.Inventory
			injected := errors.New("injected private inventory unavailable")
			switch kind {
			case "provider":
				store.provider = providerHooks{ProjectLifecycleStore: store.provider, list: func(context.Context, datatug.ProjectLifecycleScope, bool) (datatug.ProjectLifecycleCatalog, error) {
					return datatug.ProjectLifecycleCatalog{}, injected
				}}
			case "authority":
				store.runtime.Authorize = func(context.Context, datatug.ProjectLifecycleScope, bool) error { return injected }
			default:
				store.runtime.Inventory = func(ctx context.Context, scope datatug.ProjectLifecycleScope) ([]Target, error) {
					items, err := original(ctx, scope)
					if err != nil {
						return nil, err
					}
					switch kind {
					case "native":
						return nil, injected
					case "over-capacity":
						return make([]Target, 10001), nil
					case "invalid-ref":
						items[0].Item.Ref.ID = "../secret"
					case "restricted":
						items[0].Item.Restricted = true
					case "history":
						items[0].Item.Historical = true
					case "history-kind":
						items[0].Item.Ref.Kind = "run"
					case "revision":
						items[0].Revision = strings.Repeat("r", 513)
					case "duplicate":
						items = append(items, items[0])
					}
					return items, nil
				}
			}
			if _, err := store.ListProjectLifecycle(context.Background(), scope, true); err == nil {
				t.Fatal("unverified private inventory was exposed")
			}
		})
	}
}

func TestVisibilityCatalogSeparatesActiveDiscoveryAndNativeRestoreCapability(t *testing.T) {
	create, _, scope := nativeStoreFixture(t)
	store := create()
	store.runtime.Restore = nil
	catalog, err := store.ListProjectLifecycle(context.Background(), scope, false)
	if err != nil || len(catalog.Items) != 1 || catalog.Items[0].Ref.Kind != "query" {
		t.Fatalf("native archive remained active: %+v %v", catalog, err)
	}
	review, err := store.ReviewProjectLifecycle(context.Background(), scope, datatug.ProjectObjectRef{Kind: "session", ID: "session"})
	if err != nil || review.CanRestore {
		t.Fatalf("unavailable native restore advertised: %+v %v", review, err)
	}
	write := datatug.ProjectLifecycleWrite{OperationID: "no-native-port", ExpectedRevision: review.Revision, Change: datatug.ProjectLifecycleChange{Object: review.Target.Ref, Action: "restore"}}
	if _, err = store.ChangeProjectLifecycle(context.Background(), scope, write); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatal("missing native owning port bypassed", err)
	}
	original := store.runtime.Inventory
	store.runtime.Inventory = func(ctx context.Context, scope datatug.ProjectLifecycleScope) ([]Target, error) {
		items, err := original(ctx, scope)
		items = append(items, Target{Item: datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "check", ID: "another"}, Title: "Another"}, Revision: "1"})
		return items, err
	}
	if _, err = store.ListProjectLifecycle(context.Background(), scope, true); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedMetadataCASDoesNotLeavePermanentProjectBlock(t *testing.T) {
	create, state, scope := nativeStoreFixture(t)
	store := create()
	restore := restoreWrite(t, store, scope)
	store.provider = providerHooks{ProjectLifecycleStore: store.provider, change: func(context.Context, datatug.ProjectLifecycleScope, datatug.ProjectLifecycleWrite) (datatug.ProjectLifecycleAck, error) {
		return datatug.ProjectLifecycleAck{}, datatug.ErrProjectLifecycleConflict
	}}
	if _, err := store.ChangeProjectLifecycle(context.Background(), scope, restore); !errors.Is(err, datatug.ErrProjectLifecycleConflict) {
		t.Fatalf("authoritative metadata CAS rejection remained uncertain: %v", err)
	}
	if state.archived || state.restores != 1 {
		t.Fatal("owning restore receipt lost")
	}
	current, err := create().ListProjectLifecycle(context.Background(), scope, true)
	if err != nil {
		t.Fatal(err)
	}
	next := datatug.ProjectLifecycleWrite{OperationID: "after-metadata-conflict", ExpectedRevision: current.Revision, Change: datatug.ProjectLifecycleChange{Object: datatug.ProjectObjectRef{Kind: "query", ID: "kept"}, Action: "archive"}}
	if _, err = create().ChangeProjectLifecycle(context.Background(), scope, next); err != nil {
		t.Fatalf("definite metadata conflict blocked unrelated action: %v", err)
	}
}
