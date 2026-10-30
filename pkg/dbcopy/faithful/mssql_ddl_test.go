package faithful

import (
	"context"
	"strings"
	"testing"
)

func TestMSSQLCounterSQL(t *testing.T) {
	p := planMSSQL(t,
		`CREATE TABLE full_t (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO full_t (v) VALUES ('a'), ('b'), ('c')`, `DELETE FROM full_t WHERE id > 1`,
		`CREATE TABLE empty_t (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO empty_t (v) VALUES ('a')`, `DELETE FROM empty_t`,
		`CREATE TABLE "q'uote" (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)`, `INSERT INTO "q'uote" (v) VALUES ('a')`)
	d := mssqlDialect{}
	got := func(table string, next int64) string {
		tb := p.Table(table)
		return d.CounterSQL(tb, tb.Col("id"), next)
	}
	// A table with rows: RESEED sets the last number used.
	if s := got("full_t", 4); !strings.Contains(s, "DBCC CHECKIDENT (N'[full_t]', RESEED, 3) WITH NO_INFOMSGS;") || !strings.HasPrefix(s, mssqlGuard+"\n") || !strings.HasSuffix(s, "\nGO") {
		t.Errorf("%s", s)
	}
	// A table with no row: RESEED sets the first number to use.
	if s := got("empty_t", 4); !strings.Contains(s, "RESEED, 4)") {
		t.Errorf("%s", s)
	}
	if s := got("q'uote", 2); !strings.Contains(s, "N'[q''uote]'") {
		t.Errorf("a quote in the name is doubled: %s", s)
	}
	// A column that is not an identity column here has no counter to set.
	tb := p.Table("full_t")
	tb.Col("id").Choice = mssqlChoice(Choice{Family: "text"})
	if s := d.CounterSQL(tb, tb.Col("id"), 4); !strings.Contains(s, "-- the column id of full_t is not an identity column here") || strings.Contains(s, "DBCC") {
		t.Errorf("%s", s)
	}
	if mssqlIdentity(tb) != nil || strings.Contains(d.CreateTable(tb), "IDENTITY") || len(d.BeginData(tb)) != 1 || len(d.EndData(tb)) != 1 {
		t.Error("no identity column, no IDENTITY and no IDENTITY_INSERT")
	}
}

func TestMSSQLScriptFraming(t *testing.T) {
	e := sqliteWith(t,
		`CREATE TABLE a (id INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT, n NUMERIC(5,2) DEFAULT 1.5 CHECK (n > 0), UNIQUE (v))`, `INSERT INTO a (v, n) VALUES ('x', 1), ('y', 2)`,
		`CREATE TABLE b (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a(id) ON DELETE CASCADE)`, `CREATE INDEX b_a ON b (a_id DESC)`, `INSERT INTO b VALUES (1, 1)`,
		`CREATE TABLE empty_t (id INTEGER)`)
	script, _ := exportMSSQL(t, e)
	checkMSSQLLines(t, script)
	lines := strings.Split(script, "\n")
	// Everything before the first BEGIN TRANSACTION is a comment or a session setting; the batches after it all start with the guard.
	begin := -1
	for i, l := range lines {
		if l == "BEGIN TRANSACTION;" {
			begin = i
			break
		}
	}
	if begin < 0 {
		t.Fatal("no BEGIN TRANSACTION")
	}
	batches := 0
	for i := begin + 2; i < len(lines); i++ {
		if lines[i] == "GO" {
			batches++
			continue
		}
		if i > begin+2 && lines[i-1] == "GO" && lines[i] != "" && !strings.HasPrefix(lines[i], "SET NOEXEC OFF") && !strings.HasPrefix(lines[i], "-- ") {
			if lines[i] != mssqlGuard {
				t.Fatalf("the batch at line %d does not start with the guard: %.60q", i+1, lines[i])
			}
		}
	}
	if batches < 12 {
		t.Errorf("only %d batches", batches)
	}
	for _, want := range []string{
		"SET QUOTED_IDENTIFIER ON; SET ANSI_NULLS ON;", "SET XACT_ABORT ON;", "BEGIN TRANSACTION;\nGO\n" + mssqlGuard + "\nCREATE TABLE [_demodb_import]",
		"SET IDENTITY_INSERT [a] ON;\nINSERT INTO [a] ([id], [v], [n]) VALUES\n(1,N'x',1),\n(2,N'y',2);\nSET IDENTITY_INSERT [a] OFF;\nGO",
		"[n] DECIMAL(5,2) NULL DEFAULT (1.5)", "UNIQUE ([v])", "DBCC CHECKIDENT (N'[a]', RESEED, 2) WITH NO_INFOMSGS;",
		"CREATE NONCLUSTERED INDEX [b_a] ON [b] ([a_id] DESC);", "ALTER TABLE [a] WITH CHECK ADD CHECK ([n] > 0);",
		"ALTER TABLE [b] WITH CHECK ADD FOREIGN KEY ([a_id]) REFERENCES [a] ([id]) ON DELETE CASCADE;",
		"IF (SELECT COUNT_BIG(*) FROM [a]) <> 2 THROW 50000, N'the load is incomplete: table a does not hold 2 rows', 1;",
		"IF (SELECT COUNT_BIG(*) FROM [empty_t]) <> 0 THROW",
		"INSERT INTO [_demodb_import] VALUES (N'fixture', N'DemoDB contract edition', N'" + strings.Repeat("ab", 32) + "', N'v0.0.0-test', 3, 3);\nGO",
		"COMMIT TRANSACTION;\nGO\nSET NOEXEC OFF;\nGO\n-- end of script\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	// The guard is the first line of every batch the data goes in, and no table is inserted into without IDENTITY_INSERT when it has an identity column.
	if strings.Count(script, mssqlGuard+"\nSET IDENTITY_INSERT [a] ON;") != 1 || strings.Contains(script, "INSERT INTO [empty_t]") {
		t.Error("data batches")
	}
}

func TestMSSQLHeader(t *testing.T) {
	d := mssqlDialect{}
	p := planMSSQL(t, `CREATE TABLE "$(x)" (id INTEGER PRIMARY KEY)`, `CREATE VIEW v AS SELECT 1`)
	full := d.Header(p, Header{Name: "n", Edition: "ed", License: "MIT", Attribution: "by \"me\"", SourceSHA256: "00", Version: "v1"})
	bare := d.Header(p, Header{})
	join := func(l []string) string { return strings.Join(l, "\n") }
	if !strings.HasPrefix(full[0], mssqlBOM+"-- ed") || !strings.HasPrefix(bare[0], mssqlBOM+"-- Dataset: database") {
		t.Errorf("%q %q", full[0], bare[0])
	}
	for _, want := range []string{"-- Dataset: n", "-- Licence: MIT", "-- Attribution: by \"me\"", "sqlcmd -S <server> -d <database> -X -x -b -f 65001 -i <file>.sql", "$ (x)", "[view-not-carried]"} {
		if !strings.Contains(join(full), want) {
			t.Errorf("the header lacks %q:\n%s", want, join(full))
		}
	}
	if strings.Contains(join(full), "$(") {
		t.Error("the header holds a sqlcmd variable")
	}
	if strings.Contains(join(bare), "Licence") || strings.Contains(join(bare), "Attribution") {
		t.Error("no licence, no line")
	}
	for _, l := range full {
		if !strings.HasPrefix(strings.TrimPrefix(l, mssqlBOM), "--") {
			t.Errorf("not a comment: %q", l)
		}
	}
	// No warning, no list.
	if strings.Contains(join(d.Header(planMSSQL(t, `CREATE TABLE t (id INTEGER PRIMARY KEY)`), Header{})), "not carried exactly") {
		t.Error("a list of nothing")
	}
}

func TestMSSQLExportsADryRunAndTheDialectReports(t *testing.T) {
	e := sqliteWith(t, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`, `INSERT INTO t VALUES (1, 'a')`)
	var sb strings.Builder
	rep, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mssql", DB: e.DB, Header: testHeader(), DryRun: true}, &sb)
	if err != nil || sb.Len() != 0 || rep.Status != "dry-run" || len(rep.Plan) < 1 || !strings.Contains(rep.Plan[0], "CREATE TABLE [t]") || rep.To.Engine != "mssql" {
		t.Fatalf("%v %q %+v", err, sb.String(), rep)
	}
}

func TestMSSQLValidateHeader(t *testing.T) {
	d := mssqlDialect{}
	if err := d.ValidateHeader(Header{Name: strings.Repeat("é", 1000), Edition: "ok"}); err != nil {
		t.Errorf("a long name has no limit: %v", err)
	}
	for _, h := range []Header{{Name: "a\xffb"}, {Edition: "a\xffb"}, {Name: "a\x00b"}, {Edition: "a\x00b"}} {
		if err := d.ValidateHeader(h); err == nil {
			t.Errorf("%+v: no error", h)
		}
	}
}
