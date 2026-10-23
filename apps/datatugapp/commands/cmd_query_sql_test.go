package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqlQueryDB is a SQLite file with item(id, name) holding two rows, as a sqlite:// URL.
func sqlQueryDB(t *testing.T) string {
	t.Helper()
	return "sqlite://" + covCSQLiteFile(t, "items.sqlite", `CREATE TABLE item(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO item VALUES (1, 'pen'), (2, 'lamp')`)
}

func TestQuerySQL_RunsTheTextAgainstTheDatabase(t *testing.T) {
	db := sqlQueryDB(t)
	stdout, stderr, code := runQuery(t, "", "--db", db, "--sql", "SELECT id, name FROM item ORDER BY id", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	rows := withoutKey(decodeObjects(t, stdout))
	assert.Equal(t, []map[string]any{{"id": float64(1), "name": "pen"}, {"id": float64(2), "name": "lamp"}}, rows)
	assert.Contains(t, stderr, "access: running without access policies")

	// '-' reads the text from stdin, and -q keeps stderr to what is not a limitation.
	stdout, stderr, code = runQuery(t, "SELECT name FROM item WHERE id = 2\n", "--db", db, "--sql", "-", "--format", "json", "--no-policies", "-q")
	require.Zero(t, code, stderr)
	assert.Equal(t, []map[string]any{{"name": "lamp"}}, withoutKey(decodeObjects(t, stdout)))
}

func TestQuerySQL_FlagsThatDoNotGoWithIt(t *testing.T) {
	db := sqlQueryDB(t)
	for name, c := range map[string]struct {
		argv []string
		want string
	}{
		"with -f":          {[]string{"--db", db, "--sql", "SELECT 1", "-f", "q.yaml"}, "-f/--file, --from and --sql are mutually exclusive"},
		"with --from":      {[]string{"--db", db, "--sql", "SELECT 1", "--from", "item"}, "-f/--file, --from and --sql are mutually exclusive"},
		"with --project":   {[]string{"--project", "p", "--query", "q", "--sql", "SELECT 1"}, "--project/--query and --db/-f/--from/--sql are mutually exclusive"},
		"with --result-id": {[]string{"--db", db, "--sql", "SELECT 1", "--result-id", "r1"}, "--result-id is not supported with --sql yet"},
		"without a query":  {[]string{"--db", db}, "one of -f/--file, --from or --sql is required"},
		"empty":            {[]string{"--db", db, "--sql", "  "}, "--sql is empty"},
		"empty stdin":      {[]string{"--db", db, "--sql", "-"}, "--sql is empty"},
		"not a source":     {[]string{"--db", "nope://x", "--sql", "SELECT 1"}, "nope"},
		"not a reference":  {[]string{"--db", "datatug://%zz/db", "--sql", "SELECT 1"}, "datatug"},
		"no policy file":   {[]string{"--db", db, "--sql", "SELECT 1", "--policy", "/nonexistent/policy.yaml"}, "policy"},
	} {
		_, stderr, code := runQuery(t, "", c.argv...)
		assert.Equal(t, exitCodeUsage, code, name)
		assert.Contains(t, stderr, c.want, name)
	}
}

// SQL text is not read through the row and column rules of policies: with policies loaded it is
// refused, and the person is told to run it with --no-policies.
func TestQuerySQL_WithPoliciesLoadedIsRefused(t *testing.T) {
	_, stderr, code := runQuery(t, "", "--db", sqlQueryDB(t), "--sql", "SELECT id FROM item", "--policies-dir", globalPolicyDir(t), "--as", "alice")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "run it with --no-policies")
}

func TestQuerySQL_FailuresKeepTheirExitCodes(t *testing.T) {
	missing := "sqlite://" + filepath.Join(t.TempDir(), "missing.sqlite")
	_, stderr, code := runQuery(t, "", "--db", missing, "--sql", "SELECT 1", "--no-policies")
	assert.Equal(t, exitCodeDatabase, code, stderr)

	_, stderr, code = runQuery(t, "", "--db", setupQueryDB(t), "--sql", "SELECT 1", "--no-policies")
	assert.Equal(t, exitCodeUsage, code, "inGitDB runs no SQL text: %s", stderr)
}

// A datatug:// reference runs the SQL against the source the project names.
func TestQuerySQL_ADatatugReference(t *testing.T) {
	tempDatatugHome(t)
	orderProject(t, orderItemDB(t), "")
	stdout, stderr, code := runQuery(t, "", "--db", "datatug://covc/main?env=local", "--sql", "SELECT id FROM Item ORDER BY id", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	assert.Equal(t, []map[string]any{{"id": float64(1)}, {"id": float64(2)}}, withoutKey(decodeObjects(t, stdout)))

	_, stderr, code = runQuery(t, "", "--db", "datatug://nobody/main?env=local", "--sql", "SELECT 1", "--no-policies")
	assert.NotZero(t, code)
	assert.NotEmpty(t, stderr)
}

// On a server source the text must be one query: anything else is refused before it reaches the server.
func TestQuerySQL_AServerSource(t *testing.T) {
	standInForMySQL(t)
	t.Setenv("DATATUG_CMD_SQL_URL", "mysql://alice:"+serverSourceSecret+"@db.example.com/shop")
	stdout, stderr, code := runQuery(t, "", "--db", "env:DATATUG_CMD_SQL_URL", "--sql", "SELECT id, name FROM customer WHERE id = 1", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	assert.Equal(t, []map[string]any{{"id": float64(1), "name": "Ada"}}, withoutKey(decodeObjects(t, stdout)))

	_, stderr, code = runQuery(t, "", "--db", "env:DATATUG_CMD_SQL_URL", "--sql", "DELETE FROM customer", "--no-policies")
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, stderr, "SELECT or WITH")
	assert.NotContains(t, stdout+stderr, serverSourceSecret)
}

type sqlStdinGone struct{}

func (sqlStdinGone) Read([]byte) (int, error) { return 0, errors.New("stdin is gone") }

type sqlStdoutGone struct{}

func (sqlStdoutGone) Write([]byte) (int, error) { return 0, errors.New("stdout is gone") }

// runQueryWith runs `query run` with stdin and stdout of its own.
func runQueryWith(stdin io.Reader, stdout io.Writer, argv ...string) error {
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(queryCommand())
	root.SetOut(stdout)
	root.SetErr(&bytes.Buffer{})
	root.SetIn(stdin)
	root.SetArgs(append([]string{"query", "run"}, argv...))
	return root.ExecuteContext(context.Background())
}

func TestQuerySQL_StdinAndStdoutFailures(t *testing.T) {
	db := sqlQueryDB(t)
	err := runQueryWith(sqlStdinGone{}, &bytes.Buffer{}, "--db", db, "--sql", "-", "--no-policies")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read --sql from stdin: stdin is gone")

	err = runQueryWith(strings.NewReader(""), sqlStdoutGone{}, "--db", db, "--sql", "SELECT id FROM item", "--format", "json", "--no-policies")
	require.Error(t, err)
	var coder ExitCoder
	require.True(t, errors.As(err, &coder))
	assert.Equal(t, 1, coder.ExitCode())
}

// runSQLQueryCommand runs with a context of its own when the command has none.
func TestQuerySQL_WithoutAContext(t *testing.T) {
	var cmd *cobra.Command
	for _, sub := range queryCommand().Commands() {
		if sub.Name() == "run" {
			cmd = sub
		}
	}
	require.NotNil(t, cmd)
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.ParseFlags([]string{"--db", sqlQueryDB(t), "--sql", "SELECT id FROM item WHERE id = 1", "--format", "json", "--no-policies"}))
	o, err := readQueryOptions(cmd)
	require.NoError(t, err)
	require.NoError(t, runSQLQueryCommand(cmd, o))
	assert.Equal(t, []map[string]any{{"id": float64(1)}}, withoutKey(decodeObjects(t, out.String())))
}
