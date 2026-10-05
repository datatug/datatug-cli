package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// The file store treats a delete of an item that is not there as done, and answers nothing.
// A delete of a missing board, entity, folder or db server is answered as that (a built
// sentence that says the item is not found, which a route answers with a 404), and a delete
// that fails is answered as a failure (a built sentence that says it could not be deleted,
// which is a 500): the two are not the same answer. The item is looked up first, as the
// route that gets it does, and only an answer of the store that says it is not there is a
// missing item.

// deleteKind is one kind of item that a delete route removes.
type deleteKind struct {
	name string
	// shown is the item as the answers name it.
	shown string
	// build returns the project store that loads with load and deletes with remove, and the
	// call that deletes the item through the entry of this package.
	build func(load, remove func() error) (mockProjectStore, func(ctx context.Context) error)
}

func deleteKinds() []deleteKind {
	return []deleteKind{
		{"board", "b1", func(load, remove func() error) (mockProjectStore, func(context.Context) error) {
			return mockProjectStore{
				loadBoardFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Board, error) {
					return &datatug.Board{}, load()
				},
				deleteBoardFunc: func(context.Context, string) error { return remove() },
			}, func(ctx context.Context) error {
				return DeleteBoard(ctx, itemRef("b1"))
			}
		}},
		{"entity", "Customer", func(load, remove func() error) (mockProjectStore, func(context.Context) error) {
			return mockProjectStore{
				loadEntityFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Entity, error) {
					return &datatug.Entity{}, load()
				},
				deleteEntityFunc: func(context.Context, string) error { return remove() },
			}, func(ctx context.Context) error {
				return DeleteEntity(ctx, itemRef("Customer"))
			}
		}},
		{"folder", "a/b", func(load, remove func() error) (mockProjectStore, func(context.Context) error) {
			return mockProjectStore{
				loadFolderFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Folder, error) {
					return &datatug.Folder{}, load()
				},
				deleteFolderFunc: func(context.Context, string) error { return remove() },
			}, func(ctx context.Context) error {
				return DeleteFolder(ctx, itemRef("a/b"))
			}
		}},
		{"db server", "sqlserver:localhost:1433", func(load, remove func() error) (mockProjectStore, func(context.Context) error) {
			servers := mockDbServersStore{
				loadProjDbServerFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.ProjDbServer, error) {
					return &datatug.ProjDbServer{}, load()
				},
				deleteProjDbServerFunc: func(context.Context, string) error { return remove() },
			}
			return mockProjectStore{dbServersStoreFunc: func(string) datatug.ProjDbServersStore { return servers }},
				func(ctx context.Context) error { return DeleteDbServer(ctx, plainProject(), plainServer()) }
		}},
	}
}

// useProjectStore makes the factory hand out store, until the test ends.
func useProjectStore(t *testing.T, store mockProjectStore) {
	t.Helper()
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return store }}, nil
	}
}

func TestDelete_AMissingItemIsNotFoundAndAFailedDeleteIsNot(t *testing.T) {
	serveProjects(t, "p1") // the project of itemRef and plainProject is one this process serves
	ctx := context.Background()
	missing := fmt.Errorf("failed to load the item from project: %w", os.ErrNotExist)
	for _, kind := range deleteKinds() {
		t.Run(kind.name, func(t *testing.T) {
			wantMissing := kind.name + ` "` + kind.shown + `" not found`
			wantFailed := `could not delete ` + kind.name + ` "` + kind.shown + `"`
			cause := errors.New("remove /Users/operator/some-project/x: directory not empty")
			for _, tc := range []struct {
				name        string
				load        error
				remove      error
				want        string // the answer's text; empty for a delete that is done
				wantMissing bool
				deletes     int
			}{
				{"an item that is there", nil, nil, "", false, 1},
				{"an item that is not there", missing, nil, wantMissing, true, 0},
				{"an item that is not there, said by the delete", nil, fmt.Errorf("delete: %w", os.ErrNotExist), wantMissing, true, 1},
				{"an item that cannot be read is deleted all the same", errors.New("invalid character '{'"), nil, "", false, 1},
				{"an item that cannot be read and cannot be deleted", errors.New("permission denied"), cause, wantFailed, false, 1},
				{"a delete that fails", nil, cause, wantFailed, false, 1},
			} {
				deletes := 0
				store, call := kind.build(func() error { return tc.load }, func() error { deletes++; return tc.remove })
				useProjectStore(t, store)
				err := call(ctx)
				switch {
				case tc.want == "" && err != nil:
					t.Errorf("%s: %v, want it deleted", tc.name, err)
				case tc.want != "" && (err == nil || err.Error() != tc.want):
					t.Errorf("%s: %v, want exactly %q", tc.name, err, tc.want)
				}
				if err != nil && (errors.Is(err, ErrItemNotFound) != tc.wantMissing || errors.Is(err, cause) || validation.IsBadRequestError(err)) {
					t.Errorf("%s: %v is missing = %v, want %v (and no cause, no bad request)", tc.name, err, errors.Is(err, ErrItemNotFound), tc.wantMissing)
				}
				if deletes != tc.deletes {
					t.Errorf("%s: the store was asked to delete %d times, want %d", tc.name, deletes, tc.deletes)
				}
			}
		})
	}
}
