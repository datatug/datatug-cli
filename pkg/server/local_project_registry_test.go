package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/privateadmission"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestServeHTTPLocalCreateOpensImmediatelyAndArchiveOnlyHidesDiscovery(t *testing.T) {
	session, err := secureread.NewSession(secureread.SessionOptions{As: "owner", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	base := startServeHTTPWithSessionAndCapabilities(t, nil, session, api.Capabilities{AllowWrites: true, ProjectsRoot: t.TempDir(), EvidencePrivateDir: t.TempDir()})
	post := func(path, body string) *http.Response {
		t.Helper()
		resp, err := testHTTPClient.Post(base+"/datatug/"+path, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		if resp.StatusCode != 200 {
			var failure any
			_ = json.NewDecoder(resp.Body).Decode(&failure)
			t.Fatalf("%s: %d %v", path, resp.StatusCode, failure)
		}
		return resp
	}
	resp := post("projects/create_project", `{"store":"local","id":"created-through-http","title":"HTTP created project","operationId":"create-http"}`)
	var created localprojects.Project
	if err = json.NewDecoder(resp.Body).Decode(&created); err != nil || created.ID != "created-through-http" {
		t.Fatalf("bad creation result: %+v %v", created, err)
	}
	get := func(path string, value any) {
		t.Helper()
		resp, err := testHTTPClient.Get(base + "/datatug/" + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if err = json.NewDecoder(resp.Body).Decode(value); err != nil {
			t.Fatal(err)
		}
	}
	board := datatug.Board{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "first-board", Title: "First board"}}}
	if _, err = api.CreateLocalProjectBoard(context.Background(), dto.ProjectRef{StoreID: "local", ProjectID: created.ID}, "main", board); err != nil {
		t.Fatalf("new local project cannot create board: %v", err)
	}
	entity := datatug.Entity{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "first-entity", Title: "First entity"}}}
	if err = api.CreateEntity(context.Background(), dto.ProjectRef{StoreID: "local", ProjectID: created.ID}, &entity); err != nil {
		t.Fatalf("new local project cannot create entity: %v", err)
	}
	var summary map[string]any
	get("projects/project_summary?id=created-through-http", &summary)
	if summary["title"] != "HTTP created project" {
		t.Fatalf("new project cannot open: %v", summary)
	}

	service, principal, capabilities, err := api.ResolveSessionContext(context.Background(), "local", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := sessioncontext.Scope{EnvironmentID: "dev", IdentityDomain: capabilities.Environments[0].IdentityDomain}
	if _, err = service.Execute(context.Background(), principal, sessioncontext.Command{OpID: "new-session", Action: "createSession", SessionID: "kept-session", BagID: "kept-bag", DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, Title: "Kept session", Scope: &scope}); err != nil {
		t.Fatal(err)
	}
	beforeSession, err := service.GetSnapshot(context.Background(), principal, "kept-session")
	if err != nil {
		t.Fatal(err)
	}
	_, acceptedPrincipal, repository, err := api.ResolveAcceptedSessionContext(context.Background(), "local", created.ID)
	if err != nil || repository == nil || acceptedPrincipal != principal {
		t.Fatalf("owning session namespace unavailable: %v", err)
	}
	sessionLifecycleID, err := sessioncontext.SessionLifecycleID(repository.NativeNamespace(), "kept-session")
	if err != nil {
		t.Fatal(err)
	}
	var initialCatalog datatug.ProjectLifecycleCatalog
	get("projects/lifecycle/catalog?storage=local&project="+created.ID, &initialCatalog)
	foundSession := false
	for _, item := range initialCatalog.Items {
		if item.Ref.Kind == "session" {
			if item.Ref.ID != sessionLifecycleID {
				t.Fatalf("session catalog identity is not bound to its owning namespace: %+v", item.Ref)
			}
			foundSession = true
		}
	}
	if !foundSession {
		t.Fatal("new session missing from lifecycle catalog")
	}
	sessionReviewPath := "projects/lifecycle/review?storage=local&project=" + created.ID + "&projectApi=local&branch=main&kind=session&id=" + url.QueryEscape(sessionLifecycleID)
	var review datatug.ProjectLifecycleReview
	get(sessionReviewPath, &review)
	if !review.ArchiveSupported || !review.CanArchive || review.Complete {
		t.Fatalf("runtime session not archivable: %+v", review)
	}
	change := func(action, operation, revision string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"storage": "local", "project": created.ID, "projectApi": "local", "branch": "main", "operationId": operation, "expectedRevision": revision, "change": map[string]any{"object": map[string]string{"kind": "session", "id": sessionLifecycleID}, "action": action}})
		if err != nil {
			t.Fatal(err)
		}
		post("projects/lifecycle/change", string(body))
	}
	mismatchBody, _ := json.Marshal(map[string]any{"storage": "local", "project": created.ID, "projectApi": "local", "branch": "different-branch", "operationId": "wrong-branch", "expectedRevision": review.Revision, "change": map[string]any{"object": map[string]string{"kind": "session", "id": sessionLifecycleID}, "action": "archive"}})
	mismatch, err := testHTTPClient.Post(base+"/datatug/projects/lifecycle/change", "application/json", bytes.NewReader(mismatchBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = mismatch.Body.Close()
	if mismatch.StatusCode != http.StatusConflict {
		t.Fatalf("mismatched branch admitted: %d", mismatch.StatusCode)
	}
	change("archive", "hide-session", review.Revision)
	var archivedItems datatug.ProjectLifecycleCatalog
	get("projects/lifecycle/catalog?storage=local&project="+created.ID+"&archived=true", &archivedItems)
	if len(archivedItems.Items) != 1 || archivedItems.Items[0].Ref != (datatug.ProjectObjectRef{Kind: "session", ID: sessionLifecycleID}) {
		t.Fatalf("archive included active definitions: %+v", archivedItems)
	}
	var activeItems datatug.ProjectLifecycleCatalog
	get("projects/lifecycle/catalog?storage=local&project="+created.ID, &activeItems)
	if len(activeItems.Items) < 3 {
		t.Fatalf("active board/entity/environment lost: %+v", activeItems)
	}
	for _, item := range activeItems.Items {
		if item.Archived {
			t.Fatalf("active list contains archive: %+v", item)
		}
	}
	listPath := "session-context/sessions?" + url.Values{"storage": {"local"}, "project": {created.ID}, "databaseId": {principal.DatabaseID}, "projectId": {principal.ProjectID}}.Encode()
	var sessionPage sessioncontext.Page[sessioncontext.Session]
	get(listPath, &sessionPage)
	if len(sessionPage.Items) != 0 {
		t.Fatalf("hidden session still active: %+v", sessionPage)
	}
	afterSession, err := service.GetSnapshot(context.Background(), principal, "kept-session")
	if err != nil || afterSession.Session.Version != beforeSession.Session.Version || afterSession.Session.State != beforeSession.Session.State || afterSession.Session.NativeFenceGeneration != beforeSession.Session.NativeFenceGeneration {
		t.Fatalf("archive changed native session execution state: %+v %v", afterSession, err)
	}
	get(sessionReviewPath, &review)
	if !review.CanRestore {
		t.Fatalf("hidden session cannot restore: %+v", review)
	}
	change("restore", "show-session", review.Revision)
	get("projects/lifecycle/catalog?storage=local&project="+created.ID+"&archived=true", &archivedItems)
	if len(archivedItems.Items) != 0 {
		t.Fatalf("restored item remains in archive: %+v", archivedItems)
	}
	get(listPath, &sessionPage)
	if len(sessionPage.Items) != 1 {
		t.Fatalf("restored session missing: %s", fmt.Sprint(sessionPage))
	}

	// A retained native archive is restored through the owning service, while
	// generic metadata receipts remain recoverable and never restart execution.
	snapshot, err := service.GetSnapshot(context.Background(), principal, "kept-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Execute(context.Background(), principal, sessioncontext.Command{OpID: "legacy-native-archive", Action: "transitionSession", SessionID: "kept-session", DatabaseID: principal.DatabaseID, ProjectID: principal.ProjectID, ExpectedVersion: snapshot.Session.Version, Transition: "archive"}); err != nil {
		t.Fatal(err)
	}
	get("projects/lifecycle/catalog?storage=local&project="+created.ID+"&archived=true", &archivedItems)
	if len(archivedItems.Items) != 1 || archivedItems.Items[0].Ref.Kind != "session" {
		t.Fatalf("legacy native archive missing: %+v", archivedItems)
	}
	get(sessionReviewPath, &review)
	if !review.CanRestore || !review.Target.Archived {
		t.Fatalf("legacy native archive cannot restore: %+v", review)
	}
	change("restore", "restore-native-session", review.Revision)
	nativeRestored, err := service.GetSnapshot(context.Background(), principal, "kept-session")
	if err != nil || nativeRestored.Session.State != "stopped" {
		t.Fatalf("native archive restore did not preserve stopped execution: %+v %v", nativeRestored.Session, err)
	}
	get(listPath, &sessionPage)
	if len(sessionPage.Items) != 1 {
		t.Fatal("native restored session missing from active list")
	}

	// Seed a retained native Case using the actual owning storage namespace,
	// then restore through the public lifecycle route.
	projectDirectory, _ := api.ProjectDir(created.ID)
	physicalProject, err := privateadmission.PhysicalDirectory(projectDirectory)
	if err != nil {
		t.Fatal(err)
	}
	projectDigest := sha256.Sum256([]byte(physicalProject))
	casesDirectory := filepath.Join(api.GetCapabilities().EvidencePrivateDir, "detective", hex.EncodeToString(projectDigest[:16]), "cases")
	if err = os.MkdirAll(casesDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	legacyCase := detective.Case{ID: "legacy-case", Title: "Archived legacy Case", Status: "archived", Version: 2, RunRefs: []string{"kept-run"}}
	caseBody, _ := json.Marshal(legacyCase)
	casePath := filepath.Join(casesDirectory, "legacy-case.json")
	if err = os.WriteFile(casePath, caseBody, 0600); err != nil {
		t.Fatal(err)
	}
	get("projects/lifecycle/review?storage=local&project="+created.ID+"&projectApi=local&branch=main&kind=case&id=legacy-case", &review)
	if !review.CanRestore || !review.Target.Archived {
		t.Fatalf("legacy native Case cannot restore: %+v", review)
	}
	caseRestoreBody, _ := json.Marshal(map[string]any{"storage": "local", "project": created.ID, "projectApi": "local", "branch": "main", "operationId": "restore-native-case", "expectedRevision": review.Revision, "change": map[string]any{"object": map[string]string{"kind": "case", "id": "legacy-case"}, "action": "restore"}})
	post("projects/lifecycle/change", string(caseRestoreBody))
	restoredCaseBody, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	var restoredCase detective.Case
	if err = json.Unmarshal(restoredCaseBody, &restoredCase); err != nil || restoredCase.Status != "active" || restoredCase.Version != 3 || len(restoredCase.RunRefs) != 1 {
		t.Fatalf("legacy Case restoration changed history: %+v %v", restoredCase, err)
	}
	post("projects/archive_project", `{"storage":"local","project":"created-through-http","archived":true,"operationId":"archive-http"}`)
	var active []map[string]any
	get("projects/projects_summary", &active)
	if len(active) != 0 {
		t.Fatalf("archive remains discoverable: %v", active)
	}
	get("projects/project_summary?id=created-through-http", &summary) // Content/access is retained.
	var registry api.LocalProjectRegistryState
	get("projects/registry", &registry)
	if len(registry.Projects) != 1 || !registry.Projects[0].Archived {
		t.Fatalf("archive cannot be restored: %+v", registry)
	}
	post("projects/archive_project", `{"storage":"local","project":"created-through-http","archived":false,"operationId":"restore-http"}`)
	get("projects/projects_summary", &active)
	if len(active) != 1 {
		t.Fatalf("restore did not return project: %v", active)
	}
	if _, err = api.ExecutionEvidenceStore("created-through-http", nil); err != nil {
		t.Fatalf("new project evidence is unregistered: %v", err)
	}
}
