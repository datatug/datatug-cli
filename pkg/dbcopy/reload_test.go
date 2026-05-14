// reload_test.go — tests for --overwrite=reload schema-match validation
// and truncate+reload path. Covers ACs:
//   - reload-rejects-schema-mismatch
//   - reload-accepts-superset-target
// Plus a Chinook end-to-end exercising the truncate-and-reload path
// against a non-empty inGitDB target.

package dbcopy

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/ingitdb/ingitdb-cli/pkg/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-cli/pkg/ingitdb/validator"
	"github.com/stretchr/testify/assert"
)

// TestCopy_OverwriteReload_RejectsSchemaMismatch verifies
// AC:reload-rejects-schema-mismatch. Source has a column the target
// lacks; Copy must return a *ReloadSchemaMismatchError naming the
// table and the missing column, and leave the target's rows untouched.
func TestCopy_OverwriteReload_RejectsSchemaMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	tgtPath := filepath.Join(dir, "tgt.db")

	// Source: users with extra `age` column.
	makeSQLiteFile(t, srcPath,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, age INTEGER)`,
		`INSERT INTO users VALUES (1, 'a@x', 30), (2, 'b@x', 40)`,
	)
	// Target: users missing `age`. Pre-populated so we can assert "no write".
	makeSQLiteFile(t, tgtPath,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)`,
		`INSERT INTO users VALUES (10, 'x@x'), (11, 'y@x'), (12, 'z@x')`,
	)

	src, err := dalgo2sqlite.NewDatabase(srcPath)
	assert.NoError(t, err)
	tgt, err := dalgo2sqlite.NewDatabase(tgtPath)
	assert.NoError(t, err)

	_, err = Copy(context.Background(), src, tgt, CopyOpts{Overwrite: "reload"})
	assert.Error(t, err)

	var mismatch *ReloadSchemaMismatchError
	assert.True(t, errors.As(err, &mismatch),
		"expected *ReloadSchemaMismatchError, got %T: %v", err, err)
	assert.Equal(t, "users", mismatch.Table)
	assert.Equal(t, "age", mismatch.Column,
		"the missing column should be named in the error")

	// Defense: target's rows are unchanged — no write happened.
	assert.Equal(t, int64(3), countSQLiteRows(t, tgtPath, "users"),
		"target rows must be untouched when reload validation rejects")
}

// TestCopy_OverwriteReload_AcceptsSupersetTarget verifies
// AC:reload-accepts-superset-target. Target has an extra column the
// source doesn't; reload must succeed, truncate the target, and load
// source rows. The extra column must remain part of the target schema.
//
// Direction is SQLite → inGitDB: per the db-copy MVP plan the
// dalgo2sqlite target-side InsertMulti panics on map-shaped Record.Data
// when PK metadata isn't present, so we exercise the forward direction
// where the write path is fully working.
func TestCopy_OverwriteReload_AcceptsSupersetTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")

	// Source: users(id, email) with 2 rows.
	makeSQLiteFile(t, srcPath,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT)`,
		`INSERT INTO users VALUES (1, 'a@x'), (2, 'b@x')`,
	)

	src, err := dalgo2sqlite.NewDatabase(srcPath)
	assert.NoError(t, err)

	// Phase A: populate an inGitDB target with the superset schema and a
	// few rows of different ids. We do this by first running a default
	// (non-reload) copy from a "seed" SQLite DB that has the superset
	// schema and 5 pre-existing rows.
	seedPath := filepath.Join(dir, "seed.db")
	makeSQLiteFile(t, seedPath,
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, extra_internal_flag TEXT)`,
		`INSERT INTO users VALUES (10, 'x@x', 'a'), (11, 'y@x', 'b'), (12, 'z@x', 'c'), (13, 'w@x', 'd'), (14, 'v@x', 'e')`,
	)
	seed, err := dalgo2sqlite.NewDatabase(seedPath)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	seedSummary, err := Copy(context.Background(), seed, tgt, CopyOpts{})
	assert.NoError(t, err, "seed copy must succeed")
	assert.Equal(t, int64(5), seedSummary.RowsCopied,
		"seed copy loads 5 superset-schema rows into the inGitDB target")

	// Phase B: reload from the lean source. Re-open both ends fresh
	// (matching the live CLI which opens each invocation independently).
	tgt2, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	summary, err := Copy(context.Background(), src, tgt2, CopyOpts{Overwrite: "reload"})
	assert.NoError(t, err, "reload should accept a superset target")
	assert.Equal(t, int64(2), summary.RowsCopied,
		"only the 2 source rows should be loaded after truncate")
	assert.Equal(t, 0, summary.Created,
		"reload must not invoke CreateCollection on the target")

	// Target schema preserved: extra_internal_flag is still a column.
	tgt3, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)
	tgtRef := dal.NewCollectionRef("users", "", nil)
	def, err := dbschema.DescribeCollection(context.Background(), tgt3, &tgtRef)
	assert.NoError(t, err)
	hasExtra := false
	for _, f := range def.Fields {
		if string(f.Name) == "extra_internal_flag" {
			hasExtra = true
			break
		}
	}
	assert.True(t, hasExtra,
		"target's extra_internal_flag column must be preserved after reload")
}

// TestCopy_OverwriteReload_ChinookSQLiteToInGitDB exercises the full
// truncate-and-reload path against a non-empty inGitDB target. Phase 1
// copies Chinook into an empty inGitDB target. Phase 2 re-runs Copy
// with Overwrite=reload — the target is no longer empty; the engine
// must validate schema, truncate every collection, and reload all rows.
func TestCopy_OverwriteReload_ChinookSQLiteToInGitDB(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	// Phase 1: default copy populates the target with 15607 rows.
	var stderr1 bytes.Buffer
	summary1, err := Copy(context.Background(), src, tgt, CopyOpts{Stderr: &stderr1})
	assert.NoError(t, err)
	assert.Equal(t, int64(15607), summary1.RowsCopied,
		"phase 1: Chinook contributes 15607 rows")

	// Phase 2: re-open both sides and re-run with reload. Schema-match
	// must pass (source==target schema) and every table must be
	// truncated + reloaded.
	src2, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)
	tgt2, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	var stderr2 bytes.Buffer
	summary2, err := Copy(context.Background(), src2, tgt2, CopyOpts{
		Overwrite: "reload",
		Stderr:    &stderr2,
	})
	assert.NoError(t, err, "phase 2: reload should succeed against the populated target")
	assert.Equal(t, int64(15607), summary2.RowsCopied,
		"phase 2: same 15607 rows after truncate+reload")
	// Reload does NOT recreate; summary.Created should be 0.
	assert.Equal(t, 0, summary2.Created,
		"reload must not invoke CreateCollection on the target")
}
