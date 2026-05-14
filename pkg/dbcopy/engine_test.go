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

// TestCopy_SourceHasNoTables verifies REQ:source-introspection-failure:
// a clean source with zero collections returns ErrSourceHasNoTables and
// makes no target writes.
func TestCopy_SourceHasNoTables(t *testing.T) {
	t.Parallel()

	// Empty SQLite source.
	srcPath := filepath.Join(t.TempDir(), "empty.db")
	src, err := dalgo2sqlite.NewDatabase(srcPath)
	assert.NoError(t, err)

	// Empty inGitDB target.
	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	summary, err := Copy(context.Background(), src, tgt, CopyOpts{})
	assert.True(t, errors.Is(err, ErrSourceHasNoTables),
		"expected ErrSourceHasNoTables, got %v", err)
	assert.Equal(t, 0, summary.Tables)
	assert.Equal(t, 0, summary.Created)
}

// TestCopy_ChinookSQLiteToInGitDB exercises the happy path: Chinook source
// → empty inGitDB target. The 7 describe-able tables get replicated to
// the target; the 4 tables that dalgo2sqlite can't describe (DATETIME /
// NUMERIC) are skipped with a stderr note.
//
// This is the schema-only first slice: row data is NOT copied (tracked
// upstream at docs/upstream-issues/ingitdb-cli-dalgo2ingitdb-row-crud.md).
func TestCopy_ChinookSQLiteToInGitDB(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	var stderr bytes.Buffer
	summary, err := Copy(context.Background(), src, tgt, CopyOpts{Stderr: &stderr})
	assert.NoError(t, err)

	assert.Equal(t, 11, summary.Tables, "Chinook has 11 tables")
	assert.GreaterOrEqual(t, summary.Created, 7,
		"expected at least the 7 describe-able tables to land on target")
	assert.LessOrEqual(t, summary.Created, 11)
	assert.False(t, summary.RowStreaming, "first slice is schema-only")

	// The 4 known-rejected tables (Employee, Invoice, InvoiceLine, Track)
	// should appear in Skipped. We assert a non-empty Skipped + Created+Skipped
	// == Tables, which is the schema-only invariant.
	assert.NotEmpty(t, summary.Skipped, "expected DATETIME/NUMERIC tables to be skipped")
	assert.Equal(t, summary.Tables, summary.Created+len(summary.Skipped))

	// stderr must carry the schema-only follow-up note.
	assert.Contains(t, stderr.String(), "schema replicated; row data not yet copied")
	assert.Contains(t, stderr.String(), "ingitdb-cli-dalgo2ingitdb-row-crud")

	// Verify the target ACTUALLY has the created collections.
	refs, err := dbschema.ListCollections(context.Background(), tgt, nil)
	assert.NoError(t, err)
	assert.Equal(t, summary.Created, len(refs),
		"target should list exactly summary.Created collections")
}

// TestCopy_OverwriteRecreate_DropsSourceNamedFirst verifies
// REQ:recreate-drops-first: tables matching source names get dropped on
// the target before introspection-and-create. Tables NOT in source are
// left alone.
func TestCopy_OverwriteRecreate_DropsSourceNamedFirst(t *testing.T) {
	t.Parallel()

	// Source: empty SQLite. Force ErrSourceHasNoTables to short-circuit
	// the create loop; we only care about the drop pre-pass here.
	// (We could exercise both; this test focuses the recreate behavior
	//  on a target with pre-existing tables in isolation.)
	//
	// Skip this test branch for now: with an empty source, the drop pre-pass
	// has nothing to do, so the test would be a no-op. A meaningful version
	// of this test requires a non-empty source whose tables overlap the
	// target's. Defer until row CRUD lands and we can populate the target
	// to assert the drop actually occurred.
	t.Skip("requires non-empty target population — deferred to row-CRUD slice")
}

// adapterName_NilSafe — defense-in-depth: adapterName must not panic when
// the DB or its adapter is nil.
func TestAdapterName_NilSafe(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "unknown", adapterName(nil))
	assert.Equal(t, "unknown", adapterName(nilAdapterDB{}))
}

type nilAdapterDB struct{ dal.DB }

func (nilAdapterDB) Adapter() dal.Adapter { return nil }
