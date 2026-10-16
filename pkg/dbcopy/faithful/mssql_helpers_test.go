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

// mssqlEdgeSQLite builds the SQLite file of the SQL Server edge-case fixture (testdata/mssql_edge.sql).
func mssqlEdgeSQLite(t testing.TB) Endpoint {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "mssql_edge.sql"))
	if err != nil {
		t.Fatal(err)
	}
	e := newSQLite(t)
	execAll(t, e.DB, string(b))
	return e
}

// TestWriteMSSQLEdgeFixture writes the fixture to the file named by DATATUG_WRITE_EDGE_FIXTURE_MSSQL; the SQL
// Server journey (scripts/check-journey-mssql.sh) loads it.
func TestWriteMSSQLEdgeFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_EDGE_FIXTURE_MSSQL")
	if path == "" {
		t.Skip("DATATUG_WRITE_EDGE_FIXTURE_MSSQL is not set")
	}
	b, err := os.ReadFile(filepath.Join("testdata", "mssql_edge.sql"))
	if err != nil {
		t.Fatal(err)
	}
	execAll(t, openSQLite(t, path).DB, string(b))
}

// exportMSSQL runs the whole export of a SQLite endpoint and returns the script and the report.
func exportMSSQL(t testing.TB, e Endpoint) (string, *Report) {
	t.Helper()
	var buf bytes.Buffer
	rep, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mssql", DB: e.DB, Display: "sqlite://t", Header: testHeader()}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), rep
}

// planMSSQL plans a database made of statements.
func planMSSQL(t testing.TB, stmts ...string) *ScriptPlan {
	t.Helper()
	p, err := PlanScript(context.Background(), "mssql", sqliteWith(t, stmts...).DB)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// mssqlFuzzAtoms are the pieces hostile text is made of for SQL Server: what ends a literal, what sqlcmd reads as a
// command or a variable at the start of a line, and what a client or the T-SQL parser reads differently.
var mssqlFuzzAtoms = []string{"'", "''", "\"", "]", "[", "]]", "\n", "\r", "\r\n", "\x1a", "\x7f", "\t", "\u0085", "\u2028", "\u2029", "\ufeff", "\ufffd", " ", " ", "  ",
	"GO", "go", "Go", "GO 5", ":r x", ":!! cmd", "!! cmd", ":setvar a b", ":on error exit", ":quit", "EXIT", "QUIT", "RESET", "ED", "$(x)", "$(", "${x}", "$ (", "%s", "%", "_", "[a-z]",
	"😀", "𝄞", "é", "日本", "0", "0x41", "N'", "'; DROP TABLE t; --", "-- c", "/*", "*/", ";", "\\", "\\n", "NULL", "?", "@a", "NCHAR(10)", "'+NCHAR(10)+'", "THROW 50000,'x',1;", "SET NOEXEC ON", "COMMIT", "BEGIN TRANSACTION", "\ufffe"}

func mssqlFuzzString(rng *rand.Rand) string {
	var b strings.Builder
	for n := rng.Intn(7); n >= 0; n-- {
		b.WriteString(mssqlFuzzAtoms[rng.Intn(len(mssqlFuzzAtoms))])
	}
	return b.String()
}

// writeMSSQLFuzzFixture fills a SQLite database with rows of hostile text, bytes and names.
func writeMSSQLFuzzFixture(t testing.TB, e Endpoint, rows int) {
	t.Helper()
	rng := rand.New(rand.NewSource(20261017))
	execAll(t, e.DB, `CREATE TABLE fuzz (id INTEGER PRIMARY KEY, v TEXT, k TEXT UNIQUE, b BLOB, n REAL, d TEXT DEFAULT 'x')`)
	seen := map[string]bool{}
	for i := 1; i <= rows; i++ {
		clean := strings.ToValidUTF8(strings.ReplaceAll(mssqlFuzzString(rng), "\x00", ""), "?")
		key := fmt.Sprintf("%s#%d", clean, i)
		for seen[key] {
			key += "x"
		}
		seen[key] = true
		raw := make([]byte, rng.Intn(12))
		rng.Read(raw)
		raw = append([]byte(mssqlFuzzString(rng)), raw...)
		if _, err := e.DB.Exec(`INSERT INTO fuzz VALUES (?, ?, ?, ?, ?, ?)`, i, clean, key, raw, rng.NormFloat64()*1e10, mssqlFuzzString(rng)); err != nil {
			t.Fatal(err)
		}
	}
	// A text with many special characters goes through the hex form; one with a few through pieces.
	many := strings.Repeat("a\nb\r", 400) + "GO\n:r x\n$(x)"
	if _, err := e.DB.Exec(`INSERT INTO fuzz VALUES (?, ?, ?, ?, ?, ?)`, rows+1, many, "many", []byte{}, 0.5, many); err != nil {
		t.Fatal(err)
	}
	// Names: every column and table below is a name a script could be fooled by.
	for i := 0; i < 8; i++ {
		name := mssqlFuzzString(rng) + fmt.Sprint(i)
		q := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		execAll(t, e.DB, `CREATE TABLE `+q+` (`+q+` INTEGER PRIMARY KEY, `+`"c`+fmt.Sprint(i)+`" TEXT)`, `INSERT INTO `+q+` VALUES (1, 'x')`)
	}
}

// TestWriteMSSQLFuzzFixture writes the hostile fixture to the file named by DATATUG_WRITE_FUZZ_FIXTURE_MSSQL; the
// SQL Server journey loads it into a real server.
func TestWriteMSSQLFuzzFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_FUZZ_FIXTURE_MSSQL")
	if path == "" {
		t.Skip("DATATUG_WRITE_FUZZ_FIXTURE_MSSQL is not set")
	}
	writeMSSQLFuzzFixture(t, openSQLite(t, path), 4000)
}

// writeCascadeFixture fills a SQLite database with schemas of tables whose foreign keys carry random actions: SQL
// Server refuses a key that makes a cycle or a second cascade path (error 1785), so the script must downgrade
// exactly enough of them. Each schema is a set of tables s<k>_t<i> that only reference each other.
func writeCascadeFixture(t testing.TB, e Endpoint, schemas int) {
	t.Helper()
	rng := rand.New(rand.NewSource(20261018))
	acts := []string{"NO ACTION", "CASCADE", "SET NULL", "CASCADE"}
	for k := 0; k < schemas; k++ {
		n := 2 + rng.Intn(7)
		for i := 0; i < n; i++ {
			cols, fks := "id INTEGER PRIMARY KEY", ""
			for j := 0; j < rng.Intn(4); j++ {
				cols += fmt.Sprintf(", c%d INTEGER", j)
				if rng.Intn(4) > 0 {
					fks += fmt.Sprintf(", FOREIGN KEY (c%d) REFERENCES s%d_t%d(id) ON DELETE %s ON UPDATE %s", j, k, rng.Intn(n), acts[rng.Intn(4)], acts[rng.Intn(4)])
				}
			}
			execAll(t, e.DB, fmt.Sprintf("CREATE TABLE s%d_t%d (%s%s)", k, i, cols, fks), fmt.Sprintf("INSERT INTO s%d_t%d (id) VALUES (1), (2)", k, i))
		}
	}
}

// TestWriteMSSQLCascadeFixture writes the cascade fixture to the file named by DATATUG_WRITE_CASCADE_FIXTURE_MSSQL;
// the SQL Server journey loads it into a real server.
func TestWriteMSSQLCascadeFixture(t *testing.T) {
	path := os.Getenv("DATATUG_WRITE_CASCADE_FIXTURE_MSSQL")
	if path == "" {
		t.Skip("DATATUG_WRITE_CASCADE_FIXTURE_MSSQL is not set")
	}
	writeCascadeFixture(t, openSQLite(t, path), 80)
}

// The cascade fixture plans with no action SQL Server refuses, schema by schema.
func TestMSSQLCascadeFixtureIsPlannedWithoutARefusedAction(t *testing.T) {
	e := newSQLite(t)
	writeCascadeFixture(t, e, 40)
	p, err := PlanScript(context.Background(), "mssql", e.DB)
	if err != nil {
		t.Fatal(err)
	}
	bySchema := map[string][]cascadeEdge{}
	downgraded := 0
	for _, tb := range p.Tables {
		schema := tb.Name[:strings.Index(tb.Name, "_")]
		for _, fk := range tb.FKs {
			bySchema[schema] = append(bySchema[schema], cascadeEdge{fk.RefTable, tb.Name, fk.OnDelete, fk.OnUpdate})
		}
	}
	for schema, edges := range bySchema {
		if bruteRefusal(edges) {
			t.Errorf("schema %s keeps actions SQL Server refuses: %v", schema, edges)
		}
	}
	for _, w := range p.Warnings {
		if w.Kind == WarnFKActionNotCarried {
			downgraded++
		}
	}
	if downgraded < 10 {
		t.Errorf("the fixture is meant to make the analysis work: %d downgrades", downgraded)
	}
}
