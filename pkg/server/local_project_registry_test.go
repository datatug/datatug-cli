package server

import (
	"bytes"
	"encoding/json"
	"net/http"
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
	var summary map[string]any
	get("projects/project_summary?id=created-through-http", &summary)
	if summary["title"] != "HTTP created project" {
		t.Fatalf("new project cannot open: %v", summary)
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
