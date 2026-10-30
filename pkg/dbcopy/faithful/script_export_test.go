package faithful

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExportDryRunAndFilters(t *testing.T) {
	e := sqliteWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY, n TEXT)`, `CREATE TABLE b (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a(id))`, `CREATE INDEX b_a ON b (a_id)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY, v INTEGER CHECK (v > 0))`, `INSERT INTO a VALUES (1, 'x')`, `INSERT INTO b VALUES (1, 1)`)
	var buf bytes.Buffer
	rep, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Display: "sqlite://t", DryRun: true}, &buf)
	if err != nil || buf.Len() != 0 || rep.Status != "dry-run" || !rep.DryRun {
		t.Fatalf("%v %d %+v", err, buf.Len(), rep)
	}
	joined := strings.Join(rep.Plan, "\n")
	for _, want := range []string{"CREATE TABLE `a`", "CREATE INDEX `b_a`", "ADD CHECK (`v` > 0)", "FOREIGN KEY (`a_id`) REFERENCES `a` (`id`)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the plan lacks %s:\n%s", want, joined)
		}
	}
	var out bytes.Buffer
	rep.WriteText(&out)
	if !strings.Contains(out.String(), "would copy 3 tables") {
		t.Errorf("%s", out.String())
	}
	// --exclude: a key into a table that is not written is not created, and a warning says so.
	rep, err = ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, DryRun: true, Exclude: []string{"a"}}, io.Discard)
	if err != nil || rep.Totals.Tables != 2 || rep.Totals.ForeignKeys != 0 || !hasWarning(rep, WarnFKNotCarried, "b") {
		t.Fatalf("%v %+v %v", err, rep.Totals, rep.Warnings)
	}
	rep, err = ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, DryRun: true, Include: []string{"c"}}, io.Discard)
	if err != nil || rep.Totals.Tables != 1 {
		t.Fatalf("%v %+v", err, rep)
	}
	if _, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Include: []string{"nope"}}, io.Discard); err == nil || !strings.Contains(err.Error(), `"nope" does not exist`) {
		t.Errorf("%v", err)
	}
	if _, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB, Exclude: []string{"a", "b", "c"}}, io.Discard); !errors.Is(err, ErrNoTables) {
		t.Errorf("%v", err)
	}
	if _, err := ExportScript(context.Background(), ScriptRequest{Dialect: "postgres", DB: e.DB}, io.Discard); err == nil {
		t.Error("an unknown dialect")
	}
}

type failWriter struct{ after int }

func (w *failWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, errInjected
	}
	w.after -= len(p)
	return len(p), nil
}

func TestWriteScriptFailures(t *testing.T) {
	e := sqliteWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY, n TEXT)`, `INSERT INTO a VALUES (1, 'x'), (2, 'y')`)
	if _, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: e.DB}, &failWriter{}); !errors.Is(err, errInjected) {
		t.Errorf("a writer that fails: %v", err)
	}
	p := planMySQL(t, e)
	write := func(p *ScriptPlan) error {
		_, err := WriteScript(context.Background(), io.Discard, e.DB, p, testHeader())
		return err
	}
	// A value the column's type cannot hold is an error that names the row and the column.
	p.Table("a").Columns[1].Choice = Choice{Family: "int", Type: "BIGINT"}
	if err := write(p); err == nil || !strings.Contains(err.Error(), `row 1, column "n"`) {
		t.Errorf("%v", err)
	}
	// A table whose rows are not the rows that were counted.
	p = planMySQL(t, e)
	p.Table("a").Rows = 5
	if err := write(p); err == nil || !strings.Contains(err.Error(), "5 rows when it was counted and 2 were read") {
		t.Errorf("%v", err)
	}
	// A query that fails.
	p = planMySQL(t, e)
	p.Table("a").OrderBy = "nope"
	if err := write(p); err == nil || !strings.Contains(err.Error(), `table "a"`) {
		t.Errorf("%v", err)
	}
	// A table without a primary key still reports an empty key list.
	if _, rep := exportMySQL(t, sqliteWith(t, `CREATE TABLE nopk (a)`)); len(rep.Tables) != 1 || rep.Tables[0].PrimaryKey == nil {
		t.Errorf("%+v", rep.Tables)
	}
	// An empty table has no insert.
	e2 := sqliteWith(t, `CREATE TABLE a (id INTEGER PRIMARY KEY)`)
	script, _ := exportMySQL(t, e2)
	if strings.Contains(script, "INSERT INTO `a`") {
		t.Errorf("%s", script)
	}
}

func TestScriptBatching(t *testing.T) {
	stmts := []string{`CREATE TABLE n (id INTEGER PRIMARY KEY, v TEXT)`}
	for i := 1; i <= 2500; i++ {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO n VALUES (%d, 'row %d')`, i, i))
	}
	script, _ := exportMySQL(t, sqliteWith(t, stmts...))
	if got := strings.Count(script, "INSERT INTO `n`"); got != 3 {
		t.Errorf("2500 rows are 3 inserts of at most 1000 rows, got %d", got)
	}
	// Large rows: a statement ends when it is about 1 MiB.
	e := sqliteWith(t, `CREATE TABLE big (id INTEGER PRIMARY KEY, b BLOB)`)
	for i := 1; i <= 4; i++ {
		execAll(t, e.DB, fmt.Sprintf(`INSERT INTO big VALUES (%d, zeroblob(300000))`, i))
	}
	script, _ = exportMySQL(t, e)
	if got := strings.Count(script, "INSERT INTO `big`"); got != 2 {
		t.Errorf("4 rows of 600 KB of hex are 2 inserts, got %d", got)
	}
	checkLines(t, script)
}

func TestHostileValuesNeverStartALine(t *testing.T) {
	atoms := []string{"'", "\\", "\n", "\r", "\x00", "GO", "\\!", "source x", "/", "--", ";", "`", "\u2028", "\u0085", "😀", "é", " ", "DELIMITER //", "\\.", "use mysql;", "exit"}
	e := sqliteWith(t, `CREATE TABLE h (id INTEGER PRIMARY KEY, v TEXT, w BLOB, "name
GO" TEXT)`)
	rng := newTestRand()
	for i := 1; i <= 400; i++ {
		var b strings.Builder
		for n := rng.Intn(6); n >= 0; n-- {
			b.WriteString(atoms[rng.Intn(len(atoms))])
		}
		s := strings.ReplaceAll(b.String(), "\x00", "")
		if _, err := e.DB.Exec(`INSERT INTO h VALUES (?, ?, ?, ?)`, i, s, []byte(b.String()), s); err != nil {
			t.Fatal(err)
		}
	}
	execAll(t, e.DB, `INSERT INTO h VALUES (1000, CAST(x'c328ff' AS TEXT), NULL, NULL)`)
	script, rep := exportMySQL(t, e)
	checkLines(t, script)
	if !utf8.ValidString(script) {
		t.Error("the script is not UTF-8")
	}
	wantWarning(t, rep.Warnings, WarnNameRenamed, "name")
	// The invalid UTF-8 value makes the column bytes, with a warning; no value is lost.
	wantWarning(t, rep.Warnings, WarnTypeWidened, "h.v")
}
