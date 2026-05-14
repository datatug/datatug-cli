// engine_rows.go — row streaming for `datatug db copy`.
//
// Implements REQ:bounded-memory-streaming and REQ:row-insert-via-dalgo
// from spec/features/cli/db/copy/README.md.
//
// Source-side: builds a `SELECT * FROM "<table>"` text query and walks
// `ExecuteQueryToRecordsReader` results. Both DALgo backends in scope
// produce `map[string]any`-shaped Record.Data() from this path
// (dalgo2sql/reader_records.go and dalgo2ingitdb/tx_readonly.go).
//
// Target-side: each batch is written via
// `target.RunReadwriteTransaction(ctx, tx → tx.InsertMulti(batch))`.

package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// defaultRowBatchSize is the buffer size used by copyRows. Picked empirically
// — small enough to keep per-table memory bounded, big enough that InsertMulti
// amortizes its per-call overhead across multiple records.
const defaultRowBatchSize = 500

// copyRows streams every row from the source collection identified by def
// into the target via tx.InsertMulti, in batches of defaultRowBatchSize.
// Returns the count of rows successfully inserted.
//
// Caveats (deferred to follow-ups, documented in stderr when encountered):
//
//   - Composite primary keys are not yet supported on the target — single-
//     column PKs only. A composite PK causes ErrCompositePKUnsupported.
//   - Tables with NO primary key declared also error out
//     (ErrNoPrimaryKey). MVP requires a PK to construct the target record
//     key.
//   - On any per-row insert error, the function aborts the table and
//     returns the partial count plus the wrapped error.
func copyRows(
	ctx context.Context,
	src dal.DB,
	tgt dal.DB,
	def *dbschema.CollectionDef,
) (int64, error) {
	if def == nil {
		return 0, errors.New("copyRows: nil CollectionDef")
	}
	if len(def.PrimaryKey) == 0 {
		return 0, fmt.Errorf("%w: collection %q", ErrNoPrimaryKey, def.Name)
	}
	if len(def.PrimaryKey) > 1 {
		return 0, fmt.Errorf("%w: collection %q has PK %v",
			ErrCompositePKUnsupported, def.Name, def.PrimaryKey)
	}
	pkField := string(def.PrimaryKey[0])

	// Source-side read. SELECT * with quoted identifier — SQLite accepts
	// double-quoted identifiers; dalgo2sql passes the text through unchanged.
	query := dal.NewTextQuery(`SELECT * FROM "`+def.Name+`"`, nil)
	reader, err := src.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("source ExecuteQueryToRecordsReader on %q: %w", def.Name, err)
	}
	defer func() { _ = reader.Close() }()

	var (
		rowsCopied int64
		batch      = make([]dal.Record, 0, defaultRowBatchSize)
	)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// RunReadwriteTransaction commits when the worker returns nil.
		err := tgt.RunReadwriteTransaction(ctx,
			func(ctx context.Context, tx dal.ReadwriteTransaction) error {
				return tx.InsertMulti(ctx, batch)
			})
		if err != nil {
			return err
		}
		rowsCopied += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	for {
		srcRec, err := reader.Next()
		if err != nil {
			// dalgo2sql returns io.EOF on exhaustion; dalgo2ingitdb returns
			// dal.ErrNoMoreRecords. Accept either.
			if errors.Is(err, io.EOF) || errors.Is(err, dal.ErrNoMoreRecords) {
				break
			}
			return rowsCopied, fmt.Errorf("read row from %q: %w", def.Name, err)
		}

		data, ok := srcRec.Data().(map[string]any)
		if !ok {
			return rowsCopied, fmt.Errorf(
				"source %q produced unexpected Record data shape %T (expected map[string]any)",
				def.Name, srcRec.Data())
		}

		pkVal, ok := data[pkField]
		if !ok {
			return rowsCopied, fmt.Errorf(
				"row in %q missing PK column %q",
				def.Name, pkField)
		}
		if pkVal == nil {
			return rowsCopied, fmt.Errorf(
				"row in %q has nil PK %q",
				def.Name, pkField)
		}

		key := dal.NewKeyWithID(def.Name, fmt.Sprintf("%v", pkVal))
		rec := dal.NewRecordWithData(key, data)
		batch = append(batch, rec)

		if len(batch) >= defaultRowBatchSize {
			if err := flush(); err != nil {
				return rowsCopied, fmt.Errorf("insert batch into target %q: %w", def.Name, err)
			}
		}
	}

	if err := flush(); err != nil {
		return rowsCopied, fmt.Errorf("flush final batch into target %q: %w", def.Name, err)
	}
	return rowsCopied, nil
}

// ErrNoPrimaryKey is returned when a source collection has no PK declared.
// The MVP row-copy path requires a single-column PK to construct target
// record keys.
var ErrNoPrimaryKey = errors.New("source collection has no primary key declared")

// ErrCompositePKUnsupported is returned for collections whose primary key
// spans multiple columns. Composite-PK row copy is a follow-up.
var ErrCompositePKUnsupported = errors.New("composite primary key not yet supported for row copy")
