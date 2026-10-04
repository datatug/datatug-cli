package dbcopy

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
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
// backslash and a newline, is read through the source Open returns.
func TestCopyRows_SQLiteSourceNonASCIIAndEscapedFilterValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const tricky = "a\\b\nc"
	path := filepath.Join(t.TempDir(), "src.db")
	makeSQLiteFile(t, path,
		`CREATE TABLE people (id INTEGER PRIMARY KEY, "naïve" TEXT)`,
		`INSERT INTO people (id, "naïve") VALUES (1, 'plain')`,
		`INSERT INTO people (id, "naïve") VALUES (2, 'a\b`+"\n"+`c')`,
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
