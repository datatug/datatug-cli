// engine.go — copy engine for `datatug db copy`.
//
// Implements REQ:source-schema-via-dbschema, REQ:target-schema-via-ddl,
// REQ:source-introspection-failure, REQ:recreate-drops-first,
// REQ:parallel-streams-flag, REQ:concurrency-cap.
// Row streaming (REQ:bounded-memory-streaming, REQ:row-insert-via-dalgo)
// lives in engine_rows.go.

package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"

	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
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

	// ParallelStreams is the requested max number of source tables copied
	// concurrently. 0 means "use the default": runtime.NumCPU()-1 (with a
	// floor of 1). Negative values are normalized to 1. Capped to 1 when
	// either source or target advertises SupportsConcurrentConnections()==false
	// (REQ:concurrency-cap).
	ParallelStreams int

	// Filters carries resolved filtering directives (table include/exclude,
	// row WHERE predicates, row limits). nil or empty means "no filtering
	// — copy whole DB per parent Feature ACs". Subsequent tasks consume
	// this at two seams (pre-worker table filter; engine_rows query builder).
	// Spec: spec/features/cli/db/copy/filtering/README.md
	Filters *filter.Directives
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
// no PK get schema replicated but row copy is skipped with the reason
// recorded in RowSkips. Composite-PK tables are now copied (key encoded
// as `__`-joined PK values; see encodeRecordID in engine_rows.go).
//
// Concurrency: opts.ParallelStreams governs how many tables are copied in
// parallel. The effective value is capped to 1 if either source or target
// advertises SupportsConcurrentConnections()==false. When the cap reduces
// an explicitly-requested value >1, one warning line is emitted on stderr.
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

	// 2. If --overwrite is omitted, pre-flight verify the target is "empty
	//    for this copy" (REQ:empty-target-check). Any source-named target
	//    table with >=1 row aborts BEFORE we touch the target.
	if opts.Overwrite == "" {
		sourceNames := make([]string, len(refs))
		for i, r := range refs {
			sourceNames[i] = r.Name()
		}
		if err := checkEmptyTarget(ctx, target, sourceNames); err != nil {
			return summary, err
		}
	}

	// 3. If --overwrite=recreate, drop target tables that match source names
	//    BEFORE we introspect each one (REQ:recreate-drops-first).
	if opts.Overwrite == "recreate" {
		for _, ref := range refs {
			if err := ddl.DropCollection(ctx, target, ref.Name(), ddl.IfExists()); err != nil {
				return summary, fmt.Errorf("drop target collection %q: %w", ref.Name(), err)
			}
		}
	}

	// 3b. If --overwrite=reload, validate every source-named target table's
	//     schema is a superset of the source's (REQ:reload-schema-match).
	//     ALL tables are validated BEFORE any TRUNCATE; the first mismatch
	//     aborts with NO target write — satisfies AC:reload-rejects-schema-mismatch.
	if opts.Overwrite == "reload" {
		sourceBackend := backendOf(source)
		targetBackend := backendOf(target)
		for _, ref := range refs {
			if err := validateReloadSchema(ctx, source, target, &ref, sourceBackend, targetBackend); err != nil {
				return summary, err
			}
		}
	}

	// 3. Resolve effective parallelism (REQ:parallel-streams-flag, REQ:concurrency-cap).
	effective := resolveParallelism(opts.ParallelStreams, source, target, stderr)

	// 4. Per-table worker pool. Each worker: describe → create → copy rows.
	jobs := make(chan dal.CollectionRef, len(refs))
	for _, ref := range refs {
		jobs <- ref
	}
	close(jobs)

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex // guards summary mutations
		errOnce sync.Once
		firstErr error
	)

	recordErr := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

	for i := 0; i < effective; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range jobs {
				if workerCtx.Err() != nil {
					return
				}
				if err := copyOneTable(workerCtx, source, target, ref, opts, stderr, &summary, &mu); err != nil {
					recordErr(err)
					return
				}
			}
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return summary, firstErr
	}
	return summary, nil
}

// copyOneTable handles describe → create → row-copy for a single source
// collection. Mutations to the shared summary happen under mu.
func copyOneTable(
	ctx context.Context,
	source, target dal.DB,
	ref dal.CollectionRef,
	opts CopyOpts,
	stderr io.Writer,
	summary *SourceSummary,
	mu *sync.Mutex,
) error {
	def, err := dbschema.DescribeCollection(ctx, source, &ref)
	if err != nil {
		// dalgo2sqlite rejects DATETIME / NUMERIC today (see upstream
		// issue). Log the skip on stderr so the user knows, and continue
		// — the rest of the schema can still be replicated.
		mu.Lock()
		_, _ = fmt.Fprintf(stderr,
			"skipping %q: source DescribeCollection failed: %v\n",
			ref.Name(), err)
		summary.Skipped = append(summary.Skipped, ref.Name())
		mu.Unlock()
		return nil
	}

	if opts.Progress != nil {
		opts.Progress.StartTable(def.Name, -1)
	}

	// For --overwrite=reload, the target table already exists with a
	// validated superset schema (see validateReloadSchema in the Copy
	// pre-flight). We must NOT recreate it — that would drop the extra
	// target-only columns the AC requires us to preserve. Instead, we
	// truncate the target rows and then stream source rows in.
	if opts.Overwrite == "reload" {
		if err := truncateTargetCollection(ctx, target, def.Name); err != nil {
			return fmt.Errorf("truncate target %q: %w", def.Name, err)
		}
	} else {
		if err := ddl.CreateCollection(ctx, target, *def); err != nil {
			return fmt.Errorf("create target collection %q: %w", def.Name, err)
		}
		mu.Lock()
		summary.Created++
		summary.CreatedNames = append(summary.CreatedNames, def.Name)
		mu.Unlock()
	}

	// Row streaming — unless explicitly disabled.
	var rowsCopied int64
	if !opts.SchemaOnly {
		rowsCopied, err = copyRows(ctx, source, target, def)
		switch {
		case err == nil:
			mu.Lock()
			summary.RowsCopied += rowsCopied
			summary.RowsByTable[def.Name] = rowsCopied
			mu.Unlock()
		case errors.Is(err, ErrNoPrimaryKey):
			// Schema replicated; row copy not possible for this table.
			mu.Lock()
			summary.RowSkips[def.Name] = err.Error()
			_, _ = fmt.Fprintf(stderr, "row copy skipped for %q: %v\n", def.Name, err)
			mu.Unlock()
		default:
			return fmt.Errorf("row copy %q: %w", def.Name, err)
		}
	}

	if opts.Progress != nil {
		opts.Progress.FinishTable(def.Name, rowsCopied, 0)
	}
	return nil
}

// resolveParallelism computes the effective worker count from the
// requested ParallelStreams value, the default fallback, and the
// ConcurrencyAware capability of source/target. When the cap reduces an
// explicitly-requested value >1, one warning line is emitted on stderr
// per REQ:concurrency-cap.
func resolveParallelism(requested int, source, target dal.DB, stderr io.Writer) int {
	defaulted := requested == 0
	if requested == 0 {
		requested = runtime.NumCPU() - 1
		if requested < 1 {
			requested = 1
		}
	}
	if requested < 1 {
		requested = 1
	}

	srcConc := source.SupportsConcurrentConnections()
	tgtConc := target.SupportsConcurrentConnections()
	if srcConc && tgtConc {
		return requested
	}

	// Capped to 1. Warn only if the user explicitly asked for more.
	if !defaulted && requested > 1 {
		var constraining string
		switch {
		case !srcConc && !tgtConc:
			constraining = fmt.Sprintf("%s (source) and %s (target)",
				adapterName(source), adapterName(target))
		case !srcConc:
			constraining = fmt.Sprintf("%s (source)", adapterName(source))
		case !tgtConc:
			constraining = fmt.Sprintf("%s (target)", adapterName(target))
		}
		_, _ = fmt.Fprintf(stderr,
			"warning: %s requires serial writes; ignoring --parallel-streams=%d, using 1\n",
			constraining, requested)
	}
	return 1
}

// ErrSourceHasNoTables signals that the source introspected cleanly but
// has zero collections. Callers should exit 0 with a stderr note per
// REQ:source-introspection-failure.
var ErrSourceHasNoTables = errors.New("source has no tables; nothing to copy")

// backendOf maps a DALgo adapter Name() to the dbcopy backend constant
// accepted by MapType (BackendSQLite, BackendInGitDB). Falls back to
// the raw adapter name when no mapping is known; MapType will then
// reject the value with a clear error.
func backendOf(db dal.DB) string {
	switch adapterName(db) {
	case "dalgo2sqlite":
		return BackendSQLite
	case "dalgo2ingitdb":
		return BackendInGitDB
	default:
		return adapterName(db)
	}
}

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
