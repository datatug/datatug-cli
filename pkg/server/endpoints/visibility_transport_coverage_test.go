package endpoints

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/projectvisibility"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

type visibilityFailedBody struct{}

func (visibilityFailedBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (visibilityFailedBody) Close() error             { return nil }

func visibilitySessionRef(t *testing.T, id string) datatug.ProjectObjectRef {
	t.Helper()
	_, _, repo, err := api.ResolveAcceptedSessionContext(context.Background(), "local", "p")
	if err != nil {
		t.Fatal(err)
	}
	qualified, err := sessioncontext.SessionLifecycleID(repo.NativeNamespace(), id)
	if err != nil {
		t.Fatal(err)
	}
	return datatug.ProjectObjectRef{Kind: "session", ID: qualified}
}

func TestLocalArchiveTransportRejectsAmbiguousAndUnreadableRequests(t *testing.T) {
	for _, test := range []struct{ name, target, content, body string }{
		{"content type", "/", "text/plain", `{}`},
		{"query parameters", "/?project=other", "application/json", `{}`},
		{"missing archive intent", "/", "application/json", `{"storage":"local","project":"p","operationId":"op"}`},
		{"unknown path", "/", "application/json", `{"path":"/private/source","archived":true}`},
		{"duplicate intent", "/", "application/json", `{"archived":true,"archived":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			r.Header.Set("Content-Type", test.content)
			w := httptest.NewRecorder()
			archiveLocalProject(w, r)
			if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "/private/source") || w.Header().Get("Cache-Control") != "no-store, no-transform" {
				t.Fatalf("ambiguous request was admitted or disclosed input: %d %s", w.Code, w.Body.String())
			}
		})
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Content-Type", "application/json")
	r.Body = visibilityFailedBody{}
	w := httptest.NewRecorder()
	archiveLocalProject(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("failed body was accepted: %d %s", w.Code, w.Body.String())
	}
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{localprojects.ErrConflict, http.StatusConflict, "PROJECT_CONFLICT"},
		{localprojects.ErrRootRequired, http.StatusNotImplemented, "LOCAL_PROJECT_ROOT_REQUIRED"},
		{secureread.ErrAccessDenied, http.StatusForbidden, ""},
	} {
		w := httptest.NewRecorder()
		writeLocalRegistryError(w, httptest.NewRequest(http.MethodPost, "/", nil), test.err)
		if w.Code != test.status || test.code != "" && !strings.Contains(w.Body.String(), test.code) {
			t.Fatalf("wrong registry error contract: %d %s", w.Code, w.Body.String())
		}
	}
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	w = httptest.NewRecorder()
	getLocalProjectRegistry(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("anonymous registry listed projects: %d %s", w.Code, w.Body.String())
	}
}

func localVisibilityTransportFixture(t *testing.T) (projectvisibility.Runtime, datatug.ProjectLifecycleScope, *sessioncontext.Service, sessioncontext.Principal, api.SessionContextCapabilities, string) {
	t.Helper()
	root, private := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "environments", "dev"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"datatug-project.json":          `{"id":"p","title":"Visibility fixture","access":"private"}`,
		"environments/dev/dev.env.json": `{"id":"dev","title":"Development","dbServers":[]}`,
		"datatug-meanings.json":         `{"version":1,"entities":[{"documentId":"project","origin":"project","conceptId":"customer","name":"Customer","fields":[{"documentId":"project","conceptId":"customer-id","name":"Customer ID"}]}]}`,
		"checks/check-a.json":           `{"id":"check-a","title":"Known check","version":1}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "visibility-owner", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"p": root}
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	api.ConfigureSecureSession(session, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, IncidentStores: []incidentstore.ConfiguredStore{
		{StoreID: "p", Project: &incidents.ProjectRef{StoreID: "local", ProjectID: "p"}},
		{StoreID: "other", Project: &incidents.ProjectRef{StoreID: "local", ProjectID: "other"}},
	}})
	if err = api.ConfigureExecutionEvidence(paths, nil, executionstore.Options{PrivateDir: private}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = api.CloseSessionContext()
		_ = api.CloseExecutionEvidence()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
		storage.NewDatatugStore = previous
	})
	service, principal, caps, err := api.ResolveSessionContext(context.Background(), "local", "p")
	if err != nil {
		t.Fatal(err)
	}
	scope := datatug.ProjectLifecycleScope{Storage: "local", Project: "p", ActorID: principal.Actor}
	runtime, err := PrepareLocalVisibilityItems(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, scope, service, principal, caps, root
}

func TestLocalVisibilityRuntimeUsesOwningInventoryAndSessionRestoreReceipt(t *testing.T) {
	runtime, scope, service, principal, caps, root := localVisibilityTransportFixture(t)
	ctx := context.Background()
	api.ConfigureLocalVisibilityInventory(PrepareLocalVisibilityItems)
	t.Cleanup(func() { api.ConfigureLocalVisibilityInventory(nil) })
	selection := api.LocalLifecycleSelection{ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: "p"}}
	check := datatug.ProjectObjectRef{Kind: "check", ID: "check-a"}
	review, err := api.LocalProjectLifecycleReview(ctx, selection, check)
	if err != nil || !review.CanArchive || !review.ArchiveSupported || review.CanDelete {
		t.Fatalf("real API/native constructor did not expose safe visibility: %+v %v", review, err)
	}
	write := api.LocalLifecycleWrite{LocalLifecycleSelection: selection, ProjectLifecycleWrite: datatug.ProjectLifecycleWrite{OperationID: "archive-check", ExpectedRevision: review.Revision, Change: datatug.ProjectLifecycleChange{Object: check, Action: "archive"}}}
	if _, err = api.LocalProjectLifecycleChange(ctx, write); err != nil {
		t.Fatal(err)
	}
	archivedCatalog, err := api.LocalProjectLifecycleCatalog(ctx, selection, true)
	if err != nil || len(archivedCatalog.Items) != 1 || archivedCatalog.Items[0].Ref != check {
		t.Fatalf("archive was not persisted through real API: %+v %v", archivedCatalog, err)
	}
	review, err = api.LocalProjectLifecycleReview(ctx, selection, check)
	if err != nil || !review.CanRestore {
		t.Fatalf("real archived review unavailable: %+v %v", review, err)
	}
	write.OperationID, write.ExpectedRevision, write.Change.Action = "restore-check", review.Revision, "restore"
	if _, err = api.LocalProjectLifecycleChange(ctx, write); err != nil {
		t.Fatal(err)
	}
	if len(caps.Environments) != 1 {
		t.Fatal("fixture environment was not admitted")
	}
	command := sessioncontext.Command{OpID: "visibility-create", Action: "createSession", SessionID: "working-session", BagID: "working-bag", DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, Title: "Retained private session", Scope: &sessioncontext.Scope{EnvironmentID: "dev", IdentityDomain: caps.Environments[0].IdentityDomain}}
	created, err := service.Execute(ctx, principal, command)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := service.Execute(ctx, principal, sessioncontext.Command{OpID: "legacy-archive", Action: "transitionSession", SessionID: command.SessionID, DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, ExpectedVersion: created.Receipt.SessionVersion, Transition: "archive"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := runtime.Inventory(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	var target projectvisibility.Target
	seen := map[datatug.ProjectObjectRef]bool{}
	for _, item := range items {
		seen[item.Item.Ref] = true
		if item.Item.Ref.Kind == "session" {
			target = item
		}
	}
	for _, ref := range []datatug.ProjectObjectRef{{Kind: "check", ID: "check-a"}, {Kind: "meaning", ID: "project/customer"}, {Kind: "meaning", ID: "project/customer-id"}, visibilitySessionRef(t, command.SessionID)} {
		if !seen[ref] {
			t.Fatalf("owning inventory lost %+v: %+v", ref, items)
		}
	}
	if !target.Item.Archived {
		t.Fatal("native archive state was lost")
	}
	result, err := runtime.Restore(ctx, scope, target, "visibility-restore")
	if err != nil || result.Item.Archived || result.Revision == target.Revision {
		t.Fatalf("owning restore did not return new state: %+v %v", result, err)
	}
	if replay, err := runtime.Restore(ctx, scope, target, "visibility-restore"); err != nil || replay.Revision != result.Revision {
		t.Fatalf("native restore replay changed receipt: %+v %v", replay, err)
	}
	current, err := service.GetSnapshot(ctx, principal, command.SessionID)
	if err != nil || current.Session.State != "stopped" || current.Session.Version != archived.Receipt.SessionVersion+1 {
		t.Fatalf("restore restarted or lost session: %+v %v", current.Session, err)
	}
	if _, err = runtime.Restore(ctx, scope, target, "different-restore"); !errors.Is(err, projectvisibility.ErrRestoreConflict) {
		t.Fatalf("stale new operation accepted: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(root, "checks", "check-a.json")); err != nil || !strings.Contains(string(body), "Known check") {
		t.Fatal("restore changed definition bytes", err)
	}
	wrong := scope
	wrong.ActorID = "another-owner"
	if rows, err := runtime.Inventory(ctx, wrong); !errors.Is(err, sessioncontext.ErrForbidden) || rows != nil {
		t.Fatalf("wrong actor disclosed private rows: %+v %v", rows, err)
	}
	if err := runtime.Authorize(ctx, wrong, false); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatalf("foreign actor authorized: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := runtime.Authorize(canceled, scope, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restore authorized: %v", err)
	}
	if _, err := runtime.Restore(ctx, wrong, target, "foreign-restore"); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatalf("foreign restore admitted: %v", err)
	}
	bad := target
	bad.Revision = "not-a-version"
	if _, err := runtime.Restore(ctx, scope, bad, "bad-version"); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatalf("unproved version admitted: %v", err)
	}
	bad.Item.Ref.Kind = "check"
	bad.Revision = "1"
	if _, err := runtime.Restore(ctx, scope, bad, "unsupported"); !errors.Is(err, datatug.ErrProjectUsageIncomplete) {
		t.Fatalf("unsupported native transition admitted: %v", err)
	}
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	if _, err := PrepareLocalVisibilityItems(ctx, scope); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatalf("revoked constructor admitted: %v", err)
	}
}
