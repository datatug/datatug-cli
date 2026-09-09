package secureread

import "github.com/datatug/datatug-cli/pkg/accesspolicies"

// LimitationKind names the shape of one applied limitation, per
// REQ:limitation-visible ("policy name, rows filtered yes/no, hidden
// columns") plus REQ:opaque-sql-limitation's native-SQL note.
type LimitationKind string

const (
	// LimitationPolicy attributes a row condition or field allow-list to
	// the policy that applied it, with a human-readable explanation in Note
	// (accesspolicies.Line.String()).
	LimitationPolicy LimitationKind = "policy"
	// LimitationRowsFiltered marks that at least one policy's row condition
	// narrowed the query (the result may legitimately be empty).
	LimitationRowsFiltered LimitationKind = "rowsFiltered"
	// LimitationHiddenColumns lists the queried collection's fields some
	// policy's field allow-list hid from an implicit (wildcard) select.
	// Columns holds the hidden field names.
	LimitationHiddenColumns LimitationKind = "hiddenColumns"
	// LimitationNativeSQL marks that row and column policies were not
	// applied because the query was opaque SQL text
	// (REQ:opaque-sql-limitation) — only source-level allow/deny ran.
	LimitationNativeSQL LimitationKind = "nativeSql"
)

// Limitation is one policy effect a Result's caller MUST be told about
// (REQ:limitation-visible) rather than have applied silently. Not every
// field is set for every Kind: LimitationRowsFiltered carries no Policy;
// LimitationHiddenColumns carries Columns; LimitationPolicy and
// LimitationNativeSQL carry Policy and Note.
type Limitation struct {
	Kind LimitationKind
	// Policy names the policy document responsible (its access.Policy.Name());
	// "native-sql" for LimitationNativeSQL.
	Policy string
	// Note is a human-readable explanation, e.g. accesspolicies.Line.String()
	// or the opaque-SQL note text.
	Note string
	// Count is reserved for a future exact filtered-row count (e.g. "3 of 12
	// rows hidden"); nil until a caller computes it — Executor does not run
	// the extra unrestricted count query needed to fill it in.
	Count *int
	// Columns holds the hidden field names for LimitationHiddenColumns.
	Columns []string
}

// Row is one returned record: its key (native SQL rows have none, so Key is
// "") and its JSON-shaped data, already policy-redacted by the time it
// reaches here.
type Row struct {
	Key  string
	Data map[string]any
}

// Result is what every Executor.Run* method returns: the columns and rows a
// principal is allowed to see, and the limitations that applied to produce
// them.
type Result struct {
	Columns     []string
	Rows        []Row
	Limitations []Limitation
}

// limitationsFromLines turns accesspolicies.Explain's per-policy Lines into
// the LimitationPolicy / LimitationRowsFiltered entries a Result must carry.
// A denied Line never reaches here — accesspolicies.Run already turns an
// overall denial into an error the Executor propagates instead of a Result.
func limitationsFromLines(lines []accesspolicies.Line) []Limitation {
	var limitations []Limitation
	rowsFiltered := false
	for _, line := range lines {
		if !line.Allowed {
			continue
		}
		if line.Condition == "" && len(line.FieldLists) == 0 {
			continue // "no limitations" line: this policy allowed everything
		}
		limitations = append(limitations, Limitation{Kind: LimitationPolicy, Policy: line.Policy, Note: line.String()})
		if line.Condition != "" {
			rowsFiltered = true
		}
	}
	if rowsFiltered {
		limitations = append(limitations, Limitation{Kind: LimitationRowsFiltered})
	}
	return limitations
}
