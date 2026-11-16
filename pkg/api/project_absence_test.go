package api

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

func TestProjectDefinitionAbsenceDoesNotClassifyOtherReadFailures(t *testing.T) {
	old := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = old })
	for _, cause := range []error{datatug.ErrProjectDoesNotExist, fs.ErrPermission, fs.ErrNotExist, errors.New("broken metadata")} {
		storage.NewDatatugStore = func(string) (storage.Store, error) {
			return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
				return mockProjectStore{loadProjectFileFunc: func(context.Context) (datatug.ProjectFile, error) { return datatug.ProjectFile{}, cause }, loadProjectFunc: func(context.Context, ...datatug.StoreOption) (*datatug.Project, error) {
					t.Fatal("full metadata read after failed root definition")
					return nil, nil
				}}
			}}, nil
		}
		for _, read := range []func(context.Context, dto.ProjectRef) error{
			func(ctx context.Context, ref dto.ProjectRef) error { _, err := GetProjectSummary(ctx, ref); return err },
			func(ctx context.Context, ref dto.ProjectRef) error { _, err := GetProjectFull(ctx, ref); return err },
		} {
			err := read(context.Background(), dto.ProjectRef{ProjectID: "p1"})
			if err == nil || errors.Is(err, datatug.ErrProjectDoesNotExist) != (cause == datatug.ErrProjectDoesNotExist) || err.Error() != `project "p1" not found` {
				t.Fatalf("cause%v got%v", cause, err)
			}
		}
	}
}

func TestFullProjectNestedAbsenceIsNotRootDefinitionAbsence(t *testing.T) {
	old := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = old })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			return mockProjectStore{loadProjectFunc: func(context.Context, ...datatug.StoreOption) (*datatug.Project, error) {
				return nil, datatug.ErrProjectDoesNotExist
			}}
		}}, nil
	}
	_, err := GetProjectFull(context.Background(), dto.ProjectRef{ProjectID: "p1"})
	if err == nil || errors.Is(err, datatug.ErrProjectDoesNotExist) {
		t.Fatalf("nested read was classified as root absence: %v", err)
	}
}
