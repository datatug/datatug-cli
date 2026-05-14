// empty_target_test.go — tests for the pre-flight empty-target check
// (REQ:empty-target-check). Covers all three ACs from
// spec/features/cli/db/copy/README.md:
//   - empty-target-with-source-named-rows-rejected
//   - empty-target-with-unrelated-tables-accepted
//   - empty-target-with-source-named-empty-tables-accepted
//
// Test layering:
//   - The rejection AC is verified end-to-end through Copy(): the check
//     MUST fire before any write. We use SQLite source + SQLite target.
//   - The two acceptance ACs verify the check returns nil for the
//     allowed shapes (unrelated tables; source-named-but-empty tables).
//     They call checkEmptyTarget directly rather than the full Copy()
//     because the downstream SQLite-target row-copy path is exercised
//     by other tests; here we're isolating the pre-flight predicate.

package dbcopy

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/dal-go/dalgo2sqlite"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
)

// makeSQLiteFile creates a SQLite file at path and runs each statement in
// stmts against it.
func makeSQLiteFile(t *testing.T, path string, stmts ...string) {
	t.Helper()
	rawDB, err := sql.Open("sqlite3", path)
	assert.NoError(t, err)
	defer func() { _ = rawDB.Close() }()
	for _, s := range stmts {
		_, err := rawDB.Exec(s)
		assert.NoError(t, err, "stmt: %s", s)
	}
}

// countSQLiteRows returns the row count of the named table in the SQLite
// file at path. Used to assert "no target write occurred" after a
// rejection.
func countSQLiteRows(t *testing.T, path, table string) int64 {
	t.Helper()
	rawDB, err := sql.Open("sqlite3", path)
	assert.NoError(t, err)
	defer func() { _ = rawDB.Close() }()
	row := rawDB.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`)
	var n int64
	assert.NoError(t, row.Scan(&n))
	return n
}

// TestCopy_EmptyTargetCheck_RejectsConflictingTable verifies
// AC:empty-target-with-source-named-rows-rejected end-to-end. Target's
// `users` has 5 rows; source has `users` too; Copy must error out with
// NonEmptyTargetError{Table:"users", Rows:5} and leave the target
// untouched.
func TestCopy_EmptyTargetCheck_RejectsConflictingTable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	tgtPath := filepath.Join(dir, "tgt.db")

	makeSQLiteFile(t, srcPath,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO users VALUES (1, 'alice'), (2, 'bob'), (3, 'carol')`,
	)
	// Target: `users` with 5 rows (different content; matters for the
	// "no write" defense at the end).
	makeSQLiteFile(t, tgtPath,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO users VALUES (10, 'x'), (11, 'y'), (12, 'z'), (13, 'w'), (14, 'v')`,
	)

	src, err := dalgo2sqlite.NewDatabase(srcPath)
	assert.NoError(t, err)
	tgt, err := dalgo2sqlite.NewDatabase(tgtPath)
	assert.NoError(t, err)

	_, err = Copy(context.Background(), src, tgt, CopyOpts{})
	assert.Error(t, err)

	var nonEmpty *NonEmptyTargetError
	assert.True(t, errors.As(err, &nonEmpty),
		"expected *NonEmptyTargetError, got %T: %v", err, err)
	assert.Equal(t, "users", nonEmpty.Table)
	assert.Equal(t, int64(5), nonEmpty.Rows)
	assert.Contains(t, err.Error(), "--overwrite=recreate")
	assert.Contains(t, err.Error(), "--overwrite=reload")

	// Defense: target still has its original 5 rows — no write happened.
	assert.Equal(t, int64(5), countSQLiteRows(t, tgtPath, "users"),
		"target must be untouched when empty-target check rejects")
}

// TestCheckEmptyTarget_AcceptsUnrelatedTables verifies
// AC:empty-target-with-unrelated-tables-accepted at the predicate level.
// Target has only an unrelated `audit_log` table; the check is asked
// whether `users` is OK to write; it must return nil.
func TestCheckEmptyTarget_AcceptsUnrelatedTables(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgtPath := filepath.Join(dir, "tgt.db")

	stmts := []string{
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY, msg TEXT)`,
	}
	for i := 1; i <= 100; i++ {
		stmts = append(stmts,
			`INSERT INTO audit_log VALUES (`+strconv.Itoa(i)+`, 'msg')`)
	}
	makeSQLiteFile(t, tgtPath, stmts...)

	tgt, err := dalgo2sqlite.NewDatabase(tgtPath)
	assert.NoError(t, err)

	err = checkEmptyTarget(context.Background(), tgt, []string{"users"})
	assert.NoError(t, err,
		"unrelated populated tables on the target must not trigger the check")

	// audit_log unchanged (the check is read-only).
	assert.Equal(t, int64(100), countSQLiteRows(t, tgtPath, "audit_log"))
}

// TestCheckEmptyTarget_AcceptsEmptySourceNamedTable verifies
// AC:empty-target-with-source-named-empty-tables-accepted at the
// predicate level. Target's `users` table exists but is empty; the
// check must return nil (existence alone is not a conflict).
func TestCheckEmptyTarget_AcceptsEmptySourceNamedTable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgtPath := filepath.Join(dir, "tgt.db")

	makeSQLiteFile(t, tgtPath,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`,
	)
	assert.Equal(t, int64(0), countSQLiteRows(t, tgtPath, "users"),
		"sanity: target users starts empty")

	tgt, err := dalgo2sqlite.NewDatabase(tgtPath)
	assert.NoError(t, err)

	err = checkEmptyTarget(context.Background(), tgt, []string{"users"})
	assert.NoError(t, err,
		"existing empty target table must not trigger the check")
}

// TestCheckEmptyTarget_EmptySourceNames is a degenerate case: with no
// source names, every target is "empty for this copy" by vacuous truth.
func TestCheckEmptyTarget_EmptySourceNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tgtPath := filepath.Join(dir, "tgt.db")
	makeSQLiteFile(t, tgtPath,
		`CREATE TABLE anything (id INTEGER PRIMARY KEY)`,
		`INSERT INTO anything VALUES (1), (2), (3)`,
	)
	tgt, err := dalgo2sqlite.NewDatabase(tgtPath)
	assert.NoError(t, err)
	assert.NoError(t, checkEmptyTarget(context.Background(), tgt, nil))
	assert.NoError(t, checkEmptyTarget(context.Background(), tgt, []string{}))
}
