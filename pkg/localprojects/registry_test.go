package localprojects

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestCreateArchiveRestoreSurvivesRestartAndPreservesNativeProject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	registry, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := Create{Store: "local", ID: "music", Title: "My music", OperationID: "create-music"}
	created, err := registry.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "music" || created.Archived || created.Access != "private" {
		t.Fatalf("bad project: %+v", created)
	}
	native, err := filestore.LoadProjectFile(filepath.Join(root, "music"))
	if err != nil || native.Title != request.Title {
		t.Fatalf("native project cannot load: %+v %v", native, err)
	}
	envs, err := filestore.NewProjectStore(request.ID, filepath.Join(root, request.ID)).LoadEnvironments(ctx)
	if err != nil || len(envs) != 1 || envs[0].ID != "dev" {
		t.Fatalf("default environment unavailable: %+v %v", envs, err)
	}
	if err = os.WriteFile(filepath.Join(root, "music", "evidence.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err = Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := registry.Create(ctx, request)
	if err != nil || replay.Created.At != created.Created.At {
		t.Fatalf("create replay: %+v %v", replay, err)
	}
	archive := Archive{Storage: "local", Project: "music", Archived: true, OperationID: "archive-1"}
	ack, err := registry.SetArchived(ctx, archive)
	if err != nil || !ack.Project.Archived {
		t.Fatalf("archive: %+v %v", ack, err)
	}
	encoded, err := json.Marshal(ack)
	if err != nil || !strings.Contains(string(encoded), `"archived":true`) || strings.Contains(string(encoded), "localCreation") {
		t.Fatalf("bad wire: %s %v", encoded, err)
	}
	registry, err = Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := registry.List()
	if err != nil || len(projects) != 1 || !projects[0].Archived {
		t.Fatalf("archive did not persist: %+v %v", projects, err)
	}
	restore := archive
	restore.Archived = false
	restore.OperationID = "restore-1"
	if _, err = registry.SetArchived(ctx, restore); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.SetArchived(ctx, archive); err != nil {
		t.Fatal(err)
	} // Replay must not re-archive after restore.
	projects, err = registry.List()
	if err != nil || projects[0].Archived {
		t.Fatalf("old command changed restored state: %+v %v", projects, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "music", "evidence.txt"))
	if err != nil || string(body) != "keep" {
		t.Fatalf("archive changed content: %q %v", body, err)
	}
	if _, err = filestore.LoadProjectFile(filepath.Join(root, "music")); err != nil {
		t.Fatal(err)
	}
}

func TestRootBoundCreateRejectsPathsCollisionsAndChangedRetries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	r, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := Create{Store: "local", ID: "one", Title: "One", OperationID: "create-one"}
	for _, id := range []string{"../outside", "/tmp/outside", "a/b", "A", "a\\b", "..", "a ", "a;touch"} {
		bad := valid
		bad.ID = id
		if _, err = r.Create(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", id, err)
		}
	}
	if _, err = r.Create(ctx, valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Create){func(v *Create) { v.Title = "Changed" }, func(v *Create) { v.ID = "two" }, func(v *Create) { v.OperationID = "another" }} {
		bad := valid
		change(&bad)
		if _, err = r.Create(ctx, bad); !errors.Is(err, ErrConflict) {
			t.Fatalf("retry conflict accepted: %+v %v", bad, err)
		}
	}
	outside := t.TempDir()
	if runtime.GOOS != "windows" {
		if err = os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
		bad := valid
		bad.ID = "escape"
		bad.OperationID = "escape"
		if _, err = r.Create(ctx, bad); !errors.Is(err, ErrConflict) {
			t.Fatalf("symlink accepted: %v", err)
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatalf("outside modified: %v %v", entries, err)
		}
	}
	legacy, err := Open("", r.Paths())
	if err != nil {
		t.Fatal(err)
	}
	if legacy.CanCreate() {
		t.Fatal("creation allowed without root")
	}
	if _, err = legacy.Create(ctx, valid); !errors.Is(err, ErrRootRequired) {
		t.Fatal(err)
	}
}

func TestConcurrentCreateAndStateReceiptConflicts(t *testing.T) {
	r, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := Create{Store: "local", ID: "p", Title: "Concurrent", OperationID: "one"}
	var wg sync.WaitGroup
	fail := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := r.Create(context.Background(), request); fail <- e }()
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	list, err := r.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("duplicate project: %v %v", list, err)
	}
	a := Archive{Storage: "local", Project: "p", Archived: true, OperationID: "state"}
	if _, err = r.SetArchived(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	a.Archived = false
	if _, err = r.SetArchived(context.Background(), a); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	request.ID = "not-created"
	request.OperationID = "cancel"
	if _, err = r.Create(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRegistryRefusesTamperedStateAndRevokedWriter(t *testing.T) {
	root := t.TempDir()
	r, err := Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := Create{Store: "local", ID: "p", Title: "Private", OperationID: "create"}
	denied := errors.New("revoked")
	if _, err = r.Create(context.Background(), request, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if len(r.Paths()) != 0 {
		t.Fatal("revoked writer registered project")
	}
	if _, err = r.Create(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	a := Archive{Storage: "local", Project: "p", Archived: true, OperationID: "archive"}
	if _, err = r.SetArchived(context.Background(), a, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "p", stateName), []byte(`{"version":2,"archived":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.List(); !errors.Is(err, ErrConflict) {
		t.Fatalf("tampered state accepted: %v", err)
	}
}

func TestRegistryLegacyMinimalProjectAndWirePrivacy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "datatug-project.json")
	original := []byte(`{"title":"Legacy project","credentials":{"password":"private-project-password"},"databases":[{"id":"db","password":"private-db-password"}],"custom":{"token":"private-token"}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := Open("", map[string]string{"legacy-alias": root})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := registry.List()
	if err != nil || len(projects) != 1 || projects[0].ID != "legacy-alias" {
		t.Fatalf("minimal registered legacy project stopped listing: %+v %v", projects, err)
	}
	encoded, err := json.Marshal(projects)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"credentials", "private-project-password", "private-db-password", "private-token", "custom", "databases"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("registry disclosed opaque native metadata: %s", encoded)
		}
	}
	if _, err = registry.SetArchived(context.Background(), Archive{Storage: "local", Project: "legacy-alias", Archived: true, OperationID: "archive-legacy"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("archive rewrote legacy project metadata: %s %v", after, err)
	}
}
