package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

func itemTagsAPIFixture(t *testing.T, kind string) (ItemTagsSelector, string, string) {
	t.Helper()
	dir, git := setupTestGitProject(t, "tags-api")
	configureBranchReadTest(t, "tags-api", dir)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, map[string]string{"tags-api": dir}, Capabilities{AllowWrites: true})
	s := ItemTagsSelector{ProjectRef: dto.ProjectRef{StoreID: "local", ProjectID: "tags-api"}, Branch: git("symbolic-ref", "--short", "HEAD"), Kind: kind, ID: "sales"}
	name := filepath.Join(dir, "boards", "sales.board.json")
	body := `{"id":"sales","title":"Sales","tags":["sales"],"future":{"kept":true}}`
	if kind == "dbserver" {
		s.ID = "mysql:localhost:3306"
		s.Driver = "mysql"
		name = filepath.Join(dir, "dbs", "mysql", s.ID+".dbserver.json")
		body = `{"id":"mysql:localhost:3306","title":"MySQL","server":{"driver":"mysql","host":"localhost","port":3306},"tags":["sales"]}`
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
	require.NoError(t, os.WriteFile(name, []byte(body), 0o600))
	return s, dir, name
}

func itemTagsSaveRequest(t *testing.T, s ItemTagsSelector) SaveItemTagsRequest {
	t.Helper()
	read, err := LocalProjectItemTags(t.Context(), s)
	require.NoError(t, err)
	return SaveItemTagsRequest{ItemTagsSelector: s, Tags: []string{"customers"}, IfMatch: read.Revision, ExpectedBranchHead: read.BranchHead}
}

func TestItemTagsAPIValidationAndPersistence(t *testing.T) {
	s, _, name := itemTagsAPIFixture(t, "board")
	for _, change := range []func(*ItemTagsSelector){
		func(s *ItemTagsSelector) { s.ProjectID = "" }, func(s *ItemTagsSelector) { s.StoreID = "" }, func(s *ItemTagsSelector) { s.Branch = "" }, func(s *ItemTagsSelector) { s.Driver = "mysql" }, func(s *ItemTagsSelector) { s.ID = "../bad" }, func(s *ItemTagsSelector) { s.Kind = "unknown" }, func(s *ItemTagsSelector) { s.Kind = "dbserver"; s.Driver = "../driver" }, func(s *ItemTagsSelector) { s.Kind = "dbserver"; s.Driver = "mysql"; s.ID = "../bad" },
	} {
		bad := s
		change(&bad)
		require.Error(t, bad.validate())
		_, err := LocalProjectItemTags(t.Context(), bad)
		require.Error(t, err)
		_, err = SaveLocalProjectItemTags(t.Context(), SaveItemTagsRequest{ItemTagsSelector: bad})
		require.Error(t, err)
	}
	for _, tags := range [][]string{nil, make([]string, 101), {""}, {"a", "a"}, {strings.Repeat("x", 51)}, {" a"}, {"a\nb"}} {
		require.Error(t, validateItemTags(tags))
		r := SaveItemTagsRequest{ItemTagsSelector: s, Tags: tags}
		_, err := SaveLocalProjectItemTags(t.Context(), r)
		require.Error(t, err)
	}
	require.NoError(t, validateItemTags([]string{}))
	r := itemTagsSaveRequest(t, s)
	bad := r
	bad.IfMatch = ""
	_, err := SaveLocalProjectItemTags(t.Context(), bad)
	require.Error(t, err)
	bad = r
	bad.ExpectedBranchHead = "other"
	_, err = SaveLocalProjectItemTags(t.Context(), bad)
	require.ErrorIs(t, err, dto.ErrBranchHeadConflict)
	bad = r
	bad.IfMatch = strings.Repeat("0", 64)
	_, err = SaveLocalProjectItemTags(t.Context(), bad)
	require.ErrorIs(t, err, ErrItemTagsRevisionConflict)
	saved, err := SaveLocalProjectItemTags(t.Context(), r)
	require.NoError(t, err)
	require.Equal(t, r.Tags, saved.Tags)
	reloaded, err := LocalProjectItemTags(t.Context(), s)
	require.NoError(t, err)
	require.Equal(t, saved, reloaded)
	body, err := os.ReadFile(name)
	require.NoError(t, err)
	require.Contains(t, string(body), `"kept": true`)
	require.NoError(t, os.Remove(name))
	_, err = SaveLocalProjectItemTags(t.Context(), r)
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestItemTagsAPIServerReadSaveAndCatalog(t *testing.T) {
	s, _, _ := itemTagsAPIFixture(t, "dbserver")
	r := itemTagsSaveRequest(t, s)
	saved, err := SaveLocalProjectItemTags(t.Context(), r)
	require.NoError(t, err)
	require.Equal(t, r.Tags, saved.Tags)
	servers, err := LocalProjectDbServers(t.Context(), s.ProjectRef, s.Branch)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	require.Equal(t, r.Tags, servers[0].Tags)
	_, err = LocalProjectDbServers(t.Context(), s.ProjectRef, "wrong")
	require.ErrorIs(t, err, dto.ErrBranchHeadConflict)
}

func TestItemTagsAPIAuthorizationAndReadFailures(t *testing.T) {
	s, dir, _ := itemTagsAPIFixture(t, "board")
	r := itemTagsSaveRequest(t, s)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, map[string]string{s.ProjectID: dir}, Capabilities{})
	require.Error(t, authorizeItemTagsWrite(t.Context(), s))
	_, err = SaveLocalProjectItemTags(t.Context(), r)
	require.Error(t, err)
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	require.Error(t, authorizeItemTagsWrite(t.Context(), s))
	_, err = LocalProjectItemTags(t.Context(), s)
	require.Error(t, err)
	ConfigureSecureSession(session, map[string]string{s.ProjectID: dir}, Capabilities{AllowWrites: true})
	original := itemTagsProjectDir
	t.Cleanup(func() { itemTagsProjectDir = original })
	itemTagsProjectDir = func(string) (string, error) { return "", errors.New("directory unavailable") }
	_, err = LocalProjectItemTags(t.Context(), s)
	require.Error(t, err)
	_, err = SaveLocalProjectItemTags(t.Context(), r)
	require.Error(t, err)
	itemTagsProjectDir = original
	gitStateOriginal := localGitStateSeam
	t.Cleanup(func() { localGitStateSeam = gitStateOriginal })
	for _, read := range []bool{true, false} {
		calls := 0
		localGitStateSeam = func(ctx context.Context, dir string) (gitState, error) {
			calls++
			if calls == 3 {
				return gitState{}, errors.New("git unavailable")
			}
			return gitStateOriginal(ctx, dir)
		}
		if read {
			_, err = LocalProjectItemTags(t.Context(), s)
		} else {
			_, err = SaveLocalProjectItemTags(t.Context(), r)
		}
		require.Error(t, err)
	}
}

func TestItemTagsDocumentRefusals(t *testing.T) {
	for _, tc := range []struct{ name, body, kind string }{
		{"missing", "", "board"}, {"invalid-json", "{", "board"}, {"wrong-id", `{"id":"other"}`, "board"}, {"not-object", "[]", "board"}, {"bad-tags", `{"id":"sales","tags":1}`, "board"},
		{"wrong-server", `{"id":"mysql:localhost:3306","server":{"driver":"postgres"}}`, "dbserver"},
		{"bad-server", `{"id":"mysql:localhost:3306","server":1}`, "dbserver"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, name := itemTagsAPIFixture(t, tc.kind)
			if tc.body == "" {
				require.NoError(t, os.Remove(name))
			} else {
				require.NoError(t, os.WriteFile(name, []byte(tc.body), 0o600))
			}
			_, err := LocalProjectItemTags(t.Context(), s)
			require.Error(t, err)
		})
	}
	for _, mode := range []string{"dual", "symlink", "oversize", "lstat-error", "decode-error", "missing-tags", "null-tags", "invalid-server"} {
		t.Run(mode, func(t *testing.T) {
			s, dir, name := itemTagsAPIFixture(t, "board")
			switch mode {
			case "dual":
				nested := filepath.Join(dir, "boards", s.ID, "board.json")
				require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o700))
				require.NoError(t, os.WriteFile(nested, []byte(`{}`), 0o600))
			case "symlink":
				original := name + ".original"
				require.NoError(t, os.Rename(name, original))
				require.NoError(t, os.Symlink(filepath.Base(original), name))
			case "oversize":
				require.NoError(t, os.WriteFile(name, []byte(strings.Repeat("x", (256<<10)+1)), 0o600))
			case "lstat-error":
				old := itemTagsLstat
				t.Cleanup(func() { itemTagsLstat = old })
				itemTagsLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errors.New("lstat failed") }
			case "decode-error":
				old := itemTagsJSONUnmarshal
				t.Cleanup(func() { itemTagsJSONUnmarshal = old })
				itemTagsJSONUnmarshal = func([]byte, any) error { return errors.New("decode failed") }
			case "missing-tags":
				require.NoError(t, os.WriteFile(name, []byte(`{"id":"sales"}`), 0o600))
			case "null-tags":
				require.NoError(t, os.WriteFile(name, []byte(`{"id":"sales","tags":null}`), 0o600))
			case "invalid-server":
				s.Kind = "dbserver"
				s.Driver = "mysql"
				s.ID = "mysql:"
				name = filepath.Join(dir, "dbs", "mysql", s.ID+".dbserver.json")
				require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
				require.NoError(t, os.WriteFile(name, []byte(`{"id":"mysql:","server":{"driver":"mysql"}}`), 0o600))
			}
			got, err := LocalProjectItemTags(t.Context(), s)
			if mode == "missing-tags" || mode == "null-tags" {
				require.NoError(t, err)
				require.NotNil(t, got.Tags)
				require.Empty(t, got.Tags)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestItemTagsSaveBoundaryFailuresPreserveDocument(t *testing.T) {
	for _, mode := range []string{"marshal", "indent", "open", "stat", "chmod", "write", "sync", "close", "rename", "final-git", "final-read", "post-git", "post-edit"} {
		t.Run(mode, func(t *testing.T) {
			s, _, name := itemTagsAPIFixture(t, "board")
			r := itemTagsSaveRequest(t, s)
			before, err := os.ReadFile(name)
			require.NoError(t, err)
			fail := errors.New("injected boundary failure")
			marshal, indent, open, stat, chmod, write, sync, close, rename, git := jsonMarshal, itemTagsMarshalIndent, itemTagsOpenFile, itemTagsStat, fileChmod, fileWrite, fileSync, fileClose, itemTagsRename, localGitStateSeam
			t.Cleanup(func() {
				jsonMarshal, itemTagsMarshalIndent, itemTagsOpenFile, itemTagsStat, fileChmod, fileWrite, fileSync, fileClose, itemTagsRename, localGitStateSeam = marshal, indent, open, stat, chmod, write, sync, close, rename, git
			})
			switch mode {
			case "marshal":
				jsonMarshal = func(any) ([]byte, error) { return nil, fail }
			case "indent":
				itemTagsMarshalIndent = func(any, string, string) ([]byte, error) { return nil, fail }
			case "open":
				itemTagsOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, fail }
			case "stat":
				itemTagsStat = func(*os.Root, string) (os.FileInfo, error) { return nil, fail }
			case "chmod":
				fileChmod = func(*os.File, os.FileMode) error { return fail }
			case "write":
				fileWrite = func(*os.File, []byte) (int, error) { return 0, fail }
			case "sync":
				fileSync = func(*os.File) error { return fail }
			case "close":
				fileClose = func(f *os.File) error { require.NoError(t, close(f)); return fail }
			case "rename":
				itemTagsRename = func(*os.Root, string, string) error { return fail }
			default:
				calls := 0
				localGitStateSeam = func(ctx context.Context, dir string) (gitState, error) {
					calls++
					state, err := git(ctx, dir)
					if mode == "final-git" && calls == 4 {
						state.head = "changed"
					}
					if mode == "post-git" && calls == 5 {
						state.head = "changed"
					}
					if mode == "final-read" && calls == 4 {
						require.NoError(t, os.Remove(name))
					}
					if mode == "post-edit" && calls == 5 {
						require.NoError(t, os.WriteFile(name, []byte(`{"id":"sales","title":"external"}`), 0o600))
					}
					return state, err
				}
			}
			_, err = SaveLocalProjectItemTags(t.Context(), r)
			require.Error(t, err)
			if mode != "post-edit" && mode != "post-git" && mode != "final-read" {
				after, e := os.ReadFile(name)
				require.NoError(t, e)
				require.Equal(t, before, after)
			}
			if mode == "post-edit" || mode == "post-git" {
				require.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
			}
		})
	}
}

func TestItemTagsServerCatalogRefusals(t *testing.T) {
	for _, mode := range []string{"dbs-file", "bad-driver", "skip-file", "driver-file", "invalid-id", "missing-nested", "invalid-model", "bad-stored-server", "decode-model", "too-many"} {
		t.Run(mode, func(t *testing.T) {
			s, dir, name := itemTagsAPIFixture(t, "dbserver")
			switch mode {
			case "dbs-file":
				require.NoError(t, os.RemoveAll(filepath.Join(dir, "dbs")))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "dbs"), []byte("x"), 0o600))
			case "bad-driver":
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "dbs", "$bad"), 0o700))
			case "skip-file":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "dbs", "notes.txt"), []byte("x"), 0o600))
			case "driver-file":
				require.NoError(t, os.Chmod(filepath.Dir(name), 0))
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(name), 0o700) })
			case "invalid-id":
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(name), "wrong.dbserver.json"), []byte(`{}`), 0o600))
			case "missing-nested":
				require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(name), "mysql:unused"), 0o700))
			case "bad-stored-server":
				require.NoError(t, os.WriteFile(name, []byte(`{"id":"mysql:localhost:3306","server":{"driver":"postgres"}}`), 0o600))
			case "invalid-model":
				require.NoError(t, os.WriteFile(name, []byte(`{"id":"mysql:localhost:3306","server":{"driver":"mysql","host":"localhost","port":3306},"catalogs":1}`), 0o600))
			case "decode-model":
				old := itemTagsJSONUnmarshal
				t.Cleanup(func() { itemTagsJSONUnmarshal = old })
				calls := 0
				itemTagsJSONUnmarshal = func(b []byte, v any) error {
					calls++
					if calls == 3 {
						return errors.New("decode failed")
					}
					return old(b, v)
				}
			case "too-many":
				for i := 0; i < 1001; i++ {
					id := fmt.Sprintf("mysql:server%d", i)
					body := fmt.Sprintf(`{"id":%q,"server":{"driver":"mysql","host":%q}}`, id, fmt.Sprintf("server%d", i))
					require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(name), id+".dbserver.json"), []byte(body), 0o600))
				}
			}
			servers, err := LocalProjectDbServers(t.Context(), s.ProjectRef, s.Branch)
			if mode == "skip-file" || mode == "missing-nested" {
				require.NoError(t, err)
				require.Len(t, servers, 1)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestItemTagsResultMalformedTags(t *testing.T) {
	_, err := itemTagsResult(ItemTagsSelector{}, map[string]json.RawMessage{"tags": json.RawMessage(`1`)}, nil, "")
	require.Error(t, err)
}
