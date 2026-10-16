package faithful

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files of the MySQL script export")

// edgeSQLite builds the SQLite file of the edge-case fixture (testdata/mysql_edge.sql).
func edgeSQLite(t testing.TB) Endpoint {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "mysql_edge.sql"))
	if err != nil {
		t.Fatal(err)
	}
	e := newSQLite(t)
	execAll(t, e.DB, string(b))
	return e
}

// TestWriteMySQLEdgeFixture writes the fixture to the file named by DATATUG_WRITE_EDGE_FIXTURE; the MySQL
// journey (scripts/check-journey-mysql.sh) loads it.
func TestWriteMySQLEdgeFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_EDGE_FIXTURE")
	if path == "" {
		t.Skip("DATATUG_WRITE_EDGE_FIXTURE is not set")
	}
	e := openSQLite(t, path)
	b, err := os.ReadFile(filepath.Join("testdata", "mysql_edge.sql"))
	if err != nil {
		t.Fatal(err)
	}
	execAll(t, e.DB, string(b))
}

// sqliteWith builds a SQLite database from statements.
func sqliteWith(t testing.TB, stmts ...string) Endpoint {
	t.Helper()
	e := newSQLite(t)
	execAll(t, e.DB, stmts...)
	return e
}

func testHeader() Header {
	return Header{Name: "fixture", Edition: "DemoDB contract edition", License: "MIT", Attribution: "Someone", SourceSHA256: strings.Repeat("ab", 32), Version: "v0.0.0-test"}
}

// exportMySQL runs the whole export of a SQLite endpoint and returns the script and the report.
func exportMySQL(t testing.TB, e Endpoint) (string, *Report) {
	t.Helper()
	var buf bytes.Buffer
	rep, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Display: "sqlite://t", Header: testHeader()}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), rep
}

// planMySQL plans without writing.
func planMySQL(t testing.TB, e Endpoint) *ScriptPlan {
	t.Helper()
	p, err := PlanScript(context.Background(), "mysql", e.DB)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// colType is the MySQL type the plan gives table.column.
func colType(t testing.TB, p *ScriptPlan, table, column string) string {
	t.Helper()
	tb := p.Table(table)
	if tb == nil {
		t.Fatalf("no table %q in the plan", table)
	}
	c := tb.Col(column)
	if c == nil {
		t.Fatalf("no column %q.%q in the plan", table, column)
	}
	return c.Choice.Type
}

func kindCounts(ws []Warning) map[string]int {
	out := map[string]int{}
	for _, w := range ws {
		out[w.Kind]++
	}
	return out
}

func wantWarning(t testing.TB, ws []Warning, kind, objectPart string) {
	t.Helper()
	for _, w := range ws {
		if w.Kind == kind && strings.Contains(w.Object, objectPart) {
			return
		}
	}
	t.Errorf("no %s warning about %q in %v", kind, objectPart, ws)
}

func noWarning(t testing.TB, ws []Warning, kind string) {
	t.Helper()
	for _, w := range ws {
		if w.Kind == kind {
			t.Errorf("unexpected warning %s", w)
		}
	}
}

// golden compares got with the file testdata/<name>, or rewrites it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
		for i := 0; i < len(wl) && i < len(gl); i++ {
			if wl[i] != gl[i] {
				t.Fatalf("%s differs from the output at line %d (run with -update to rewrite):\nwant %.300s\ngot  %.300s", name, i+1, wl[i], gl[i])
			}
		}
		t.Fatalf("%s differs from the output in length (%d lines, want %d); run with -update to rewrite", name, len(gl), len(wl))
	}
}

func jsonOf(t testing.TB, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

var _ = sql.ErrNoRows

func newTestRand() *rand.Rand { return rand.New(rand.NewSource(20261016)) }

// PlanScript2 plans a database made of the statements and returns the error.
func PlanScript2(t testing.TB, stmts ...string) (*ScriptPlan, error) {
	t.Helper()
	return PlanScript(context.Background(), "mysql", sqliteWith(t, stmts...).DB)
}

// fuzzAtoms are the pieces hostile text is made of: what ends a literal, starts a client command, a comment or
// a line, or that a server or a client reads differently from what it is.
var fuzzAtoms = []string{"'", "''", "\\", "\\'", "\n", "\r", "\r\n", "\x1a", "\x7f", "\t", "\u0085", " ", " ", "GO", "go", "\\!", "\\g", "\\G", "source ", "\\. ", "/", "--", "-- ", "#", "/*", "*/", "/*!50000", ";", "\\;", "`", "\"",
	"$(", "${", "%s", "%", "_", "😀", "𝄞", "é", "日本", " ", "  ", "0", "x'41'", "X'", "_utf8mb4 X'", "'; DROP TABLE t; --", "DELIMITER //", "\\d", "use mysql;", "exit", "quit", "\\q", "tee /tmp/x", "\\T x", "prompt", "\\u", "NULL", "?", "@a", "�"}

// fuzzString is a random hostile string.
func fuzzString(rng *rand.Rand) string {
	var b strings.Builder
	for n := rng.Intn(7); n >= 0; n-- {
		b.WriteString(fuzzAtoms[rng.Intn(len(fuzzAtoms))])
	}
	return b.String()
}

// writeFuzzFixture fills a SQLite database with rows of hostile text, bytes and names.
func writeFuzzFixture(t testing.TB, e Endpoint, rows int) {
	t.Helper()
	rng := rand.New(rand.NewSource(20261016))
	execAll(t, e.DB, `CREATE TABLE fuzz (id INTEGER PRIMARY KEY, v TEXT, k TEXT UNIQUE, b BLOB, n REAL, d TEXT DEFAULT 'x')`)
	seen := map[string]bool{}
	for i := 1; i <= rows; i++ {
		clean := strings.ToValidUTF8(strings.ReplaceAll(fuzzString(rng), "\x00", ""), "?")
		key := fmt.Sprintf("%s#%d", clean, i)
		for seen[key] {
			key += "x"
		}
		seen[key] = true
		raw := make([]byte, rng.Intn(12))
		rng.Read(raw)
		raw = append([]byte(fuzzString(rng)), raw...)
		if _, err := e.DB.Exec(`INSERT INTO fuzz VALUES (?, ?, ?, ?, ?, ?)`, i, clean, key, raw, rng.NormFloat64()*1e10, fuzzString(rng)); err != nil {
			t.Fatal(err)
		}
	}
	// Names: every column and table below is a name a script could be fooled by.
	for i := 0; i < 8; i++ {
		name := fuzzString(rng) + fmt.Sprint(i)
		q := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		execAll(t, e.DB, `CREATE TABLE `+q+` (`+q+` INTEGER PRIMARY KEY, `+`"c`+fmt.Sprint(i)+`" TEXT)`, `INSERT INTO `+q+` VALUES (1, 'x')`)
	}
}

// TestWriteMySQLFuzzFixture writes the hostile fixture to the file named by DATATUG_WRITE_FUZZ_FIXTURE; the MySQL
// journey loads it into a real server.
func TestWriteMySQLFuzzFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_FUZZ_FIXTURE")
	if path == "" {
		t.Skip("DATATUG_WRITE_FUZZ_FIXTURE is not set")
	}
	writeFuzzFixture(t, openSQLite(t, path), 4000)
}

// The same hostile data, exported without a server: no data byte starts a line.
func TestFuzzFixtureScriptHasNoLineStartedByData(t *testing.T) {
	e := newSQLite(t)
	writeFuzzFixture(t, e, 600)
	script, _ := exportMySQL(t, e)
	checkLines(t, script)
}
