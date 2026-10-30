package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLScriptOracleWritesAScriptWithoutAByteOrderMark(t *testing.T) {
	src := scriptSource(t)
	out := filepath.Join(t.TempDir(), "out.sql")
	_, stderr, err := runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript://"+out+"?dialect=oracle&name=demo&license=MIT&edition=Edition%20X")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db copy: copied 2 tables, 3 rows")
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	script := string(b)
	assert.True(t, strings.HasPrefix(script, "-- Edition X\n"), "UTF-8 without a byte order mark, LF line ends")
	assert.NotContains(t, script, "\r")
	for _, want := range []string{"-- Dataset: demo", "-- Licence: MIT", `CREATE TABLE "p"`, "INSERT INTO \"p\" (\"id\", \"name\") VALUES\n(1,'x'),\n(2,'y');\nCOMMIT;", `ADD FOREIGN KEY ("p") REFERENCES "p" ("id")`, "NLS_LANG=.AL32UTF8 sqlplus -L -S <user>/<password>@<service> @<file>.sql", "dialect=oracle", "WHENEVER SQLERROR EXIT FAILURE ROLLBACK"} {
		assert.Contains(t, script, want)
	}
	// A dry run reports the plan with the SQL statements and writes nothing.
	stdout, _, err := runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript://"+filepath.Join(t.TempDir(), "x.sql")+"?dialect=oracle", "--dry-run", "--json")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), `"engine": "oracle"`)
	assert.Contains(t, stdout.String(), `CREATE TABLE \"p\"`)
	// A name the marker cannot hold is refused before anything is written.
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", "sqlscript://"+filepath.Join(t.TempDir(), "y.sql")+"?dialect=oracle&name="+strings.Repeat("x", 4001))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the marker column takes at most 4000")
}
