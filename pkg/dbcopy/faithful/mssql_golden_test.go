package faithful

import (
	"regexp"
	"strings"
	"testing"
)

func TestMSSQLEdgeFixtureGolden(t *testing.T) {
	script, rep := exportMSSQL(t, mssqlEdgeSQLite(t))
	golden(t, "mssql_edge.golden.sql", script)
	golden(t, "mssql_edge.warnings.json", jsonOf(t, rep.Warnings))
	if rep.Status != "ok-with-warnings" || rep.To.Engine != "mssql" || rep.Totals.Tables != 69 {
		t.Errorf("report: %s %s %+v", rep.Status, rep.To.Engine, rep.Totals)
	}
}

func TestMSSQLScriptIsDeterministic(t *testing.T) {
	a, _ := exportMSSQL(t, mssqlEdgeSQLite(t))
	b, _ := exportMSSQL(t, mssqlEdgeSQLite(t))
	if a != b {
		t.Fatal("two exports of the same database are not the same bytes")
	}
}

// mssqlAllowedLine is what a line of a script may start with: only tokens the generator wrote.
var mssqlAllowedLine = regexp.MustCompile(`^(\x{feff}?-- |--$|$|SET |GO$|BEGIN TRANSACTION;$|IF @@TRANCOUNT=0 SET NOEXEC ON$|CREATE |INSERT INTO |\(|ALTER TABLE |DBCC CHECKIDENT |COMMIT TRANSACTION;$|IF \(SELECT COUNT_BIG|  \[|  PRIMARY KEY |  CONSTRAINT |  UNIQUE |\);$)`)

// sqlcmdCommand is what sqlcmd reads as a command at the start of a line (leading blanks do not matter):
// GO, EXIT, QUIT, RESET, ED, anything that starts with a colon, and !!.
var sqlcmdCommand = regexp.MustCompile(`(?i)^\s*(go|exit|quit|reset|ed)\b|^\s*:|^\s*!!`)

// checkMSSQLLines asserts that every line of a script starts with a token the generator wrote, that the only
// line sqlcmd would read as a command is a GO line the generator wrote, and that no raw control character is
// in it, so no data byte can start a line.
func checkMSSQLLines(t testing.TB, script string) {
	t.Helper()
	if strings.ContainsAny(script, "\r\x00") {
		t.Fatal("the script holds a raw CR or NUL")
	}
	if !strings.HasPrefix(script, "\xef\xbb\xbf-- ") {
		t.Fatal("the script does not start with a byte order mark")
	}
	for i, l := range strings.Split(script, "\n") {
		if !mssqlAllowedLine.MatchString(l) {
			t.Fatalf("line %d starts with a token the generator did not write: %.80q", i+1, l)
		}
		if sqlcmdCommand.MatchString(l) && l != "GO" {
			t.Fatalf("line %d would be read as a sqlcmd command: %.80q", i+1, l)
		}
		if strings.ContainsAny(l, "\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f\x1a\x1b\x7f\u0085\u2028\u2029") {
			t.Fatalf("line %d holds a raw control or line-break character: %.80q", i+1, l)
		}
	}
}

func TestMSSQLEdgeFixtureNoDataByteStartsALine(t *testing.T) {
	script, _ := exportMSSQL(t, mssqlEdgeSQLite(t))
	checkMSSQLLines(t, script)
}

func TestMSSQLFuzzFixtureNoDataByteStartsALine(t *testing.T) {
	e := newSQLite(t)
	writeMSSQLFuzzFixture(t, e, 600)
	script, _ := exportMSSQL(t, e)
	checkMSSQLLines(t, script)
	// Every row of the fuzz table is one line that starts with a parenthesis.
	rows := 0
	for _, l := range strings.Split(script, "\n") {
		if strings.HasPrefix(l, "(") {
			rows++
		}
	}
	if rows < 600 {
		t.Errorf("only %d row lines", rows)
	}
}
