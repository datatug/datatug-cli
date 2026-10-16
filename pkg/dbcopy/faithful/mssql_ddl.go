package faithful

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// mssqlGuard starts every batch of the script that follows BEGIN TRANSACTION. XACT_ABORT rolls the transaction
// back at the first failed statement, and from then on @@TRANCOUNT is 0, so every later batch compiles and does
// nothing; without the guard a client that goes on after an error (sqlcmd without -b) would run the rest of the
// script outside the transaction and keep half of the data.
const mssqlGuard = "IF @@TRANCOUNT=0 SET NOEXEC ON"

// mssqlBOM is the byte order mark the script starts with: SQL Server Management Studio and sqlcmd -i read a file
// that has one as UTF-8.
const mssqlBOM = "\ufeff"

// identity returns the column the table creates as an IDENTITY column, or nil.
func mssqlIdentity(t *ScriptTable) *ScriptColumn {
	for _, c := range t.Columns {
		if c.AutoInc && c.Choice.Family == "int" {
			return c
		}
	}
	return nil
}

func (d mssqlDialect) batch(lines ...string) string {
	return mssqlGuard + "\n" + strings.Join(lines, "\n") + "\nGO"
}

func (d mssqlDialect) CreateTable(t *ScriptTable) string {
	var parts []string
	id := mssqlIdentity(t)
	for _, c := range t.Columns {
		def := d.QuoteIdent(c.Name) + " " + c.Choice.Type
		if c == id {
			def += " IDENTITY(1,1)"
		}
		if c.NotNull {
			def += " NOT NULL"
		} else {
			def += " NULL"
		}
		if c.Default != "" {
			def += " DEFAULT " + c.Default
		}
		parts = append(parts, def)
	}
	if len(t.PK) > 0 {
		parts = append(parts, "PRIMARY KEY ("+d.quoteList(t.PK)+")")
	}
	for _, u := range t.Uniques {
		s := ""
		if u.Name != "" {
			s += "CONSTRAINT " + d.QuoteIdent(u.Name) + " "
		}
		parts = append(parts, s+"UNIQUE ("+d.quoteList(u.Columns)+")")
	}
	return d.batch("CREATE TABLE " + d.QuoteIdent(t.Name) + " (\n  " + strings.Join(parts, ",\n  ") + "\n);")
}

func (d mssqlDialect) IndexSQL(t *ScriptTable, ix Index) string {
	cols := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		cols[i] = d.QuoteIdent(c.Name)
		if c.Desc {
			cols[i] += " DESC"
		}
	}
	s := "CREATE "
	if ix.Unique {
		s += "UNIQUE "
	}
	s += "NONCLUSTERED INDEX " + d.QuoteIdent(ix.Name) + " ON " + d.QuoteIdent(t.Name) + " (" + strings.Join(cols, ", ") + ")"
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return d.batch(s + ";")
}

func (d mssqlDialect) CheckSQL(t *ScriptTable, ck Check) string {
	s := "ALTER TABLE " + d.QuoteIdent(t.Name) + " WITH CHECK ADD "
	if ck.Name != "" {
		s += "CONSTRAINT " + d.QuoteIdent(ck.Name) + " "
	}
	return d.batch(s + "CHECK (" + ck.Expr + ");")
}

func (d mssqlDialect) FKSQL(p *ScriptPlan, t *ScriptTable, fk ForeignKey) string {
	s := "ALTER TABLE " + d.QuoteIdent(t.Name) + " WITH CHECK ADD "
	if fk.Name != "" {
		s += "CONSTRAINT " + d.QuoteIdent(fk.Name) + " "
	}
	s += "FOREIGN KEY (" + d.quoteList(fk.Columns) + ") REFERENCES " + d.QuoteIdent(fk.RefTable) + " (" + d.quoteList(fk.RefColumns) + ")"
	if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
		s += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
		s += " ON UPDATE " + fk.OnUpdate
	}
	return d.batch(s + ";")
}

// CounterSQL sets the identity counter so the next row gets the number SQLite would give it. RESEED sets the
// last value used, except in a table that holds no row yet, where it sets the first one to use.
func (d mssqlDialect) CounterSQL(t *ScriptTable, c *ScriptColumn, next int64) string {
	if mssqlIdentity(t) != c {
		return d.batch("-- " + commentText("the column "+c.Name+" of "+t.Name+" is not an identity column here; its counter is not set"))
	}
	if t.Rows > 0 {
		next--
	}
	name := strings.ReplaceAll(d.QuoteIdent(t.Name), "'", "''")
	return d.batch("DBCC CHECKIDENT (N'" + name + "', RESEED, " + strconv.FormatInt(next, 10) + ") WITH NO_INFOMSGS;")
}

func (d mssqlDialect) InsertHead(t *ScriptTable) string {
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	return "INSERT INTO " + d.QuoteIdent(t.Name) + " (" + d.quoteList(names) + ") VALUES"
}

// BatchLimits are the most rows of one INSERT ... VALUES (SQL Server refuses more than 1,000) and its size.
func (mssqlDialect) BatchLimits() (int, int) { return 1000, 1 << 20 }

func (d mssqlDialect) BeginData(t *ScriptTable) []string {
	l := []string{mssqlGuard}
	if mssqlIdentity(t) != nil {
		l = append(l, "SET IDENTITY_INSERT "+d.QuoteIdent(t.Name)+" ON;")
	}
	return l
}

func (d mssqlDialect) EndData(t *ScriptTable) []string {
	var l []string
	if mssqlIdentity(t) != nil {
		l = append(l, "SET IDENTITY_INSERT "+d.QuoteIdent(t.Name)+" OFF;")
	}
	return append(l, "GO")
}

// mssqlComment makes text safe for a "-- " comment line: no control character survives, and "$(" (a sqlcmd
// variable, read even in a comment when the client runs without -x) is broken.
func mssqlComment(s string) string {
	return strings.ReplaceAll(commentText(s), "$(", "$ (")
}

func (d mssqlDialect) Header(p *ScriptPlan, h Header) []string {
	var l []string
	add := func(format string, args ...any) {
		l = append(l, strings.TrimRight("-- "+mssqlComment(fmt.Sprintf(format, args...)), " "))
	}
	raw := func(s string) { l = append(l, strings.TrimRight("-- "+s, " ")) }
	name := h.Name
	if name == "" {
		name = "database"
	}
	if h.Edition != "" {
		add("%s", h.Edition)
	}
	add("Dataset: %s", name)
	add("Generated by datatug %s (datatug db copy --to sqlscript:///<file>.sql?dialect=mssql) from a SQLite file with SHA-256 %s", h.Version, h.SourceSHA256)
	if h.License != "" {
		add("Licence: %s", h.License)
	}
	if h.Attribution != "" {
		add("Attribution: %s", h.Attribution)
	}
	raw("")
	raw("Requires SQL Server 2016 or later. Verified on SQL Server 2022.")
	raw("Load it into an EMPTY database with sqlcmd, from the file (the byte order mark of this file is read as UTF-8 by -i, not by a redirect):")
	raw("  sqlcmd -S <server> -d <database> -X -x -b -f 65001 -i <file>.sql")
	raw("-X turns off the sqlcmd commands that run programs (!! and ED), -x turns off scripting variable substitution, -b stops at the first error, -f 65001 reads UTF-8.")
	raw("No data in this file can start a line, so a line is never a sqlcmd command; every statement is one batch ended by a GO line.")
	raw("The script is one transaction: a failed load is rolled back and leaves the database as it was. The first statement creates the table " + MySQLMarkerTable + " and fails")
	raw("when it exists (a second load stops there); the last one fills it, after every table was counted: a row there means the load completed.")
	raw("DROP TABLE " + d.QuoteIdent(MySQLMarkerTable) + " removes the marker once you no longer need it.")
	raw("Text comparison in SQL Server is case-insensitive by default, and = and UNIQUE ignore trailing spaces, unlike SQLite's exact comparison:")
	raw("keys that differ only by case or by trailing spaces in SQLite are duplicates here and stop the load. Text columns are NVARCHAR (UTF-16 code units).")
	raw("A unique key over nullable columns allows one NULL in SQL Server and many in SQLite; a key whose columns hold several NULLs is written as a filtered unique index.")
	tables := make([]string, len(p.Tables))
	for i, t := range p.Tables {
		tables[i] = t.Name
	}
	add("Tables (%d): %s", len(tables), strings.Join(tables, ", "))
	if len(p.Warnings) > 0 {
		raw("")
		add("%d thing(s) are not carried exactly:", len(p.Warnings))
		for _, w := range p.Warnings {
			add("  [%s] %s: %s", w.Kind, w.Object, w.Message)
		}
	}
	l[0] = mssqlBOM + l[0]
	return l
}

func (d mssqlDialect) Prelude(p *ScriptPlan, h Header) []string {
	q := d.QuoteIdent(MySQLMarkerTable)
	return []string{
		"SET QUOTED_IDENTIFIER ON; SET ANSI_NULLS ON; SET ANSI_PADDING ON; SET ANSI_WARNINGS ON; SET ARITHABORT ON; SET CONCAT_NULL_YIELDS_NULL ON; SET NUMERIC_ROUNDABORT OFF; SET XACT_ABORT ON; SET NOCOUNT ON;",
		"GO",
		"BEGIN TRANSACTION;",
		"GO",
		d.batch("CREATE TABLE " + q + " ([dataset] NVARCHAR(MAX) NOT NULL, [edition] NVARCHAR(MAX) NOT NULL, [source_sha256] CHAR(64) NOT NULL, [datatug_version] NVARCHAR(MAX) NOT NULL, [tables_count] INT NOT NULL, [rows_count] BIGINT NOT NULL);"),
	}
}

func (d mssqlDialect) Epilogue(p *ScriptPlan, h Header) []string {
	var rows int64
	checks := []string{mssqlGuard}
	for _, t := range p.Tables {
		rows += t.Rows
		checks = append(checks, fmt.Sprintf("IF (SELECT COUNT_BIG(*) FROM %s) <> %d THROW 50000, %s, 1;", d.QuoteIdent(t.Name), t.Rows,
			mssqlTextLiteral("the load is incomplete: table "+t.Name+" does not hold "+strconv.FormatInt(t.Rows, 10)+" rows")))
	}
	checks = append(checks, "GO")
	return append(checks,
		d.batch(fmt.Sprintf("INSERT INTO %s VALUES (%s, %s, %s, %s, %d, %d);", d.QuoteIdent(MySQLMarkerTable),
			mssqlTextLiteral(h.Name), mssqlTextLiteral(h.Edition), mssqlTextLiteral(h.SourceSHA256), mssqlTextLiteral(h.Version), len(p.Tables), rows)),
		d.batch("COMMIT TRANSACTION;"),
		"SET NOEXEC OFF;",
		"GO",
		"-- end of script",
	)
}

// ValidateHeader refuses a dataset name or edition the script cannot carry: invalid UTF-8 cannot be written as
// NVARCHAR text, and a NUL has no place in a header comment. The marker columns are NVARCHAR(MAX), so no length
// limit applies.
func (mssqlDialect) ValidateHeader(h Header) error {
	for _, f := range []struct{ what, value string }{{"name", h.Name}, {"edition", h.Edition}} {
		switch {
		case !utf8.ValidString(f.value):
			return fmt.Errorf("the %s is not valid UTF-8, which SQL Server text cannot hold", f.what)
		case strings.IndexByte(f.value, 0) >= 0:
			return fmt.Errorf("the %s holds a NUL character, which the script header cannot hold", f.what)
		}
	}
	return nil
}
