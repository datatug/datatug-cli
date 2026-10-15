package faithful

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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
	// Overwrite is "" (the target must hold none of the source's tables), "recreate" (drop them first) or
	// "reload" (empty them and load the rows again; their schema stays).
	Overwrite        string
	Include, Exclude []string
	// Filters is the --where / --limit of each table, by source table name.
	Filters  map[string]RowFilter
	DryRun   bool
	Progress io.Writer
}

// ReloadMismatchError says a table the copy would reload has a schema the source rows do not fit.
type ReloadMismatchError struct {
	Table    string
	Problems []string
}

func (e *ReloadMismatchError) Error() string {
	return fmt.Sprintf("--overwrite=reload: table %q on the target does not fit the source: %s; nothing was changed", e.Table, strings.Join(e.Problems, "; "))
}

// ErrNoTables says the source holds no table to copy.
var ErrNoTables = errors.New("source has no tables; nothing to copy")

// ConflictError says the target already holds tables of the source.
type ConflictError struct{ Tables []string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("target already has table(s) %s; use --overwrite=recreate to replace them or --overwrite=reload to empty and load them again", strings.Join(e.Tables, ", "))
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

// legacySidecar is the table an earlier `db copy` (the DALgo path) wrote next to the data to remember
// which columns held timestamps. It is not user data.
const legacySidecar = "_dalgo_time_columns"

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
	if keep[legacySidecar] && len(include) == 0 {
		delete(keep, legacySidecar)
		*warnings = append(*warnings, Warning{WarnObjectNotCarried, legacySidecar,
			"table written by an earlier `db copy` to remember which columns hold timestamps; it is not data of the database and is not copied (name it with --include to copy it)"})
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

// sessionSettings pins what the server prints and how long it lets a statement run, for the length of
// the transaction: the text a copy reads (dates, intervals, floats) must not depend on the settings
// of the database or the role, and a copy must not be cut by a statement timeout.
const sessionSettings = `SELECT set_config('datestyle', 'ISO, MDY', true), set_config('intervalstyle', 'postgres', true),
  set_config('extra_float_digits', '3', true), set_config('timezone', 'UTC', true),
  set_config('statement_timeout', '0', true), set_config('idle_in_transaction_session_timeout', '0', true)`

type sessionExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func pgSession(ctx context.Context, q sessionExecer) error {
	if _, err := q.ExecContext(ctx, sessionSettings); err != nil {
		return fmt.Errorf("set the session: %w", err)
	}
	return nil
}

// Copy copies the source into the target and returns the report. The report is returned with
// ExitCode and Error set on failure as well. Nothing on the target changes unless the whole copy
// succeeds (it runs in one transaction on both engines), and the source is read as one snapshot
// (one transaction on both engines).
func Copy(ctx context.Context, from, to Endpoint, opts Options) (*Report, error) {
	rep := &Report{Command: "db copy", DryRun: opts.DryRun,
		From: EndpointInfo{Engine: from.Engine, Display: from.Display}, To: EndpointInfo{Engine: to.Engine, Display: to.Display},
		Tables: []TableReport{}, Warnings: []Warning{}}
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
	if opts.Overwrite != "" && opts.Overwrite != "recreate" && opts.Overwrite != "reload" {
		return fail(2, fmt.Errorf("--overwrite=%s is not supported: use recreate or reload", opts.Overwrite))
	}

	// Read the source: its catalog and its rows, in one snapshot.
	var (
		cat  *Catalog
		srcQ Queryer
		err  error
	)
	switch from.Engine {
	case SQLite:
		tx, terr := from.DB.BeginTx(ctx, nil)
		if terr != nil {
			return fail(4, fmt.Errorf("begin read of the source: %w", terr))
		}
		defer func() { _ = tx.Rollback() }()
		srcQ = tx
		cat, err = ReadSQLite(ctx, tx)
	case Postgres:
		tx, terr := from.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if terr != nil {
			return fail(4, fmt.Errorf("begin read of the source: %w", terr))
		}
		defer func() { _ = tx.Rollback() }()
		if serr := pgSession(ctx, tx); serr != nil {
			return fail(4, serr)
		}
		srcQ = tx
		cat, err = ReadPostgres(ctx, tx, from.schema())
	}
	if err != nil {
		return fail(1, fmt.Errorf("read source schema: %w", err))
	}
	var extra []Warning
	all := map[string]bool{}
	for _, t := range cat.Tables {
		all[t.Name] = true
	}
	if err := filterTables(cat, opts.Include, opts.Exclude, &extra); err != nil {
		return fail(2, err)
	}
	if len(cat.Tables) == 0 {
		return fail(1, ErrNoTables)
	}
	filters, err := compileFilters(from.Engine, cat, all, opts.Filters)
	if err != nil {
		return fail(2, err)
	}

	// Plan.
	var plan *Plan
	switch {
	case from.Engine == SQLite && to.Engine == SQLite:
		plan = PlanSQLiteToSQLite(cat)
	case from.Engine == SQLite:
		stats, serr := scanSQLiteStats(ctx, srcQ, cat)
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
	for _, t := range plan.Tables {
		t.Filter = filters[t.Name]
	}
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
		for _, c := range create {
			rep.Plan = append(rep.Plan, c.sql+";")
		}
		for _, t := range plan.Tables {
			if t.Filter != nil {
				rep.Plan = append(rep.Plan, fmt.Sprintf("-- load the rows of %s that match %s (of %d)", t.Name, t.Filter.text, t.Src.Rows))
				continue
			}
			rep.Plan = append(rep.Plan, fmt.Sprintf("-- load %d rows into %s", t.Src.Rows, t.Name))
		}
		for _, s := range post {
			rep.Plan = append(rep.Plan, s.sql+";")
		}
		rep.Status = "dry-run"
		return rep, nil
	}

	x := &executor{ctx: ctx, plan: plan, to: to, from: from, srcQ: srcQ, opts: opts, rep: rep, filtered: len(filters) > 0}
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

// compileFilters turns --where / --limit into SQL of the source engine, by table. A filter on a table
// that is not in the source, or not part of this copy, is an error.
func compileFilters(src Engine, cat *Catalog, inSource map[string]bool, in map[string]RowFilter) (map[string]*compiledFilter, error) {
	out := map[string]*compiledFilter{}
	names := make([]string, 0, len(in))
	for n := range in {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t := cat.Table(n)
		switch {
		case !inSource[n]:
			return nil, fmt.Errorf("--where / --limit: table %q does not exist in the source", n)
		case t == nil:
			return nil, fmt.Errorf("--where / --limit: table %q is not part of this copy (--include / --exclude)", n)
		}
		cf, err := compileRowFilter(src, t, in[n])
		if err != nil {
			return nil, err
		}
		out[n] = cf
	}
	return out, nil
}

// scanSQLiteStats reads every column of every table once, to learn what each holds.
func scanSQLiteStats(ctx context.Context, db Queryer, cat *Catalog) (map[string][]*colStats, error) {
	out := map[string][]*colStats{}
	for _, t := range cat.Tables {
		sts := make([]*colStats, len(t.Columns))
		names := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			sts[i] = newStats(parseDecl(c.SourceType))
			names[i] = c.Name
		}
		out[t.Name] = sts
		rows, err := db.QueryContext(ctx, "SELECT "+sqliteSelectList(names)+" FROM "+quoteIdent(t.Name))
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", t.Name, err)
		}
		vals, ptrs := sqliteRowBuffer(len(names))
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("table %q: %w", t.Name, err)
			}
			fixEmptyBlobs(vals)
			for i := range names {
				sts[i].add(vals[i])
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// sqliteSelectList reads columns of a SQLite table value for value. Each column is read as +"c" (a
// unary plus has the value of its operand and no declared type, so the driver does not turn a DATE
// or DATETIME text into a time), and the list ends with one flag per column that says the value is an
// empty blob: the driver returns an empty blob as NULL.
func sqliteSelectList(names []string) string {
	exprs := make([]string, 0, 2*len(names))
	for _, n := range names {
		exprs = append(exprs, "+"+quoteIdent(n))
	}
	for _, n := range names {
		exprs = append(exprs, "(typeof("+quoteIdent(n)+") = 'blob' AND length("+quoteIdent(n)+") = 0)")
	}
	return strings.Join(exprs, ", ")
}

// sqliteRowBuffer is the scan target of a row read with sqliteSelectList.
func sqliteRowBuffer(n int) (vals, ptrs []any) {
	vals = make([]any, 2*n)
	ptrs = make([]any, 2*n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	return vals, ptrs
}

// fixEmptyBlobs gives every column whose flag is set the empty blob the driver read as NULL.
func fixEmptyBlobs(vals []any) {
	n := len(vals) / 2
	for i := 0; i < n; i++ {
		if flag, _ := vals[n+i].(int64); flag != 0 {
			vals[i] = []byte{}
		}
	}
}

// createStmt is a CREATE TABLE of the target.
type createStmt struct {
	table string
	sql   string
}

// postStmt is a statement that follows the data.
type postStmt struct {
	table string // the table it belongs to, "" for a view or other object
	// replace drops the object of the same name before it is created, when the target is overwritten
	// (a view or trigger an earlier copy wrote is replaced by the source's, not kept).
	replace string
	sql     string
	alt     string // tried when sql fails with a foreign key violation (a foreign key NOT VALID)
	kind    string // warning kind when it cannot be created; "" for a statement that must succeed
	obj     string
	// altKind is the warning kind when alt had to be used.
	altKind string
}

func buildStatements(p *Plan, to Engine) (create []createStmt, post []postStmt) {
	if to == Postgres {
		for _, t := range p.Tables {
			create = append(create, createStmt{t.Name, pgCreateTable(p.Schema, t)})
		}
		for _, t := range p.Tables {
			for _, c := range t.Columns {
				if c.Marker != nil {
					post = append(post, postStmt{table: t.Name, sql: pgCommentSQL(p.Schema, t.Name, c), obj: t.Name + "." + c.Name})
				}
			}
		}
		for _, t := range p.Tables {
			for _, ix := range t.Indexes {
				post = append(post, postStmt{table: t.Name, sql: pgIndexSQL(p.Schema, t.Name, ix), kind: WarnIndexNotCarried, obj: t.Name + "." + ix.Name})
			}
			for _, ck := range t.Checks {
				post = append(post, postStmt{table: t.Name, sql: pgCheckSQL(p.Schema, t.Name, ck), kind: WarnCheckNotCarried, obj: t.Name + "." + ck.Name})
			}
		}
		for _, t := range p.Tables {
			for _, fk := range t.FKs {
				post = append(post, postStmt{table: t.Name, sql: pgFKSQL(p.Schema, t.Name, fk, false), alt: pgFKSQL(p.Schema, t.Name, fk, true),
					kind: WarnFKNotCarried, altKind: WarnFKNotValidated, obj: t.Name + "(" + strings.Join(fk.Columns, ",") + ")"})
			}
		}
		return
	}
	for _, t := range p.Tables {
		create = append(create, createStmt{t.Name, sqliteCreateTable(t)})
	}
	for _, t := range p.Tables {
		for _, ix := range t.Indexes {
			post = append(post, postStmt{table: t.Name, sql: sqliteIndexSQL(t.Name, ix), kind: WarnIndexNotCarried, obj: t.Name + "." + ix.Name})
		}
	}
	return
}

type executor struct {
	ctx      context.Context
	plan     *Plan
	from     Endpoint
	to       Endpoint
	srcQ     Queryer
	opts     Options
	rep      *Report
	filtered bool
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

func (x *executor) qualifiedTarget(name string) string {
	if x.to.Engine == Postgres {
		return qualified(x.to.schema(), name)
	}
	return quoteIdent(name)
}

func (x *executor) run(create []createStmt, post []postStmt) error {
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
		if err := pgSession(ctx, tx); err != nil {
			cancel()
			return err
		}
	}
	defer cancel()

	// Conflicts, then the drop of what is replaced, or the check of what is reloaded.
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
		switch x.opts.Overwrite {
		case "recreate":
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
		case "reload":
			if err := x.prepareReload(q, existing); err != nil {
				return err
			}
		default:
			return &ConflictError{Tables: existing}
		}
	}
	for _, c := range create {
		if x.reloaded(c.table) {
			continue
		}
		if _, err := q.ExecContext(ctx, c.sql); err != nil {
			return fmt.Errorf("create table: %w\n%s", err, c.sql)
		}
	}

	// Data.
	order := x.plan.Tables
	if x.opts.Overwrite == "reload" && x.to.Engine == Postgres {
		order = parentsFirst(order)
	}
	if err := x.truncate(q, order); err != nil {
		return err
	}
	for _, t := range order {
		start := time.Now()
		x.progress("copying %s (est. %d rows)…", t.Name, t.Src.Rows)
		n, err := x.loadTable(q, t)
		if err != nil {
			return fmt.Errorf("copy rows of %q: %w", t.Name, err)
		}
		// The source is read in one snapshot: a table that gives another number of rows than the
		// snapshot counted at its start means the snapshot is not what it should be.
		if t.Filter == nil && n != t.Src.Rows {
			return fmt.Errorf("table %q: the source held %d rows in the snapshot and %d were read", t.Name, t.Src.Rows, n)
		}
		t.Copied, t.Loaded = n, true
		x.warnLosses(t)
		x.progress("copied %s: %d rows in %s", t.Name, n, time.Since(start).Round(time.Millisecond))
	}
	if err := x.counters(q); err != nil {
		return err
	}

	// Constraints and indexes that follow the data.
	for _, s := range post {
		if x.reloaded(s.table) {
			continue
		}
		if s.replace != "" && x.opts.Overwrite != "" {
			if _, err := q.ExecContext(ctx, s.replace); err != nil {
				return fmt.Errorf("%s: %w", s.obj, err)
			}
		}
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
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM "+x.qualifiedTarget(t.Name)).Scan(&n); err != nil {
			return err
		}
		if n != t.Copied {
			return fmt.Errorf("table %q: %d rows were written and %d are in the target", t.Name, t.Copied, n)
		}
	}
	return commit()
}

// reloaded says the table exists on the target and keeps its schema.
func (x *executor) reloaded(table string) bool {
	if table == "" {
		return false
	}
	for _, t := range x.plan.Tables {
		if t.Name == table {
			return t.Reload
		}
	}
	return false
}

// warnLosses reports the values of a table that the target could not hold as they were.
func (x *executor) warnLosses(t *TargetTable) {
	for _, c := range t.Columns {
		if c.loss == nil {
			continue
		}
		obj := t.Name + "." + c.Name
		if c.loss.nan > 0 {
			x.plan.warn(WarnValuesNormalized, obj, "%d NaN value(s) became NULL: SQLite has no NaN", c.loss.nan)
		}
		if c.loss.negZero > 0 {
			x.plan.warn(WarnValuesNormalized, obj, "%d negative zero value(s) became 0: SQLite does not keep the sign of a zero", c.loss.negZero)
		}
	}
}

// truncate empties the tables that are reloaded.
func (x *executor) truncate(q execer, order []*TargetTable) error {
	var names []string
	for _, t := range order {
		if t.Reload {
			names = append(names, t.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	if x.to.Engine == Postgres {
		qs := make([]string, len(names))
		for i, n := range names {
			qs[i] = qualified(x.to.schema(), n)
		}
		if _, err := q.ExecContext(x.ctx, "TRUNCATE "+strings.Join(qs, ", ")+" RESTART IDENTITY"); err != nil {
			return fmt.Errorf("empty the tables that are reloaded: %w", err)
		}
		return nil
	}
	for _, n := range names {
		if _, err := q.ExecContext(x.ctx, "DELETE FROM "+quoteIdent(n)); err != nil {
			return fmt.Errorf("empty table %q: %w", n, err)
		}
	}
	return nil
}

// parentsFirst orders tables so that a table comes after the tables its foreign keys reference (a
// reloaded table keeps its foreign keys, so a child cannot be loaded before its parent). Tables in a
// cycle keep their order.
func parentsFirst(tables []*TargetTable) []*TargetTable {
	in := map[string]*TargetTable{}
	for _, t := range tables {
		in[t.Name] = t
	}
	done := map[string]bool{}
	var out []*TargetTable
	var visit func(t *TargetTable, path map[string]bool)
	visit = func(t *TargetTable, path map[string]bool) {
		if done[t.Name] || path[t.Name] {
			return
		}
		path[t.Name] = true
		for _, fk := range t.FKs {
			if p := in[fk.RefTable]; p != nil && p != t {
				visit(p, path)
			}
		}
		delete(path, t.Name)
		if !done[t.Name] {
			done[t.Name] = true
			out = append(out, t)
		}
	}
	for _, t := range tables {
		visit(t, map[string]bool{})
	}
	return out
}

// prepareReload checks that every existing table fits the rows that will be loaded into it, and marks
// it as reloaded. A target column may be extra (it keeps its default); a source column that the target
// lacks, another key, or a column of a kind the values do not fit, is an error before anything is written.
func (x *executor) prepareReload(q execer, existing []string) error {
	var tcat *Catalog
	var err error
	if x.to.Engine == Postgres {
		tcat, err = ReadPostgres(x.ctx, q, x.to.schema())
	} else {
		tcat, err = ReadSQLite(x.ctx, q)
	}
	if err != nil {
		return fmt.Errorf("read target schema: %w", err)
	}
	for _, n := range existing {
		var pt *TargetTable
		for _, t := range x.plan.Tables {
			if t.Name == n {
				pt = t
			}
		}
		tt := tcat.Table(n)
		if pt == nil || tt == nil {
			return fmt.Errorf("%q on the target is not a table; nothing was changed", n)
		}
		if problems := reloadProblems(x.to.Engine, pt, tt); len(problems) > 0 {
			return &ReloadMismatchError{Table: n, Problems: problems}
		}
		pt.Reload = true
		if x.to.Engine == Postgres {
			for _, c := range pt.Columns {
				if tc := tt.Column(c.Name); tc != nil && (c.Cast != "" || tc.Special != "") {
					c.Cast = tc.SourceType
				}
			}
		}
	}
	return nil
}

func reloadProblems(to Engine, pt *TargetTable, tt *Table) []string {
	var problems []string
	if strings.Join(pt.PK, ",") != strings.Join(tt.PK, ",") {
		problems = append(problems, fmt.Sprintf("primary key (%s) is not the source's (%s)", strings.Join(tt.PK, ", "), strings.Join(pt.PK, ", ")))
	}
	have := map[string]bool{}
	for _, c := range pt.Columns {
		have[c.Name] = true
		tc := tt.Column(c.Name)
		switch {
		case tc == nil:
			problems = append(problems, fmt.Sprintf("column %q is missing", c.Name))
		case to == Postgres && !reloadKindFits(c, tc):
			problems = append(problems, fmt.Sprintf("column %q is %s and the source values are %s", c.Name, tc.SourceType, c.Type))
		}
	}
	for _, tc := range tt.Columns {
		if !have[tc.Name] && tc.NotNull && tc.Default == "" && tc.Identity == IdentityNone && tc.Generated == "" {
			problems = append(problems, fmt.Sprintf("extra column %q is NOT NULL without a default", tc.Name))
		}
	}
	return problems
}

func reloadKindFits(c *TargetColumn, tc *Column) bool {
	numeric := func(k Kind) bool { return k == KindInt || k == KindFloat || k == KindDecimal }
	switch {
	case tc.Special != "":
		return c.Type == tc.Special || c.Kind == KindText
	case tc.Kind == c.Kind:
		return true
	case tc.Kind == KindInt:
		return false
	}
	return numeric(tc.Kind) && numeric(c.Kind)
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

func (x *executor) sourceSelect(t *TargetTable) (string, []any) {
	var from, order, list string
	if x.from.Engine == SQLite {
		names := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			names[i] = c.Name
		}
		list = sqliteSelectList(names)
		from = quoteIdent(t.Name)
		order = "rowid"
	} else {
		exprs := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			exprs[i] = c.readExpr
		}
		list = strings.Join(exprs, ", ")
		from = qualified(x.from.schema(), t.Name)
		order = "ctid"
	}
	if len(t.PK) > 0 {
		order = quoteList(t.PK)
	}
	query := "SELECT " + list + " FROM " + from
	var args []any
	if f := t.Filter; f != nil {
		if f.where != "" {
			query += " WHERE " + f.where
			args = f.args
		}
		query += " ORDER BY " + order
		if f.limit > 0 {
			query += " LIMIT " + strconv.Itoa(f.limit)
		}
		return query, args
	}
	return query + " ORDER BY " + order, nil
}

func (x *executor) loadTable(q execer, t *TargetTable) (int64, error) {
	ctx := x.ctx
	ncols := len(t.Columns)
	batchRows := batchSize(x.to.Engine, ncols)
	query, args := x.sourceSelect(t)
	rows, err := x.srcQ.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var vals, ptrs []any
	if x.from.Engine == SQLite {
		vals, ptrs = sqliteRowBuffer(ncols)
	} else {
		vals, ptrs = make([]any, ncols), make([]any, ncols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
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
		if x.from.Engine == SQLite {
			fixEmptyBlobs(vals)
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
	b.WriteString("INSERT INTO " + x.qualifiedTarget(t.Name) + " (" + quoteList(names) + ") VALUES ")
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
			} else if blob, ok := row[i].([]byte); ok && len(blob) == 0 {
				// The driver binds an empty blob as NULL: it is written as the literal it is.
				b.WriteString("X''")
				continue
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
			qn := x.qualifiedTarget(t.Name)
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

// tolerated is a failure of a statement the copy can do without (an index, a check, a foreign key): it
// becomes a warning that names the real reason. code is the SQLSTATE on PostgreSQL.
type tolerated struct {
	err  error
	code string
}

func (t *tolerated) Error() string { return t.err.Error() }

func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

// sqlstateTolerable says the failure of a statement is about the statement and the data (a data
// exception, an integrity violation, a syntax error or an undefined name: class 22, 23 and 42) and
// not about the connection, a timeout or a lock: only the first kind may become a warning.
func sqlstateTolerable(code string) bool {
	return strings.HasPrefix(code, "22") || strings.HasPrefix(code, "23") || strings.HasPrefix(code, "42")
}

// exec runs stmt; when it may fail (tolerant) a failure of the statement itself is returned as
// *tolerated and the transaction is left usable (a savepoint on PostgreSQL). Any other failure
// (a timeout, a lost connection) is returned as it is.
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
		code := ""
		if x.to.Engine == Postgres {
			var pe *pgconn.PgError
			if !errors.As(err, &pe) || !sqlstateTolerable(pe.Code) {
				return err
			}
			code = pe.Code
			if _, rerr := q.ExecContext(ctx, "ROLLBACK TO SAVEPOINT dt_try"); rerr != nil {
				return rerr
			}
		}
		return &tolerated{err: err, code: code}
	}
	if x.to.Engine == Postgres {
		_, err := q.ExecContext(ctx, "RELEASE SAVEPOINT dt_try")
		return err
	}
	return nil
}

// foreignKeyViolation is SQLSTATE 23503.
const foreignKeyViolation = "23503"

func (x *executor) applyPost(q execer, s postStmt) error {
	err := x.exec(q, s.sql, s.kind != "")
	if err == nil {
		return nil
	}
	var tol *tolerated
	if !errors.As(err, &tol) {
		return fmt.Errorf("%s: %w", s.obj, err)
	}
	if s.alt != "" && tol.code == foreignKeyViolation && x.exec(q, s.alt, true) == nil {
		why := "existing rows violate this foreign key in the source"
		if x.filtered {
			why = "rows reference a parent row that is not on the target (the source has such rows, or --where / --limit left the parent out)"
		}
		x.plan.warn(s.altKind, s.obj, "%s: it was created NOT VALID (enforced for new rows, existing rows not checked): %v", why, oneLine(tol))
		return nil
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
	why := "copied as they are in the source"
	if x.filtered {
		why = "the source has such rows, or --where / --limit left the parent out"
	}
	for k, n := range counts {
		x.plan.warn(WarnForeignKeyViolations, k, "%d row(s) reference a parent row that does not exist (%s)", n, why)
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
