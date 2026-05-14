// engine.go — copy engine for `datatug db copy`.
//
// Implements REQ:source-schema-via-dbschema, REQ:target-schema-via-ddl,
// REQ:source-introspection-failure, REQ:recreate-drops-first.
// Row streaming (REQ:bounded-memory-streaming, REQ:row-insert-via-dalgo)
// lives in engine_rows.go.

package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
)

// CopyOpts controls a Copy call.
type CopyOpts struct {
	// Overwrite is "" (require empty target), "recreate" (drop source-named
	// tables, then create from source), or "reload" (reserved — row-level
	// semantics; behaves like "recreate" for schema-only copies until row
	// CRUD lands).
	Overwrite string

	// Stderr receives per-table skip / row-error notes and the final
	// summary note. Defaults to discard if nil.
	Stderr io.Writer

	// Progress, if non-nil, receives per-table progress lines.
	Progress *ProgressWriter

	// SchemaOnly, if true, skips row streaming entirely and only replicates
	// the target schema. Useful for E2E-test scaffolding and for backends
	// where row streaming isn't supported in this direction.
	SchemaOnly bool
}

// SourceSummary is what Copy reports back to the caller.
type SourceSummary struct {
	Tables        int
	Created       int
	CreatedNames  []string // names of collections created on the target, in source-iteration order
	Skipped       []string // tables skipped at DescribeCollection time
	RowsCopied    int64    // total rows inserted into the target across all tables
	RowsByTable   map[string]int64
	RowSkips      map[string]string // table → reason (e.g. composite PK, no PK)
	TargetBackend string            // adapter name of target, for error messages
}

// Copy replicates the source database into the target — schema first,
// then row data per table.
//
// Schema replication uses DALgo dbschema/ddl. Row streaming uses
// ExecuteQueryToRecordsReader on the source and RunReadwriteTransaction →
// InsertMulti on the target (see engine_rows.go).
//
// If opts.SchemaOnly is true, only schema is replicated.
//
// Tables the source can't describe (e.g. dalgo2sqlite rejecting DATETIME /
// NUMERIC) are appended to Skipped and processing continues. Tables with
// no PK or a composite PK get schema replicated but row copy is skipped
// with the reason recorded in RowSkips.
//
// Errors:
//   - ErrSourceHasNoTables — source introspects cleanly but has zero
//     collections.
//   - any other error — wrapped with the failing operation and table name.
func Copy(ctx context.Context, source, target dal.DB, opts CopyOpts) (SourceSummary, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	summary := SourceSummary{
		TargetBackend: adapterName(target),
		RowsByTable:   map[string]int64{},
		RowSkips:      map[string]string{},
	}

	// 1. Introspect source.
	refs, err := dbschema.ListCollections(ctx, source, nil)
	if err != nil {
		return summary, fmt.Errorf("list source collections: %w", err)
	}
	summary.Tables = len(refs)
	if len(refs) == 0 {
		return summary, ErrSourceHasNoTables
	}

	// 2. If --overwrite=recreate, drop target tables that match source names
	//    BEFORE we introspect each one (REQ:recreate-drops-first).
	if opts.Overwrite == "recreate" {
		for _, ref := range refs {
			if err := ddl.DropCollection(ctx, target, ref.Name(), ddl.IfExists()); err != nil {
				return summary, fmt.Errorf("drop target collection %q: %w", ref.Name(), err)
			}
		}
	}

	// 3. For each source table: describe → create on target → stream rows.
	for _, ref := range refs {
		def, err := dbschema.DescribeCollection(ctx, source, &ref)
		if err != nil {
			// dalgo2sqlite rejects DATETIME / NUMERIC today (see upstream
			// issue). Log the skip on stderr so the user knows, and continue
			// — the rest of the schema can still be replicated.
			fmt.Fprintf(stderr,
				"skipping %q: source DescribeCollection failed: %v\n",
				ref.Name(), err)
			summary.Skipped = append(summary.Skipped, ref.Name())
			continue
		}

		if opts.Progress != nil {
			opts.Progress.StartTable(def.Name, -1)
		}

		if err := ddl.CreateCollection(ctx, target, *def); err != nil {
			return summary, fmt.Errorf("create target collection %q: %w", def.Name, err)
		}
		summary.Created++
		summary.CreatedNames = append(summary.CreatedNames, def.Name)

		// Row streaming — unless explicitly disabled.
		var rowsCopied int64
		if !opts.SchemaOnly {
			rowsCopied, err = copyRows(ctx, source, target, def)
			switch {
			case err == nil:
				summary.RowsCopied += rowsCopied
				summary.RowsByTable[def.Name] = rowsCopied
			case errors.Is(err, ErrNoPrimaryKey),
				errors.Is(err, ErrCompositePKUnsupported):
				// Schema replicated; row copy not possible for this table.
				summary.RowSkips[def.Name] = err.Error()
				fmt.Fprintf(stderr, "row copy skipped for %q: %v\n", def.Name, err)
			default:
				return summary, fmt.Errorf("row copy %q: %w", def.Name, err)
			}
		}

		if opts.Progress != nil {
			opts.Progress.FinishTable(def.Name, rowsCopied, 0)
		}
	}

	return summary, nil
}

// ErrSourceHasNoTables signals that the source introspected cleanly but
// has zero collections. Callers should exit 0 with a stderr note per
// REQ:source-introspection-failure.
var ErrSourceHasNoTables = errors.New("source has no tables; nothing to copy")

// adapterName returns the adapter Name() if available, else "unknown".
func adapterName(db dal.DB) string {
	if db == nil {
		return "unknown"
	}
	a := db.Adapter()
	if a == nil {
		return "unknown"
	}
	return a.Name()
}
