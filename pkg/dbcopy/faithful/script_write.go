package faithful

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
)

// ScriptUsageError is a request the user can fix by changing it (a table that does not exist, a header value
// the script cannot carry): the command exits with 2, not 1.
type ScriptUsageError struct{ Err error }

func (e *ScriptUsageError) Error() string { return e.Err.Error() }
func (e *ScriptUsageError) Unwrap() error { return e.Err }

// headerValidator is implemented by a dialect that can refuse a header up front: the script must not load
// every row and then fail on its last statement.
type headerValidator interface {
	ValidateHeader(h Header) error
}

// lineLimiter is implemented by a dialect whose client cannot read a line longer than some length (SQL*Plus): the
// row writer starts a new line between two values when the next one would pass MaxLine.
type lineLimiter interface {
	MaxLine() int
}

// rowStatementer is implemented by a dialect that cannot write every row inside a multi-row insert: RowStatement
// is a statement of its own for a row that needs one (ok), and the row is then left out of the batch.
type rowStatementer interface {
	RowStatement(t *ScriptTable, vals []any) (stmt string, ok bool, err error)
}

// everyCounter is implemented by a dialect whose identity counter does not follow the rows that were inserted with
// a value of their own: its CounterSQL is written for every auto-numbered column, not only when SQLite keeps a counter.
type everyCounter interface {
	CountsEveryIdentity() bool
}

func countsEveryIdentity(d Dialect) bool {
	e, ok := d.(everyCounter)
	return ok && e.CountsEveryIdentity()
}

// hyphenContinuer is implemented by a dialect whose client reads a line that ends in a hyphen as continued on the next line
// (SQL*Plus, outside a "--" comment): the writer refuses such a line, so nothing is ever joined.
type hyphenContinuer interface {
	HyphenContinues() bool
}

// lineGuard refuses a script that has a line longer than a client can read: the dialect wraps what it writes, and a line
// that is over the limit anyway is a generator fault that must not become a skipped statement (SQL*Plus ignores an
// overlong line, SP2-0027, and goes on). The error names the line.
type lineGuard struct {
	w        io.Writer
	max      int
	noHyphen bool   // a non-comment line may not end in a hyphen
	cur      int    // bytes of the line being written
	head     string // its first bytes
	last     byte   // its last byte that is not a blank
}

func (g *lineGuard) Write(b []byte) (int, error) {
	for _, c := range b {
		if c == '\n' {
			if g.noHyphen && g.last == '-' && !strings.HasPrefix(g.head, "--") {
				return 0, fmt.Errorf("the script has a line that ends in a hyphen, which its client joins with the next line (it starts %q): the statement cannot be wrapped at a safe place", g.head)
			}
			g.cur, g.head, g.last = 0, "", 0
			continue
		}
		if g.cur < 60 {
			g.head += string(c)
		}
		if c != ' ' && c != '\t' {
			g.last = c
		}
		g.cur++
		if g.cur > g.max {
			return 0, fmt.Errorf("the script has a line longer than the %d characters its client reads (it starts %q): the statement cannot be wrapped at a safe place", g.max, g.head)
		}
	}
	return g.w.Write(b)
}

// WriteScript streams the script of a plan: the header, the session settings, every table, its rows (in a total
// order, so the bytes are the same on every run), the counters of the auto-numbered columns, then indexes,
// checks and foreign keys (keys follow the data), and last the marker row. The rows are read from src one
// table at a time. It returns the number of rows written.
//
// Every line starts with a token this function wrote; no value can start one (see Dialect.Literal).
func WriteScript(ctx context.Context, w io.Writer, src Queryer, p *ScriptPlan, h Header) (int64, error) {
	d := p.Dialect
	if ll, ok := d.(lineLimiter); ok {
		hc, _ := d.(hyphenContinuer)
		w = &lineGuard{w: w, max: ll.MaxLine(), noHyphen: hc != nil && hc.HyphenContinues()}
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	line := func(s string) { _, _ = bw.WriteString(s); _ = bw.WriteByte('\n') }
	lines := func(ls []string) {
		for _, l := range ls {
			line(l)
		}
	}
	lines(d.Header(p, h))
	line("")
	lines(d.Prelude(p, h))
	line("")
	for _, t := range p.Tables {
		line(d.CreateTable(t))
	}
	var total int64
	for _, t := range p.Tables {
		n, err := writeRows(ctx, bw, src, p, t)
		if err != nil {
			return total, fmt.Errorf("table %q: %w", t.Name, err)
		}
		total += n
	}
	for _, t := range p.Tables {
		for _, c := range t.Columns {
			if c.AutoInc && (c.Src.SequenceStart > 0 || countsEveryIdentity(d)) {
				line(d.CounterSQL(t, c, c.Src.SequenceStart))
			}
		}
	}
	for _, t := range p.Tables {
		for _, ix := range t.Indexes {
			line(d.IndexSQL(t, ix))
		}
	}
	for _, t := range p.Tables {
		for _, ck := range t.Checks {
			line(d.CheckSQL(t, ck))
		}
	}
	for _, t := range p.Tables {
		for _, fk := range t.FKs {
			line(d.FKSQL(p, t, fk))
		}
	}
	lines(d.Epilogue(p, h))
	return total, bw.Flush()
}

// joinRow writes the values of a row as "(a,b,c)". With a limit, a value that would take its line past the limit
// starts a line of its own: the line still starts with a value the writer wrote.
func joinRow(lits []string, limit int) string {
	var row strings.Builder
	row.WriteByte('(')
	cur := 1 // the length of the line being written
	for i, lit := range lits {
		if i > 0 {
			row.WriteByte(',')
			cur++
		}
		first, _, _ := strings.Cut(lit, "\n")
		if limit > 0 && i > 0 && cur+len(first) > limit {
			row.WriteByte('\n')
			cur = 0
		}
		row.WriteString(lit)
		if k := strings.LastIndexByte(lit, '\n'); k >= 0 {
			cur = len(lit) - k - 1
		} else {
			cur += len(lit)
		}
	}
	row.WriteByte(')')
	return row.String()
}

func writeRows(ctx context.Context, bw *bufio.Writer, src Queryer, p *ScriptPlan, t *ScriptTable) (int64, error) {
	d := p.Dialect
	if t.Rows == 0 {
		return 0, nil
	}
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Src.Name
	}
	rows, err := src.QueryContext(ctx, "SELECT "+scriptSelectList(t)+" FROM "+quoteIdent(t.Src.Name)+" ORDER BY "+t.OrderBy)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	maxRows, maxBytes := d.BatchLimits()
	head := d.InsertHead(t)
	vals, ptrs := sqliteRowBuffer(len(names))
	limit := 0
	if ll, ok := d.(lineLimiter); ok {
		limit = ll.MaxLine()
	}
	own, _ := d.(rowStatementer)
	var n int64
	inBatch, size := 0, 0
	var sb strings.Builder
	flush := func() {
		if inBatch == 0 {
			return
		}
		for _, l := range d.BeginData(t) {
			_, _ = bw.WriteString(l + "\n")
		}
		_, _ = bw.WriteString(head + "\n")
		_, _ = bw.WriteString(sb.String())
		_, _ = bw.WriteString(";\n")
		for _, l := range d.EndData(t) {
			_, _ = bw.WriteString(l + "\n")
		}
		sb.Reset()
		inBatch, size = 0, 0
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		fixEmptyBlobs(vals)
		if own != nil {
			stmt, ok, err := own.RowStatement(t, vals)
			if err != nil {
				return n, fmt.Errorf("row %d: %w", n+1, err)
			}
			if ok {
				flush()
				_, _ = bw.WriteString(stmt + "\n")
				n++
				continue
			}
		}
		lits := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			lit, err := d.Literal(c.Choice, vals[i])
			if err != nil {
				return n, fmt.Errorf("row %d, column %q: %w", n+1, c.Src.Name, err)
			}
			lits[i] = lit
		}
		var row strings.Builder
		row.WriteString(joinRow(lits, limit))
		if inBatch > 0 {
			sb.WriteString(",\n")
		}
		sb.WriteString(row.String())
		inBatch++
		size += row.Len()
		n++
		if inBatch >= maxRows || size >= maxBytes {
			flush()
		}
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	flush()
	if n != t.Rows {
		return n, fmt.Errorf("the table held %d rows when it was counted and %d were read", t.Rows, n)
	}
	return n, nil
}

// ScriptRequest is one SQL-script export.
type ScriptRequest struct {
	Dialect string
	DB      *sql.DB
	Display string
	Header  Header
	// DryRun plans and reports, and writes nothing.
	DryRun bool
	// Include and Exclude name the tables to write (all when both are empty).
	Include, Exclude []string
}

// ExportScript reads the SQLite database of the request in one read transaction, plans the script and, unless
// it is a dry run, writes it to w. The report has the same shape as the report of a copy.
func ExportScript(ctx context.Context, req ScriptRequest, w io.Writer) (*Report, error) {
	d, err := DialectByName(req.Dialect)
	if err != nil {
		return nil, err
	}
	if err := ValidateNotice(req.Header.Notice); err != nil {
		return nil, &ScriptUsageError{err}
	}
	if v, ok := d.(headerValidator); ok {
		if err := v.ValidateHeader(req.Header); err != nil {
			return nil, &ScriptUsageError{err}
		}
	}
	tx, err := req.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	cat, err := ReadSQLite(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := filterTables(cat, req.Include, req.Exclude, &cat.Notes); err != nil {
		return nil, &ScriptUsageError{err}
	}
	stats, err := scanScriptStats(ctx, tx, cat)
	if err != nil {
		return nil, err
	}
	p, err := PlanSQLiteTo(ctx, d, tx, cat, stats)
	if err != nil {
		return nil, err
	}
	rep := &Report{Command: "db copy", DryRun: req.DryRun, From: EndpointInfo{Engine: SQLite, Display: req.Display}, To: EndpointInfo{Engine: Engine(d.Name()), Display: "sqlscript"}}
	rep.fillScript(p)
	if req.DryRun {
		rep.Status = "dry-run"
		rep.Plan = dryRunPlan(p)
		return rep, nil
	}
	if _, err := WriteScript(ctx, w, tx, p, req.Header); err != nil {
		return rep, err
	}
	rep.Status = "ok"
	if len(rep.Warnings) > 0 {
		rep.Status = "ok-with-warnings"
	}
	return rep, nil
}

// PlanScript reads and plans without writing; the cross-engine verifier uses it to know what the script created.
func PlanScript(ctx context.Context, dialect string, db *sql.DB) (*ScriptPlan, error) {
	d, err := DialectByName(dialect)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	cat, err := ReadSQLite(ctx, tx)
	if err != nil {
		return nil, err
	}
	stats, err := scanScriptStats(ctx, tx, cat)
	if err != nil {
		return nil, err
	}
	return PlanSQLiteTo(ctx, d, tx, cat, stats)
}

func dryRunPlan(p *ScriptPlan) []string {
	d := p.Dialect
	var out []string
	for _, t := range p.Tables {
		out = append(out, d.CreateTable(t))
	}
	for _, t := range p.Tables {
		for _, ix := range t.Indexes {
			out = append(out, d.IndexSQL(t, ix))
		}
		for _, ck := range t.Checks {
			out = append(out, d.CheckSQL(t, ck))
		}
		for _, fk := range t.FKs {
			out = append(out, d.FKSQL(p, t, fk))
		}
	}
	return out
}

func (r *Report) fillScript(p *ScriptPlan) {
	r.Tables, r.Totals = nil, Totals{}
	for _, t := range p.Tables {
		tr := TableReport{Name: t.Name, Rows: t.Rows, Columns: len(t.Columns), PrimaryKey: t.PK, ForeignKeys: len(t.FKs),
			Indexes: len(t.Indexes), UniqueConstraints: len(t.Uniques), CheckConstraints: len(t.Checks)}
		r.Tables = append(r.Tables, tr)
		r.Totals.Tables++
		r.Totals.Rows += tr.Rows
		r.Totals.Columns += tr.Columns
		if len(t.PK) > 0 {
			r.Totals.PrimaryKeys++
		}
		r.Totals.ForeignKeys += tr.ForeignKeys
		r.Totals.Indexes += tr.Indexes
		r.Totals.UniqueConstraints += tr.UniqueConstraints
		r.Totals.CheckConstraints += tr.CheckConstraints
	}
	r.Warnings = p.Warnings
	if r.Warnings == nil {
		r.Warnings = []Warning{}
	}
}
