package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestNativeQueryFolderCreateUsesQueryStoreAndSurvivesReload(t *testing.T) {
	dir, git := setupTestGitProject(t, "query-folders")
	branch := git("symbolic-ref", "--short", "HEAD")
	paths := map[string]string{"query-folders": dir}
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
	factory := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = factory })
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	filestore.SetProjectPath("query-folders", dir)
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "query-folders"}
	ctx := context.Background()
	for _, request := range []QueryFolderCreate{{ProjectRef: ref, Branch: branch, Path: "~", Name: "reports"}, {ProjectRef: ref, Branch: branch, Path: "~/reports", Name: "monthly"}} {
		result, err := CreateProjectQueryFolder(ctx, request)
		if err != nil || result == nil || result.ID != request.Name {
			t.Fatalf("creation identity: %v", err)
		}
		if _, err = CreateProjectQueryFolder(ctx, request); err != nil {
			t.Fatal("replay failed", err)
		}
	}
	declared, err := DeclaredProjectQueryFolders(ctx, ref)
	if err != nil || !reflect.DeepEqual(declared, []string{"reports", "reports/monthly"}) {
		t.Fatalf("reload=%v error=%v", declared, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "folders", "reports")); !os.IsNotExist(err) {
		t.Fatal("query creation touched general folders metadata")
	}
	_, err = CreateProjectQueryFolder(ctx, QueryFolderCreate{ProjectRef: ref, Branch: "different-branch", Path: "~", Name: "wrong"})
	if !errors.Is(err, dto.ErrBranchHeadConflict) {
		t.Fatalf("wrong branch accepted: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "queries", "wrong")); !os.IsNotExist(err) {
		t.Fatal("branch denial changed files")
	}
	_, err = CreateProjectQueryFolder(ctx, QueryFolderCreate{ProjectRef: ref, Branch: branch, Path: "~/../outside", Name: "wrong"})
	if err == nil {
		t.Fatal("invalid path accepted")
	}
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: false})
	if _, err = CreateProjectQueryFolder(ctx, QueryFolderCreate{ProjectRef: ref, Branch: branch, Path: "~", Name: "denied"}); !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatal("read-only agent created folder")
	}
}
