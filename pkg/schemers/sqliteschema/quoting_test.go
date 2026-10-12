package sqliteschema

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // pure Go, the driver a scan opens: these tests need no cgo
)

// The names of tables and indexes are read out of the scanned file, and a file is
// not trusted. The driver of a scan runs every statement of a query string, so a
// name that is put into SQL as text can ATTACH a database and create a file where
// the scan was run. Every statement the provider makes with a name has to treat it
// as one: these tests give each of them names that are not safe to paste.
const (
	apostropheTable = `it's`
	bracketTable    = `a]b`
	quoteTable      = `q"r`
	attachTable     = `x'); ATTACH DATABASE 'p.db' AS p; CREATE TABLE p.t(c); --`
	attachIndex     = `ix'); ATTACH DATABASE 'q.db' AS q; CREATE TABLE q.t(c); --`
)

// hostileProvider is a provider over a database of such names, made with the
// pure-Go driver, and the (empty) working directory in which any file a name makes
// the provider create would appear.
func hostileProvider(t *testing.T) (provider schemaProvider, workDir string) {
	t.Helper()
	workDir = t.TempDir()
	t.Chdir(workDir)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "hostile.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE "` + apostropheTable + `" (id INTEGER PRIMARY KEY, code TEXT UNIQUE)`,
		`CREATE TABLE "` + bracketTable + `" (id INTEGER PRIMARY KEY, its_id INTEGER REFERENCES "` + apostropheTable + `"(id), qty INTEGER)`,
		`CREATE INDEX "` + attachIndex + `" ON "` + bracketTable + `"(qty)`,
		`CREATE TABLE "q""r" (id INTEGER)`,
		`CREATE TABLE "` + attachTable + `" (c TEXT)`,
		`INSERT INTO "` + bracketTable + `" (id, qty) VALUES (1, 5), (2, 6)`,
		`INSERT INTO "q""r" (id) VALUES (1), (2), (3)`,
	} {
		_, err = db.Exec(statement)
		require.NoError(t, err, statement)
	}
	return NewSchemaProvider(func() (*sql.DB, error) { return db, nil }).(schemaProvider), workDir
}

func assertNothingCreated(t *testing.T, workDir string) {
	t.Helper()
	entries, err := os.ReadDir(workDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a name in the database must not make the provider create a file")
}

func TestQuoting_Columns(t *testing.T) {
	provider, workDir := hostileProvider(t)
	for table, want := range map[string][]string{
		apostropheTable: {"id", "code"},
		bracketTable:    {"id", "its_id", "qty"},
		quoteTable:      {"id"},
		attachTable:     {"c"},
	} {
		ref := dal.NewRootCollectionRef(table, "")
		columns, err := provider.GetColumns(context.Background(), "", schemer.ColumnsFilter{CollectionRef: &ref})
		require.NoError(t, err, table)
		var names []string
		for _, column := range columns {
			names = append(names, column.Name)
		}
		assert.Equal(t, want, names, table)
	}
	assertNothingCreated(t, workDir)
}

func TestQuoting_ForeignKeys(t *testing.T) {
	provider, workDir := hostileProvider(t)

	foreignKeys, err := provider.GetForeignKeys(context.Background(), "main", bracketTable)
	require.NoError(t, err)
	require.Len(t, foreignKeys, 1)
	assert.Equal(t, schemer.FKAnchor{Name: bracketTable, Columns: []string{"its_id"}}, foreignKeys[0].From)
	assert.Equal(t, apostropheTable, foreignKeys[0].To.Name)

	for _, table := range []string{apostropheTable, quoteTable, attachTable} {
		foreignKeys, err = provider.GetForeignKeys(context.Background(), "main", table)
		require.NoError(t, err, table)
		assert.Empty(t, foreignKeys, table)
	}

	referrers, err := provider.GetReferrers(context.Background(), "main", apostropheTable)
	require.NoError(t, err)
	require.Len(t, referrers, 1)
	assert.Equal(t, bracketTable, referrers[0].From.Name)
	assertNothingCreated(t, workDir)
}

func TestQuoting_Constraints(t *testing.T) {
	provider, workDir := hostileProvider(t)

	read := func(table string) (constraints []*schemer.Constraint) {
		reader, err := provider.GetConstraints(context.Background(), "", "main", table)
		require.NoError(t, err, table)
		for {
			constraint, err := reader.NextConstraint()
			if err == io.EOF {
				return constraints
			}
			require.NoError(t, err, table)
			constraints = append(constraints, constraint)
		}
	}

	// A foreign key of a table with a bracket in its name, to one with an apostrophe.
	constraints := read(bracketTable)
	require.Len(t, constraints, 1)
	assert.Equal(t, "FOREIGN KEY", constraints[0].Type)
	assert.Equal(t, apostropheTable, constraints[0].RefTableName)
	assert.Equal(t, "its_id", constraints[0].ColumnName)

	// A UNIQUE constraint of a table with an apostrophe: its index is named after
	// the table, so the name of the index has the apostrophe too, in the statement
	// that reads the index's columns.
	constraints = read(apostropheTable)
	require.Len(t, constraints, 1)
	assert.Equal(t, "UNIQUE", constraints[0].Type)
	assert.Equal(t, "code", constraints[0].ColumnName)
	assert.Equal(t, "sqlite_autoindex_it's_1", constraints[0].Name)

	assert.Empty(t, read(attachTable))
	assertNothingCreated(t, workDir)
}

func TestQuoting_IndexesAndTheirColumns(t *testing.T) {
	provider, workDir := hostileProvider(t)

	reader, err := provider.GetIndexes(context.Background(), "", "main", bracketTable)
	require.NoError(t, err)
	var names []string
	for {
		index, err := reader.NextIndex()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, index.Name)
	}
	assert.Equal(t, []string{attachIndex}, names)

	columnsReader, err := provider.GetIndexColumns(context.Background(), "", "main", bracketTable, attachIndex)
	require.NoError(t, err)
	column, err := columnsReader.NextIndexColumn()
	require.NoError(t, err)
	assert.Equal(t, "qty", column.Name)
	_, err = columnsReader.NextIndexColumn()
	assert.Equal(t, io.EOF, err)

	// The index of a UNIQUE constraint of a table with an apostrophe.
	columnsReader, err = provider.GetIndexColumns(context.Background(), "", "main", apostropheTable, "sqlite_autoindex_it's_1")
	require.NoError(t, err)
	column, err = columnsReader.NextIndexColumn()
	require.NoError(t, err)
	assert.Equal(t, "code", column.Name)
	assertNothingCreated(t, workDir)
}

func TestQuoting_RecordsCount(t *testing.T) {
	provider, workDir := hostileProvider(t)
	ctx := context.Background()

	for _, schema := range []string{"", "main"} {
		for table, want := range map[string]int{bracketTable: 2, quoteTable: 3, apostropheTable: 0, attachTable: 0} {
			count, err := provider.RecordsCount(ctx, "", schema, table)
			require.NoError(t, err, "schema %q table %q", schema, table)
			require.NotNil(t, count)
			assert.Equal(t, want, *count, "schema %q table %q", schema, table)
		}
	}

	// A schema name is a name too: no such database, and no file made.
	_, err := provider.RecordsCount(ctx, "", `main"; ATTACH DATABASE 'z.db' AS z; --`, bracketTable)
	assert.Error(t, err)
	assertNothingCreated(t, workDir)
}

func TestQuoteIdentifier(t *testing.T) {
	for name, want := range map[string]string{
		`orders`:   `"orders"`,
		`it's`:     `"it's"`,
		`a]b`:      `"a]b"`,
		`q"r`:      `"q""r"`,
		`"`:        `""""`,
		`two""`:    `"two"""""`,
		`with sp.`: `"with sp."`,
		``:         `""`,
	} {
		assert.Equal(t, want, quoteIdentifier(name), name)
	}
}

func TestQuoteLiteral(t *testing.T) {
	for name, want := range map[string]string{
		`orders`:                   `'orders'`,
		`it's`:                     `'it''s'`,
		`'`:                        `''''`,
		`''`:                       `''''''`,
		`a]b`:                      `'a]b'`,
		`q"r`:                      `'q"r'`,
		`x'); ATTACH DATABASE 'p'`: `'x''); ATTACH DATABASE ''p'''`,
		``:                         `''`,
	} {
		assert.Equal(t, want, quoteLiteral(name), name)
	}
}

func TestPragmaSQL(t *testing.T) {
	assert.Equal(t, `PRAGMA table_info('orders')`, pragmaSQL("table_info", "orders"))
	assert.Equal(t, `PRAGMA index_info('it''s')`, pragmaSQL("index_info", "it's"))
}
