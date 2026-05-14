package dbcopy

import (
	"bytes"
	"context"
	"errors"
	"os"
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
// → empty inGitDB target. All 11 Chinook tables are now describe-able
// (after dalgo2sqlite DATETIME/NUMERIC support) and row-copy-able
// (composite-PK encoding handles PlaylistTrack). Total rows copied:
// 15607 (sum of all Chinook row counts).
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
	assert.Equal(t, 11, summary.Created, "all 11 Chinook tables are now describe-able and replicable")
	assert.Empty(t, summary.Skipped, "DATETIME/NUMERIC types are now recognized by dalgo2sqlite")

	// Schema invariant: every source table either landed or was skipped.
	assert.Equal(t, summary.Tables, summary.Created+len(summary.Skipped))

	// Row streaming should have moved real data for every table — including
	// PlaylistTrack (composite PK via `__`-joined key encoding).
	assert.Equal(t, int64(15607), summary.RowsCopied,
		"full Chinook is 15607 rows: 347+275+59+8+25+412+2240+5+18+8715+3503")
	assert.Empty(t, summary.RowSkips, "no row-copy skips expected on Chinook")
	assert.Contains(t, summary.RowsByTable, "PlaylistTrack",
		"PlaylistTrack should appear in per-table row counts")
	t.Logf("rows copied: %d (by table: %v)", summary.RowsCopied, summary.RowsByTable)

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

// TestCopy_ChinookCompositePK_PlaylistTrack covers the composite-PK
// row-copy path: PlaylistTrack has PK (PlaylistId, TrackId) and 8715 rows
// in the canonical Chinook fixture. Each row's target key is the two PK
// values joined by `__`, so row (PlaylistId=1, TrackId=3402) lands at
// `<tgt>/PlaylistTrack/$records/1__3402.yaml`.
func TestCopy_ChinookCompositePK_PlaylistTrack(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	summary, err := Copy(context.Background(), src, tgt, CopyOpts{})
	assert.NoError(t, err)

	// PlaylistTrack must appear in RowsByTable and NOT in RowSkips.
	assert.NotContains(t, summary.RowSkips, "PlaylistTrack",
		"composite-PK table should not be row-skipped anymore")
	got, ok := summary.RowsByTable["PlaylistTrack"]
	assert.True(t, ok, "PlaylistTrack must appear in RowsByTable")
	assert.Equal(t, int64(8715), got,
		"PlaylistTrack should copy all 8715 Chinook rows")

	// RowsCopied is the sum across tables — PlaylistTrack contributes to it.
	assert.GreaterOrEqual(t, summary.RowsCopied, int64(8715),
		"RowsCopied must include PlaylistTrack's 8715 rows")

	// Spot-check the on-disk file for row (PlaylistId=1, TrackId=3402).
	// inGitDB lays records out at <projectPath>/<table>/$records/<id>.yaml.
	wantFile := filepath.Join(tgtDir, "PlaylistTrack", "$records", "1__3402.yaml")
	_, err = os.Stat(wantFile)
	assert.NoError(t, err,
		"expected composite-PK record file at %s", wantFile)
}

// TestEncodeRecordID covers the encodeRecordID helper directly:
// single-column PK passes through, composite PK joins with `__`, and
// missing or nil PK columns produce a column-naming error rather than
// a silent empty segment.
func TestEncodeRecordID(t *testing.T) {
	t.Parallel()

	t.Run("single column", func(t *testing.T) {
		id, err := encodeRecordID("Album", []string{"AlbumId"},
			map[string]any{"AlbumId": 42, "Title": "x"})
		assert.NoError(t, err)
		assert.Equal(t, "42", id)
	})

	t.Run("composite two columns", func(t *testing.T) {
		id, err := encodeRecordID("PlaylistTrack",
			[]string{"PlaylistId", "TrackId"},
			map[string]any{"PlaylistId": 1, "TrackId": 3402})
		assert.NoError(t, err)
		assert.Equal(t, "1__3402", id)
	})

	t.Run("composite preserves PK order", func(t *testing.T) {
		// If def.PrimaryKey said (TrackId, PlaylistId), the encoded ID
		// must follow that order — not alphabetical.
		id, err := encodeRecordID("PlaylistTrack",
			[]string{"TrackId", "PlaylistId"},
			map[string]any{"PlaylistId": 1, "TrackId": 3402})
		assert.NoError(t, err)
		assert.Equal(t, "3402__1", id)
	})

	t.Run("missing column errors", func(t *testing.T) {
		_, err := encodeRecordID("PlaylistTrack",
			[]string{"PlaylistId", "TrackId"},
			map[string]any{"PlaylistId": 1})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "TrackId")
	})

	t.Run("nil column errors", func(t *testing.T) {
		_, err := encodeRecordID("PlaylistTrack",
			[]string{"PlaylistId", "TrackId"},
			map[string]any{"PlaylistId": 1, "TrackId": nil})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "TrackId")
	})
}
