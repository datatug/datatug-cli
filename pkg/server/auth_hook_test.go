package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// writeCapabilityForAuthGateTests is AllowWrites:true: this file's test is
// specifically about the AUTH hook (api.AuthTokenFromHTTPRequest /
// apicore.VerifyRequest), not Task 12's separate write-capability gate
// (requireWriteCapability) — using the plain startServeHTTPWithSession
// default (every write off) would refuse createProject before the auth
// hook it exists to test ever runs, at the wrong layer.
var writeCapabilityForAuthGateTests = api.Capabilities{AllowWrites: true}

// authHookProjectFixture writes a minimal, valid datatug-project.json (the
// "created"/"access" fields ProjectFile.Validate() requires — see
// datatug-core's pkg/datatug/project.go) so LoadProjectFile succeeds.
func authHookProjectFixture(t *testing.T, projectID string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	fixture := `{"id":"` + projectID + `","title":"Auth Hook Test Project","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return map[string]string{projectID: dir}
}

// TestServeHTTP_ProjectSummary_ReturnsSummary is brief S36's item 1
// regression test: GET /datatug/projects/project_summary against a temp
// project must return 200 with the summary, not panic on the nil
// apicore.GetAuthTokenFromHttpRequest hook (sneat-go-core/apicore, wired in
// ServeHTTP -> api.AuthTokenFromHTTPRequest, see auth_hook.go) and not fail
// with "no store configured" (project_api.go's GetProjectSummary now goes
// through storage.NewDatatugStore, the same factory ServeHTTP wires,
// instead of the unwired storage.GetStore). It also proves the web client's
// actual query shape works: project.service.ts's getProjectSummaryRequest
// sends `?id=<projectId>` with no `project`/`storage` param at all (see
// projectRefByID in pkg/server/endpoints/constants.go).
func TestServeHTTP_ProjectSummary_ReturnsSummary(t *testing.T) {
	const projectID = "auth-hook-project"
	pathsByID := authHookProjectFixture(t, projectID)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "agent1", NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	baseURL := startServeHTTPWithSessionAndCapabilities(t, pathsByID, session, writeCapabilityForAuthGateTests)

	resp, err := testHTTPClient.Get(baseURL + "/datatug/projects/project_summary?id=" + projectID)
	if err != nil {
		t.Fatalf("GET project_summary: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var summary struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if summary.ID != projectID {
		t.Errorf("summary.ID = %q, want %q", summary.ID, projectID)
	}
	if summary.Title != "Auth Hook Test Project" {
		t.Errorf("summary.Title = %q, want %q", summary.Title, "Auth Hook Test Project")
	}
}

// minimalCreateProjectPolicy is the smallest access policy document
// accesspolicies.Load can decode, used only to prove a *loaded policy set*
// exists for the "anonymous refused" half of
// TestServeHTTP_AuthGate_RefusesAnAnonymousRequestWhenAPolicySetExists — its
// rules are never evaluated because the request never reaches the
// executor: the auth hook refuses it before secureread even runs.
const minimalCreateProjectPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: auth-hook-test
default: deny
ruleSets:
  admin:
    - path: /**
      rules:
        - id: admin-full-access
          effect: allow
          operations: [readwrite]
bindings:
  roles:
    admin: [admin]
`

// postCreateProject POSTs a minimally-valid create_project request body and
// returns the answer.
func postCreateProject(t *testing.T, baseURL string) (*http.Response, error) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"id": "new-project", "title": "New Project"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return testHTTPClient.Post(baseURL+"/datatug/projects/create_project?store=files", "application/json", bytes.NewReader(body))
}

// TestServeHTTP_CreateProject_IsNotImplemented: the store of the files has no way to create a
// project, so with writes allowed the route answers 501 with a built sentence, whoever asks, and
// does not stop the connection with no answer (the file store's CreateProject panics). Without
// writes it is refused with the write capability's own 403, as every write route is.
func TestServeHTTP_CreateProject_IsNotImplemented(t *testing.T) {
	pathsByID := authHookProjectFixture(t, "auth-hook-project")
	session, err := secureread.NewSession(secureread.SessionOptions{As: "agent1", NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	t.Run("answered 501 when writes are allowed", func(t *testing.T) {
		baseURL := startServeHTTPWithSessionAndCapabilities(t, pathsByID, session, writeCapabilityForAuthGateTests)
		resp, err := postCreateProject(t, baseURL)
		if err != nil {
			t.Fatalf("postCreateProject: %v (a request that stops the connection with no answer)", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusNotImplemented {
			t.Fatalf("status = %d, want 501 (%s)", resp.StatusCode, body)
		}
		if want := `{"error":"creating a project is not implemented by this agent yet","code":"NOT_IMPLEMENTED"}` + "\n"; string(body) != want {
			t.Errorf("body = %q, want %q", body, want)
		}
	})

	t.Run("refused by the write capability when writes are not allowed", func(t *testing.T) {
		baseURL := startServeHTTPWithSession(t, pathsByID, session)
		resp, err := postCreateProject(t, baseURL)
		if err != nil {
			t.Fatalf("postCreateProject: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})
}

// TestServeHTTP_AuthGate_RefusesAnAnonymousRequestWhenAPolicySetExists is brief S36's item 1
// regression test for the AuthRequired path: the serve principal is accepted (see
// TestServeHTTP_ProjectSummary_ReturnsSummary), an anonymous request is refused once a policy
// set exists. It asks a route that answers through apicore, which is where the hook runs
// (api.AuthTokenFromHTTPRequest, wired as apicore.GetAuthTokenFromHttpRequest by ServeHTTP).
func TestServeHTTP_AuthGate_RefusesAnAnonymousRequestWhenAPolicySetExists(t *testing.T) {
	const projectID = "auth-hook-project"
	pathsByID := authHookProjectFixture(t, projectID)
	policiesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(policiesDir, "policy.yaml"), []byte(minimalCreateProjectPolicy), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	policies, err := accesspolicies.Load(accesspolicies.LoadOptions{Dir: policiesDir})
	if err != nil {
		t.Fatalf("accesspolicies.Load: %v", err)
	}
	if len(policies) == 0 {
		t.Fatalf("expected at least one loaded policy document")
	}
	// secureread.NewSession refuses to build this Session at all
	// (ErrNoPrincipal: a policy set with no --as/--role/--group), which
	// is resolveServeSession's own defense against ever starting serve
	// this way. This test builds the Session directly instead,
	// precisely to prove the HTTP-level hook (api.AuthTokenFromHTTPRequest)
	// still refuses this state on its own rather than assuming that
	// upstream guard always ran (see auth_hook.go's doc comment).
	anonymousWithPolicies := secureread.Session{Policies: policies, Principal: nil, Unrestricted: false}
	baseURL := startServeHTTPWithSession(t, pathsByID, anonymousWithPolicies)

	resp, err := testHTTPClient.Get(baseURL + "/datatug/projects/project_summary?id=" + projectID)
	if err != nil {
		t.Fatalf("GET project_summary: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Not a 401: sneat-go-core's apicore.Execute has a separate,
	// pre-existing gap on this exact path — when VerifyRequest returns
	// facade.ErrUnauthorized, Execute only logs it and returns
	// (api_http.go: "if err != nil { logus.Errorf(...); return }"),
	// unlike the getContext error branch a few lines below it, which
	// does call httpserver.HandleError. No WriteHeader ever happens, so
	// Go's server sends an implicit 200 with an empty body rather than
	// a real 401 — flagged to the lead session as an upstream fix
	// candidate for sneat-go-core, out of this repo's scope. What this
	// test can assert, and what actually matters for
	// REQ:server-acl-all-reads, is the refusal itself: the request
	// never reaches the worker, so the body carries no summary.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("anonymous project_summary body = %q, want empty (refused before the handler ran)", body)
	}
}
