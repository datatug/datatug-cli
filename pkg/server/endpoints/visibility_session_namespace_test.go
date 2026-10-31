package endpoints

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/projectvisibility"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/stretchr/testify/require"
)

func TestRetainedSessionVisibilityRuntimeRejectsSymlinkRetargetBeforeMutation(t *testing.T) {
	_, _, _, _, _, rootA := localVisibilityTransportFixture(t)
	rootB := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(rootB, "datatug-project.json"), []byte(`{"id":"p","title":"Replacement source","access":"private"}`), 0600))
	link := filepath.Join(t.TempDir(), "served-project")
	require.NoError(t, os.Symlink(rootA, link))
	require.NoError(t, api.CloseSessionContext())
	session, err := secureread.NewSession(secureread.SessionOptions{As: "visibility-owner", NoPolicies: true})
	require.NoError(t, err)
	api.ConfigureSecureSession(session, map[string]string{"p": link}, api.GetCapabilities())
	ctx := context.Background()
	service, principal, caps, err := api.ResolveSessionContext(ctx, "local", "p")
	require.NoError(t, err)
	scope := datatug.ProjectLifecycleScope{Storage: "local", Project: "p", ActorID: principal.Actor}
	runtime, err := PrepareLocalVisibilityItems(ctx, scope)
	require.NoError(t, err)
	created, err := service.Execute(ctx, principal, sessioncontext.Command{OpID: "create-retarget", Action: "createSession", SessionID: "same-session", BagID: "retarget-bag", DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, Title: "Keep retained archive", Scope: &sessioncontext.Scope{EnvironmentID: "dev", IdentityDomain: caps.Environments[0].IdentityDomain}})
	require.NoError(t, err)
	archived, err := service.Execute(ctx, principal, sessioncontext.Command{OpID: "archive-retarget", Action: "transitionSession", SessionID: "same-session", DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, ExpectedVersion: created.Receipt.SessionVersion, Transition: "archive"})
	require.NoError(t, err)
	ref := visibilitySessionRef(t, "same-session")
	generation := api.SecurityContextID()
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(rootB, link))
	require.Equal(t, generation, api.SecurityContextID(), "retarget test must not rely on session reconfiguration")
	require.ErrorIs(t, runtime.Authorize(ctx, scope, true), sessioncontext.ErrForbidden)
	rows, err := runtime.Inventory(ctx, scope)
	require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	require.Nil(t, rows)
	_, err = runtime.Restore(ctx, scope, projectvisibility.Target{Item: datatug.ProjectUsageItem{Ref: ref, Archived: true}, Revision: "2"}, "retarget-restore")
	require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	// Rebind only the symlink, without changing the process principal or source
	// selector. The old native archive must remain untouched and readable.
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(rootA, link))
	snapshot, err := service.GetSnapshot(ctx, principal, "same-session")
	require.NoError(t, err)
	require.Equal(t, "archived", snapshot.Session.State)
	require.Equal(t, archived.Receipt.SessionVersion, snapshot.Session.Version)
}

func TestRetainedSessionNamespaceAndRecoveryRecheckFailuresPropagate(t *testing.T) {
	runtime, scope, _, principal, _, _ := localVisibilityTransportFixture(t)
	restoreVisibilityIO(t)
	ctx := context.Background()
	target := projectvisibility.Target{Item: datatug.ProjectUsageItem{Ref: visibilitySessionRef(t, "s"), Archived: true}, Revision: "4"}
	previous := visibilityAcceptedSessionContext
	failure := errors.New("owning namespace changed during recovery")
	visibilityAcceptedSessionContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
		return nil, sessioncontext.Principal{}, nil, failure
	}
	rows, err := runtime.Inventory(ctx, scope)
	require.ErrorIs(t, err, failure)
	require.Nil(t, rows)
	require.ErrorIs(t, runtime.Authorize(ctx, scope, true), failure)
	visibilityAcceptedSessionContext = previous
	manager, err := sessionstore.New(filepath.Join(t.TempDir(), "session-context"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	otherRepository, err := manager.NativeRepository("p", t.TempDir(), t.TempDir())
	require.NoError(t, err)
	visibilityAcceptedSessionContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
		return nil, principal, otherRepository, nil
	}
	require.ErrorIs(t, runtime.Authorize(ctx, scope, true), sessioncontext.ErrForbidden, "a changed owning namespace must not keep old runtime authority")
	visibilityAcceptedSessionContext = previous
	visibilityExecuteSession = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, sessioncontext.Command) (sessioncontext.CommandResult, error) {
		visibilityAcceptedSessionContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
			return nil, sessioncontext.Principal{}, nil, failure
		}
		return sessioncontext.CommandResult{}, sessioncontext.ErrConflict
	}
	visibilityReadOperation = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, string) (sessioncontext.Receipt, error) {
		t.Fatal("recovery read old private storage after current namespace failed")
		return sessioncontext.Receipt{}, nil
	}
	_, err = runtime.Restore(ctx, scope, target, "restore")
	require.ErrorIs(t, err, failure)
}

func TestSessionLifecycleIdentityCannotCrossPhysicalProjectRoots(t *testing.T) {
	ctx := context.Background()
	runtimeA, scopeA, serviceA, principalA, capsA, rootA := localVisibilityTransportFixture(t)
	create := func(service *sessioncontext.Service, p sessioncontext.Principal, caps api.SessionContextCapabilities) sessioncontext.CommandResult {
		result, err := service.Execute(ctx, p, sessioncontext.Command{OpID: "same-create", Action: "createSession", SessionID: "same-session", BagID: "same-bag", DatabaseID: p.DatabaseID, ProjectID: p.ProjectID, Title: "Same record ID", Scope: &sessioncontext.Scope{EnvironmentID: "dev", IdentityDomain: caps.Environments[0].IdentityDomain}})
		require.NoError(t, err)
		return result
	}
	createdA := create(serviceA, principalA, capsA)
	refA := visibilitySessionRef(t, "same-session")
	api.ConfigureLocalVisibilityInventory(PrepareLocalVisibilityItems)
	t.Cleanup(func() { api.ConfigureLocalVisibilityInventory(nil) })
	selection := api.LocalLifecycleSelection{ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: "p"}}
	review, err := api.LocalProjectLifecycleReview(ctx, selection, refA)
	require.NoError(t, err)
	require.True(t, review.CanArchive)
	_, err = api.LocalProjectLifecycleChange(ctx, api.LocalLifecycleWrite{LocalLifecycleSelection: selection, ProjectLifecycleWrite: datatug.ProjectLifecycleWrite{OperationID: "archive-qualified-session", ExpectedRevision: review.Revision, Change: datatug.ProjectLifecycleChange{Object: refA, Action: "archive"}}})
	require.NoError(t, err)
	oldOverlay, err := os.ReadFile(filepath.Join(rootA, ".datatug-visibility.json"))
	require.NoError(t, err)
	activeA, err := filterLocalVisibleSessions(ctx, "p", principalA, []sessioncontext.Session{{ID: "same-session"}})
	require.NoError(t, err)
	require.Empty(t, activeA)
	preservedA, err := serviceA.GetSnapshot(ctx, principalA, "same-session")
	require.NoError(t, err)
	require.Equal(t, "active", preservedA.Session.State)
	require.Equal(t, createdA.Receipt.SessionVersion, preservedA.Session.Version)
	require.NoError(t, api.CloseSessionContext())

	// The source selector remains local/p, while its owning physical mount and
	// private parent change. A copied source overlay must not hide the new record.
	runtimeB, scopeB, serviceB, principalB, capsB, rootB := localVisibilityTransportFixture(t)
	createdB := create(serviceB, principalB, capsB)
	refB := visibilitySessionRef(t, "same-session")
	require.NotEqual(t, refA.ID, refB.ID)
	require.NoError(t, os.WriteFile(filepath.Join(rootB, ".datatug-visibility.json"), oldOverlay, 0600))
	activeB, err := filterLocalVisibleSessions(ctx, "p", principalB, []sessioncontext.Session{{ID: "same-session"}})
	require.NoError(t, err)
	require.Len(t, activeB, 1)
	query := url.Values{"storage": {"local"}, "project": {"p"}, "databaseId": {principalB.DatabaseID}, "projectId": {principalB.ProjectID}}
	w := httptest.NewRecorder()
	sessionContextHandler(w, httptest.NewRequest(http.MethodGet, "/session-context/sessions?"+query.Encode(), nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "same-session")
	_, err = api.LocalProjectLifecycleReview(ctx, selection, refA)
	require.Error(t, err, "old namespace must not gain a target in the replacement mount")
	require.ErrorIs(t, runtimeA.Authorize(ctx, scopeA, true), sessioncontext.ErrForbidden)
	rows, err := runtimeB.Inventory(ctx, scopeB)
	require.NoError(t, err)
	found := false
	for _, row := range rows {
		if row.Item.Ref.Kind == "session" {
			require.Equal(t, refB, row.Item.Ref)
			found = true
		}
	}
	require.True(t, found)
	archived, err := serviceB.Execute(ctx, principalB, sessioncontext.Command{OpID: "native-archive", Action: "transitionSession", SessionID: "same-session", DatabaseID: principalB.DatabaseID, ProjectID: principalB.ProjectID, ExpectedVersion: createdB.Receipt.SessionVersion, Transition: "archive"})
	require.NoError(t, err)
	for _, badID := range []string{"same-session", refA.ID, refB.ID + "/extra"} {
		target := projectvisibility.Target{Item: datatug.ProjectUsageItem{Ref: datatug.ProjectObjectRef{Kind: "session", ID: badID}, Archived: true}, Revision: "2"}
		_, err = runtimeB.Restore(ctx, scopeB, target, "foreign-restore")
		require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	}
	unchanged, err := serviceB.GetSnapshot(ctx, principalB, "same-session")
	require.NoError(t, err)
	require.Equal(t, "archived", unchanged.Session.State)
	require.Equal(t, archived.Receipt.SessionVersion, unchanged.Session.Version)
}

func TestSessionVisibilityNamespaceFailuresNeverBecomeRawIdentityFallback(t *testing.T) {
	_, scope, _, principal, _, _ := localVisibilityTransportFixture(t)
	restoreVisibilityIO(t)
	failure := errors.New("owning namespace unavailable")
	previous := visibilityAcceptedSessionContext
	visibilityAcceptedSessionContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
		return nil, sessioncontext.Principal{}, nil, failure
	}
	_, err := PrepareLocalVisibilityItems(context.Background(), scope)
	require.ErrorIs(t, err, failure)
	rows, err := filterLocalVisibleSessions(context.Background(), "p", principal, []sessioncontext.Session{{ID: "s"}})
	require.ErrorIs(t, err, failure)
	require.Nil(t, rows)
	visibilityAcceptedSessionContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
		return nil, principal, nil, nil
	}
	_, err = localVisibilitySessionNamespace(context.Background(), "p", principal)
	require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	visibilityAcceptedSessionContext = func(ctx context.Context, storage, project string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
		service, p, repo, err := previous(ctx, storage, project)
		p.Actor = "foreign"
		return service, p, repo, err
	}
	_, err = localVisibilitySessionNamespace(context.Background(), "p", principal)
	require.ErrorIs(t, err, sessioncontext.ErrForbidden)
	visibilityAcceptedSessionContext = previous
	rows, err = filterLocalVisibleSessions(context.Background(), "p", principal, []sessioncontext.Session{{ID: "bad/id"}})
	require.Error(t, err)
	require.Nil(t, rows)
	runtime, err := PrepareLocalVisibilityItems(context.Background(), scope)
	require.NoError(t, err)
	visibilityListSessions = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, string, int) (sessioncontext.Page[sessioncontext.Session], error) {
		return sessioncontext.Page[sessioncontext.Session]{Items: []sessioncontext.Session{{ID: "bad/id"}}}, nil
	}
	targets, err := runtime.Inventory(context.Background(), scope)
	require.Error(t, err)
	require.Nil(t, targets)
}
