package dbcopy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
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

// TestCopy_NilFiltersTreatedAsNoFilter is a regression for the new
// CopyOpts.Filters field added in Task 2 of the filtering plan: passing
// nil or a zero-value *filter.Directives MUST behave identically to
// omitting Filters entirely. Until Task 3 wires Filters into the engine,
// this test just verifies CopyOpts construction with Filters set does
// not panic and Copy() returns the same well-known result it would
// without Filters.
func TestCopy_NilFiltersTreatedAsNoFilter(t *testing.T) {
	t.Parallel()

	// Empty SQLite source — Copy() will return ErrSourceHasNoTables,
	// which is the expected baseline behavior regardless of Filters.
	srcPath := filepath.Join(t.TempDir(), "empty.db")
	src, err := dalgo2sqlite.NewDatabase(srcPath)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	// Test both nil and zero-value *filter.Directives — both must be
	// treated as "no filtering" and produce identical behavior to the
	// no-Filters baseline.
	for _, name := range []string{"nil", "zero-value"} {
		t.Run(name, func(t *testing.T) {
			opts := CopyOpts{}
			if name == "zero-value" {
				opts.Filters = &filter.Directives{}
			}
			_, err := Copy(context.Background(), src, tgt, opts)
			assert.True(t, errors.Is(err, ErrSourceHasNoTables),
				"with Filters=%s, expected ErrSourceHasNoTables (no behavior change), got %v", name, err)
		})
	}
}

// AC:include-flag-narrows-to-listed-tables — REQ:include-flag.
func TestCopy_IncludeNarrowsToListedTables(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	opts := CopyOpts{
		Filters: &filter.Directives{IncludeTables: []string{"Customer", "Invoice"}},
	}
	summary, err := Copy(context.Background(), src, tgt, opts)
	assert.NoError(t, err)
	assert.Equal(t, 2, summary.Tables, "include narrows to 2 tables")

	got := append([]string(nil), summary.CreatedNames...)
	sort.Strings(got)
	assert.Equal(t, []string{"Customer", "Invoice"}, got)
}

// AC:exclude-flag-skips-listed-tables — REQ:exclude-flag.
func TestCopy_ExcludeSkipsListedTables(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	opts := CopyOpts{
		Filters: &filter.Directives{ExcludeTables: []string{"Genre", "MediaType"}},
	}
	summary, err := Copy(context.Background(), src, tgt, opts)
	assert.NoError(t, err)
	assert.Equal(t, 9, summary.Tables, "exclude drops 2 of 11 Chinook tables")

	for _, name := range summary.CreatedNames {
		assert.NotEqual(t, "Genre", name)
		assert.NotEqual(t, "MediaType", name)
	}
}

// AC:limit-applies-per-table — Chinook Invoice has 412 rows; --limit
// Invoice:50 should produce a target collection of exactly 50.
//
// Depends on a local-replace of dal-go/dalgo2sql that adds an
// `emitSQL` shim translating dalgo's `SELECT TOP N` into ANSI/SQLite
// `LIMIT N` (see go.mod). The proper dialect-aware emission lives
// under the `dalgo-dialect-aware-sql-emission` sibling Idea — once
// that lands, the shim disappears. Track: REQ:limit-compiles-to-dalgo-limit.
func TestCopy_LimitNarrowsRowCount(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Invoice"},
			LimitsByTable: map[string]int{"Invoice": 50},
		},
	}
	summary, err := Copy(context.Background(), src, tgt, opts)
	assert.NoError(t, err)
	assert.Equal(t, int64(50), summary.RowsByTable["Invoice"], "Invoice rows = 50 (Chinook 412 capped)")
}

// AC:where-single-condition — Chinook has 13 Customers with Country='USA'.
func TestCopy_WhereSingleCondition(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Customer"},
			Where: map[string]*filter.PredicateGroup{
				"Customer": {
					Operator: filter.And,
					Conditions: []filter.Predicate{
						{Field: "Country", Operator: filter.OpEqual, Value: "USA"},
					},
				},
			},
		},
	}
	summary, err := Copy(context.Background(), src, tgt, opts)
	assert.NoError(t, err)
	assert.Equal(t, int64(13), summary.RowsByTable["Customer"], "Customer rows = 13 (Country='USA')")
}

// AC:where-and-composition — Country='USA' AND SupportRepId=3 → 4 rows.
//
// Blocked on an upstream dalgo SQL-emission bug: dal.GroupCondition.String()
// joins multi-condition WHERE clauses with `strings.Join(conds, "AND")`
// (no surrounding spaces), so the emitted SQL is `... = "USA"ANDSupportRepId
// = 3` which SQLite rejects with a syntax error. The fix is a one-line
// edit in dal-go/dalgo (`" "+string(v.operator)+" "`), but per Task 8's
// scope discipline that change is out of scope and must be handled in a
// separate dalgo-side commit, analogous to how Task 7 handled the
// SELECT-TOP-vs-LIMIT emission bug (commit 1adac80). Un-skip once the
// dalgo replace carries the spacing fix.
func TestCopy_WhereAndComposition(t *testing.T) {
	t.Skip("blocked on upstream dalgo GroupCondition.String() spacing bug; see comment above")
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Customer"},
			Where: map[string]*filter.PredicateGroup{
				"Customer": {
					Operator: filter.And,
					Conditions: []filter.Predicate{
						{Field: "Country", Operator: filter.OpEqual, Value: "USA"},
						{Field: "SupportRepId", Operator: filter.OpEqual, Value: "3"},
					},
				},
			},
		},
	}
	summary, err := Copy(context.Background(), src, tgt, opts)
	assert.NoError(t, err)
	assert.Equal(t, int64(4), summary.RowsByTable["Customer"], "Customer rows = 4 (USA + Rep=3)")
}

// AC:unknown-table-in-include-rejected — REQ:table-not-found.
func TestCopy_UnknownTableInIncludeRejected(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	src, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	opts := CopyOpts{
		Filters: &filter.Directives{IncludeTables: []string{"Customer", "Users"}},
	}
	_, err = Copy(context.Background(), src, tgt, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Users", "error must name unknown table")
}

// errFilterNotSupportedAdapter wraps a real source adapter and rejects
// any structured query containing a WHERE clause, mimicking a future
// backend that doesn't push down WHERE.
//
// Embeds the concrete *dalgo2sqlite.Database (not the dal.DB interface)
// so the engine's dbschema.SchemaReader type assertion resolves against
// the inherited method set.
type errFilterNotSupportedAdapter struct {
	*dalgo2sqlite.Database
}

func (e *errFilterNotSupportedAdapter) ExecuteQueryToRecordsReader(
	ctx context.Context, q dal.Query,
) (dal.RecordsReader, error) {
	if sq, ok := q.(dal.StructuredQuery); ok && sq.Where() != nil {
		return nil, fmt.Errorf("stub backend: filter axis 'where' not supported by this driver")
	}
	return e.Database.ExecuteQueryToRecordsReader(ctx, q)
}

// AC:backend-without-pushdown-exits-1 — REQ:backend-coverage.
func TestCopy_BackendWithoutWherePushdownExits1(t *testing.T) {
	t.Parallel()

	chinook, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	realSrc, err := dalgo2sqlite.NewDatabase(chinook)
	assert.NoError(t, err)
	src := &errFilterNotSupportedAdapter{Database: realSrc}

	tgtDir := t.TempDir()
	tgt, err := dalgo2ingitdb.NewDatabase(tgtDir, validator.NewCollectionsReader())
	assert.NoError(t, err)

	opts := CopyOpts{
		Filters: &filter.Directives{
			IncludeTables: []string{"Customer"},
			Where: map[string]*filter.PredicateGroup{
				"Customer": {
					Operator: filter.And,
					Conditions: []filter.Predicate{
						{Field: "Country", Operator: filter.OpEqual, Value: "USA"},
					},
				},
			},
		},
	}
	_, err = Copy(context.Background(), src, tgt, opts)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported",
		"error must mention the underlying 'not supported' reason")
	assert.Contains(t, err.Error(), "push-down",
		"error must mention push-down support gap to map to exit 1")
}
