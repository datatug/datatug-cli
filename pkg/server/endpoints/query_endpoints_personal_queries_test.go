package endpoints

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/personalqueries"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
)

// setPersonalQueriesBaseDir points personalqueries.ResolveProjectDir's
// $DATATUG_PERSONAL_DIR override at a fresh, hermetic t.TempDir() for the
// life of one test (S172). Every test below that reaches getPersonalQueries
// must call this first — otherwise it would fall through to the real
// ~/.datatug/projects on the machine running the suite.
func setPersonalQueriesBaseDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv(personalqueries.DirEnv, base)
	return base
}

// writePersonalQuery writes one .query.json file under this task's (S172)
// on-disk personal-queries convention — personalqueries.ResolveProjectDir's
// own resolved "<base>/<projectID>/queries/" — mirroring
// writeApplicableQueries' own fixture-writing style (semantic_fixture_test.go),
// exactly as getPersonalQueries (via loadModuleQueries) reads it. Callers
// must have already pointed $DATATUG_PERSONAL_DIR at a test directory via
// setPersonalQueriesBaseDir.
func writePersonalQuery(t *testing.T, projectID, id, title string) {
	t.Helper()
	dir, err := personalqueries.ResolveProjectDir(projectID)
	if err != nil {
		t.Fatalf("personalqueries.ResolveProjectDir(%q): %v", projectID, err)
	}
	personalDir := filepath.Join(dir, "queries")
	mustMkdirAll(t, personalDir)
	mustWriteFile(t, filepath.Join(personalDir, id+".query.json"), `{
		"id": "`+id+`",
		"title": "`+title+`",
		"type": "SQL"
	}`)
}

// writeLegacyPersonalQuery writes a query file at S169's ORIGINAL on-disk
// location — "<projectDir>/user:<principalID>/queries/<id>.query.json", a
// SIBLING of the shared project's own queries/ tree — the layout S172
// replaced with personalqueries.ResolveProjectDir's home-rooted directory.
// Used only by TestGetPersonalQueries_OldSiblingDirLayoutIsNoLongerRead, to
// prove that old location is dead, not merely undocumented.
func writeLegacyPersonalQuery(t *testing.T, projectDir, principalID, id, title string) {
	t.Helper()
	legacyDir := filepath.Join(projectDir, "user:"+principalID, "queries")
	mustMkdirAll(t, legacyDir)
	mustWriteFile(t, filepath.Join(legacyDir, id+".query.json"), `{
		"id": "`+id+`",
		"title": "`+title+`",
		"type": "SQL"
	}`)
}

// configureAnonymousSession wires api.ConfigureSecureSession the way
// `datatug serve --no-policies` (no --as/--role/--group at all) does —
// Session.Principal stays nil, so api.SecurePrincipalID() returns "" (see
// its own doc comment: "Unrestricted with no --as, or a role/group-only
// principal"). Mirrors constants_test.go's configureServedProjects, scoped
// to one project like configureSemanticSession.
func configureAnonymousSession(t *testing.T, projectDir, projectID string) {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	api.ConfigureSecureSession(session, map[string]string{projectID: projectDir}, api.Capabilities{})
}

// TestGetPersonalQueries_PrincipalSeesOwnPersonalQueries covers S169/S172's
// core case: a principal with queries under their own home-rooted personal
// directory (personalqueries.ResolveProjectDir) sees exactly those,
// folder-qualified the same way getAllQueries' shared tree already is —
// and the shared tree never gains the personal query either (the two
// roots are fully isolated, different directories entirely: this doubles
// as this task's required root=shared-never-leaks-personal-files
// coverage).
func TestGetPersonalQueries_PrincipalSeesOwnPersonalQueries(t *testing.T) {
	setPersonalQueriesBaseDir(t)
	projectDir, projectID := writeSemanticTestProject(t)
	writePersonalQuery(t, projectID, "my-scratchpad", "My scratchpad query")
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	folder, err := getPersonalQueries(context.Background(), dto.ProjectRef{StoreID: "files", ProjectID: projectID})
	if err != nil {
		t.Fatalf("getPersonalQueries: %v", err)
	}
	const wantRootID = "user:alice"
	if folder.ID != wantRootID {
		t.Errorf("root folder ID = %q, want %q", folder.ID, wantRootID)
	}
	if len(folder.Items) != 1 || folder.Items[0].ID != "my-scratchpad" {
		t.Fatalf("personal root items = %+v, want exactly [my-scratchpad]", folder.Items)
	}
	if len(folder.Folders) != 0 {
		t.Errorf("personal root folders = %+v, want none", folder.Folders)
	}

	shared, err := getAllQueries(context.Background(), dto.ProjectRef{StoreID: "files", ProjectID: projectID})
	if err != nil {
		t.Fatalf("getAllQueries: %v", err)
	}
	got := indexQueriesByCanonicalID(shared, "")
	if _, leaked := got["my-scratchpad"]; leaked {
		t.Errorf("alice's personal query leaked into the shared root: %+v", got)
	}
	if len(got) != len(wantDemoQueryTypes) {
		t.Errorf("shared root query count = %d, want %d (unaffected by the personal write): %+v", len(got), len(wantDemoQueryTypes), got)
	}
}

// TestGetPersonalQueries_OldSiblingDirLayoutIsNoLongerRead is S172's
// regression test: a file written at S169's original on-disk location (a
// "user:<id>/queries/" sibling of the project's own shared queries/ tree,
// INSIDE the project directory) must NOT be returned by getPersonalQueries
// any more — only personalqueries.ResolveProjectDir's home-rooted
// directory is read.
func TestGetPersonalQueries_OldSiblingDirLayoutIsNoLongerRead(t *testing.T) {
	setPersonalQueriesBaseDir(t)
	projectDir, projectID := writeSemanticTestProject(t)
	writeLegacyPersonalQuery(t, projectDir, "alice", "old-location-query", "Should not be found")
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	folder, err := getPersonalQueries(context.Background(), dto.ProjectRef{StoreID: "files", ProjectID: projectID})
	if err != nil {
		t.Fatalf("getPersonalQueries: %v", err)
	}
	if len(folder.Items) != 0 || len(folder.Folders) != 0 {
		t.Errorf("personal folder = %+v, want empty (the old sibling-directory location must not be read any more)", folder)
	}
}

// TestGetPersonalQueries_AnonymousPrincipalGetsEmptyFolder covers this
// task's own documented fallback: "an unauthenticated/anonymous principal
// gets an empty personal root, never an error" — even when SOME principal
// (alice) does own personal queries in the same project, proving the
// anonymous caller truly gets nothing back rather than an accident of an
// otherwise-empty fixture.
func TestGetPersonalQueries_AnonymousPrincipalGetsEmptyFolder(t *testing.T) {
	setPersonalQueriesBaseDir(t)
	projectDir, projectID := writeSemanticTestProject(t)
	writePersonalQuery(t, projectID, "my-scratchpad", "My scratchpad query")
	configureAnonymousSession(t, projectDir, projectID)

	if got := api.SecurePrincipalID(); got != "" {
		t.Fatalf("test setup: SecurePrincipalID() = %q, want \"\" (anonymous)", got)
	}

	folder, err := getPersonalQueries(context.Background(), dto.ProjectRef{StoreID: "files", ProjectID: projectID})
	if err != nil {
		t.Fatalf("getPersonalQueries: %v", err)
	}
	if len(folder.Items) != 0 || len(folder.Folders) != 0 {
		t.Errorf("anonymous personal folder = %+v, want empty", folder)
	}
}

// TestGetQueriesHandler_HTTP_RootShared proves the additive parameter is
// truly additive: an explicit ?root=shared produces the exact same
// response body as no `root` param at all (TestGetQueriesHandler_HTTP's
// own pre-S169 request shape).
func TestGetQueriesHandler_HTTP_RootShared(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	reqNoRoot := httptest.NewRequest(http.MethodGet, "/datatug/queries/all_queries?project="+projectID, nil)
	rrNoRoot := httptest.NewRecorder()
	getQueriesHandler(rrNoRoot, reqNoRoot)

	reqShared := httptest.NewRequest(http.MethodGet, "/datatug/queries/all_queries?project="+projectID+"&root=shared", nil)
	rrShared := httptest.NewRecorder()
	getQueriesHandler(rrShared, reqShared)

	if rrNoRoot.Code != http.StatusOK {
		t.Fatalf("no-root status = %d, body = %s", rrNoRoot.Code, rrNoRoot.Body.String())
	}
	if rrShared.Code != http.StatusOK {
		t.Fatalf("root=shared status = %d, body = %s", rrShared.Code, rrShared.Body.String())
	}
	if rrNoRoot.Body.String() != rrShared.Body.String() {
		t.Errorf("root=shared body differs from the no-root default:\n%s\nvs\n%s", rrNoRoot.Body.String(), rrShared.Body.String())
	}
}

// TestGetQueriesHandler_HTTP_RootPersonal covers the full route
// (registration, request parsing, JSON encoding — not just the
// business-logic function) for ?root=personal with a principal that has
// personal queries, and asserts the shared demo queries are absent from
// that response.
func TestGetQueriesHandler_HTTP_RootPersonal(t *testing.T) {
	setPersonalQueriesBaseDir(t)
	projectDir, projectID := writeSemanticTestProject(t)
	writePersonalQuery(t, projectID, "my-scratchpad", "My scratchpad query")
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := httptest.NewRequest(http.MethodGet, "/datatug/queries/all_queries?project="+projectID+"&root=personal", nil)
	rr := httptest.NewRecorder()
	getQueriesHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "my-scratchpad") {
		t.Errorf("response missing personal query: %s", body)
	}
	if !strings.Contains(body, `"id":"user:alice"`) {
		t.Errorf("response missing personal root id: %s", body)
	}
	for _, unwanted := range []string{"customer-invoices", "customer-export", "invoice-lines", "country-facts"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("root=personal response leaked shared query %q: %s", unwanted, body)
		}
	}
}

// TestGetQueriesHandler_HTTP_RootPersonal_Anonymous is
// TestGetQueriesHandler_HTTP_RootPersonal's anonymous-principal
// counterpart, over the real HTTP handler.
func TestGetQueriesHandler_HTTP_RootPersonal_Anonymous(t *testing.T) {
	setPersonalQueriesBaseDir(t)
	projectDir, projectID := writeSemanticTestProject(t)
	writePersonalQuery(t, projectID, "my-scratchpad", "My scratchpad query")
	configureAnonymousSession(t, projectDir, projectID)

	req := httptest.NewRequest(http.MethodGet, "/datatug/queries/all_queries?project="+projectID+"&root=personal", nil)
	rr := httptest.NewRecorder()
	getQueriesHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); strings.Contains(body, "my-scratchpad") {
		t.Errorf("anonymous root=personal leaked alice's personal query: %s", body)
	}
}

// TestGetQueriesHandler_HTTP_InvalidRoot covers an unrecognized ?root=
// value: 400 INVALID_REQUEST naming the "root" field, never a silent
// fallback to shared and never a 500.
func TestGetQueriesHandler_HTTP_InvalidRoot(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := httptest.NewRequest(http.MethodGet, "/datatug/queries/all_queries?project="+projectID+"&root=bogus", nil)
	rr := httptest.NewRecorder()
	getQueriesHandler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want %d", rr.Code, rr.Body.String(), http.StatusBadRequest)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"code":"INVALID_REQUEST"`) {
		t.Errorf("body = %s, want code INVALID_REQUEST", body)
	}
	if !strings.Contains(body, `"field":"root"`) {
		t.Errorf("body = %s, want field \"root\"", body)
	}
}

// TestParseQueriesRoot is a direct table-driven test of the ?root= parsing
// rule TestGetQueriesHandler_HTTP_RootShared/RootPersonal/InvalidRoot
// exercise indirectly over HTTP.
func TestParseQueriesRoot(t *testing.T) {
	tests := []struct {
		name    string
		values  url.Values
		want    string
		wantErr bool
	}{
		{name: "missing_defaults_to_shared", values: url.Values{}, want: queriesRootShared},
		{name: "blank_defaults_to_shared", values: url.Values{"root": {"  "}}, want: queriesRootShared},
		{name: "explicit_shared", values: url.Values{"root": {"shared"}}, want: queriesRootShared},
		{name: "explicit_personal", values: url.Values{"root": {"personal"}}, want: queriesRootPersonal},
		{name: "invalid", values: url.Values{"root": {"bogus"}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseQueriesRoot(tt.values)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidQueriesRoot) {
					t.Fatalf("err = %v, want ErrInvalidQueriesRoot", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseQueriesRoot: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
