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
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// defaultRowBatchSize is the buffer size used by copyRows. Picked empirically
// — small enough to keep per-table memory bounded, big enough that InsertMulti
// amortizes its per-call overhead across multiple records.
const defaultRowBatchSize = 500

// compositePKSeparator joins the string-encoded PK column values for a
// composite-PK row into a single record-key ID. Chosen for inGitDB's
// `<table>/$records/<key>.yaml` layout: filename-safe, no URL-encoding,
// and unlikely to collide with values that appear in numeric or short-
// string PK columns.
const compositePKSeparator = "__"

// copyRows streams every row from the source collection identified by def
// into the target via tx.InsertMulti, in batches of defaultRowBatchSize.
// Returns the count of rows successfully inserted.
//
// PK encoding for the target record key:
//
//   - Single-column PK → the raw PK value, formatted via fmt.Sprintf("%v", v).
//   - Composite PK (2+ columns) → each PK column's value formatted via
//     fmt.Sprintf("%v", v), joined with `__` in the order def.PrimaryKey
//     reports (i.e. the order the source DescribeCollection returned).
//
// Caveats (deferred to follow-ups, documented in stderr when encountered):
//
//   - Tables with NO primary key declared error out (ErrNoPrimaryKey).
//     MVP requires a PK to construct the target record key.
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
	pkFields := make([]string, len(def.PrimaryKey))
	for i, p := range def.PrimaryKey {
		pkFields[i] = string(p)
	}

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

		id, err := encodeRecordID(def.Name, pkFields, data)
		if err != nil {
			return rowsCopied, err
		}

		key := dal.NewKeyWithID(def.Name, id)
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
// The MVP row-copy path requires a declared PK to construct target record
// keys.
var ErrNoPrimaryKey = errors.New("source collection has no primary key declared")

// encodeRecordID builds the target record ID string from a row's PK column
// values. Single-column PK → raw `%v` of the value. Composite PK → each
// column's `%v` joined by compositePKSeparator in the given pkFields order.
//
// Returns an error if any PK column is missing from data or has a nil value
// — we don't silently substitute an empty segment, because that would let
// rows with nullable-but-actually-null PK columns collide on the target.
func encodeRecordID(collection string, pkFields []string, data map[string]any) (string, error) {
	if len(pkFields) == 1 {
		f := pkFields[0]
		v, ok := data[f]
		if !ok {
			return "", fmt.Errorf("row in %q missing PK column %q", collection, f)
		}
		if v == nil {
			return "", fmt.Errorf("row in %q has nil PK %q", collection, f)
		}
		return fmt.Sprintf("%v", v), nil
	}
	parts := make([]string, len(pkFields))
	for i, f := range pkFields {
		v, ok := data[f]
		if !ok {
			return "", fmt.Errorf("row in %q missing PK column %q", collection, f)
		}
		if v == nil {
			return "", fmt.Errorf("row in %q has nil PK %q", collection, f)
		}
		parts[i] = fmt.Sprintf("%v", v)
	}
	return strings.Join(parts, compositePKSeparator), nil
}
