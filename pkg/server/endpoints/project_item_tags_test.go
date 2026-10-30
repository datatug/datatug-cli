package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

func tagFixture(t *testing.T, kind, layout string) (api.ItemTagsSelector, string, string) {
	t.Helper()
	scope, dir := realCaptureSetup(t, "admin", []string{"admin"})
	selector := api.ItemTagsSelector{ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: scope.Project}, Branch: runGit(t, dir, "symbolic-ref", "--short", "HEAD"), Kind: kind, ID: "revenue"}
	name := "boards/revenue.board.json"
	body := `{"id":"revenue","title":"Revenue","tags":["sales"],"panels":[{"id":"keep"}],"future":{"must":"survive"}}`
	if layout == "nested" {
		name = "boards/revenue/board.json"
	}
	if kind == "dbserver" {
		selector.ID = "mysql:localhost:3306"
		selector.Driver = "mysql"
		name = "dbs/mysql/mysql:localhost:3306.dbserver.json"
		body = `{"id":"mysql:localhost:3306","title":"Local MySQL","tags":["sales"],"server":{"driver":"mysql","host":"localhost","port":3306},"catalogs":[{"id":"sales"}],"future":{"must":"survive"}}`
		if layout == "nested" {
			name = "dbs/mysql/mysql:localhost:3306/.datatug-dbserver.json"
		}
	}
	filename := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o700))
	require.NoError(t, os.WriteFile(filename, []byte(body), 0o640))
	return selector, dir, filename
}

func getTags(t *testing.T, s api.ItemTagsSelector) (*httptest.ResponseRecorder, api.ItemTags) {
	t.Helper()
	q := url.Values{"storage": {s.StoreID}, "project": {s.ProjectID}, "branch": {s.Branch}, "kind": {s.Kind}, "id": {s.ID}}
	if s.Driver != "" {
		q.Set("driver", s.Driver)
	}
	w := httptest.NewRecorder()
	getProjectItemTags(w, httptest.NewRequest(http.MethodGet, "/datatug/projects/item_tags?"+q.Encode(), nil))
	var result api.ItemTags
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	}
	return w, result
}

func putTags(t *testing.T, s api.ItemTagsSelector, read api.ItemTags, tags []string) (*httptest.ResponseRecorder, api.ItemTags) {
	t.Helper()
	request := api.SaveItemTagsRequest{ItemTagsSelector: s, Tags: tags, IfMatch: read.Revision, ExpectedBranchHead: read.BranchHead}
	body, err := json.Marshal(request)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	saveProjectItemTags(w, httptest.NewRequest(http.MethodPut, "/datatug/projects/item_tags", bytes.NewReader(body)))
	var result api.ItemTags
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	}
	return w, result
}

func TestProjectItemTagsPersistOnlyTagsAndReload(t *testing.T) {
	for _, kind := range []string{"board", "dbserver"} {
		for _, layout := range []string{"flat", "nested"} {
			t.Run(kind+"/"+layout, func(t *testing.T) {
				selector, dir, filename := tagFixture(t, kind, layout)
				before, err := os.ReadFile(filename)
				require.NoError(t, err)
				w, read := getTags(t, selector)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Equal(t, []string{"sales"}, read.Tags)
				require.Len(t, read.Revision, 64)
				w, saved := putTags(t, selector, read, []string{"sales", "chinook"})
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.NotEqual(t, read.Revision, saved.Revision)
				after, err := os.ReadFile(filename)
				require.NoError(t, err)
				var original, current map[string]any
				require.NoError(t, json.Unmarshal(before, &original))
				require.NoError(t, json.Unmarshal(after, &current))
				delete(original, "tags")
				delete(current, "tags")
				require.Equal(t, original, current)
				info, err := os.Stat(filename)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
				w, reloaded := getTags(t, selector)
				require.Equal(t, http.StatusOK, w.Code)
				require.Equal(t, saved, reloaded)
				w, removed := putTags(t, selector, reloaded, []string{})
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Empty(t, removed.Tags)
				if kind == "board" {
					boards, err := api.LocalProjectBoards(t.Context(), selector.ProjectRef, selector.Branch)
					require.NoError(t, err)
					require.Len(t, boards, 1)
					require.Empty(t, boards[0].Tags)
				} else {
					servers, err := api.LocalProjectDbServers(t.Context(), selector.ProjectRef, selector.Branch)
					require.NoError(t, err)
					require.Len(t, servers, 1)
					require.Empty(t, servers[0].Tags)
				}
				require.Equal(t, read.BranchHead, runGit(t, dir, "rev-parse", "HEAD"))
			})
		}
	}
}

func TestProjectItemTagsRefuseStaleAndInvalidWrites(t *testing.T) {
	selector, dir, filename := tagFixture(t, "board", "flat")
	_, read := getTags(t, selector)
	original, err := os.ReadFile(filename)
	require.NoError(t, err)
	for _, tags := range [][]string{nil, {""}, {"sales", "sales"}, {strings.Repeat("x", 51)}, {" sales"}, {"a\nb"}} {
		w, _ := putTags(t, selector, read, tags)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	}
	bad := selector
	bad.ID = "../revenue"
	w, _ := putTags(t, bad, read, []string{"new"})
	require.Equal(t, http.StatusBadRequest, w.Code)
	bad = selector
	bad.ProjectID = "unserved"
	w, _ = putTags(t, bad, read, []string{"new"})
	require.NotEqual(t, http.StatusOK, w.Code)
	bad = selector
	bad.Branch = "other"
	w, _ = putTags(t, bad, read, []string{"new"})
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "BRANCH_HEAD_CONFLICT")
	stale := read
	stale.BranchHead = "old"
	w, _ = putTags(t, selector, stale, []string{"new"})
	require.Equal(t, http.StatusConflict, w.Code)
	foreign := bytes.Replace(original, []byte("Revenue"), []byte("Changed title"), 1)
	require.NoError(t, os.WriteFile(filename, foreign, 0o640))
	w, _ = putTags(t, selector, read, []string{"new"})
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "ITEM_REVISION_CONFLICT")
	actual, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, foreign, actual)
	require.NoError(t, os.Remove(filename))
	w, _ = putTags(t, selector, read, []string{"new"})
	require.Equal(t, http.StatusNotFound, w.Code)
	require.NoFileExists(t, filename)
	require.NotContains(t, runGit(t, dir, "status", "--porcelain"), "datatug-tags-")
}

func TestProjectItemTagsRequirePrincipalPolicyAndWriteCapability(t *testing.T) {
	for _, mode := range []string{"readonly", "no-writes", "no-principal"} {
		t.Run(mode, func(t *testing.T) {
			selector, dir, filename := tagFixture(t, "board", "flat")
			_, read := getTags(t, selector)
			before, err := os.ReadFile(filename)
			require.NoError(t, err)
			options := secureread.SessionOptions{As: "admin", Roles: []string{"admin"}, PoliciesDir: filepath.Join(dir, "policies")}
			caps := api.Capabilities{AllowWrites: true}
			if mode == "readonly" {
				options.As = "sam"
				options.Roles = []string{"support"}
			}
			if mode == "no-writes" {
				caps.AllowWrites = false
			}
			session, err := secureread.NewSession(options)
			require.NoError(t, err)
			if mode == "no-principal" {
				session = secureread.Session{}
			}
			api.ConfigureSecureSession(session, map[string]string{selector.ProjectID: dir}, caps)
			w, _ := putTags(t, selector, read, []string{"new"})
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			after, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, before, after)
			if mode == "no-principal" {
				w, _ := getTags(t, selector)
				require.Equal(t, http.StatusForbidden, w.Code)
			}
		})
	}
}

func TestProjectItemTagsRefuseSymlinkAndAmbiguousLayouts(t *testing.T) {
	for _, mode := range []string{"symlink", "dual"} {
		t.Run(mode, func(t *testing.T) {
			selector, _, filename := tagFixture(t, "board", "flat")
			_, read := getTags(t, selector)
			original, err := os.ReadFile(filename)
			require.NoError(t, err)
			if mode == "symlink" {
				outside := filepath.Join(t.TempDir(), "outside.json")
				require.NoError(t, os.WriteFile(outside, original, 0o600))
				require.NoError(t, os.Remove(filename))
				require.NoError(t, os.Symlink(outside, filename))
			} else {
				nested := filepath.Join(filepath.Dir(filename), "revenue", "board.json")
				require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o700))
				require.NoError(t, os.WriteFile(nested, original, 0o600))
			}
			w, _ := putTags(t, selector, read, []string{"new"})
			require.NotEqual(t, http.StatusOK, w.Code)
			actual, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, original, actual)
		})
	}
}

func TestProjectItemTagsCapabilitiesAndServerCatalog(t *testing.T) {
	selector, _, _ := tagFixture(t, "dbserver", "flat")
	q := url.Values{"storage": {"local"}, "project": {selector.ProjectID}, "branch": {selector.Branch}}
	w := httptest.NewRecorder()
	getProjectDbServers(w, httptest.NewRequest(http.MethodGet, "/datatug/dbservers/all_dbservers?"+q.Encode(), nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var servers []datatug.ProjDbServer
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &servers))
	require.Len(t, servers, 1)
	require.Equal(t, []string{"sales"}, servers[0].Tags)
	require.Equal(t, selector.ID, servers[0].ID)
	w = httptest.NewRecorder()
	projectCapabilitiesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/projects/capabilities?"+q.Encode(), nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"boardTags":true`)
	require.Contains(t, w.Body.String(), `"dbServerTags":true`)
}

func TestProjectItemTagsMalformedTransportsChangeNothing(t *testing.T) {
	selector, dir, _ := tagFixture(t, "board", "flat")
	q := url.Values{"storage": {"local"}, "project": {selector.ProjectID}, "branch": {selector.Branch}, "kind": {"board"}, "id": {selector.ID}}
	before := treeDigest(t, dir)
	for _, tc := range []struct {
		name, method, path, body string
		handler                  http.HandlerFunc
		status                   int
	}{
		{"extra selector", "GET", "/datatug/projects/item_tags?" + q.Encode() + "&extra=x", "", getProjectItemTags, 400},
		{"missing project", "GET", "/datatug/projects/item_tags?storage=unknown&project=" + selector.ProjectID, "", getProjectItemTags, 400},
		{"missing board", "GET", "/datatug/projects/item_tags?" + q.Encode() + "&unused=bad", "", getProjectItemTags, 400},
		{"large body", "PUT", "/datatug/projects/item_tags", strings.Repeat("x", 16<<10+1), saveProjectItemTags, 400},
		{"malformed body", "PUT", "/datatug/projects/item_tags", "{", saveProjectItemTags, 400},
		{"servers missing project", "GET", "/datatug/dbservers/all_dbservers?storage=unknown&project=" + selector.ProjectID, "", getProjectDbServers, 400},
		{"servers wrong branch", "GET", "/datatug/dbservers/all_dbservers?storage=local&project=" + selector.ProjectID + "&branch=not-current", "", getProjectDbServers, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			require.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
	// A valid selector reaches the API and propagates an absent item error.
	q.Set("id", "absent")
	w := httptest.NewRecorder()
	getProjectItemTags(w, httptest.NewRequest("GET", "/datatug/projects/item_tags?"+q.Encode(), nil))
	require.Equal(t, 404, w.Code)
	assertTreeUnchanged(t, dir, before)
}

func TestProjectDbServerTagsCatalogRequiresPrincipal(t *testing.T) {
	selector, dir, _ := tagFixture(t, "board", "flat")
	api.ConfigureSecureSession(secureread.Session{}, map[string]string{selector.ProjectID: dir}, api.Capabilities{})
	w := httptest.NewRecorder()
	getProjectDbServers(w, httptest.NewRequest("GET", "/datatug/dbservers/all_dbservers?project="+selector.ProjectID, nil))
	require.Equal(t, http.StatusForbidden, w.Code)
}
