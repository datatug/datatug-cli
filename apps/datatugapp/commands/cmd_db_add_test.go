package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addProject is a registered project (id "shop") that has a project file and no environment.
func addProject(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"),
		[]byte(`{"id":"shop","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`), 0o644))
	registerProjectForTest(t, "shop", dir)
	return dir
}

func runDbAdd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := DatatugCommand()
	var out, errOut bytes.Buffer
	root.SetArgs(append([]string{"db", "add"}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func projectFilesOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		if !entry.IsDir() {
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			rel, _ := filepath.Rel(dir, path)
			files[filepath.ToSlash(rel)] = string(data)
		}
		return nil
	}))
	return files
}

func withLookupEnv(t *testing.T, env map[string]string) {
	t.Helper()
	previous := dbAddLookupEnv
	dbAddLookupEnv = func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	t.Cleanup(func() { dbAddLookupEnv = previous })
}

func TestDbAddSQLiteJourney(t *testing.T) {
	dir := addProject(t)
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	before, _ := os.ReadFile(dbPath)

	stdout, stderr, err := runDbAdd(t, "--project", "shop", "--db", dbPath, "--env", "local")
	require.NoError(t, err)
	assert.Equal(t, `{"added":true,"id":"shop","env":"local","driver":"sqlite3","dbRef":"datatug://shop/shop?env=local"}`+"\n", stdout)
	assert.Empty(t, stderr)
	files := projectFilesOf(t, dir)
	assert.Contains(t, files["environments/local/catalogs/shop/shop.db.json"], `"driver": "sqlite3"`)
	assert.Contains(t, files["environments/local/local.env.json"], `"catalogs"`)
	after, _ := os.ReadFile(dbPath)
	assert.Equal(t, before, after, "the database is not touched")
	_, walErr := os.Stat(dbPath + "-wal")
	assert.True(t, os.IsNotExist(walErr))

	// shown, not scanned, with its path
	shown, _, err := runShowCommand(t, "-p", "shop", "--format", "json", "--depth", "sources")
	require.NoError(t, err)
	var doc struct {
		Environments []struct {
			Sources []map[string]any
		}
	}
	require.NoError(t, json.Unmarshal([]byte(shown), &doc))
	require.Len(t, doc.Environments[0].Sources, 1)
	source := doc.Environments[0].Sources[0]
	assert.Equal(t, "shop", source["id"])
	assert.Equal(t, true, source["notScanned"])
	assert.Equal(t, canonical(t, dbPath), canonical(t, source["path"].(string)))
	text, _, err := runShowCommand(t, "-p", "shop", "--depth", "sources")
	require.NoError(t, err)
	assert.NotContains(t, text, dbPath, "the text output is unchanged")

	// the same database again changes nothing, whatever the spelling of its path or its name
	stdout, _, err = runDbAdd(t, "--project", "shop", "--db", "sqlite://"+dbPath, "--env", "local", "--name", "alias")
	require.NoError(t, err)
	assert.Equal(t, `{"added":false,"id":"shop","env":"local","driver":"sqlite3","dbRef":"datatug://shop/shop?env=local"}`+"\n", stdout)
	assert.Equal(t, files, projectFilesOf(t, dir))

	// the one environment is the default; text format
	stdout, _, err = runDbAdd(t, "--project", "shop", "--db", dbPath, "--format", "text")
	require.NoError(t, err)
	assert.Equal(t, "already registered as shop (sqlite3) in environment local: --db datatug://shop/shop\n", stdout)
	other := filepath.Join(t.TempDir(), "My Crm.sqlite")
	writeCRMDB(t, other)
	stdout, _, err = runDbAdd(t, "--project", "shop", "--db", other, "--format", "text")
	require.NoError(t, err)
	assert.Equal(t, "added My-Crm (sqlite3) in environment local: --db datatug://shop/My-Crm\n", stdout)

	// another database under a taken name is refused and writes nothing
	files = projectFilesOf(t, dir)
	third := filepath.Join(t.TempDir(), "third.db")
	require.NoError(t, os.WriteFile(third, []byte("x"), 0o644))
	_, _, err = runDbAdd(t, "--project", "shop", "--db", third, "--name", "shop")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, `--name "shop" is already a "sqlite3" source of environment "local"`)
	assert.Equal(t, files, projectFilesOf(t, dir))

	// a scan of the registered database describes it and keeps its registration
	_, err = runScanCommand(t, "-d", dir, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.NoError(t, err)
	stdout, _, err = runDbAdd(t, "--project", "shop", "--db", dbPath)
	require.NoError(t, err)
	assert.Equal(t, `{"added":false,"id":"shop","env":"local","driver":"sqlite3","dbRef":"datatug://shop/shop"}`+"\n", stdout)
}

func canonical(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}

func TestDbAddPostgresHoldsTheVariableNameOnly(t *testing.T) {
	dir := addProject(t)
	const url = "postgres://alice:s3cr3tpw@db.internal:5432/shop"
	withLookupEnv(t, map[string]string{"DATATUG_SHOP_PG": url, "DATATUG_SQLITE": "sqlite://x.db", "DATATUG_JUNK": "junk", "DATATUG_DSN_ENV_ALLOW": "ALLOWED_PG", "ALLOWED_PG": url})

	stdout, stderr, err := runDbAdd(t, "--project", "shop", "--db", "env:DATATUG_SHOP_PG", "--name", "shop-pg", "--env", "prod")
	require.NoError(t, err)
	assert.Equal(t, `{"added":true,"id":"shop-pg","env":"prod","driver":"postgres","dbRef":"datatug://shop/shop-pg?env=prod"}`+"\n", stdout)
	assert.Empty(t, stderr)
	files := projectFilesOf(t, dir)
	assert.JSONEq(t, `{"dsnEnv":"DATATUG_SHOP_PG"}`, files["connections/prod/shop-pg.json"])
	for name, content := range files {
		for _, secret := range []string{"alice", "s3cr3tpw", "db.internal", "5432"} {
			assert.NotContains(t, content, secret, name)
		}
	}
	shown, _, err := runShowCommand(t, "-p", "shop", "--format", "json", "--depth", "sources")
	require.NoError(t, err)
	assert.Contains(t, shown, `"dsnEnv": "DATATUG_SHOP_PG"`)
	assert.NotContains(t, shown, `"path"`)

	// the same variable is the same database
	stdout, _, err = runDbAdd(t, "--project", "shop", "--db", "env:DATATUG_SHOP_PG", "--name", "again", "--env", "prod")
	require.NoError(t, err)
	assert.Equal(t, `{"added":false,"id":"shop-pg","env":"prod","driver":"postgres","dbRef":"datatug://shop/shop-pg?env=prod"}`+"\n", stdout)
	assert.Equal(t, files, projectFilesOf(t, dir))

	// a variable that is allowed by the operator, and one that is not set (noted, not checked)
	stdout, _, err = runDbAdd(t, "--project", "shop", "--db", "env:ALLOWED_PG", "--name", "listed", "--env", "prod")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"added":true`)
	stdout, stderr, err = runDbAdd(t, "--project", "shop", "--db", "env:DATATUG_NOT_SET", "--name", "later", "--env", "prod")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"added":true`)
	assert.Equal(t, "note: environment variable DATATUG_NOT_SET is not set here, so what it holds was not checked; the project records its name only\n", stderr)

	// refusals, none of which echoes a value
	for name, tc := range map[string]struct {
		args    []string
		message string
	}{
		"not a variable name": {[]string{"--db", "env:lower-case", "--name", "x", "--env", "prod"}, "NAME must be an environment variable name"},
		"not allowed":         {[]string{"--db", "env:NOT_ALLOWED", "--name", "x", "--env", "prod"}, "which is not allowed"},
		"holds a sqlite url":  {[]string{"--db", "env:DATATUG_SQLITE", "--name", "x", "--env", "prod"}, "must hold a postgres:// URL, not a sqlite source"},
		"holds junk":          {[]string{"--db", "env:DATATUG_JUNK", "--name", "x", "--env", "prod"}, "does not hold a supported source URL"},
		"needs a name":        {[]string{"--db", "env:DATATUG_SHOP_PG", "--env", "prod"}, "--name is required for an env:NAME source"},
	} {
		_, stderr, err := runDbAdd(t, append([]string{"--project", "shop"}, tc.args...)...)
		require.Error(t, err, name)
		assert.Equal(t, 2, exitCodeOf(t, err), name)
		assert.ErrorContains(t, err, tc.message, name)
		assert.NotContains(t, err.Error()+stderr, "s3cr3tpw", name)
		assert.NotContains(t, err.Error()+stderr, "junk", name)
	}
}

func TestDbAddRefusesCredentialedURLsWithoutEchoingThem(t *testing.T) {
	dir := addProject(t)
	for _, url := range []string{
		"postgres://alice:s3cr3tpw@db.internal:5432/shop?sslmode=disable",
		"postgresql://alice:s3cr3tpw@db.internal/shop",
	} {
		stdout, stderr, err := runDbAdd(t, "--project", "shop", "--db", url, "--name", "x", "--env", "prod")
		require.Error(t, err)
		assert.Equal(t, 2, exitCodeOf(t, err))
		assert.EqualError(t, err, "--db is a PostgreSQL connection URL, which carries credentials a project must not hold: put it in an environment variable and pass --db env:NAME")
		for _, secret := range []string{"alice", "s3cr3tpw", "db.internal", "sslmode"} {
			assert.NotContains(t, err.Error()+stdout+stderr, secret)
		}
	}
	assert.Len(t, projectFilesOf(t, dir), 1, "nothing is written")

	_, _, err := runDbAdd(t, "--project", "shop", "--db", "ingitdb://./x", "--name", "x", "--env", "prod")
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "a ingitdb source cannot be registered")
	_, _, err = runDbAdd(t, "--project", "shop", "--db", "nonsense:thing", "--name", "x", "--env", "prod")
	assert.Equal(t, 2, exitCodeOf(t, err))
}

func TestDbAddUsageAndNotFound(t *testing.T) {
	dir := addProject(t)
	db := filepath.Join(t.TempDir(), "a.sqlite")
	require.NoError(t, os.WriteFile(db, []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(t.TempDir(), "d"), 0o755))
	folder := t.TempDir()

	for name, tc := range map[string]struct {
		args    []string
		code    int
		message string
	}{
		"bad format":        {[]string{"--project", "shop", "--db", db, "--format", "yaml"}, 2, `unsupported --format "yaml"`},
		"no project":        {[]string{"--db", db}, 2, "--project is required"},
		"no db":             {[]string{"--project", "shop"}, 2, "--db is required"},
		"missing file":      {[]string{"--project", "shop", "--db", filepath.Join(folder, "gone.sqlite"), "--env", "e"}, 2, "cannot be found"},
		"folder":            {[]string{"--project", "shop", "--db", folder + "/x.db", "--env", "e"}, 2, "cannot be found"},
		"no env at all":     {[]string{"--project", "shop", "--db", db}, 2, "the project has no environment yet"},
		"bad env":           {[]string{"--project", "shop", "--db", db, "--env", "a/b"}, 2, "--env is not a plain name"},
		"bad name":          {[]string{"--project", "shop", "--db", db, "--env", "e", "--name", "a b"}, 2, "--name"},
		"unknown project":   {[]string{"--project", "nope", "--db", db, "--env", "e"}, 3, `unknown project "nope"`},
		"unknown file":      {[]string{"--project", "shop", "--db", writeText(t, "notes.txt"), "--env", "e"}, 2, "not a recognised database file"},
		"unnamed from file": {[]string{"--project", "shop", "--db", writeText(t, "---.sqlite"), "--env", "e"}, 2, "--name is required: the file name"},
	} {
		_, _, err := runDbAdd(t, tc.args...)
		require.Error(t, err, name)
		assert.Equal(t, tc.code, exitCodeOf(t, err), name)
		assert.ErrorContains(t, err, tc.message, name)
	}
	require.NoError(t, os.Mkdir(filepath.Join(folder, "dir.db"), 0o755))
	_, _, err := runDbAdd(t, "--project", "shop", "--db", filepath.Join(folder, "dir.db"), "--env", "e")
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "is a folder, not a SQLite file")

	// a registered folder with no project file is not found
	empty := t.TempDir()
	registerProjectForTest(t, "empty", empty)
	_, _, err = runDbAdd(t, "--project", "empty", "--db", db, "--env", "e")
	assert.Equal(t, 3, exitCodeOf(t, err))
	assert.Len(t, projectFilesOf(t, dir), 1)
}

func writeText(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte("plain text, not a database"), 0o644))
	return path
}

func TestDbAddDefaultEnvironment(t *testing.T) {
	newDB := func(t *testing.T, name string) string {
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
		return path
	}
	envOf := func(t *testing.T, stdout string) string {
		var result dbAddResult
		require.NoError(t, json.Unmarshal([]byte(stdout), &result))
		return result.Env
	}
	seed := func(t *testing.T, envs ...string) string {
		dir := addProject(t)
		for _, env := range envs {
			_, _, err := runDbAdd(t, "--project", "shop", "--db", newDB(t, "seed.db"), "--name", "seed-"+env, "--env", env)
			require.NoError(t, err)
		}
		return dir
	}

	t.Run("one environment", func(t *testing.T) {
		seed(t, "prod")
		out, _, err := runDbAdd(t, "--project", "shop", "--db", newDB(t, "x.db"))
		require.NoError(t, err)
		assert.Equal(t, "prod", envOf(t, out))
	})
	t.Run("several with local", func(t *testing.T) {
		dir := seed(t, "dev", "local", "prod")
		out, _, err := runDbAdd(t, "--project", "shop", "--db", newDB(t, "x.db"))
		require.NoError(t, err)
		assert.Equal(t, "local", envOf(t, out))
		assert.Contains(t, projectFilesOf(t, dir), "environments/local/catalogs/x/x.db.json")
		assert.NotContains(t, projectFilesOf(t, dir), "environments/dev/catalogs/x/x.db.json")
	})
	t.Run("several without local", func(t *testing.T) {
		dir := seed(t, "prod", "dev")
		before := projectFilesOf(t, dir)
		_, _, err := runDbAdd(t, "--project", "shop", "--db", newDB(t, "x.db"))
		assert.Equal(t, 2, exitCodeOf(t, err))
		assert.EqualError(t, err, "--env is required: the project has 2 environments (dev, prod)")
		assert.Equal(t, before, projectFilesOf(t, dir))
	})
	t.Run("none", func(t *testing.T) {
		dir := addProject(t)
		_, _, err := runDbAdd(t, "--project", "shop", "--db", newDB(t, "x.db"))
		assert.Equal(t, 2, exitCodeOf(t, err))
		assert.ErrorContains(t, err, "the project has no environment yet")
		assert.Len(t, projectFilesOf(t, dir), 1)
	})
	t.Run("explicit env is unchanged", func(t *testing.T) {
		seed(t, "dev", "local")
		out, _, err := runDbAdd(t, "--project", "shop", "--db", newDB(t, "x.db"), "--env", "dev")
		require.NoError(t, err)
		assert.Equal(t, "dev", envOf(t, out))
		out, _, err = runDbAdd(t, "--project", "shop", "--db", newDB(t, "y.db"), "--env", "staging")
		require.NoError(t, err)
		assert.Equal(t, "staging", envOf(t, out))
	})
}

func TestDbAddFailuresOfTheProjectFiles(t *testing.T) {
	dir := addProject(t)
	db := filepath.Join(t.TempDir(), "a.db")
	require.NoError(t, os.WriteFile(db, []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments", "local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "local", "local.env.json"), []byte("{"), 0o644))

	_, _, err := runDbAdd(t, "--project", "shop", "--db", db, "--env", "local")
	require.Error(t, err)
	var coder ExitCoder
	assert.False(t, errors.As(err, &coder), "exit 1")
	assert.EqualError(t, err, "the environments of the project cannot be read")
	// the default environment cannot be read either
	_, _, err = runDbAdd(t, "--project", "shop", "--db", db)
	assert.EqualError(t, err, "the environments of the project cannot be read")
}

func TestDbAddSeamsThatFail(t *testing.T) {
	addProject(t)
	db := filepath.Join(t.TempDir(), "a.db")
	require.NoError(t, os.WriteFile(db, []byte("x"), 0o644))
	previous := absolutePath
	absolutePath = func(path string) (string, error) {
		if strings.HasSuffix(path, "a.db") {
			return "", errors.New("no working directory")
		}
		return previous(path)
	}
	t.Cleanup(func() { absolutePath = previous })
	_, _, err := runDbAdd(t, "--project", "shop", "--db", "sqlite://./a.db", "--env", "e")
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "cannot resolve the --db path")
}

func TestDefaultSourceName(t *testing.T) {
	for path, want := range map[string]string{
		"/x/shop.sqlite":   "shop",
		"/x/My Shop.db":    "My-Shop",
		"/x/_hidden.db":    "hidden",
		"/x/archive.v2.db": "archive.v2",
		"/x/café.sqlite3":  "café",
		"/x/data":          "data",
	} {
		got, err := defaultSourceName(dbAddSource{sqlitePath: path})
		require.NoError(t, err, path)
		assert.Equal(t, want, got, path)
	}
	for _, path := range []string{"/x/---.db", "/x/.db", "/x/ .db"} {
		_, err := defaultSourceName(dbAddSource{sqlitePath: path})
		assert.Equal(t, 2, exitCodeOf(t, err), path)
	}
	_, err := defaultSourceName(dbAddSource{dsnEnv: "DATATUG_X"})
	assert.Equal(t, 2, exitCodeOf(t, err))
}

func TestDbAddIsAListedSubcommandWithHelp(t *testing.T) {
	root := DatatugCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"db", "add", "--help"})
	require.NoError(t, root.Execute())
	for _, want := range []string{"--project", "--db", "--env", "--name", "--format", "env:NAME", "never"} {
		assert.Contains(t, out.String(), want)
	}
}
