package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"net/http"
	"net/url"
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
		defer resp.Body.Close()
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
	var review datatug.ProjectLifecycleReview
	get("projects/lifecycle/review?storage=local&project="+created.ID+"&kind=session&id=kept-session", &review)
	if !review.ArchiveSupported || !review.CanArchive || review.Complete {
		t.Fatalf("runtime session not archivable: %+v", review)
	}
	change := func(action, operation, revision string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"storage": "local", "project": created.ID, "operationId": operation, "expectedRevision": revision, "change": map[string]any{"object": map[string]string{"kind": "session", "id": "kept-session"}, "action": action}})
		if err != nil {
			t.Fatal(err)
		}
		post("projects/lifecycle/change", string(body))
	}
	change("archive", "hide-session", review.Revision)
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
	get("projects/lifecycle/review?storage=local&project="+created.ID+"&kind=session&id=kept-session", &review)
	if !review.CanRestore {
		t.Fatalf("hidden session cannot restore: %+v", review)
	}
	change("restore", "show-session", review.Revision)
	get(listPath, &sessionPage)
	if len(sessionPage.Items) != 1 {
		t.Fatalf("restored session missing: %s", fmt.Sprint(sessionPage))
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
