package faithful

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
)

// WriteScript streams the script of a plan: the header, the session settings, every table, its rows (in a total
// order, so the bytes are the same on every run), the counters of the auto-numbered columns, then indexes,
// checks and foreign keys (keys follow the data), and last the marker row. The rows are read from src one
// table at a time. It returns the number of rows written.
//
// Every line starts with a token this function wrote; no value can start one (see Dialect.Literal).
func WriteScript(ctx context.Context, w io.Writer, src Queryer, p *ScriptPlan, h Header) (int64, error) {
	d := p.Dialect
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
			if c.AutoInc && c.Src.SequenceStart > 0 {
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

func writeRows(ctx context.Context, bw *bufio.Writer, src Queryer, p *ScriptPlan, t *ScriptTable) (int64, error) {
	d := p.Dialect
	if t.Rows == 0 {
		return 0, nil
	}
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Src.Name
	}
	rows, err := src.QueryContext(ctx, "SELECT "+sqliteSelectList(names)+" FROM "+quoteIdent(t.Src.Name)+" ORDER BY "+t.OrderBy)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	maxRows, maxBytes := d.BatchLimits()
	head := d.InsertHead(t)
	vals, ptrs := sqliteRowBuffer(len(names))
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
		var row strings.Builder
		row.WriteByte('(')
		for i, c := range t.Columns {
			lit, err := d.Literal(c.Choice, vals[i])
			if err != nil {
				return n, fmt.Errorf("row %d, column %q: %w", n+1, c.Src.Name, err)
			}
			if i > 0 {
				row.WriteByte(',')
			}
			row.WriteString(lit)
		}
		row.WriteByte(')')
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
		return nil, err
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
