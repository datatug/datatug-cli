package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestAcceptedSessionResolverKeepsCallerAndPhysicalMountAdmission(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	paths := map[string]string{"p": project}
	originalRoot := sessionResolvePrivateDir
	originalDir, originalEval, originalNew := sessionContextProjectDir, filepathEvalSymlinks, sessionstoreNew
	defer func() {
		sessionResolvePrivateDir = originalRoot
		sessionContextProjectDir = originalDir
		filepathEvalSymlinks = originalEval
		sessionstoreNew = originalNew
		_ = CloseSessionContext()
		ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	}()
	configure := func(actor string, writes, unrestricted bool, root string) {
		ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: actor}, Unrestricted: unrestricted}, paths, Capabilities{AllowWrites: writes, EvidencePrivateDir: root})
	}
	ctx := context.Background()
	configure("alice", true, true, private)
	if _, _, _, err := ResolveAcceptedSessionContext(ctx, "foreign", "p"); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("foreign source accepted", err)
	}
	configure("", true, true, private)
	if _, _, _, err := ResolveAcceptedSessionContext(ctx, "local", "p"); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("unauthenticated actor accepted", err)
	}
	configure("alice", true, true, private)
	sessionContextProjectDir = func(string) (string, bool) { return "", false }
	if _, _, _, err := ResolveAcceptedSessionContext(ctx, "local", "p"); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("withdrawn source accepted", err)
	}
	sessionContextProjectDir = originalDir
	failed := errors.New("path admission fault")
	sessionResolvePrivateDir = func(string) (string, error) { return "", failed }
	if _, _, _, err := ResolveAcceptedSessionContext(ctx, "local", "p"); !errors.Is(err, sessioncontext.ErrUnavailable) {
		t.Fatal("private root resolver failure accepted", err)
	}
	sessionResolvePrivateDir = originalRoot
	filepathEvalSymlinks = func(string) (string, error) { return "", failed }
	if _, _, _, err := ResolveAcceptedSessionContext(ctx, "local", "p"); !errors.Is(err, sessioncontext.ErrUnavailable) {
		t.Fatal("physical resolver failure accepted", err)
	}
	filepathEvalSymlinks = originalEval
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	configure("alice", true, true, file)
	if _, _, _, err := ResolveAcceptedSessionContext(ctx, "local", "p"); !errors.Is(err, sessioncontext.ErrUnavailable) {
		t.Fatal("invalid private root accepted", err)
	}
	configure("alice", true, true, private)
	service, p, repo, err := ResolveAcceptedSessionContext(ctx, "local", "p")
	if err != nil {
		t.Fatal(err)
	}
	n := repo.NativeNamespace()
	id := sessioncontext.DetectiveIdentity{Namespace: n, Actor: p.Actor}
	if err = AcceptedSessionActorGate(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	other := id
	other.Actor = "bob"
	if err = AcceptedSessionActorGate(ctx, p, other); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal(err)
	}
	command := sessioncontext.Command{OpID: "create", Action: "createSession", SessionID: "session", BagID: "bag", DatabaseID: p.DatabaseID, ProjectID: p.ProjectID, Title: "Private", Scope: &sessioncontext.Scope{EnvironmentID: "qa", IdentityDomain: "tenant"}}
	configure("alice", false, true, private)
	if _, err = service.Execute(ctx, p, command); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("write gate bypassed", err)
	}
	configure("alice", true, true, private)
	sessionContextProjectDir = func(string) (string, bool) { return "", false }
	if _, err = service.Execute(ctx, p, command); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("withdrawn mount reused", err)
	}
	sessionContextProjectDir = originalDir
	filepathEvalSymlinks = func(string) (string, error) { return "", failed }
	if _, err = service.Execute(ctx, p, command); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("physical mount fault reused", err)
	}
	filepathEvalSymlinks = originalEval
	sessionContextProjectDir = func(string) (string, bool) { return t.TempDir(), true }
	if _, err = service.Execute(ctx, p, command); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("foreign physical mount reused", err)
	}
	sessionContextProjectDir = originalDir
	if _, err = service.Execute(ctx, p, command); err != nil {
		t.Fatal("valid owner rejected", err)
	}
	configure("alice", true, true, t.TempDir())
	if _, _, _, err = ResolveAcceptedSessionContext(ctx, "local", "p"); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("cached private authority retargeted", err)
	}
	if err = CloseSessionContext(); err != nil {
		t.Fatal(err)
	}
	configure("alice", true, true, private)
	sessionstoreNew = func(string) (*sessionstore.Manager, error) { return nil, nil }
	if _, _, _, err = ResolveAcceptedSessionContext(ctx, "local", "p"); !errors.Is(err, sessioncontext.ErrUnavailable) {
		t.Fatal("nil private authority default allowed", err)
	}
}
