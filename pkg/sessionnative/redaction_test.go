package sessionnative

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/privateadmission"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

type redactionFixture struct {
	worker          *PinRedactionWorker
	repo            *sessionstore.NativeRepository
	native          *detective.LocalStore
	principal       sessioncontext.Principal
	ids             []sessioncontext.DetectiveIdentity
	privateDir      string
	calls           int
	content, active bool
}

func newRedactionFixture(t *testing.T) *redactionFixture {
	t.Helper()
	ctx := context.Background()
	project, private := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(project, "queries"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "queries", "q.query.json"), []byte(`{"createdAt":"2026-01-01T00:00:00Z","id":"q","type":"SQL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	native, err := detective.NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = native.SaveCheck(ctx, detective.Check{ID: "check", Title: "Private Check", Inputs: []detective.Input{{Name: "id", Type: apicontract.ValueTypeInteger, Cardinality: "scalar", Required: true}}, Steps: []detective.Step{{ID: "query", Kind: "query", QueryID: "q", Bindings: []detective.Binding{{Input: "id", FromInput: "id"}}}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err = native.SaveCase(ctx, detective.Case{ID: "case", Title: "Retained Case"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.New(filepath.Join(private, "session-context"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	repo, err := manager.NativeRepository("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	f := &redactionFixture{repo: repo, native: native, content: true, active: true}
	f.principal = sessioncontext.Principal{Actor: "alice", DatabaseID: repo.DatabaseID(), ProjectID: "p", Executor: true}
	f.worker, err = NewPinRedactionWorker(repo, native, func(context.Context, sessioncontext.Access) error {
		if !f.content {
			return sessioncontext.ErrForbidden
		}
		return nil
	}, func(_ context.Context, p sessioncontext.Principal) error {
		if !f.active || p.Actor != f.principal.Actor || p.DatabaseID != f.principal.DatabaseID || p.ProjectID != f.principal.ProjectID {
			return sessioncontext.ErrForbidden
		}
		return nil
	}, func(_ context.Context, p sessioncontext.Principal, id sessioncontext.DetectiveIdentity) error {
		if !f.active || id.Actor != p.Actor || id.Namespace != repo.NativeNamespace() {
			return sessioncontext.ErrForbidden
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = native.ConfigureNativeFence(func(ctx context.Context, token sessioncontext.DetectiveFence) error {
		return f.worker.owner.CheckFence(ctx, f.principal, token)
	}); err != nil {
		t.Fatal(err)
	}
	scope := sessioncontext.Scope{EnvironmentID: "qa", IdentityDomain: "tenant"}
	if _, err = f.worker.service.Execute(ctx, f.principal, sessioncontext.Command{OpID: "create", Action: "createSession", SessionID: "session", BagID: "bag", DatabaseID: f.principal.DatabaseID, ProjectID: "p", Title: "Private session", Scope: &scope}); err != nil {
		t.Fatal(err)
	}
	n := repo.NativeNamespace()
	for _, suffix := range []string{"one", "two"} {
		snapshot, err := f.worker.service.GetSnapshot(ctx, f.principal, "session")
		if err != nil {
			t.Fatal(err)
		}
		value := apicontract.NewIntegerValue("9007199254740993")
		ref := apicontract.SessionInputReference{SchemaVersion: 1, StorageID: n.StorageID, DatabaseID: n.DatabaseID, ProjectID: n.ProjectID, NamespaceID: n.NamespaceID, SessionID: "session", InputSnapshotID: "input-" + suffix, ContextSetID: snapshot.ContextSet.ID, EnvironmentID: scope.EnvironmentID, IdentityDomain: scope.IdentityDomain}
		capture, err := native.CaptureNativeInput(ctx, detective.NativeCaptureRequest{CheckID: "check", CaseID: "case", NativeOperationID: "native-" + suffix, Actor: "alice", Inputs: map[string]core.InputValue{"id": {Scalar: &value}}, Reference: ref}, func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
			return sessioncontext.DetectiveAcceptedQueryPin{QueryID: "q", Revision: "revision", DefinitionJSON: json.RawMessage(`{"createdAt":"2026-01-01T00:00:00Z","id":"q","title":"Private query","type":"SQL","text":"select private_pin_text"}`)}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.worker.service.PinDetectiveInput(ctx, f.principal, sessioncontext.PinDetectiveInputCommand{ClientRequestDigest: strings.Repeat("a", 64), OpID: "pin-" + suffix, SessionID: "session", RunID: "run-" + suffix, InputSnapshotID: ref.InputSnapshotID, ExpectedVersion: snapshot.Session.Version, ExpectedContextSetID: snapshot.ContextSet.ID, Scope: scope, Pin: capture.Pin, Case: capture.Case, NativeBinding: capture.Binding}); err != nil {
			t.Fatal(err)
		}
		accepted, err := f.worker.owner.ResolveOperation(ctx, f.principal, "pin-"+suffix)
		if err != nil {
			t.Fatal(err)
		}
		token, pin, first, err := f.worker.owner.ClaimDispatchOnce(ctx, f.principal, accepted.Identity)
		if err != nil || !first {
			t.Fatal(err)
		}
		if _, err = native.ExecuteNativePin(ctx, token, pin, first, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
			f.calls++
			return apicontract.Result{}, nil
		}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = f.worker.owner.Reconcile(ctx, f.principal, accepted.Identity); err != nil {
			t.Fatal(err)
		}
		handoff, version, err := f.worker.owner.ReadHandoff(ctx, f.principal, accepted.Identity)
		if err != nil {
			t.Fatal(err)
		}
		receiptID, err := native.DeliverNativeHandoff(ctx, accepted.Identity, handoff, pin.Reference, func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if err = f.worker.owner.SettleHandoff(ctx, f.principal, accepted.Identity, version, true, receiptID); err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, accepted.Identity)
	}
	path, err := privateadmission.ProjectPath(project, private)
	if err != nil {
		t.Fatal(err)
	}
	f.privateDir = filepath.Dir(path)
	return f
}

func TestBoundedLocalPinRedactionPreservesCaseEvidenceAndRecovery(t *testing.T) {
	f := newRedactionFixture(t)
	ctx := context.Background()
	for _, id := range f.ids {
		data, err := os.ReadFile(filepath.Join(f.privateDir, "action-operations", id.NativeOperationID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, privateValue := range []string{"private_pin_text", "9007199254740993", "effectiveInputs", "definitionJson"} {
			if strings.Contains(string(data), privateValue) {
				t.Fatal("full private pin copied into native journal", privateValue)
			}
		}
	}
	page, err := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", PinRedactionContinuation{}, 1)
	if err != nil || page.Generation != 1 || page.Phase != "pins" || page.Cursor == "" {
		t.Fatal("bounded first page", page, err)
	}
	stale := page
	stale.Generation++
	if _, err = f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", stale, 1); !errors.Is(err, sessioncontext.ErrConflict) {
		t.Fatal("stale generation resumed", err)
	}
	if _, err = f.worker.RedactPage(ctx, f.principal, "pin-one", "other-delete", page, 1); !errors.Is(err, sessioncontext.ErrConflict) {
		t.Fatal("different deletion identity resumed", err)
	}
	f.content = false // continuation needs active accepted actor, not revoked content.
	if _, err = f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", page, 1); err != nil {
		t.Fatal("permanent marker continuation failed after content revocation", err)
	}
	for rounds := 0; page.Phase != "done" && rounds < 10; rounds++ {
		page, err = f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", page, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	if page.Phase != "done" || f.calls != 2 {
		t.Fatal("scrub incomplete or repeated source", page, f.calls)
	}
	if replay, err := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", page, 1); err != nil || replay != page {
		t.Fatal("completed continuation not idempotent", replay, err)
	}
	if _, err = f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", PinRedactionContinuation{Phase: "references", Generation: page.Generation}, 100); err != nil {
		t.Fatal("reference replay failed", err)
	}
	if err = f.repo.View(ctx, func(reader sessioncontext.Reader) error {
		for _, suffix := range []string{"one", "two"} {
			var input sessioncontext.InputSnapshot
			if _, err := reader.Load(ctx, sessioncontext.Inputs, "input-"+suffix, &input); err != nil {
				return err
			}
			if input.Detective != nil || input.ParametersOrigin != "redacted" {
				t.Fatal("private executable pin retained")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(f.privateDir, "session-evidence-handoffs"))
	if err != nil || len(entries) != 2 {
		t.Fatal("session reference receipts lost", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(f.privateDir, "session-evidence-handoffs", entry.Name()))
		if err != nil || !strings.Contains(string(data), `"availability": "unavailable"`) {
			t.Fatal("reference not marked unavailable", err)
		}
	}
	links, err := os.ReadDir(filepath.Join(f.privateDir, "case-run-links"))
	if err != nil || len(links) != 2 {
		t.Fatal("separately owned Case evidence erased", err)
	}
	for _, id := range f.ids {
		accepted, err := f.worker.owner.ResolveOperation(ctx, f.principal, "pin-"+strings.TrimPrefix(id.RunID, "run-"))
		if err != nil || accepted.Identity != id {
			t.Fatal("sanitized recovery mapping lost", err)
		}
		if _, err = f.worker.owner.ReadStatus(ctx, f.principal, id); err != nil {
			t.Fatal(err)
		}
	}
	f.active = false
	if _, err = f.worker.RedactPage(ctx, f.principal, "missing", "delete", page, 1); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("inactive caller probed missing operations", err)
	}
}

func TestRedactionWorkerConstructorFailsClosed(t *testing.T) {
	f := newRedactionFixture(t)
	authorize := func(context.Context, sessioncontext.Access) error { return nil }
	active := func(context.Context, sessioncontext.Principal) error { return nil }
	accepted := func(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) error { return nil }
	for _, build := range []func() error{
		func() error { _, e := NewPinRedactionWorker(nil, f.native, authorize, active, accepted); return e },
		func() error {
			_, e := NewPinRedactionWorker(&sessionstore.NativeRepository{}, f.native, authorize, active, accepted)
			return e
		},
		func() error { _, e := NewPinRedactionWorker(f.repo, nil, authorize, active, accepted); return e },
		func() error { _, e := NewPinRedactionWorker(f.repo, f.native, nil, active, accepted); return e },
		func() error { _, e := NewPinRedactionWorker(f.repo, f.native, authorize, nil, accepted); return e },
		func() error { _, e := NewPinRedactionWorker(f.repo, f.native, authorize, active, nil); return e },
	} {
		if !errors.Is(build(), sessioncontext.ErrUnavailable) {
			t.Fatal("missing gate accepted")
		}
	}
	sentinel := errors.New("factory unavailable")
	oldService, oldOwner := newPinService, newNativeOwner
	t.Cleanup(func() { newPinService, newNativeOwner = oldService, oldOwner })
	newPinService = func(sessioncontext.Repository, sessioncontext.Authorizer) (*sessioncontext.Service, error) {
		return nil, sentinel
	}
	if _, e := NewPinRedactionWorker(f.repo, f.native, authorize, active, accepted); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	newPinService = oldService
	newNativeOwner = func(*sessioncontext.Service, func(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) error, func(context.Context, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error)) (*sessioncontext.DetectiveOwner, error) {
		return nil, sentinel
	}
	if _, e := NewPinRedactionWorker(f.repo, f.native, authorize, active, accepted); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
}

func TestRedactionContinuationRejectsInvalidAndMissingAuthority(t *testing.T) {
	f := newRedactionFixture(t)
	ctx := context.Background()
	for _, page := range []PinRedactionContinuation{{Generation: -1}, {Cursor: "unexpected"}, {Generation: 1}, {Phase: "invented"}, {Phase: "references"}, {Phase: "done"}} {
		if _, e := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", page, 1); !errors.Is(e, sessioncontext.ErrInvalid) {
			t.Fatalf("%+v: %v", page, e)
		}
	}
	for _, limit := range []int{0, 101} {
		if _, e := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", PinRedactionContinuation{}, limit); !errors.Is(e, sessioncontext.ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, e := f.worker.RedactPage(ctx, f.principal, "missing", "delete", PinRedactionContinuation{}, 1); e == nil {
		t.Fatal("missing acceptance allowed")
	}
	if _, e := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", PinRedactionContinuation{Phase: "references", Generation: 1}, 1); e == nil {
		t.Fatal("missing permanent marker allowed")
	}
	if _, e := f.worker.RedactPage(ctx, f.principal, "pin-one", "", PinRedactionContinuation{}, 1); e == nil {
		t.Fatal("invalid deletion accepted")
	}
}

type redactionRepositoryFault struct {
	sessioncontext.Repository
	view func(context.Context, func(sessioncontext.Reader) error) error
}

func (r redactionRepositoryFault) View(ctx context.Context, fn func(sessioncontext.Reader) error) error {
	return r.view(ctx, fn)
}

type redactionReaderFault struct {
	sessioncontext.Reader
	rows    []json.RawMessage
	listErr error
	loadErr error
}

func (r redactionReaderFault) List(context.Context, string, string, string, int) ([]json.RawMessage, string, error) {
	return r.rows, "", r.listErr
}
func (r redactionReaderFault) Load(ctx context.Context, collection, id string, dest any) (int64, error) {
	if r.loadErr != nil {
		return 0, r.loadErr
	}
	return r.Reader.Load(ctx, collection, id, dest)
}

func TestRedactionWorkerStorageAndReceiptFaults(t *testing.T) {
	ctx := context.Background()
	t.Run("marker read failure after durable scrub", func(t *testing.T) {
		f := newRedactionFixture(t)
		sentinel := errors.New("marker unavailable")
		f.worker.repo = redactionRepositoryFault{Repository: f.repo, view: func(context.Context, func(sessioncontext.Reader) error) error { return sentinel }}
		if _, e := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", PinRedactionContinuation{}, 100); !errors.Is(e, sentinel) {
			t.Fatal(e)
		}
		// The durable marker makes retry safe despite the failed acknowledgement.
		f.worker.repo = f.repo
		if _, e := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", PinRedactionContinuation{}, 100); e != nil {
			t.Fatal(e)
		}
	})
	for _, kind := range []string{"list", "malformed", "foreign-session", "foreign-owner", "foreign-namespace", "missing-acceptance", "changed-run", "native-receipt"} {
		t.Run(kind, func(t *testing.T) {
			f := newRedactionFixture(t)
			page, e := f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", PinRedactionContinuation{}, 100)
			if e != nil {
				t.Fatal(e)
			}
			var work sessioncontext.AcceptedDetectiveWork
			if e = f.repo.View(ctx, func(r sessioncontext.Reader) error {
				_, e := r.Load(ctx, sessioncontext.DetectiveWork, "run-one", &work)
				return e
			}); e != nil {
				t.Fatal(e)
			}
			var rows []json.RawMessage
			var listErr error
			switch kind {
			case "list":
				listErr = errors.New("list unavailable")
			case "malformed":
				rows = []json.RawMessage{json.RawMessage(`{`)}
			case "foreign-session":
				work.SessionID = "different-session"
			case "foreign-owner":
				work.Owner = "mallory"
			case "foreign-namespace":
				work.Namespace.NamespaceID = "foreign"
			case "missing-acceptance":
				work.ClientOperationID = "missing"
			case "changed-run":
				work.RunID = "different-run"
			case "native-receipt":
				entries, e := os.ReadDir(filepath.Join(f.privateDir, "session-evidence-handoffs"))
				if e != nil {
					t.Fatal(e)
				}
				for _, entry := range entries {
					if e = os.WriteFile(filepath.Join(f.privateDir, "session-evidence-handoffs", entry.Name()), []byte(`{`), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			if rows == nil {
				raw, e := json.Marshal(work)
				if e != nil {
					t.Fatal(e)
				}
				rows = []json.RawMessage{raw}
			}
			f.worker.repo = redactionRepositoryFault{Repository: f.repo, view: func(ctx context.Context, fn func(sessioncontext.Reader) error) error {
				return f.repo.View(ctx, func(r sessioncontext.Reader) error {
					return fn(redactionReaderFault{Reader: r, rows: rows, listErr: listErr})
				})
			}}
			_, e = f.worker.RedactPage(ctx, f.principal, "pin-one", "delete", page, 100)
			if kind == "foreign-session" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("invalid metadata accepted")
			}
			if f.calls != 2 {
				t.Fatal("scrub dispatched source")
			}
		})
	}
}
