// engine.go — schema-only first slice of `datatug db copy`.
//
// Implements REQ:source-schema-via-dbschema, REQ:target-schema-via-ddl,
// REQ:source-introspection-failure, REQ:recreate-drops-first.
//
// Row streaming (REQ:bounded-memory-streaming, REQ:row-insert-via-dalgo) is
// deferred until dalgo2ingitdb lands row CRUD; see
// docs/upstream-issues/ingitdb-cli-dalgo2ingitdb-row-crud.md.

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

	// Stderr receives the schema-only follow-up note and any concurrency
	// warning. Defaults to discard if nil.
	Stderr io.Writer

	// Progress, if non-nil, receives per-table progress lines.
	Progress *ProgressWriter
}

// SourceSummary is what Copy reports back to the caller.
type SourceSummary struct {
	Tables        int
	Created       int
	Skipped       []string // tables skipped (e.g. dalgo2sqlite DATETIME/NUMERIC rejection)
	RowStreaming  bool     // true once dalgo2ingitdb lands row CRUD; today: false
	TargetBackend string   // adapter name of target, for error messages
}

// Copy replicates the source database into the target.
//
// First-slice scope: schema only — collections, primary keys, indexes.
// Row data is NOT copied; see package doc and the upstream issue tracked
// at docs/upstream-issues/ingitdb-cli-dalgo2ingitdb-row-crud.md.
//
// Errors:
//   - ErrSourceHasNoTables — source introspects cleanly but has zero
//     collections; the caller should exit 0 with a stderr note (per
//     REQ:source-introspection-failure).
//   - any other error — wrapped with the failing operation and table name.
func Copy(ctx context.Context, source, target dal.DB, opts CopyOpts) (SourceSummary, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	summary := SourceSummary{TargetBackend: adapterName(target)}

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

	// 3. For each source table: describe, then create on target.
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

		if opts.Progress != nil {
			opts.Progress.FinishTable(def.Name, 0, 0)
		}
	}

	// 4. Emit the schema-only follow-up note. Row streaming hasn't shipped
	//    yet; the user deserves to know.
	fmt.Fprintln(stderr,
		"note: schema replicated; row data not yet copied — dalgo2ingitdb row CRUD pending (see docs/upstream-issues/ingitdb-cli-dalgo2ingitdb-row-crud.md)")

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
