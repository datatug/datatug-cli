package faithful

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oracleEdgeSQLite builds the SQLite file of the Oracle edge-case fixture (testdata/oracle_edge.sql).
func oracleEdgeSQLite(t testing.TB) Endpoint {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "oracle_edge.sql"))
	if err != nil {
		t.Fatal(err)
	}
	e := newSQLite(t)
	execAll(t, e.DB, string(b))
	return e
}

// TestWriteOracleEdgeFixture writes the fixture to the file named by DATATUG_WRITE_EDGE_FIXTURE_ORACLE; the Oracle
// journey (scripts/check-journey-oracle.sh) loads it.
func TestWriteOracleEdgeFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_EDGE_FIXTURE_ORACLE")
	if path == "" {
		t.Skip("DATATUG_WRITE_EDGE_FIXTURE_ORACLE is not set")
	}
	b, err := os.ReadFile(filepath.Join("testdata", "oracle_edge.sql"))
	if err != nil {
		t.Fatal(err)
	}
	execAll(t, openSQLite(t, path).DB, string(b))
}

// exportOracle runs the whole export of a SQLite endpoint and returns the script and the report.
func exportOracle(t testing.TB, e Endpoint) (string, *Report) {
	t.Helper()
	var buf bytes.Buffer
	rep, err := ExportScript(context.Background(), ScriptRequest{Dialect: "oracle", DB: e.DB, Display: "sqlite://t", Header: testHeader()}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), rep
}

// planOracle plans a database made of statements.
func planOracle(t testing.TB, stmts ...string) *ScriptPlan {
	t.Helper()
	p, err := PlanScript(context.Background(), "oracle", sqliteWith(t, stmts...).DB)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// planOracleErr plans a database made of statements and returns the error.
func planOracleErr(t testing.TB, stmts ...string) (*ScriptPlan, error) {
	t.Helper()
	return PlanScript(context.Background(), "oracle", sqliteWith(t, stmts...).DB)
}

// oracleFuzzAtoms are the pieces hostile text is made of for Oracle: what ends a literal, what SQL*Plus reads as a command
// or a variable at the start of a line or inside a statement, and what a client or the SQL parser reads differently.
var oracleFuzzAtoms = []string{"'", "''", "\"", "\n", "\r", "\r\n", "\x1a", "\x7f", "\t", "\u0085", "\u2028", "\u2029", "\ufeff", "\ufffd", "\ufffe", "\uffff", " ", " ", "  ",
	"/", "\n/", "/\n", "@", "@@x", "@/etc/x", "EXIT", "exit", "QUIT", "SET DEFINE ON", "SET ", "#", "REM x", "PROMPT x", "HOST id", "!id", "WHENEVER SQLERROR CONTINUE", "START x", "ED", "RUN", "GET x",
	"&x", "&&y", "&1", "&name.", "&", "$(x)", "${x}", "--", "-- c", "/*", "*/", ";", ";\n", "\\", "\\n", "\\0085", "||", "'||CHR(10)||'", "CHR(10)", "UNISTR('\\0041')", "TO_CLOB(", "NULL", "?", ":a",
	"; DROP TABLE t; --", "END;", "BEGIN NULL; END;", "COMMIT", "😀", "𝄞", "é", "日本", "0", "0x41", "x'41'", "%s", "%", "_", "[a-z]"}

func oracleFuzzString(rng *rand.Rand) string {
	var b strings.Builder
	for n := rng.Intn(7); n >= 0; n-- {
		b.WriteString(oracleFuzzAtoms[rng.Intn(len(oracleFuzzAtoms))])
	}
	return b.String()
}

// writeOracleFuzzFixture fills a SQLite database with rows of hostile text, bytes and names.
func writeOracleFuzzFixture(t testing.TB, e Endpoint, rows int) {
	t.Helper()
	rng := rand.New(rand.NewSource(20261019))
	execAll(t, e.DB, `CREATE TABLE fuzz (id INTEGER PRIMARY KEY, v TEXT, k TEXT UNIQUE, b BLOB, n REAL, d TEXT DEFAULT 'x', c VARCHAR(30))`)
	seen := map[string]bool{}
	for i := 1; i <= rows; i++ {
		clean := strings.ToValidUTF8(strings.ReplaceAll(oracleFuzzString(rng), "\x00", ""), "?")
		key := fmt.Sprintf("%s#%d", clean, i)
		for seen[key] {
			key += "x"
		}
		seen[key] = true
		raw := make([]byte, rng.Intn(12))
		rng.Read(raw)
		raw = append([]byte(oracleFuzzString(rng)), raw...)
		short := []rune(strings.ToValidUTF8(strings.ReplaceAll(oracleFuzzString(rng), "\x00", ""), "?"))
		if len(short) > 30 {
			short = short[:30]
		}
		if _, err := e.DB.Exec(`INSERT INTO fuzz VALUES (?, ?, ?, ?, ?, ?, ?)`, i, clean, key, raw, rng.NormFloat64()*1e10, oracleFuzzString(rng), string(short)); err != nil {
			t.Fatal(err)
		}
	}
	// A text with many special characters is one long expression; one over 4,000 bytes is a CLOB, written in groups.
	many := strings.Repeat("a\nb\r", 400) + "/\n@x\n&y"
	if _, err := e.DB.Exec(`INSERT INTO fuzz VALUES (?, ?, ?, ?, ?, ?, ?)`, rows+1, many, "many", []byte{}, 0.5, many, "x"); err != nil {
		t.Fatal(err)
	}
	// Names: every column and table below is a name a script could be fooled by.
	for i := 0; i < 8; i++ {
		name := oracleFuzzString(rng) + fmt.Sprint(i)
		q := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		execAll(t, e.DB, `CREATE TABLE `+q+` (`+q+` INTEGER PRIMARY KEY, `+`"c`+fmt.Sprint(i)+`" TEXT)`, `INSERT INTO `+q+` VALUES (1, 'x')`)
	}
}

// TestWriteOracleFuzzFixture writes the hostile fixture to the file named by DATATUG_WRITE_FUZZ_FIXTURE_ORACLE; the Oracle
// journey loads it into a real server.
func TestWriteOracleFuzzFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_FUZZ_FIXTURE_ORACLE")
	if path == "" {
		t.Skip("DATATUG_WRITE_FUZZ_FIXTURE_ORACLE is not set")
	}
	writeOracleFuzzFixture(t, openSQLite(t, path), 4000)
}

// TestWriteOracleCascadeFixture writes the cascade fixture (80 schemas of tables whose foreign keys carry random actions)
// to the file named by DATATUG_WRITE_CASCADE_FIXTURE_ORACLE: Oracle takes cycles and several cascade paths, so every
// script must load with the actions it carries.
func TestWriteOracleCascadeFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_CASCADE_FIXTURE_ORACLE")
	if path == "" {
		t.Skip("DATATUG_WRITE_CASCADE_FIXTURE_ORACLE is not set")
	}
	writeCascadeFixture(t, openSQLite(t, path), 80)
}
