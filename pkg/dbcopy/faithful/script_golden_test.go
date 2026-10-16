package faithful

import (
	"regexp"
	"strings"
	"testing"
)

func TestEdgeFixtureGolden(t *testing.T) {
	script, rep := exportMySQL(t, edgeSQLite(t))
	golden(t, "mysql_edge.golden.sql", script)
	golden(t, "mysql_edge.warnings.json", jsonOf(t, rep.Warnings))
	if rep.Status != "ok-with-warnings" || rep.To.Engine != "mysql" || rep.Totals.Tables != 46 {
		t.Errorf("report: %s %s %+v", rep.Status, rep.To.Engine, rep.Totals)
	}
}

func TestScriptIsDeterministic(t *testing.T) {
	a, _ := exportMySQL(t, edgeSQLite(t))
	b, _ := exportMySQL(t, edgeSQLite(t))
	if a != b {
		t.Fatal("two exports of the same database are not the same bytes")
	}
	// A table without a primary key has a total order: its rows come in rowid order, whatever the data.
	e := sqliteWith(t, `CREATE TABLE t (a TEXT, b INTEGER)`, `INSERT INTO t VALUES ('x', 1), ('x', 1), ('b', 2), (NULL, NULL)`)
	s1, _ := exportMySQL(t, e)
	s2, _ := exportMySQL(t, e)
	if s1 != s2 || !strings.Contains(s1, "('x',1),\n('x',1),\n('b',2),\n(NULL,NULL);") {
		t.Errorf("rows of a table without a key:\n%s", s1)
	}
}

// allowedLine is what a line of a script may start with: only tokens the generator wrote.
var allowedLine = regexp.MustCompile(`^(-- |--$|$|SET |CREATE |INSERT INTO |\(|START TRANSACTION;|COMMIT;|ALTER TABLE |  |\) ENGINE=)`)

// checkLines asserts that every line of a script starts with a token the generator wrote and that no
// carriage return, NUL or other raw control character is in it, so no data byte can start a line.
func checkLines(t testing.TB, script string) {
	t.Helper()
	if strings.ContainsAny(script, "\r\x00") {
		t.Fatal("the script holds a raw CR or NUL")
	}
	for i, l := range strings.Split(script, "\n") {
		if !allowedLine.MatchString(l) {
			t.Fatalf("line %d starts with a token the generator did not write: %.80q", i+1, l)
		}
		if strings.ContainsAny(l, "\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f\x1a\x1b\x7f") {
			t.Fatalf("line %d holds a raw control character: %.80q", i+1, l)
		}
	}
}

func TestEdgeFixtureNoDataByteStartsALine(t *testing.T) {
	script, _ := exportMySQL(t, edgeSQLite(t))
	checkLines(t, script)
	for _, bad := range []string{"\nGO", "\\! echo pwned", "source /etc/passwd", "DROP DATABASE x;"} {
		for i, l := range strings.Split(script, "\n") {
			if strings.HasPrefix(l, bad) || strings.HasPrefix(l, strings.TrimLeft(bad, "\n")) {
				t.Errorf("line %d starts with data %q: %.80q", i+1, bad, l)
			}
		}
	}
}
