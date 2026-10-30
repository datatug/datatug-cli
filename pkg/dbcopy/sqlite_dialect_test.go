package dbcopy

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSQLiteOptions replaces the sqlite constructor seam with one that
// records the DbOptions it was given and still opens the real database.
func captureSQLiteOptions(t *testing.T) *dalgo2sql.DbOptions {
	t.Helper()
	var got dalgo2sql.DbOptions
	orig := newSQLiteDatabaseWithOptions
	t.Cleanup(func() { newSQLiteDatabaseWithOptions = orig })
	newSQLiteDatabaseWithOptions = func(path string, schema dal.Schema, opts dalgo2sql.DbOptions) (*dalgo2sqlite.Database, error) {
		got = opts
		return orig(path, schema, opts)
	}
	return &got
}

// TestOpen_SQLiteSetsStructuredQueryDialect proves every open entry point
// opts SQLite into the safe structured-query compiler, so no structured read
// reaches the legacy text emitter (dalgo2sql SQL-01 makes that fail closed
// for non-ASCII identifiers and for tab, newline and backslash values).
func TestOpen_SQLiteSetsStructuredQueryDialect(t *testing.T) {
	// Not parallel: the seam is package state.
	path := filepath.Join(t.TempDir(), "d.db")
	makeSQLiteFile(t, path, "CREATE TABLE t (id INTEGER PRIMARY KEY)")
	ref := BackendRef{Scheme: "sqlite", Path: path}
	ctx := context.Background()

	opens := map[string]func(BackendRef, context.Context) (dal.DB, error){
		"Open":                 BackendRef.Open,
		"OpenForTest":          BackendRef.OpenForTest,
		"OpenProtected":        BackendRef.OpenProtected,
		"OpenProtectedForTest": BackendRef.OpenProtectedForTest,
	}
	names := make([]string, 0, len(opens))
	for name := range opens {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got := captureSQLiteOptions(t)
		db, err := opens[name](ref, ctx)
		require.NoError(t, err, name)
		assert.NotNil(t, db, name)
		assert.Equal(t, "sqlite", got.StructuredQueryDialect, name)
	}
}

// TestCopyRows_SQLiteSourceNonASCIIAndEscapedFilterValue is the read-path
// acceptance: a column with a non-ASCII name, filtered by a value holding a
// backslash, a newline and a bracketed fragment, is read through the source
// Open returns. The bracketed fragment makes this fail on the legacy emitter,
// which strips every [...] pair from the statement, string literals included.
func TestCopyRows_SQLiteSourceNonASCIIAndEscapedFilterValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const tricky = "a\\b\n[c]"
	path := filepath.Join(t.TempDir(), "src.db")
	makeSQLiteFile(t, path,
		`CREATE TABLE people (id INTEGER PRIMARY KEY, "naïve" TEXT)`,
		`INSERT INTO people (id, "naïve") VALUES (1, 'plain')`,
		`INSERT INTO people (id, "naïve") VALUES (2, 'a\b`+"\n"+`[c]')`,
	)
	src, err := BackendRef{Scheme: "sqlite", Path: path}.Open(ctx)
	require.NoError(t, err)

	def := &dbschema.CollectionDef{
		Name:       "people",
		PrimaryKey: []dal.FieldName{"id"},
		Fields: []dbschema.FieldDef{
			{Name: "id", Type: dbschema.Int},
			{Name: "naïve", Type: dbschema.String},
		},
	}
	opts := CopyOpts{Filters: &filter.Directives{
		Where: map[string]*filter.PredicateGroup{
			"people": {
				Operator: filter.And,
				Conditions: []filter.Predicate{
					{Field: "naïve", Operator: filter.OpEqual, Value: tricky},
				},
			},
		},
	}}

	var inserted []record.Record
	tgt := captureInsertsDB{DB: src, inserted: &inserted}
	n, err := copyRows(ctx, src, tgt, def, opts)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	require.Len(t, inserted, 1)
	data := inserted[0].Data().(map[string]any)
	assert.Equal(t, tricky, data["naïve"])
}

// captureInsertsDB is a copy target that records the records inserted,
// leaving the read path under test as the only thing that touches SQLite.
type captureInsertsDB struct {
	dal.DB
	inserted *[]record.Record
}

func (c captureInsertsDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, _ ...dal.TransactionOption) error {
	return worker(ctx, captureInsertsTx{inserted: c.inserted})
}

type captureInsertsTx struct {
	dal.ReadwriteTransaction
	inserted *[]record.Record
}

func (c captureInsertsTx) InsertMulti(_ context.Context, records []record.Record, _ ...dal.InsertOption) error {
	*c.inserted = append(*c.inserted, records...)
	return nil
}

// copyInvoicesWhere copies the Chinook Invoice table, filtered by one
// predicate, from a source opened through BackendRef.Open (so with the
// production dialect) and returns the number of rows copied.
func copyInvoicesWhere(t *testing.T, field string, op filter.OperatorToken, value string) int64 {
	t.Helper()
	ctx := context.Background()
	chinook, err := filepath.Abs("testdata/chinook.db")
	require.NoError(t, err)
	src, err := BackendRef{Scheme: "sqlite", Path: chinook}.Open(ctx)
	require.NoError(t, err)
	tgt, err := dalgo2ingitdb.NewDatabase(t.TempDir(), validator.NewCollectionsReader())
	require.NoError(t, err)
	summary, err := Copy(ctx, src, tgt, CopyOpts{Filters: &filter.Directives{
		IncludeTables: []string{"Invoice"},
		Where: map[string]*filter.PredicateGroup{
			"Invoice": {
				Operator:   filter.And,
				Conditions: []filter.Predicate{{Field: field, Operator: op, Value: value}},
			},
		},
	}})
	require.NoError(t, err)
	return summary.RowsByTable["Invoice"]
}

// TestCopy_SQLiteSourceDateFilter pins `--where` on a DATETIME column to the
// counts the legacy emitter returned (verified with sqlite3 against the
// fixture: 329 at or after 2010-01-01, 83 before, 412 in all). SQLite stores
// the column as text, so the date must reach the compiler as a text constant;
// as a time.Time its range guard (typeof IN integer, real) matches no row.
func TestCopy_SQLiteSourceDateFilter(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		op   filter.OperatorToken
		want int64
	}{
		"greater or equal": {filter.OpGreaterOrEqual, 329},
		"less than":        {filter.OpLessThan, 83},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, copyInvoicesWhere(t, "InvoiceDate", tc.op, "2010-01-01"))
		})
	}
}

// TestSQLiteTextTimeConstants covers the rewrite directly: a time.Time
// constant becomes its RFC 3339 text, every other condition shape is kept,
// and the input slice is not modified.
func TestSQLiteTextTimeConstants(t *testing.T) {
	t.Parallel()
	when := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	group := dal.NewGroupCondition(dal.And, dal.WhereField("a", dal.Equal, 1))
	fieldCmp := dal.WhereField("a", dal.Equal, dal.Field("b"))
	in := []dal.Condition{
		dal.WhereField("d", dal.GreaterOrEqual, when),
		dal.WhereField("s", dal.Equal, "x"),
		fieldCmp,
		group,
	}
	got := sqliteTextTimeConstants(in)
	require.Len(t, got, 4)
	assert.Equal(t, dal.WhereField("d", dal.GreaterOrEqual, "2010-01-01T00:00:00Z"), got[0])
	assert.Equal(t, in[1], got[1])
	assert.Equal(t, fieldCmp, got[2])
	assert.Equal(t, group, got[3])
	assert.Equal(t, dal.WhereField("d", dal.GreaterOrEqual, when), in[0], "input untouched")
	assert.Empty(t, sqliteTextTimeConstants(nil))
}
