package dalgoschema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqliteLikeReader reads like dalgo2sqlite's lenient reader: ListCollections lists
// tables only, a table with no name as the zero reference, and ListViews lists the
// views apart from them.
type sqliteLikeReader struct {
	*fakeReader
	unnamed []dal.CollectionRef // listed before the named tables
	views   []dal.CollectionRef
}

func (r *sqliteLikeReader) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	refs, err := r.fakeReader.ListCollections(ctx, parent)
	if err != nil {
		return nil, err
	}
	return append(append([]dal.CollectionRef{}, r.unnamed...), refs...), nil
}

func (r *sqliteLikeReader) ListViews(context.Context) ([]dal.CollectionRef, error) {
	return r.views, nil
}

func strPtr(s string) *string { return &s }

// sqliteFile is a file with a table that declares its types, a table the reader
// keeps no source definition of, a view, a view whose table was dropped, and an
// unnamed table. (SQLite keeps tables and views in one namespace: a file can hold an
// unnamed table or an unnamed view, not both.)
func sqliteFile() *sqliteLikeReader {
	reader := &fakeReader{
		collections: []string{"items", "plain", "names"}, // "names" is a view a reader also lists as a collection
		defs: map[string]*dbschema.CollectionDef{
			"items": {
				Name: "items",
				Fields: []dbschema.FieldDef{
					{Name: "id", Type: dbschema.Int},
					{Name: "doc", Type: dbschema.Null, Nullable: true},
					{Name: "made", Type: dbschema.Time, Nullable: true},
					{Name: "extra", Type: dbschema.String, Nullable: true}, // not among the source columns
				},
				PrimaryKey: fields("id"),
				ForeignKeys: []dbschema.ForeignKeyDef{
					{Name: "items_fk_0", Fields: fields("id"), ReferencedCollection: "gone", ReferencedFields: fields("id")},
				},
				SourceDefinition: &dbschema.SourceDefinition{
					Dialect: "sqlite",
					Columns: []dbschema.SourceColumnDef{
						{Name: "id", DeclaredType: "INTEGER", PrimaryKeyPosition: 1},
						{Name: "doc", DeclaredType: "JSON"},
						{Name: "made", DeclaredType: "DATETIME", DefaultSQL: strPtr("CURRENT_TIMESTAMP")},
					},
					Indexes: []dbschema.SourceIndexDef{
						{Name: "sqlite_autoindex_items_1", Unique: true, Origin: "u", Columns: []dbschema.SourceIndexColumnDef{
							{Position: 0, Name: strPtr("doc"), Key: true}, {Position: 1, Key: false},
						}},
						{Name: "items_expr", Origin: "c", Partial: true, Columns: []dbschema.SourceIndexColumnDef{{Position: 0, Key: true}}},
						{Name: "items_pk", Unique: true, Origin: "pk"},
					},
				},
			},
			"plain": {Name: "plain", Fields: []dbschema.FieldDef{{Name: "a", Type: dbschema.String}}},
			"names": {Name: "names", Fields: []dbschema.FieldDef{{Name: "full_name", Type: dbschema.Null, Nullable: true}},
				SourceDefinition: &dbschema.SourceDefinition{Dialect: "sqlite", Columns: []dbschema.SourceColumnDef{{Name: "full_name"}}}},
			"labels": {Name: "labels", Fields: []dbschema.FieldDef{{Name: "label", Type: dbschema.String, Nullable: true}}},
		},
		indexes: map[string][]dbschema.IndexDef{
			"plain": {{Name: "plain_a", Collection: "plain", Fields: fields("a")}},
		},
	}
	return &sqliteLikeReader{
		fakeReader: reader,
		unnamed:    []dal.CollectionRef{{}},
		views: []dal.CollectionRef{
			dal.NewRootCollectionRef("broken", ""),
			dal.NewRootCollectionRef("labels", ""),
			dal.NewRootCollectionRef("names", ""),
		},
	}
}

func sqliteProvider(reader dbschema.SchemaReader, warnings io.Writer) schemer.SchemaProvider {
	return NewSchemaProviderWithOptions(reader, nil, "file", "main", Options{Warnings: warnings, SourceDefinitions: true})
}

func readCollections(t *testing.T, provider schemer.SchemaProvider) []*datatug.CollectionInfo {
	t.Helper()
	reader, err := provider.GetCollections(context.Background(), nil)
	require.NoError(t, err)
	var collections []*datatug.CollectionInfo
	for {
		collection, err := reader.NextCollection()
		if errors.Is(err, io.EOF) {
			return collections
		}
		require.NoError(t, err)
		collections = append(collections, collection)
	}
}

func TestOptions_CollectionsLeaveOutWhatCannotBeReadAndSaySo(t *testing.T) {
	var warnings bytes.Buffer
	collections := readCollections(t, sqliteProvider(sqliteFile(), &warnings))
	var got []string
	for _, collection := range collections {
		got = append(got, fmt.Sprintf("%s %s.%s", collection.DbType, collection.Schema(), collection.Name()))
	}
	assert.Equal(t, []string{"BASE TABLE main.items", "BASE TABLE main.plain", "VIEW main.names", "VIEW main.labels"}, got,
		"a view listed apart from the tables comes after them, once; a view that cannot be read and an unnamed one are left out")
	assert.Equal(t, `warning: table "" of schema "main" is left out of the project: its name is empty
warning: view "broken" of schema "main" is left out of the project: its definition cannot be read: "collection \"broken\" not found"
`, warnings.String())

	unnamedView := sqliteFile()
	unnamedView.unnamed = nil
	unnamedView.views = append([]dal.CollectionRef{{}}, unnamedView.views...)
	warnings.Reset()
	assert.Len(t, readCollections(t, sqliteProvider(unnamedView, &warnings)), 4)
	assert.Contains(t, warnings.String(), `warning: view "" of schema "main" is left out of the project: its name is empty`)
}

func TestOptions_WithoutWarningsAViewIsNotReadAhead(t *testing.T) {
	reader := sqliteFile()
	collections := readCollections(t, sqliteProvider(reader, nil))
	assert.Len(t, collections, 5, "the broken view stays: the scan fails on it as it did")
	assert.Zero(t, reader.callCount("DescribeCollection"))
}

func TestOptions_ColumnsHaveTheirDeclaredTypesAndDefaults(t *testing.T) {
	provider := sqliteProvider(sqliteFile(), io.Discard)
	ref := dal.NewRootCollectionRef("items", "")
	columns, err := provider.GetColumns(context.Background(), "file", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)
	type column struct {
		name, dbType string
		def          *string
		position, pk int
		nullable     bool
	}
	var got []column
	for _, c := range columns {
		got = append(got, column{c.Name, c.DbType, c.Default, c.OrdinalPosition, c.PrimaryKeyPosition, c.IsNullable})
	}
	assert.Equal(t, []column{
		{"id", "INTEGER", nil, 1, 1, false},
		{"doc", "JSON", nil, 2, 0, true},
		{"made", "DATETIME", strPtr("CURRENT_TIMESTAMP"), 3, 0, true},
		{"extra", "string", nil, 4, 0, true}, // a field the source does not list keeps its portable type
	}, got)

	plain := dal.NewRootCollectionRef("plain", "")
	columns, err = provider.GetColumns(context.Background(), "file", schemer.ColumnsFilter{CollectionRef: &plain})
	require.NoError(t, err)
	assert.Equal(t, "string", columns[0].DbType, "a reader that keeps no source definition gives the portable type")
}

func TestOptions_ZeroBasedOrdinals(t *testing.T) {
	provider := NewSchemaProviderWithOptions(sqliteFile(), nil, "file", "main", Options{ZeroBasedOrdinals: true})
	ref := dal.NewRootCollectionRef("items", "")
	columns, err := provider.GetColumns(context.Background(), "file", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)
	for i, column := range columns {
		assert.Equal(t, i, column.OrdinalPosition, column.Name)
	}
}

func TestOptions_WithoutSourceDefinitionsColumnsArePortable(t *testing.T) {
	provider := NewSchemaProviderWithOptions(sqliteFile(), nil, "file", "main", Options{})
	ref := dal.NewRootCollectionRef("items", "")
	columns, err := provider.GetColumns(context.Background(), "file", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)
	assert.Equal(t, "int", columns[0].DbType)
	assert.Nil(t, columns[2].Default)
}

func TestOptions_IndexesFromTheSourceCatalog(t *testing.T) {
	ctx := context.Background()
	provider := sqliteProvider(sqliteFile(), io.Discard)
	reader, err := provider.GetIndexes(ctx, "file", "main", "items")
	require.NoError(t, err)
	var indexes []datatug.Index
	for {
		index, err := reader.NextIndex()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		assert.Equal(t, schemer.TableRef{SchemaName: "main", TableName: "items"}, index.TableRef)
		indexes = append(indexes, *index.Index)
	}
	assert.Equal(t, []datatug.Index{
		{Name: "sqlite_autoindex_items_1", Type: "INDEX", Origin: "u", IsUnique: true, IsUniqueConstraint: true},
		{Name: "items_expr", Type: "INDEX", Origin: "c", IsPartial: true},
		{Name: "items_pk", Type: "INDEX", Origin: "pk", IsUnique: true, IsPrimaryKey: true},
	}, indexes)

	columnNames := func(index string) []string {
		t.Helper()
		reader, err := provider.GetIndexColumns(ctx, "file", "main", "items", index)
		require.NoError(t, err)
		var names []string
		for {
			column, err := reader.NextIndexColumn()
			if errors.Is(err, io.EOF) {
				return names
			}
			require.NoError(t, err)
			names = append(names, column.Name)
		}
	}
	assert.Equal(t, []string{"doc"}, columnNames("sqlite_autoindex_items_1"), "a term that is not a key column is not listed")
	assert.Equal(t, []string{""}, columnNames("items_expr"), "an expression has no name")
	assert.Nil(t, columnNames("items_pk"))
	_, err = provider.GetIndexColumns(ctx, "file", "main", "items", "nope")
	assert.ErrorContains(t, err, `index "nope" not found on items`)

	// A table the reader keeps no source definition of has the portable indexes.
	reader, err = provider.GetIndexes(ctx, "file", "main", "plain")
	require.NoError(t, err)
	index, err := reader.NextIndex()
	require.NoError(t, err)
	assert.Equal(t, "plain_a", index.Name)
	plainColumns, err := provider.GetIndexColumns(ctx, "file", "main", "plain", "plain_a")
	require.NoError(t, err)
	column, err := plainColumns.NextIndexColumn()
	require.NoError(t, err)
	assert.Equal(t, "a", column.Name)
}

func TestOptions_IndexesFailWhenTheTableCannotBeDescribed(t *testing.T) {
	reader := sqliteFile()
	reader.errs = map[string]error{"DescribeCollection": errors.New("boom")}
	provider := sqliteProvider(reader, io.Discard)
	_, err := provider.GetIndexes(context.Background(), "file", "main", "items")
	assert.ErrorContains(t, err, "boom")
	_, err = provider.GetIndexColumns(context.Background(), "file", "main", "items", "items_expr")
	assert.ErrorContains(t, err, "boom")
}

func TestOptions_ForeignKeyToATableNotListedIsAWarning(t *testing.T) {
	var warnings bytes.Buffer
	provider := sqliteProvider(sqliteFile(), &warnings)
	reader, err := provider.GetConstraints(context.Background(), "file", "main", "items")
	require.NoError(t, err)
	_, err = reader.NextConstraint()
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, "warning: table \"items\" of schema \"main\" has a foreign key to \"gone\", which is not a table of the database: the reference is not read\n", warnings.String())
}

func TestRootCause(t *testing.T) {
	inner := errors.New("SQL logic error: no such table: main.doomed (1)")
	assert.Equal(t, inner, rootCause(fmt.Errorf("describe broken: %w", fmt.Errorf("dalgo2sqlite: pragma: %w", inner))))
	assert.Equal(t, inner, rootCause(inner))
}
