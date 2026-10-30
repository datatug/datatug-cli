package sessionstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestPrivateOVDBSessionRestartConflictReplay(t *testing.T) {
	t.Parallel()
	root, project := t.TempDir(), t.TempDir()
	scope := sessioncontext.Scope{EnvironmentID: "uat", IdentityDomain: "uat-orders"}
	allow := true
	open := func() (*Manager, *sessioncontext.Service, sessioncontext.Principal) {
		m, err := New(root)
		if err != nil {
			t.Fatal(err)
		}
		r, err := m.Repository("p", project)
		if err != nil {
			t.Fatal(err)
		}
		// Second call to m.Repository exercises the cached mount fast-path.
		if _, err := m.Repository("p", project); err != nil {
			t.Fatal(err)
		}
		s, err := sessioncontext.NewService(r, func(_ context.Context, a sessioncontext.Access) error {
			if !allow || a.Scope != nil && *a.Scope != scope {
				return sessioncontext.ErrForbidden
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return m, s, sessioncontext.Principal{Actor: "alice", DatabaseID: r.DatabaseID(), ProjectID: "p"}
	}
	m, s, p := open()
	ctx := context.Background()
	create := sessioncontext.Command{OpID: "create", Action: "createSession", SessionID: "s1", BagID: "b1", DatabaseID: p.DatabaseID, ProjectID: "p", Title: "private-title", Scope: &scope}
	created, err := s.Execute(ctx, p, create)
	if err != nil {
		t.Fatal(err)
	}
	second := create
	second.OpID, second.SessionID, second.BagID = "create2", "s2", "b2"
	if _, err = s.Execute(ctx, p, second); err != nil {
		t.Fatal(err)
	}
	value := apicontract.NewIntegerValue("9007199254740993")
	put := sessioncontext.Command{OpID: "put", Action: "putEntry", SessionID: "s1", ExpectedVersion: 1, BagID: "b1", ExpectedBagVersion: 1, EntryID: "e1", RevisionID: "r1", Subject: &sessioncontext.Subject{ConceptID: "orders", FieldID: "order-id", FieldMappingRef: "mapping"}, Value: &sessioncontext.ValueEnvelope{SchemaVersion: 1, Kind: "scalar", Scalar: &value}}
	if _, err = s.Execute(ctx, p, put); err != nil {
		t.Fatal(err)
	}
	stale := put
	stale.OpID, stale.RevisionID = "stale", "r2"
	if _, err = s.Execute(ctx, p, stale); !errors.Is(err, sessioncontext.ErrConflict) {
		t.Fatalf("stale writer: %v", err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, s, p = open()
	defer func() { _ = m.Close() }()
	page, err := s.ListSessions(ctx, p, "", 100)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("reopened sessions: %+v %v", page, err)
	}
	snapshot, err := s.GetSnapshot(ctx, p, "s1")
	if err != nil || len(snapshot.Revisions) != 1 || snapshot.Revisions[0].Value.Scalar.Str != value.Str {
		t.Fatalf("retained lossless value: %+v %v", snapshot, err)
	}
	replayed, err := s.Execute(ctx, p, create)
	if err != nil || !reflect.DeepEqual(replayed.Receipt, created.Receipt) {
		t.Fatalf("lost acknowledgement: %+v %v", replayed, err)
	}
	encoded, _ := json.Marshal(replayed)
	if strings.Contains(string(encoded), "private-title") || strings.Contains(string(encoded), value.Str) {
		t.Fatalf("private data in receipt: %s", encoded)
	}
	other := p
	other.Actor = "bob"
	if _, err = s.GetSnapshot(ctx, other, "s1"); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatalf("owner read: %v", err)
	}
	allow = false
	if _, err = s.Execute(ctx, p, create); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatalf("revoked replay: %v", err)
	}
	if _, err = s.ReadOperation(ctx, p, "create"); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatalf("revoked receipt: %v", err)
	}
}

func TestPrivateOVDBRejectsGitAndSymlinkTargets(t *testing.T) {
	t.Parallel()
	git := t.TempDir()
	if err := os.Mkdir(filepath.Join(git, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(git, "private")); err == nil {
		t.Fatal("accepted private context in repository")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(git, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(link, "private")); err == nil {
		t.Fatal("accepted symlink into repository")
	}
	project := t.TempDir()
	projectLink := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(project, projectLink); err != nil {
		t.Fatal(err)
	}
	m, err := New(filepath.Join(projectLink, "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err = m.Repository("p", project); err == nil {
		t.Fatal("accepted symlinked private storage inside served project")
	}
}

func TestSessionStoreErrorBranches(t *testing.T) {
	t.Parallel()
	// Empty root rejected.
	if _, err := ResolvePrivateDirectory(""); !errors.Is(err, sessioncontext.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty root, got %v", err)
	}

	// Ancestor is a regular file -> os.Lstat(ancestor) returns ENOTDIR (non-ErrNotExist).
	tmp := t.TempDir()
	regFile := filepath.Join(tmp, "regular-file")
	if err := os.WriteFile(regFile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePrivateDirectory(filepath.Join(regFile, "child")); err == nil {
		t.Fatal("expected error when ancestor is a regular file")
	}

	// Path itself is a regular file -> os.Lstat(current/.git) returns ENOTDIR.
	if _, err := ResolvePrivateDirectory(regFile); err == nil {
		t.Fatal("expected error when path is a regular file")
	}

	// Dangling symlink -> os.Lstat succeeds, EvalSymlinks fails.
	dangling := filepath.Join(tmp, "dangling")
	if err := os.Symlink(filepath.Join(tmp, "does-not-exist"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePrivateDirectory(dangling); err == nil {
		t.Fatal("expected error for dangling symlink")
	}

	// Symlink to valid directory -> rejected by info.Mode()&os.ModeSymlink != 0.
	validDir := filepath.Join(tmp, "valid-dir")
	if err := os.MkdirAll(validDir, 0700); err != nil {
		t.Fatal(err)
	}
	dirSymlink := filepath.Join(tmp, "dir-symlink")
	if err := os.Symlink(validDir, dirSymlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePrivateDirectory(dirSymlink); !errors.Is(err, sessioncontext.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for symlink directory, got %v", err)
	}

	// secureFile error branches: non-ErrExist and directory target.
	if err := secureFile(filepath.Join(tmp, "no-such-dir", "file")); err == nil {
		t.Fatal("expected error from secureFile when parent dir does not exist")
	}
	if err := secureFile(validDir); !errors.Is(err, sessioncontext.ErrInvalid) {
		t.Fatalf("expected ErrInvalid when secureFile target is directory, got %v", err)
	}

	// Repository with nonexistent projectDir.
	root := filepath.Join(tmp, "store-root")
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.Repository("p", filepath.Join(tmp, "missing-project")); err == nil {
		t.Fatal("expected error for missing projectDir")
	}

	// Compute session dir id for validDir.
	projectDir := validDir
	resolvedProject, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-create sessions.sqlite as a directory so openSecuredSQLite fails.
	m2Root := filepath.Join(tmp, "store-root-2")
	m2, err := New(m2Root)
	if err != nil {
		t.Fatal(err)
	}
	// Trigger privatePath(dir) failure by placing .git in m2Root.
	if err := os.MkdirAll(filepath.Join(m2Root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Repository("p", resolvedProject); err == nil {
		t.Fatal("expected error when .git is added to manager root")
	}
	if err := os.RemoveAll(filepath.Join(m2Root, ".git")); err != nil {
		t.Fatal(err)
	}

	// Trigger database secureFile failure by pre-creating sessions.sqlite as a directory.
	// First open once on a throwaway manager to learn the session id directory name.
	probeRoot := filepath.Join(tmp, "probe-root")
	probeMgr, err := New(probeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probeMgr.Repository("p", resolvedProject); err != nil {
		t.Fatal(err)
	}
	_ = probeMgr.Close()
	entries, err := os.ReadDir(probeRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 session dir, got %v %v", entries, err)
	}
	sessionID := entries[0].Name()

	// Pre-create sessions.sqlite as a directory.
	dbAsDir := filepath.Join(m2Root, sessionID, "sessions.sqlite")
	if err := os.MkdirAll(dbAsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Repository("p", resolvedProject); !errors.Is(err, sessioncontext.ErrInvalid) {
		t.Fatalf("expected ErrInvalid when sessions.sqlite is a directory, got %v", err)
	}
	if err := os.RemoveAll(dbAsDir); err != nil {
		t.Fatal(err)
	}

	// Pre-create sessions.sqlite with invalid non-SQLite bytes so db.Exec fails.
	if err := os.WriteFile(dbAsDir, []byte("not a valid sqlite database header"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Repository("p", resolvedProject); err == nil {
		t.Fatal("expected error when sessions.sqlite is corrupted")
	}
	if err := os.Remove(dbAsDir); err != nil {
		t.Fatal(err)
	}

	// Pre-create session-context.ovdb.yaml as a directory so secureFile(manifestPath) fails.
	manifestAsDir := filepath.Join(m2Root, sessionID, "session-context.ovdb.yaml")
	if err := os.MkdirAll(manifestAsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Repository("p", resolvedProject); !errors.Is(err, sessioncontext.ErrInvalid) {
		t.Fatalf("expected ErrInvalid when manifestPath is a directory, got %v", err)
	}
}
