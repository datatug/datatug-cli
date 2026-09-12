package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/strongo/validation"
)

// The legacy queries/create_query, update_query and delete_query routes
// used to be gated only by the process-wide --allow-writes flag: with it
// set, any serving principal - a read-only one included - could write or
// delete any query file, and an id such as "../x" addressed a file outside
// the project's queries/ tree. These tests pin the fix: the same
// project-write authorization queries/capture uses, plus location and
// content validation, all before the store is touched.

// servedWritableProject registers dir as projectID under a session built
// from opts, with the given --allow-writes setting, and returns the
// project's queries/ directory.
func servedWritableProject(t *testing.T, opts secureread.SessionOptions, allowWrites bool) (projectID, queriesDir string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	queriesDir = filepath.Join(dir, storage.QueriesFolder)
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// filestore.SetProjectPath panics on a reused project ID, so derive a
	// unique one from the (sub)test name.
	projectID = "legacy-write-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	filestore.SetProjectPath(projectID, dir)
	pathsByID := map[string]string{projectID: dir}
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	configureWriteSession(t, opts, pathsByID, Capabilities{AllowWrites: allowWrites})
	return projectID, queriesDir
}

func adminSession(t *testing.T) secureread.SessionOptions {
	return secureread.SessionOptions{As: "alice", Roles: []string{"admin"}, PoliciesDir: writePoliciesDir(t)}
}

func supportSession(t *testing.T) secureread.SessionOptions {
	return secureread.SessionOptions{As: "sam", Roles: []string{"support"}, PoliciesDir: writePoliciesDir(t)}
}

func legacyQuery(folderPath, id string) datatug.QueryDefWithFolderPath {
	return datatug.QueryDefWithFolderPath{
		FolderPath: folderPath,
		QueryDef: datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id, Title: id}},
			Type:        datatug.QueryTypeSQL,
		},
	}
}

func createRequest(projectID string, q datatug.QueryDefWithFolderPath) dto.CreateQuery {
	return dto.CreateQuery{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, Query: q}
}

func updateRequest(projectID, id string, q datatug.QueryDefWithFolderPath) dto.UpdateQuery {
	return dto.UpdateQuery{
		ProjectItemRef: dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: id},
		Query:          q,
	}
}

func deleteRef(projectID, id string) dto.ProjectItemRef {
	return dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: id}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists (stat err %v); nothing should have been written", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s changed:\n got %s\nwant %s", path, got, want)
	}
}

func TestCreateQuery_Authorization(t *testing.T) {
	t.Run("refused without --allow-writes", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, secureread.SessionOptions{NoPolicies: true}, false)
		_, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("~", "q1")))
		if !errors.Is(err, secureread.ErrAccessDenied) {
			t.Fatalf("expected an access denial, got %v", err)
		}
		assertNotExists(t, filepath.Join(queriesDir, "q1.query.json"))
	})
	t.Run("a read-only principal is refused", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, supportSession(t), true)
		_, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("~", "q1")))
		if !errors.Is(err, secureread.ErrAccessDenied) {
			t.Fatalf("expected an access denial, got %v", err)
		}
		assertNotExists(t, filepath.Join(queriesDir, "q1.query.json"))
	})
	t.Run("an authorized principal writes", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		if _, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("~", "q1"))); err != nil {
			t.Fatalf("CreateQuery: %v", err)
		}
		if _, err := os.Stat(filepath.Join(queriesDir, "q1.query.json")); err != nil {
			t.Errorf("expected the query file to be written: %v", err)
		}
	})
	t.Run("the unrestricted local owner writes with --allow-writes", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, secureread.SessionOptions{NoPolicies: true}, true)
		if _, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("~", "q1"))); err != nil {
			t.Fatalf("CreateQuery: %v", err)
		}
		if _, err := os.Stat(filepath.Join(queriesDir, "q1.query.json")); err != nil {
			t.Errorf("expected the query file to be written: %v", err)
		}
	})
}

func TestCreateQuery_RejectsUnsafeLocationsAndContent(t *testing.T) {
	credentialQuery := legacyQuery("~", "q1")
	credentialQuery.Targets = []datatug.QueryDefTarget{{Driver: "sqlite3", Credentials: datatug.Credentials{Password: "hunter2"}}}
	tests := []struct {
		name  string
		query datatug.QueryDefWithFolderPath
	}{
		{name: "parent-directory id", query: legacyQuery("~", "../escape")},
		{name: "id with a separator", query: legacyQuery("~", "a/b")},
		{name: "dot-dot id", query: legacyQuery("~", "..")},
		{name: "id with a backslash", query: legacyQuery("~", `..\escape`)},
		{name: "id with NUL", query: legacyQuery("~", "a\x00b")},
		{name: "traversing folder", query: legacyQuery("../outside", "q1")},
		{name: "absolute folder", query: legacyQuery("/etc", "q1")},
		{name: "credentials in a target", query: credentialQuery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
			_, err := CreateQuery(context.Background(), createRequest(projectID, tt.query))
			if !validation.IsBadRequestError(err) {
				t.Fatalf("expected a bad-request error, got %v", err)
			}
			entries, readErr := os.ReadDir(queriesDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Errorf("queries/ holds %d entries; nothing should have been written", len(entries))
			}
			assertNotExists(t, filepath.Join(filepath.Dir(queriesDir), "escape.query.json"))
			assertNotExists(t, filepath.Join(filepath.Dir(filepath.Dir(queriesDir)), "escape.query.json"))
		})
	}
}

func TestUpdateQuery_Authorization(t *testing.T) {
	const existing = `{"id":"q1","title":"q1","type":"SQL"}`
	t.Run("a read-only principal is refused", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, supportSession(t), true)
		path := filepath.Join(queriesDir, "q1.query.json")
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
		changed := legacyQuery("~", "q1")
		changed.Title = "changed"
		_, err := UpdateQuery(context.Background(), updateRequest(projectID, "q1", changed))
		if !errors.Is(err, secureread.ErrAccessDenied) {
			t.Fatalf("expected an access denial, got %v", err)
		}
		assertFileContent(t, path, existing)
	})
	t.Run("an unsafe id is rejected", func(t *testing.T) {
		projectID, _ := servedWritableProject(t, adminSession(t), true)
		_, err := UpdateQuery(context.Background(), updateRequest(projectID, "q1", legacyQuery("~", "../escape")))
		if !validation.IsBadRequestError(err) {
			t.Fatalf("expected a bad-request error, got %v", err)
		}
	})
	t.Run("an authorized principal updates", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		path := filepath.Join(queriesDir, "q1.query.json")
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
		changed := legacyQuery("~", "q1")
		changed.Title = "changed"
		if _, err := UpdateQuery(context.Background(), updateRequest(projectID, "q1", changed)); err != nil {
			t.Fatalf("UpdateQuery: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) == existing {
			t.Errorf("expected the query file to change")
		}
	})
}

func TestDeleteQuery_Authorization(t *testing.T) {
	const existing = `{"id":"q1","title":"q1","type":"SQL"}`
	t.Run("a read-only principal is refused", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, supportSession(t), true)
		path := filepath.Join(queriesDir, "q1.query.json")
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
		err := DeleteQuery(context.Background(), deleteRef(projectID, "q1"))
		if !errors.Is(err, secureread.ErrAccessDenied) {
			t.Fatalf("expected an access denial, got %v", err)
		}
		assertFileContent(t, path, existing)
	})
	t.Run("a traversing id is rejected and the target survives", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		outside := filepath.Join(filepath.Dir(queriesDir), "outside.query.json")
		if err := os.WriteFile(outside, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
		err := DeleteQuery(context.Background(), deleteRef(projectID, "../outside"))
		if !validation.IsBadRequestError(err) {
			t.Fatalf("expected a bad-request error, got %v", err)
		}
		assertFileContent(t, outside, existing)
	})
	t.Run("an authorized principal deletes", func(t *testing.T) {
		projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
		path := filepath.Join(queriesDir, "q1.query.json")
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := DeleteQuery(context.Background(), deleteRef(projectID, "q1")); err != nil {
			t.Fatalf("DeleteQuery: %v", err)
		}
		assertNotExists(t, path)
	})
}
