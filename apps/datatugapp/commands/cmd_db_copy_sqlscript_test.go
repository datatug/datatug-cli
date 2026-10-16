package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scriptSource(t *testing.T) string {
	t.Helper()
	return sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY, name TEXT)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
		`INSERT INTO p VALUES (1, 'x'), (2, 'y')`, `INSERT INTO c VALUES (1, 1)`, `CREATE VIEW pv AS SELECT id FROM p`)
}

func sqlscript(path string, query string) string {
	return "sqlscript://" + path + "?dialect=mysql" + query
}

func TestSQLScriptWritesAScriptAndReports(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "new", "dir", "out.sql")
	_, stderr, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, "&name=demo&license=MIT&edition=Edition%20X"))
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db copy: copied 2 tables, 3 rows")
	assert.Contains(t, stderr.String(), "view-not-carried")
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	script := string(b)
	for _, want := range []string{"-- Edition X", "-- Dataset: demo", "-- Licence: MIT", "CREATE TABLE `p`", "INSERT INTO `p` (`id`, `name`) VALUES\n(1,'x'),\n(2,'y');", "ADD FOREIGN KEY", "datatug db copy --to sqlscript:"} {
		assert.Contains(t, script, want)
	}
	if info, err := os.Stat(out); assert.NoError(t, err) {
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	}
	// No temporary file is left behind.
	entries, _ := os.ReadDir(filepath.Dir(out))
	assert.Len(t, entries, 1)
	// The name is the source file's, without its extension, when none is given.
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--overwrite", "recreate")
	require.NoError(t, err)
	b, _ = os.ReadFile(out)
	assert.Contains(t, string(b), "-- Dataset: source")
}

func TestSQLScriptRefusesToReplaceAFile(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	require.NoError(t, os.WriteFile(out, []byte("precious"), 0o644))
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""))
	require.Error(t, err)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "--overwrite=recreate")
	b, _ := os.ReadFile(out)
	assert.Equal(t, "precious", string(b), "the file is untouched")
	stdout, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--json")
	require.Error(t, err)
	assert.Contains(t, stdout.String(), `"status": "failed"`)
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--overwrite", "recreate")
	require.NoError(t, err)
	b, _ = os.ReadFile(out)
	assert.Contains(t, string(b), "CREATE TABLE")
}

func TestSQLScriptDryRunWritesNothing(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "dry", "out.sql")
	stdout, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--dry-run", "--json")
	require.NoError(t, err)
	var rep faithful.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rep))
	assert.Equal(t, "dry-run", rep.Status)
	assert.NotEmpty(t, rep.Plan)
	_, statErr := os.Stat(filepath.Dir(out))
	assert.True(t, os.IsNotExist(statErr), "a dry run creates no folder")
	// A dry run does not mind a file that is there.
	require.NoError(t, os.MkdirAll(filepath.Dir(out), 0o755))
	require.NoError(t, os.WriteFile(out, []byte("x"), 0o644))
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--dry-run")
	require.NoError(t, err)
}

func TestSQLScriptFailOnWarning(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	stdout, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--fail-on-warning", "--json")
	require.Error(t, err)
	assert.Equal(t, exitCodeWarnings, exitCodeOf(t, err))
	assert.Contains(t, stdout.String(), `"exitCode": 3`)
	_, statErr := os.Stat(out)
	assert.NoError(t, statErr, "the script is written; the exit code only says it was not exact")
	clean := sqliteFixture(t, `CREATE TABLE q (id INTEGER PRIMARY KEY)`)
	_, _, err = runCopy(t, "db", "copy", "--from", clean, "--to", sqlscript(filepath.Join(t.TempDir(), "q.sql"), ""), "--fail-on-warning")
	require.NoError(t, err)
}

func TestSQLScriptIncludeAndExclude(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--include", "p")
	require.NoError(t, err)
	b, _ := os.ReadFile(out)
	assert.NotContains(t, string(b), "CREATE TABLE `c`")
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--include", "p", "--exclude", "c", "--overwrite", "recreate")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, ""), "--include", "nope", "--overwrite", "recreate")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err), "a table that does not exist is a usage error")
	assert.Contains(t, err.Error(), `"nope" does not exist`)
}

func TestSQLScriptRelativePath(t *testing.T) {
	src := scriptSource(t)
	t.Chdir(t.TempDir())
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript:out.sql?dialect=mysql")
	require.NoError(t, err)
	_, statErr := os.Stat("out.sql")
	assert.NoError(t, statErr)
}

func TestSQLScriptUsageErrors(t *testing.T) {
	src := scriptSource(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.sql")
	other := sqliteFixture(t, `CREATE TABLE z (id INTEGER)`)
	cases := map[string]struct {
		args []string
		want string
	}{
		"no dialect":         {[]string{"--to", "sqlscript://" + out}, "dialect is missing"},
		"unknown dialect":    {[]string{"--to", "sqlscript://" + out + "?dialect=postgres"}, "implemented: mssql, mysql, oracle"},
		"unknown parameter":  {[]string{"--to", sqlscript(out, "&zip=1")}, `unknown parameter "zip"`},
		"no path":            {[]string{"--to", "sqlscript:?dialect=mysql"}, "file path is missing"},
		"a host":             {[]string{"--to", "sqlscript://host/x.sql?dialect=mysql"}, "path and a query only"},
		"a fragment":         {[]string{"--to", sqlscript(out, "#f")}, "path and a query only"},
		"not a URL":          {[]string{"--to", "sqlscript://%zz"}, "not a valid URL"},
		"from-schema":        {[]string{"--to", sqlscript(out, ""), "--from-schema", "s"}, "--from-schema is not supported"},
		"to-schema":          {[]string{"--to", sqlscript(out, ""), "--to-schema", "s"}, "--to-schema is not supported"},
		"decimals":           {[]string{"--to", sqlscript(out, ""), "--decimals", "text"}, "--decimals is not supported"},
		"where":              {[]string{"--to", sqlscript(out, ""), "--where", "p:id:=:1"}, "--where is not supported"},
		"limit":              {[]string{"--to", sqlscript(out, ""), "--limit", "p:1"}, "--limit is not supported"},
		"parallel-streams":   {[]string{"--to", sqlscript(out, ""), "--parallel-streams", "2"}, "--parallel-streams is not supported"},
		"progress":           {[]string{"--to", sqlscript(out, ""), "--progress"}, "--progress is not supported"},
		"reload":             {[]string{"--to", sqlscript(out, ""), "--overwrite", "reload"}, "does not apply to a script file"},
		"a postgres source":  {[]string{"--from", "postgres://u@127.0.0.1/db", "--to", sqlscript(out, "")}, "from a sqlite:// source"},
		"an unknown scheme":  {[]string{"--from", "nosuch://x", "--to", sqlscript(out, "")}, "--from:"},
		"the same file":      {[]string{"--from", "sqlite://" + out, "--to", sqlscript(out, "")}, "same file"},
		"a missing source":   {[]string{"--from", "sqlite://" + filepath.Join(dir, "missing.db"), "--to", sqlscript(out, "")}, "open --from"},
		"a source with none": {[]string{"--from", other, "--to", sqlscript(out, ""), "--include", "z", "--exclude", "z"}, ""},
	}
	require.NoError(t, os.WriteFile(out, []byte("x"), 0o644))
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"db", "copy"}, c.args...)
			hasFrom := false
			for _, a := range c.args {
				hasFrom = hasFrom || a == "--from"
			}
			if !hasFrom {
				args = append(args, "--from", src)
			}
			// the destination exists in these cases; the checks under test come before that refusal or are not about it
			args = append(args, "--overwrite", "recreate")
			if name == "reload" {
				args = args[:len(args)-2]
			}
			_, _, err := runCopy(t, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestSQLScriptQueryParametersAreNeverDropped(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	// A ";" in a value used to make the parameter vanish without a word: the attribution a licence needs.
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, "&attribution=Data by X; converted by Y"))
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "%3B")
	_, statErr := os.Stat(out)
	assert.True(t, os.IsNotExist(statErr))
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, "&attribution=Data%20by%20X%3B%20converted%20by%20Y"))
	require.NoError(t, err)
	b, _ := os.ReadFile(out)
	assert.Contains(t, string(b), "-- Attribution: Data by X; converted by Y")
	// A parameter given twice is refused, whichever it is.
	for _, query := range []string{"&dialect=postgres", "&name=a&name=b", "&license=MIT&license=GPL"} {
		_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, query), "--overwrite", "recreate")
		require.Error(t, err, query)
		assert.Equal(t, 2, exitCodeOf(t, err), query)
		assert.Contains(t, err.Error(), "times; give it once", query)
	}
}

func TestSQLScriptSameFileIsReportedBeforeAnExistingFile(t *testing.T) {
	src := scriptSource(t)
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(strings.TrimPrefix(src, "sqlite://"), ""))
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "same file")
}

func TestSQLScriptRefusesAHeaderTheMarkerCannotHold(t *testing.T) {
	src := scriptSource(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.sql")
	long := strings.Repeat("x", 256)
	for _, query := range []string{"&name=" + long, "&edition=" + long} {
		stdout, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, query), "--json")
		require.Error(t, err)
		assert.Equal(t, 2, exitCodeOf(t, err))
		assert.Contains(t, stdout.String(), "marker table")
		entries, _ := os.ReadDir(dir)
		assert.Empty(t, entries, "nothing is written or left behind")
	}
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", sqlscript(out, "&name="+long[:255]))
	require.NoError(t, err)
}

func TestSQLScriptJSONReportsUsageErrors(t *testing.T) {
	stdout, _, err := runCopy(t, "db", "copy", "--from", scriptSource(t), "--to", "sqlscript:///x.sql", "--json")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	var rep faithful.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rep))
	assert.Equal(t, "failed", rep.Status)
	assert.Equal(t, 2, rep.ExitCode)
}

func TestSQLScriptOpenFailureIsExit4(t *testing.T) {
	restore := dbcopy.SetSQLOpenerForTest(func(string, string) (*sql.DB, error) { return nil, errors.New("cannot open") })
	defer restore()
	_, _, err := runCopy(t, "db", "copy", "--from", scriptSource(t), "--to", sqlscript(filepath.Join(t.TempDir(), "o.sql"), ""))
	require.Error(t, err)
	assert.Equal(t, 4, exitCodeOf(t, err))
}

func TestSQLScriptExportFailureLeavesNothing(t *testing.T) {
	empty := sqliteFixture(t, `CREATE VIEW v AS SELECT 1`)
	out := filepath.Join(t.TempDir(), "out.sql")
	_, _, err := runCopy(t, "db", "copy", "--from", empty, "--to", sqlscript(out, ""))
	require.Error(t, err)
	assert.Equal(t, 1, exitCodeOf(t, err))
	entries, _ := os.ReadDir(filepath.Dir(out))
	assert.Empty(t, entries, "no half written file and no temporary file")
}

func TestWriteSQLScriptFileSystemFailures(t *testing.T) {
	src := scriptSource(t)
	db, err := sql.Open("sqlite3", strings.TrimPrefix(src, "sqlite://"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	req := faithful.ScriptRequest{Dialect: "mysql", DB: db}
	dir := t.TempDir()
	// The folder cannot be made: a file is in its way.
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	_, err = writeSQLScript(context.Background(), req, filepath.Join(file, "sub", "o.sql"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create the folder")
	// The temporary file cannot be made in a folder that cannot be written.
	ro := filepath.Join(dir, "ro")
	require.NoError(t, os.Mkdir(ro, 0o755))
	require.NoError(t, os.Chmod(ro, 0o500))
	defer func() { _ = os.Chmod(ro, 0o755) }()
	if f, err := os.Create(filepath.Join(ro, "probe")); err == nil { // running as root: the folder is writable anyway
		_ = f.Close()
	} else {
		_, err = writeSQLScript(context.Background(), req, filepath.Join(ro, "o.sql"), false)
		require.Error(t, err)
	}
	// A file that appears between the check and the end is not replaced.
	target := filepath.Join(dir, "taken.sql")
	require.NoError(t, os.WriteFile(target, []byte("mine"), 0o644))
	_, err = writeSQLScript(context.Background(), req, target, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write taken.sql")
	b, _ := os.ReadFile(target)
	assert.Equal(t, "mine", string(b))
	// Replacing a folder fails too.
	folder := filepath.Join(dir, "folder")
	require.NoError(t, os.Mkdir(folder, 0o755))
	_, err = writeSQLScript(context.Background(), req, folder, true)
	require.Error(t, err)
	// A dry run writes nowhere.
	req.DryRun = true
	_, err = writeSQLScript(context.Background(), req, filepath.Join(file, "never"), false)
	require.NoError(t, err)
}

func TestWriteSQLScriptOnAFileSystemWithoutHardLinks(t *testing.T) {
	src := scriptSource(t)
	db, err := sql.Open("sqlite3", strings.TrimPrefix(src, "sqlite://"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	req := faithful.ScriptRequest{Dialect: "mysql", DB: db}
	old := linkFile
	linkFile = func(string, string) error { return errors.New("operation not supported") }
	defer func() { linkFile = old }()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.sql")
	_, err = writeSQLScript(context.Background(), req, out, false)
	require.NoError(t, err)
	b, _ := os.ReadFile(out)
	assert.Contains(t, string(b), "CREATE TABLE `p`")
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "no temporary file is left")
	// A file that is there is not replaced even then.
	_, err = writeSQLScript(context.Background(), req, out, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write out.sql")
	// The copy that fails is removed again: from is a folder, which opens and cannot be read.
	gone := filepath.Join(dir, "gone.sql")
	require.Error(t, putInPlace(dir, gone))
	_, statErr := os.Stat(gone)
	assert.True(t, os.IsNotExist(statErr), "the half-made file is removed")
	// A file that somebody moved onto the name during the copy is not the file this call made: it stays.
	linkFile = func(string, string) error { return errors.New("operation not supported") }
	stolen := filepath.Join(dir, "stolen.sql")
	other := filepath.Join(dir, "other.sql")
	require.NoError(t, os.WriteFile(other, []byte("theirs"), 0o644))
	copyContents = func(io.Writer, io.Reader) (int64, error) {
		require.NoError(t, os.Rename(other, stolen))
		return 0, errors.New("disk full")
	}
	require.Error(t, putInPlace(out, stolen))
	b2, _ := os.ReadFile(stolen)
	assert.Equal(t, "theirs", string(b2), "the file of somebody else is not removed")
	copyContents = io.Copy
	// A source that cannot be opened.
	require.Error(t, putInPlace(filepath.Join(dir, "missing"), gone))
	// Where the link fails with "exists", that is the answer.
	linkFile = func(string, string) error { return os.ErrExist }
	require.ErrorIs(t, putInPlace(out, filepath.Join(dir, "x.sql")), os.ErrExist)
}

func TestCreateExclusiveFailsWithoutRandomness(t *testing.T) {
	old := randomBytes
	randomBytes = func([]byte) error { return errors.New("no entropy") }
	defer func() { randomBytes = old }()
	_, err := createExclusive(t.TempDir(), ".x-")
	require.Error(t, err)
	_, err = writeSQLScript(context.Background(), faithful.ScriptRequest{Dialect: "mysql"}, filepath.Join(t.TempDir(), "o.sql"), false)
	require.Error(t, err)
}

func TestFileSHA256(t *testing.T) {
	_, err := fileSHA256(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
	_, err = fileSHA256(t.TempDir()) // a folder opens and cannot be read
	require.Error(t, err)
	p := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(p, []byte("abc"), 0o644))
	got, err := fileSHA256(p)
	require.NoError(t, err)
	assert.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", got)
	assert.NotEmpty(t, sqlScriptVersion())
	assert.Equal(t, "v1.2.3", versionOf(&debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true))
	assert.Equal(t, "dev", versionOf(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true))
	assert.Equal(t, "dev", versionOf(&debug.BuildInfo{}, true))
	assert.Equal(t, "dev", versionOf(nil, false))
}

var _ = io.Discard
