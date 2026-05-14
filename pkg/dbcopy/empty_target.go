// empty_target.go — pre-flight empty-target check for `datatug db copy`.
//
// Implements REQ:empty-target-check from spec/features/cli/db/copy/README.md.
//
// When --overwrite is omitted, the target must be "empty for this copy":
// every source-named table either does not exist on the target, or exists
// with zero rows. Unrelated target tables (names not in source) are
// ignored. The first conflicting source-named table with >=1 row aborts
// the copy with a NonEmptyTargetError BEFORE any write occurs.

package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// NonEmptyTargetError is returned by checkEmptyTarget when at least one
// source-named table exists on the target with >=1 row. The user should
// rerun with --overwrite=recreate or --overwrite=reload.
type NonEmptyTargetError struct {
	Table string
	Rows  int64
}

func (e *NonEmptyTargetError) Error() string {
	return fmt.Sprintf(
		"target table %q already contains %d row(s) — pass --overwrite=recreate or --overwrite=reload",
		e.Table, e.Rows)
}

// checkEmptyTarget returns nil if the target is "empty for this copy" —
// every source-named table on the target either does not exist or has
// zero rows. Otherwise it returns a *NonEmptyTargetError naming the
// first conflicting table and its row count.
//
// Implements REQ:empty-target-check.
func checkEmptyTarget(ctx context.Context, target dal.DB, sourceTableNames []string) error {
	if len(sourceTableNames) == 0 {
		return nil
	}

	// Enumerate target collections so we can distinguish "table doesn't
	// exist on target" (fine) from "table exists and has rows" (conflict).
	refs, err := dbschema.ListCollections(ctx, target, nil)
	if err != nil {
		return fmt.Errorf("list target collections for empty-target check: %w", err)
	}
	existing := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		existing[r.Name()] = struct{}{}
	}

	for _, name := range sourceTableNames {
		if _, ok := existing[name]; !ok {
			// Table doesn't exist on target — empty for our purposes.
			continue
		}
		rows, err := countRows(ctx, target, name)
		if err != nil {
			return fmt.Errorf("count rows in target %q for empty-target check: %w", name, err)
		}
		if rows > 0 {
			return &NonEmptyTargetError{Table: name, Rows: rows}
		}
	}
	return nil
}

// countRows returns the number of rows in the given target collection.
// Uses ExecuteQueryToRecordsReader and walks the cursor — correctness
// over efficiency for MVP; the precise count powers
// NonEmptyTargetError.Rows so the user sees the actual conflicting count.
func countRows(ctx context.Context, db dal.DB, collection string) (int64, error) {
	query := dal.NewTextQuery(`SELECT * FROM "`+collection+`"`, nil)
	reader, err := db.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		return 0, err
	}
	defer func() { _ = reader.Close() }()

	var n int64
	for {
		_, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, dal.ErrNoMoreRecords) {
				return n, nil
			}
			return n, err
		}
		n++
	}
}
