package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLScriptMSSQLWritesAScriptWithAByteOrderMark(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	_, stderr, err := runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript://"+out+"?dialect=mssql&name=demo&license=MIT&edition=Edition%20X")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db copy: copied 2 tables, 3 rows")
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	script := string(b)
	assert.True(t, strings.HasPrefix(script, "\xef\xbb\xbf-- Edition X\n"), "UTF-8 with a byte order mark, LF line ends")
	assert.NotContains(t, script, "\r")
	for _, want := range []string{"-- Dataset: demo", "-- Licence: MIT", "CREATE TABLE [p]", "INSERT INTO [p] ([id], [name]) VALUES\n(1,N'x'),\n(2,N'y');", "WITH CHECK ADD FOREIGN KEY ([p]) REFERENCES [p] ([id])", "sqlcmd -S <server> -d <database> -X -x -b -f 65001 -i <file>.sql", "dialect=mssql"} {
		assert.Contains(t, script, want)
	}
	// A dry run reports the plan with the T-SQL statements and writes nothing.
	stdout, _, err := runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript://"+filepath.Join(t.TempDir(), "x.sql")+"?dialect=mssql", "--dry-run", "--json")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), `"engine": "mssql"`)
	assert.Contains(t, stdout.String(), "CREATE TABLE [p]")
}
