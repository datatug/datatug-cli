package commands

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covDSetVar swaps a package-level seam for the duration of the test.
func covDSetVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// covDSkipIfRoot skips permission-based tests, which root bypasses.
func covDSkipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed for root")
	}
}

// covDChmod changes mode for the test and restores it (so TempDir cleanup works).
func covDChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// covDSourceRepo creates a local git repository with one commit, usable as a
// clone source without any network access.
func covDSourceRepo(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	repo, err := git.PlainInit(src, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o600))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("f.txt")
	require.NoError(t, err)
	_, err = wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}})
	require.NoError(t, err)
	return src
}

func TestCovDDefaultCloneOrUpdateRepo(t *testing.T) {
	t.Run("clone then pull", func(t *testing.T) {
		src := covDSourceRepo(t)
		dst := filepath.Join(t.TempDir(), "a", "clone")
		require.NoError(t, defaultCloneOrUpdateRepo(dst, src))
		assert.FileExists(t, filepath.Join(dst, "f.txt"))
		// Second call takes the update path and is already up to date.
		require.NoError(t, defaultCloneOrUpdateRepo(dst, src))
	})
	t.Run("bare repository has no worktree", func(t *testing.T) {
		dir := t.TempDir()
		_, err := git.PlainInit(dir, true)
		require.NoError(t, err)
		err = defaultCloneOrUpdateRepo(dir, "unused")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "worktree")
	})
	t.Run("stat error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
		err := defaultCloneOrUpdateRepo(filepath.Join(file, "sub"), "unused")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to stat")
	})
	t.Run("parent directory cannot be created", func(t *testing.T) {
		covDSkipIfRoot(t)
		root := t.TempDir()
		covDChmod(t, root, 0o500)
		err := defaultCloneOrUpdateRepo(filepath.Join(root, "a", "b"), "unused")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parent directory")
	})
	t.Run("clone error", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "clone")
		err := defaultCloneOrUpdateRepo(dst, filepath.Join(t.TempDir(), "no-such-repo"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to git clone")
	})
}

func TestCovDDefaultDownloadSQLiteSource(t *testing.T) {
	t.Run("request error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		covDSetVar(t, &demoSQLiteSourceURL, url)
		err := defaultDownloadSQLiteSource(filepath.Join(t.TempDir(), "a.sqlite"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get request failed")
	})
	t.Run("truncated body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("abc"))
		}))
		t.Cleanup(srv.Close)
		covDSetVar(t, &demoSQLiteSourceURL, srv.URL)
		err := defaultDownloadSQLiteSource(filepath.Join(t.TempDir(), "a.sqlite"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to write response body")
	})
}

func TestCovDFindSQLiteCatalogPaths(t *testing.T) {
	t.Run("missing root", func(t *testing.T) {
		_, err := findSQLiteCatalogPaths(filepath.Join(t.TempDir(), "absent"))
		require.Error(t, err)
	})
	t.Run("unreadable catalog", func(t *testing.T) {
		covDSkipIfRoot(t)
		dir := t.TempDir()
		p := filepath.Join(dir, "x.db.json")
		require.NoError(t, os.WriteFile(p, []byte(`{}`), 0o600))
		covDChmod(t, p, 0)
		_, err := findSQLiteCatalogPaths(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read")
	})
	t.Run("invalid json", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "x.db.json"), []byte(`{bad`), 0o600))
		_, err := findSQLiteCatalogPaths(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse")
	})
	t.Run("user-specific home cannot be expanded", func(t *testing.T) {
		dir := t.TempDir()
		writeCatalogFile(t, filepath.Join(dir, "x.db.json"), "sqlite3", "~someone/x.sqlite")
		_, err := findSQLiteCatalogPaths(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to expand path")
	})
}

func TestCovDEnsureSQLiteFilesErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	under := filepath.Join(blocker, "db.sqlite")

	t.Run("stat error", func(t *testing.T) {
		err := demoCommand{}.ensureSQLiteFiles([]string{under})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to check for existing")
	})
	t.Run("mkdir error", func(t *testing.T) {
		err := demoCommand{ResetDB: true}.ensureSQLiteFiles([]string{under})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create directory")
	})
	t.Run("download error", func(t *testing.T) {
		covDSetVar(t, &downloadSQLiteSource, func(...string) error { return errors.New("dl boom") })
		err := demoCommand{}.ensureSQLiteFiles([]string{filepath.Join(t.TempDir(), "a.sqlite")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to download")
	})
}

func TestCovDVerifySQLiteFileOpenError(t *testing.T) {
	covDSetVar(t, &demoSQLOpen, func(string, string) (*sql.DB, error) { return nil, errors.New("open boom") })
	require.Error(t, verifySQLiteFile("x"))
}

func TestCovDRegisterDemoProjectReadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".datatug.yaml"), 0o755))
	err := registerDemoProject("demo", "/p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read")
}

func TestCovDDemoSetFlagDefault(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("k", "", "")
	require.NoError(t, demoSetFlag(cmd, "k", "v"))
	got, _ := cmd.Flags().GetString("k")
	assert.Equal(t, "v", got)
	require.Error(t, demoSetFlag(cmd, "nope", "v"))
}

func TestCovDDemoExecuteErrors(t *testing.T) {
	setup := func(t *testing.T) (home, reposDir string) {
		home = t.TempDir()
		t.Setenv("HOME", home)
		reposDir = filepath.Join(home, "datatug", demoOrgRepo)
		covDSetVar(t, &serveDemoProjectFunc, func(string) error { return nil })
		return home, reposDir
	}
	seedProject := func(t *testing.T, reposDir, catalog string) {
		t.Helper()
		if catalog == "" {
			require.NoError(t, os.MkdirAll(filepath.Join(reposDir, demoProjectFolder), 0o755))
			return
		}
		require.NoError(t, os.MkdirAll(filepath.Join(reposDir, demoProjectFolder), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(reposDir, demoProjectFolder, "c.db.json"), []byte(catalog), 0o600))
	}

	t.Run("reset project removal fails", func(t *testing.T) {
		covDSkipIfRoot(t)
		_, reposDir := setup(t)
		require.NoError(t, os.MkdirAll(reposDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(reposDir, "f"), []byte("x"), 0o600))
		covDChmod(t, reposDir, 0o500)
		err := demoCommand{ResetProject: true}.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to remove existing demo project clone")
	})
	t.Run("clone fails", func(t *testing.T) {
		setup(t)
		covDSetVar(t, &cloneOrUpdateRepo, func(string, string) error { return errors.New("clone boom") })
		err := demoCommand{}.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to clone/update")
	})
	t.Run("project folder missing", func(t *testing.T) {
		setup(t)
		covDSetVar(t, &cloneOrUpdateRepo, func(string, string) error { return nil })
		err := demoCommand{}.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found after cloning")
	})
	t.Run("catalog config unreadable", func(t *testing.T) {
		_, reposDir := setup(t)
		covDSetVar(t, &cloneOrUpdateRepo, func(string, string) error {
			seedProject(t, reposDir, `{bad`)
			return nil
		})
		err := demoCommand{}.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SQLite catalog config")
	})
	t.Run("sqlite files fail", func(t *testing.T) {
		home, reposDir := setup(t)
		covDSetVar(t, &cloneOrUpdateRepo, func(string, string) error {
			seedProject(t, reposDir, `{"driver":"sqlite3","path":"`+filepath.Join(home, "db.sqlite")+`"}`)
			return nil
		})
		covDSetVar(t, &downloadSQLiteSource, func(...string) error { return errors.New("dl boom") })
		require.Error(t, demoCommand{}.Execute())
	})
	t.Run("register fails", func(t *testing.T) {
		home, reposDir := setup(t)
		covDSetVar(t, &cloneOrUpdateRepo, func(string, string) error {
			seedProject(t, reposDir, "")
			return nil
		})
		require.NoError(t, os.Mkdir(filepath.Join(home, ".datatug.yaml"), 0o755))
		err := demoCommand{}.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to register demo project")
	})
}
