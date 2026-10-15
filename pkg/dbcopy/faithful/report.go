package faithful

import (
	"fmt"
	"io"
	"strings"
)

// TableReport is what happened to one table.
type TableReport struct {
	Name              string   `json:"name"`
	Rows              int64    `json:"rows"`
	Columns           int      `json:"columns"`
	PrimaryKey        []string `json:"primaryKey"`
	ForeignKeys       int      `json:"foreignKeys"`
	Indexes           int      `json:"indexes"`
	UniqueConstraints int      `json:"uniqueConstraints"`
	CheckConstraints  int      `json:"checkConstraints"`
}

// Totals are the sums over all tables.
type Totals struct {
	Tables            int   `json:"tables"`
	Rows              int64 `json:"rows"`
	Columns           int   `json:"columns"`
	PrimaryKeys       int   `json:"primaryKeys"`
	ForeignKeys       int   `json:"foreignKeys"`
	Indexes           int   `json:"indexes"`
	UniqueConstraints int   `json:"uniqueConstraints"`
	CheckConstraints  int   `json:"checkConstraints"`
}

// Report is the machine-readable summary of a copy (the JSON of `db copy --json`).
type Report struct {
	Command string        `json:"command"`
	Status  string        `json:"status"` // ok | ok-with-warnings | dry-run | failed
	DryRun  bool          `json:"dryRun"`
	From    EndpointInfo  `json:"from"`
	To      EndpointInfo  `json:"to"`
	Tables  []TableReport `json:"tables"`
	Totals  Totals        `json:"totals"`
	// Warnings lists every feature that was not carried exactly, per object.
	Warnings []Warning `json:"warnings"`
	// Plan is the DDL that is (dry-run: would be) executed.
	Plan     []string `json:"plan,omitempty"`
	ExitCode int      `json:"exitCode"`
	Error    string   `json:"error,omitempty"`
}

// EndpointInfo describes one side of the copy without secrets.
type EndpointInfo struct {
	Engine  Engine `json:"engine"`
	Display string `json:"display"`
	Schema  string `json:"schema,omitempty"`
}

// WriteText prints the report for people.
func (r *Report) WriteText(w io.Writer) {
	verb := "copied"
	if r.DryRun {
		verb = "would copy"
	}
	_, _ = fmt.Fprintf(w, "db copy: %s %d tables, %d rows (%d primary keys, %d foreign keys, %d unique constraints, %d indexes, %d check constraints) %s -> %s\n",
		verb, r.Totals.Tables, r.Totals.Rows, r.Totals.PrimaryKeys, r.Totals.ForeignKeys, r.Totals.UniqueConstraints, r.Totals.Indexes, r.Totals.CheckConstraints, r.From.Engine, r.To.Engine)
	if len(r.Warnings) > 0 {
		_, _ = fmt.Fprintf(w, "db copy: %d thing(s) not carried exactly:\n", len(r.Warnings))
		for _, wn := range r.Warnings {
			_, _ = fmt.Fprintf(w, "  warning [%s] %s: %s\n", wn.Kind, wn.Object, wn.Message)
		}
	}
	if r.DryRun {
		_, _ = fmt.Fprintln(w, "plan:")
		for _, s := range r.Plan {
			_, _ = fmt.Fprintln(w, "  "+strings.ReplaceAll(s, "\n", "\n  "))
		}
	}
}

func (r *Report) fill(p *Plan) {
	r.Tables = nil
	r.Totals = Totals{}
	for _, tt := range p.Tables {
		rows := tt.Src.Rows
		if tt.Loaded {
			rows = tt.Copied
		}
		tr := TableReport{Name: tt.Name, Rows: rows, Columns: len(tt.Columns), PrimaryKey: tt.PK,
			ForeignKeys: len(tt.FKs), Indexes: len(tt.Indexes), UniqueConstraints: len(tt.Uniques), CheckConstraints: len(tt.Checks)}
		r.Tables = append(r.Tables, tr)
		r.Totals.Tables++
		r.Totals.Rows += tr.Rows
		r.Totals.Columns += tr.Columns
		if len(tt.PK) > 0 {
			r.Totals.PrimaryKeys++
		}
		r.Totals.ForeignKeys += tr.ForeignKeys
		r.Totals.Indexes += tr.Indexes
		r.Totals.UniqueConstraints += tr.UniqueConstraints
		r.Totals.CheckConstraints += tr.CheckConstraints
	}
	sortWarnings(p.Warnings)
	r.Warnings = p.Warnings
	if r.Warnings == nil {
		r.Warnings = []Warning{}
	}
}
