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
