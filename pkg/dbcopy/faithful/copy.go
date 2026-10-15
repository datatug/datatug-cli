package faithful

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite" // the SQLite driver, also used to test CHECK expressions
)

// Endpoint is one side of a copy.
type Endpoint struct {
	Engine  Engine
	DB      *sql.DB
	Display string
	// Schema is the PostgreSQL schema read from (source) or written to (target); "public" when empty.
	Schema string
}

// Options controls a copy.
type Options struct {
	// Overwrite is "" (the target must hold none of the source's tables) or "recreate" (drop them first).
	Overwrite        string
	Include, Exclude []string
	DryRun           bool
	Progress         io.Writer
}

// ErrNoTables says the source holds no table to copy.
var ErrNoTables = errors.New("source has no tables; nothing to copy")

// ConflictError says the target already holds tables of the source.
type ConflictError struct{ Tables []string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("target already has table(s) %s; use --overwrite=recreate to replace them", strings.Join(e.Tables, ", "))
}

// NameError says a name cannot be written on the target.
type NameError struct{ Object, Reason string }

func (e *NameError) Error() string {
	return fmt.Sprintf("%s cannot be created on the target: %s; nothing was changed", e.Object, e.Reason)
}

func (e Endpoint) schema() string {
	if e.Schema == "" {
		return "public"
	}
	return e.Schema
}

func filterTables(cat *Catalog, include, exclude []string, warnings *[]Warning) error {
	known := map[string]bool{}
	for _, t := range cat.Tables {
		known[t.Name] = true
	}
	for _, n := range append(append([]string(nil), include...), exclude...) {
		if !known[n] {
			return fmt.Errorf("table %q does not exist in the source", n)
		}
	}
	keep := map[string]bool{}
	for _, t := range cat.Tables {
		keep[t.Name] = true
	}
	if len(include) > 0 {
		keep = map[string]bool{}
		for _, n := range include {
			keep[n] = true
		}
	}
	for _, n := range exclude {
		delete(keep, n)
	}
	var tables []*Table
	for _, t := range cat.Tables {
		if keep[t.Name] {
			tables = append(tables, t)
		}
	}
	cat.Tables = tables
	for _, t := range cat.Tables {
		var fks []ForeignKey
		for _, fk := range t.FKs {
			if keep[fk.RefTable] {
				fks = append(fks, fk)
				continue
			}
			*warnings = append(*warnings, Warning{WarnFKNotCarried, t.Name, fmt.Sprintf("foreign key (%s) references %q, which is not part of this copy", strings.Join(fk.Columns, ", "), fk.RefTable)})
		}
		t.FKs = fks
	}
	return nil
}

// Copy copies the source into the target and returns the report. The report is returned with
// ExitCode and Error set on failure as well. Nothing on the target changes unless the whole copy
// succeeds (it runs in one transaction on both engines).
func Copy(ctx context.Context, from, to Endpoint, opts Options) (*Report, error) {
	rep := &Report{Command: "db copy", DryRun: opts.DryRun,
		From: EndpointInfo{Engine: from.Engine, Display: from.Display}, To: EndpointInfo{Engine: to.Engine, Display: to.Display},
		Warnings: []Warning{}}
	if from.Engine == Postgres {
		rep.From.Schema = from.schema()
	}
	if to.Engine == Postgres {
		rep.To.Schema = to.schema()
	}
	fail := func(code int, err error) (*Report, error) {
		rep.Status, rep.ExitCode, rep.Error = "failed", code, err.Error()
		return rep, err
	}
	if from.Engine == to.Engine && from.Engine != SQLite {
		return fail(2, fmt.Errorf("faithful copy is between SQLite and PostgreSQL, and SQLite to SQLite; %s to %s is not handled here", from.Engine, to.Engine))
	}

	// Read the source.
	var (
		cat  *Catalog
		srcQ Queryer
		err  error
	)
	switch from.Engine {
	case SQLite:
		srcQ = from.DB
		cat, err = ReadSQLite(ctx, from.DB)
	case Postgres:
		tx, terr := from.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if terr != nil {
			return fail(4, fmt.Errorf("begin read of the source: %w", terr))
		}
		defer func() { _ = tx.Rollback() }()
		srcQ = tx
		cat, err = ReadPostgres(ctx, tx, from.schema())
	}
	if err != nil {
		return fail(1, fmt.Errorf("read source schema: %w", err))
	}
	var extra []Warning
	if err := filterTables(cat, opts.Include, opts.Exclude, &extra); err != nil {
		return fail(2, err)
	}
	if len(cat.Tables) == 0 {
		return fail(1, ErrNoTables)
	}

	// Plan.
	var plan *Plan
	switch {
	case from.Engine == SQLite && to.Engine == SQLite:
		plan = PlanSQLiteToSQLite(cat)
	case from.Engine == SQLite:
		stats, serr := scanSQLiteStats(ctx, from.DB, cat)
		if serr != nil {
			return fail(1, fmt.Errorf("scan source values: %w", serr))
		}
		for _, t := range cat.Tables {
			for i, col := range t.Columns {
				if contains(t.PK, col.Name) && stats[t.Name][i].nulls > 0 {
					return fail(1, fmt.Errorf("table %q: primary key column %q holds NULL, which PostgreSQL cannot carry in a primary key; nothing was changed", t.Name, col.Name))
				}
			}
		}
		plan = PlanSQLiteToPG(cat, stats, to.schema())
	default:
		plan = PlanPGToSQLite(cat, checkCompiles)
	}
	plan.Warnings = append(plan.Warnings, extra...)
	if to.Engine == Postgres {
		shortenPGNames(plan)
		if err := validatePGNames(plan); err != nil {
			return fail(1, err)
		}
	}
	create, post := buildStatements(plan, to.Engine)
	if from.Engine == SQLite && to.Engine == SQLite {
		create, post = sameEngineStatements(cat, plan)
	}
	rep.fill(plan)
	if opts.DryRun {
		for _, s := range create {
			rep.Plan = append(rep.Plan, s+";")
		}
		for _, t := range plan.Tables {
			rep.Plan = append(rep.Plan, fmt.Sprintf("-- load %d rows into %s", t.Src.Rows, t.Name))
		}
		for _, s := range post {
			rep.Plan = append(rep.Plan, s.sql+";")
		}
		rep.Status = "dry-run"
		return rep, nil
	}

	if opts.Overwrite != "" && opts.Overwrite != "recreate" {
		return fail(2, fmt.Errorf("--overwrite=%s is not supported between SQLite and PostgreSQL yet; use recreate", opts.Overwrite))
	}
	x := &executor{ctx: ctx, plan: plan, to: to, from: from, srcQ: srcQ, opts: opts, rep: rep}
	if err := x.run(create, post); err != nil {
		return fail(1, err)
	}
	rep.fill(plan)
	rep.Status = "ok"
	if len(rep.Warnings) > 0 {
		rep.Status = "ok-with-warnings"
	}
	return rep, nil
}

// checkCompiles tells whether SQLite accepts a CHECK / WHERE expression over the columns.
func checkCompiles(cols []string, expr string) error {
	db, _ := sql.Open("sqlite", ":memory:") // the driver is registered: Open cannot fail
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	_, err := db.Exec("CREATE TABLE t (" + quoteList(cols) + ", CHECK (" + expr + "))")
	return err
}

func validatePGNames(p *Plan) error {
	chk := func(object, name string) error {
		if len(name) > 63 {
			return &NameError{Object: object, Reason: fmt.Sprintf("its name is %d bytes and PostgreSQL takes at most 63", len(name))}
		}
		if strings.ContainsRune(name, 0) {
			return &NameError{Object: object, Reason: "its name holds a NUL byte"}
		}
		return nil
	}
	for _, t := range p.Tables {
		if err := chk(fmt.Sprintf("table %q", t.Name), t.Name); err != nil {
			return err
		}
		for _, c := range t.Columns {
			if err := chk(fmt.Sprintf("column %q of table %q", c.Name, t.Name), c.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// shortenPGNames fits the names that PostgreSQL does not need to be exact into 63 bytes: an index
// gets its name cut and ends in a hash of the whole name (index-renamed), and a constraint that
// has a longer name is created with the name PostgreSQL gives it (constraint-name-not-carried).
// A table or column name that is too long cannot be changed and is refused by validatePGNames.
func shortenPGNames(p *Plan) {
	for _, t := range p.Tables {
		for i := range t.Indexes {
			if long := t.Indexes[i].Name; len(long) > 63 {
				sum := sha256.Sum256([]byte(long))
				short := truncateUTF8(long, 54) + "_" + hex.EncodeToString(sum[:4])
				p.warn(WarnIndexRenamed, t.Name+"."+long, "index name is %d bytes and PostgreSQL takes at most 63: created as %q", len(long), short)
				t.Indexes[i].Name = short
			}
		}
		drop := func(kind, name string) string {
			if len(name) > 63 {
				p.warn(WarnConstraintNameLost, t.Name+"."+name, "%s name is %d bytes and PostgreSQL takes at most 63: created with PostgreSQL's own name", kind, len(name))
				return ""
			}
			return name
		}
		t.PKName = drop("primary key", t.PKName)
		for i := range t.Uniques {
			t.Uniques[i].Name = drop("unique constraint", t.Uniques[i].Name)
		}
		for i := range t.FKs {
			t.FKs[i].Name = drop("foreign key", t.FKs[i].Name)
		}
		for i := range t.Checks {
			t.Checks[i].Name = drop("check constraint", t.Checks[i].Name)
		}
	}
}

func truncateUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// scanSQLiteStats reads every column of every table once, to learn what each holds.
func scanSQLiteStats(ctx context.Context, db *sql.DB, cat *Catalog) (map[string][]*colStats, error) {
	out := map[string][]*colStats{}
	for _, t := range cat.Tables {
		sts := make([]*colStats, len(t.Columns))
		exprs := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			sts[i] = newStats(parseDecl(c.SourceType))
			exprs[i] = sqliteReadExpr(c)
		}
		out[t.Name] = sts
		rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM "+quoteIdent(t.Name))
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", t.Name, err)
		}
		vals := make([]any, len(exprs))
		ptrs := make([]any, len(exprs))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("table %q: %w", t.Name, err)
			}
			for i, v := range vals {
				sts[i].add(v)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// postStmt is a statement that follows the data.
type postStmt struct {
	sql  string
	alt  string // tried when sql fails (a foreign key NOT VALID)
	kind string // warning kind when it cannot be created; "" for a statement that must succeed
	obj  string
	// altKind is the warning kind when alt had to be used.
	altKind string
}

func buildStatements(p *Plan, to Engine) (create []string, post []postStmt) {
	if to == Postgres {
		for _, t := range p.Tables {
			create = append(create, pgCreateTable(p.Schema, t))
		}
		for _, t := range p.Tables {
			for _, c := range t.Columns {
				if c.Marker != nil {
					post = append(post, postStmt{sql: pgCommentSQL(p.Schema, t.Name, c), obj: t.Name + "." + c.Name})
				}
			}
		}
		for _, t := range p.Tables {
			for _, ix := range t.Indexes {
				post = append(post, postStmt{sql: pgIndexSQL(p.Schema, t.Name, ix), kind: WarnIndexNotCarried, obj: t.Name + "." + ix.Name})
			}
			for _, ck := range t.Checks {
				post = append(post, postStmt{sql: pgCheckSQL(p.Schema, t.Name, ck), kind: WarnCheckNotCarried, obj: t.Name + "." + ck.Name})
			}
		}
		for _, t := range p.Tables {
			for _, fk := range t.FKs {
				post = append(post, postStmt{sql: pgFKSQL(p.Schema, t.Name, fk, false), alt: pgFKSQL(p.Schema, t.Name, fk, true),
					kind: WarnFKNotCarried, altKind: WarnFKNotValidated, obj: t.Name + "(" + strings.Join(fk.Columns, ",") + ")"})
			}
		}
		return
	}
	for _, t := range p.Tables {
		create = append(create, sqliteCreateTable(t))
	}
	for _, t := range p.Tables {
		for _, ix := range t.Indexes {
			post = append(post, postStmt{sql: sqliteIndexSQL(t.Name, ix), kind: WarnIndexNotCarried, obj: t.Name + "." + ix.Name})
		}
	}
	return
}

type executor struct {
	ctx  context.Context
	plan *Plan
	from Endpoint
	to   Endpoint
	srcQ Queryer
	opts Options
	rep  *Report
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (x *executor) progress(format string, args ...any) {
	if x.opts.Progress != nil {
		_, _ = fmt.Fprintf(x.opts.Progress, format+"\n", args...)
	}
}

func (x *executor) run(create []string, post []postStmt) error {
	ctx := x.ctx
	var (
		q      execer
		commit func() error
		cancel func()
	)
	if x.to.Engine == SQLite {
		conn, err := x.to.DB.Conn(ctx)
		if err != nil {
			return fmt.Errorf("open target: %w", err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin write of the target: %w", err)
		}
		q, commit, cancel = tx, tx.Commit, func() { _ = tx.Rollback() }
	} else {
		tx, err := x.to.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin write of the target: %w", err)
		}
		q, commit, cancel = tx, tx.Commit, func() { _ = tx.Rollback() }
	}
	defer cancel()

	// Conflicts, then the drop of what is replaced.
	names := make([]string, len(x.plan.Tables))
	for i, t := range x.plan.Tables {
		names[i] = t.Name
	}
	if x.to.Engine == Postgres && x.to.schema() != "public" {
		if _, err := q.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(x.to.schema())); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	}
	existing, err := x.existingTables(q, names)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if x.opts.Overwrite != "recreate" {
			return &ConflictError{Tables: existing}
		}
		if x.to.Engine == Postgres {
			qs := make([]string, len(existing))
			for i, n := range existing {
				qs[i] = qualified(x.to.schema(), n)
			}
			if _, err := q.ExecContext(ctx, "DROP TABLE "+strings.Join(qs, ", ")); err != nil {
				return fmt.Errorf("drop the tables that are replaced: %w", err)
			}
		} else {
			for _, n := range existing {
				if _, err := q.ExecContext(ctx, "DROP TABLE "+quoteIdent(n)); err != nil {
					return fmt.Errorf("drop table %q: %w", n, err)
				}
			}
		}
	}
	for _, s := range create {
		if _, err := q.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("create table: %w\n%s", err, s)
		}
	}

	// Data.
	loaded := map[string]int64{}
	for _, t := range x.plan.Tables {
		n, err := x.loadTable(q, t)
		if err != nil {
			return fmt.Errorf("copy rows of %q: %w", t.Name, err)
		}
		loaded[t.Name] = n
		x.progress("copied %s: %d rows", t.Name, n)
		t.Src.Rows = n // what was copied: a source that is written during the copy is read as it was found
	}
	if err := x.counters(q); err != nil {
		return err
	}

	// Constraints and indexes that follow the data.
	for _, s := range post {
		if err := x.applyPost(q, s); err != nil {
			return err
		}
	}
	if x.to.Engine == SQLite {
		if err := x.foreignKeyCheck(q); err != nil {
			return err
		}
	}

	// Verify.
	for _, t := range x.plan.Tables {
		var n int64
		var qn string
		if x.to.Engine == Postgres {
			qn = qualified(x.to.schema(), t.Name)
		} else {
			qn = quoteIdent(t.Name)
		}
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM "+qn).Scan(&n); err != nil {
			return err
		}
		if n != loaded[t.Name] {
			return fmt.Errorf("table %q: %d rows were written and %d are in the target", t.Name, loaded[t.Name], n)
		}
	}
	return commit()
}

func (x *executor) existingTables(q execer, names []string) ([]string, error) {
	var existing []string
	var rows *sql.Rows
	var err error
	if x.to.Engine == Postgres {
		rows, err = q.QueryContext(x.ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f') AND c.relname = ANY($2) ORDER BY 1`, x.to.schema(), names)
	} else {
		rows, err = q.QueryContext(x.ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY 1`)
	}
	if err != nil {
		return nil, fmt.Errorf("look for existing tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if want[n] {
			existing = append(existing, n)
		}
	}
	sort.Strings(existing)
	return existing, rows.Err()
}

func (x *executor) sourceSelect(t *TargetTable) string {
	exprs := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		exprs[i] = c.readExpr
	}
	var from, order string
	if x.from.Engine == SQLite {
		from = quoteIdent(t.Name)
		order = "rowid"
	} else {
		from = qualified(x.from.schema(), t.Name)
		order = "ctid"
	}
	if len(t.PK) > 0 {
		order = quoteList(t.PK)
	}
	return "SELECT " + strings.Join(exprs, ", ") + " FROM " + from + " ORDER BY " + order
}

func (x *executor) loadTable(q execer, t *TargetTable) (int64, error) {
	ctx := x.ctx
	ncols := len(t.Columns)
	batchRows := batchSize(x.to.Engine, ncols)
	rows, err := x.srcQ.QueryContext(ctx, x.sourceSelect(t))
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	vals := make([]any, ncols)
	ptrs := make([]any, ncols)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	var batch [][]any
	var total int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := x.insert(q, t, batch); err != nil {
			return err
		}
		total += int64(len(batch))
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return total, err
		}
		row := make([]any, ncols)
		for i, c := range t.Columns {
			v, err := c.enc(vals[i])
			if err != nil {
				return total, fmt.Errorf("column %q: %w", c.Name, err)
			}
			row[i] = v
		}
		batch = append(batch, row)
		if len(batch) >= batchRows {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

func (x *executor) insert(q execer, t *TargetTable, batch [][]any) error {
	ncols := len(t.Columns)
	names := make([]string, ncols)
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	var b strings.Builder
	if x.to.Engine == Postgres {
		b.WriteString("INSERT INTO " + qualified(x.to.schema(), t.Name) + " (" + quoteList(names) + ") VALUES ")
	} else {
		b.WriteString("INSERT INTO " + quoteIdent(t.Name) + " (" + quoteList(names) + ") VALUES ")
	}
	args := make([]any, 0, len(batch)*ncols)
	for r, row := range batch {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		for i, c := range t.Columns {
			if i > 0 {
				b.WriteString(", ")
			}
			if x.to.Engine == Postgres {
				ph := "$" + strconv.Itoa(len(args)+1)
				if c.Cast != "" {
					ph += "::text::" + c.Cast
				}
				b.WriteString(ph)
			} else {
				b.WriteString("?")
			}
			args = append(args, row[i])
		}
		b.WriteString(")")
	}
	_, err := q.ExecContext(x.ctx, b.String(), args...)
	return err
}

// counters moves every auto-number counter past the rows that were copied.
func (x *executor) counters(q execer) error {
	ctx := x.ctx
	for _, t := range x.plan.Tables {
		for _, c := range t.Columns {
			if c.Identity == IdentityNone {
				continue
			}
			var maxID sql.NullInt64
			var qn string
			if x.to.Engine == Postgres {
				qn = qualified(x.to.schema(), t.Name)
			} else {
				qn = quoteIdent(t.Name)
			}
			if err := q.QueryRowContext(ctx, "SELECT max("+quoteIdent(c.Name)+") FROM "+qn).Scan(&maxID); err != nil {
				return err
			}
			next := c.Src.SequenceStart
			if maxID.Valid && maxID.Int64+1 > next {
				next = maxID.Int64 + 1
			}
			if next < 1 {
				continue
			}
			if x.to.Engine == Postgres {
				if _, err := q.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s RESTART WITH %d", qn, quoteIdent(c.Name), next)); err != nil {
					return fmt.Errorf("set the counter of %s.%s: %w", t.Name, c.Name, err)
				}
			} else if c.Identity == IdentityAutoincrement && next > 1 {
				if _, err := q.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name = ?", t.Name); err != nil {
					return err
				}
				if _, err := q.ExecContext(ctx, "INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)", t.Name, next-1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type tolerated struct{ err error }

func (t *tolerated) Error() string { return t.err.Error() }

func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

// exec runs stmt; when it may fail (tolerant) a failure is returned as *tolerated and the
// transaction is left usable (a savepoint on PostgreSQL).
func (x *executor) exec(q execer, stmt string, tolerant bool) error {
	ctx := x.ctx
	if !tolerant {
		_, err := q.ExecContext(ctx, stmt)
		return err
	}
	if x.to.Engine == Postgres {
		if _, err := q.ExecContext(ctx, "SAVEPOINT dt_try"); err != nil {
			return err
		}
	}
	if _, err := q.ExecContext(ctx, stmt); err != nil {
		if x.to.Engine == Postgres {
			if _, rerr := q.ExecContext(ctx, "ROLLBACK TO SAVEPOINT dt_try"); rerr != nil {
				return rerr
			}
		}
		return &tolerated{err}
	}
	if x.to.Engine == Postgres {
		_, err := q.ExecContext(ctx, "RELEASE SAVEPOINT dt_try")
		return err
	}
	return nil
}

func (x *executor) applyPost(q execer, s postStmt) error {
	err := x.exec(q, s.sql, s.kind != "")
	if err == nil {
		return nil
	}
	var tol *tolerated
	if !errors.As(err, &tol) {
		return fmt.Errorf("%s: %w", s.obj, err)
	}
	if s.alt != "" {
		if err2 := x.exec(q, s.alt, true); err2 == nil {
			x.plan.warn(s.altKind, s.obj, "existing rows violate this foreign key in the source; it was created NOT VALID (enforced for new rows, existing rows not checked): %v", oneLine(tol))
			return nil
		}
	}
	x.plan.warn(s.kind, s.obj, "not created on the target: %v", oneLine(tol))
	return nil
}

func (x *executor) foreignKeyCheck(q execer) error {
	rows, err := q.QueryContext(x.ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for rows.Next() {
		var table, parent sql.NullString
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			_ = rows.Close()
			return err
		}
		counts[table.String+" -> "+parent.String]++
	}
	_ = rows.Close()
	for k, n := range counts {
		x.plan.warn(WarnForeignKeyViolations, k, "%d row(s) reference a parent row that does not exist (copied as they are in the source)", n)
	}
	return nil
}

// batchSize is how many rows one INSERT carries: bounded by the number of parameters a statement may
// have (65535 on PostgreSQL, 32766 on SQLite).
func batchSize(to Engine, ncols int) int {
	rows, params := 500, 30000
	if to == Postgres {
		rows, params = 1000, 60000
	}
	return max(min(rows, params/max(ncols, 1)), 1)
}
