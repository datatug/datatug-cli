package api

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keysLayout is a layout of one schema "main" with the tables and the views given.
func keysLayout(tables, views []*datatug.CollectionInfo) scannedLayout {
	return scannedLayout{folders: []scannedFolder{{schema: "main", kind: "tables", tables: tables}, {schema: "main", kind: "views", tables: views}}}
}

// keysProject is a project folder with the nested db model shop, as a scan leaves it.
func keysProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "dbmodels", "shop"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dbmodels", "shop", "shop.dbmodel.json"), []byte(`{"id":"shop"}`+"\n"), 0o644))
	return dir
}

func lineTable() *datatug.CollectionInfo {
	table := layoutTable("order_line", layoutColumn("customer_id", "INTEGER", 0), layoutColumn("qty", "INTEGER", 0))
	table.ForeignKeys = datatug.ForeignKeys{{Name: "fk_customer", Columns: []string{"customer_id"}, RefTable: datatug.NewTableKey("Customer", "main", "shop", nil), RefColumns: []string{"id"}}}
	table.Indexes = []*datatug.Index{
		{Name: "order_line_qty", Columns: []*datatug.IndexColumn{{Name: "qty"}}},
		{Name: "order_line_sku", IsUnique: true, Columns: []*datatug.IndexColumn{{Name: "sku", IsDescending: true}}},
		{Name: "sqlite_autoindex_order_line_1", Origin: "pk", IsUnique: true, Columns: []*datatug.IndexColumn{{Name: "id"}}},
		{Name: "by_lower_sku", Columns: []*datatug.IndexColumn{{Name: ""}}},
		nil,
	}
	return table
}

func TestWriteScannedKeys_WritesTheKeysAndTheIndexesOfTheTables(t *testing.T) {
	dir := keysProject(t)
	view := layoutView("customer_names")
	view.ForeignKeys = datatug.ForeignKeys{{Name: "never", Columns: []string{"x"}, RefTable: datatug.NewTableKey("y", "main", "", nil)}}
	view.Indexes = []*datatug.Index{{Name: "never", Columns: []*datatug.IndexColumn{{Name: "x"}}}}
	layout := keysLayout([]*datatug.CollectionInfo{layoutTable("Customer"), lineTable()}, []*datatug.CollectionInfo{view})

	require.NoError(t, writeScannedForeignKeys(dir, "shop", layout))
	require.NoError(t, writeScannedIndexes(dir, "shop", layout))

	keys, err := filestore.LoadModelForeignKeys(dir, "shop")
	require.NoError(t, err)
	require.Len(t, keys, 1, "a view has no keys, and a table without one is not listed")
	assert.Equal(t, "main", keys[0].Table.Schema())
	assert.Equal(t, "order_line", keys[0].Table.Name())
	require.Len(t, keys[0].ForeignKeys, 1)
	assert.Equal(t, "fk_customer", keys[0].ForeignKeys[0].Name)
	assert.Equal(t, []string{"id"}, keys[0].ForeignKeys[0].RefColumns)

	indexes, err := filestore.LoadModelIndexes(dir, "shop")
	require.NoError(t, err)
	require.Len(t, indexes, 1)
	var names []string
	for _, index := range indexes[0].Indexes {
		names = append(names, index.Name)
	}
	assert.Equal(t, []string{"order_line_qty", "order_line_sku"}, names, "an index on an expression and SQLite's primary key index are left out")
	assert.True(t, indexes[0].Indexes[1].IsUnique)
	assert.True(t, indexes[0].Indexes[1].Columns[0].IsDescending)
}

func TestWriteScannedKeys_NoFileForAScanThatFoundNone(t *testing.T) {
	dir := keysProject(t)
	layout := keysLayout([]*datatug.CollectionInfo{layoutTable("Customer")}, nil)
	require.NoError(t, writeScannedForeignKeys(dir, "shop", layout))
	require.NoError(t, writeScannedIndexes(dir, "shop", layout))
	assert.NoFileExists(t, filepath.Join(dir, "dbmodels", "shop", "shop.refs.json"))
	assert.NoFileExists(t, filepath.Join(dir, "dbmodels", "shop", "shop.indexes.json"))
}

// A rescan of a database whose keys and indexes are gone empties the files an earlier scan wrote.
func TestWriteScannedKeys_ARescanThatFoundNoneEmptiesTheFiles(t *testing.T) {
	dir := keysProject(t)
	require.NoError(t, writeScannedForeignKeys(dir, "shop", keysLayout([]*datatug.CollectionInfo{lineTable()}, nil)))
	require.NoError(t, writeScannedIndexes(dir, "shop", keysLayout([]*datatug.CollectionInfo{lineTable()}, nil)))

	empty := keysLayout([]*datatug.CollectionInfo{layoutTable("order_line")}, nil)
	require.NoError(t, writeScannedForeignKeys(dir, "shop", empty))
	require.NoError(t, writeScannedIndexes(dir, "shop", empty))
	keys, err := filestore.LoadModelForeignKeys(dir, "shop")
	require.NoError(t, err)
	assert.Empty(t, keys)
	indexes, err := filestore.LoadModelIndexes(dir, "shop")
	require.NoError(t, err)
	assert.Empty(t, indexes)
	assert.FileExists(t, filepath.Join(dir, "dbmodels", "shop", "shop.refs.json"))
}

// A file that cannot be read is replaced with what the scan found, which is nothing.
func TestWriteScannedKeys_AnUnreadableFileIsReplaced(t *testing.T) {
	dir := keysProject(t)
	for _, name := range []string{"shop.refs.json", "shop.indexes.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "dbmodels", "shop", name), []byte("not json"), 0o644))
	}
	empty := keysLayout(nil, nil)
	require.NoError(t, writeScannedForeignKeys(dir, "shop", empty))
	require.NoError(t, writeScannedIndexes(dir, "shop", empty))
	keys, err := filestore.LoadModelForeignKeys(dir, "shop")
	require.NoError(t, err)
	assert.Empty(t, keys)
	indexes, err := filestore.LoadModelIndexes(dir, "shop")
	require.NoError(t, err)
	assert.Empty(t, indexes)
}

func TestWriteScannedKeys_AFailedWriteNamesTheModel(t *testing.T) {
	cause := errors.New("disk full")
	previousKeys, previousIndexes := saveModelForeignKeys, saveModelIndexes
	t.Cleanup(func() { saveModelForeignKeys, saveModelIndexes = previousKeys, previousIndexes })
	saveModelForeignKeys = func(string, string, []datatug.TableForeignKeys) error { return cause }
	saveModelIndexes = func(string, string, []datatug.TableIndexes) error { return cause }
	layout := keysLayout([]*datatug.CollectionInfo{lineTable()}, nil)

	err := writeScannedForeignKeys(t.TempDir(), "shop", layout)
	assert.EqualError(t, err, `failed to write the foreign keys of db model "shop": disk full`)
	assert.ErrorIs(t, err, cause)
	err = writeScannedIndexes(t.TempDir(), "shop", layout)
	assert.EqualError(t, err, `failed to write the indexes of db model "shop": disk full`)
	assert.ErrorIs(t, err, cause)
}

func TestStorableIndex(t *testing.T) {
	for name, index := range map[string]*datatug.Index{
		"nil":           nil,
		"primary key":   {Name: "pk", Origin: "pk", Columns: []*datatug.IndexColumn{{Name: "id"}}},
		"no column":     {Name: "empty"},
		"a nil column":  {Name: "holey", Columns: []*datatug.IndexColumn{{Name: "a"}, nil}},
		"an expression": {Name: "lower", Columns: []*datatug.IndexColumn{{Name: ""}}},
	} {
		assert.False(t, storableIndex(index), name)
	}
	assert.True(t, storableIndex(&datatug.Index{Name: "u", Origin: "u", Columns: []*datatug.IndexColumn{{Name: "sku"}}}), "a unique constraint's index is stored")
}

// A scan whose foreign keys cannot be written fails, and says so.
func TestSaveScannedProject_AFailedWriteOfTheForeignKeysFailsTheSave(t *testing.T) {
	cause := errors.New("disk full")
	previous := saveModelForeignKeys
	t.Cleanup(func() { saveModelForeignKeys = previous })
	saveModelForeignKeys = func(string, string, []datatug.TableForeignKeys) error { return cause }
	projectDir := t.TempDir()
	dbPath := filepath.Join(projectDir, "shop.db")
	require.NoError(t, os.WriteFile(dbPath, nil, 0o644))
	project := layoutProject(t, dbPath, layoutSchema("main", []*datatug.CollectionInfo{lineTable()}, nil))

	err := SaveScannedProject(context.Background(), layoutStore(projectDir), projectDir, project, layoutScanned, &bytes.Buffer{})
	assert.ErrorIs(t, err, cause)
	assert.NoFileExists(t, filepath.Join(projectDir, "dbmodels", "shop", "shop.indexes.json"), "nothing is written after the failure")
}

// A scan of another environment that has no order_line leaves what the file holds for order_line, whose folder
// an earlier scan wrote; a scan that reads order_line again says what it read.
func TestWriteScannedKeys_AScanKeepsTheTablesItDidNotRead(t *testing.T) {
	dir := keysProject(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "dbmodels", "shop", "main", "tables", "order_line"), 0o755))
	require.NoError(t, writeScannedForeignKeys(dir, "shop", keysLayout([]*datatug.CollectionInfo{lineTable()}, nil)))
	require.NoError(t, writeScannedIndexes(dir, "shop", keysLayout([]*datatug.CollectionInfo{lineTable()}, nil)))

	other := keysLayout([]*datatug.CollectionInfo{layoutTable("Customer")}, nil)
	require.NoError(t, writeScannedForeignKeys(dir, "shop", other))
	require.NoError(t, writeScannedIndexes(dir, "shop", other))
	keys, err := filestore.LoadModelForeignKeys(dir, "shop")
	require.NoError(t, err)
	require.Len(t, keys, 1, "the keys of order_line stay")
	indexes, err := filestore.LoadModelIndexes(dir, "shop")
	require.NoError(t, err)
	require.Len(t, indexes, 1, "the indexes of order_line stay")

	reread := keysLayout([]*datatug.CollectionInfo{layoutTable("order_line")}, nil)
	reread.written = map[folderKey]bool{{"main", "tables", "order_line"}: true}
	require.NoError(t, writeScannedForeignKeys(dir, "shop", reread))
	require.NoError(t, writeScannedIndexes(dir, "shop", reread))
	keys, err = filestore.LoadModelForeignKeys(dir, "shop")
	require.NoError(t, err)
	assert.Empty(t, keys, "order_line was read again, with no key")
	indexes, err = filestore.LoadModelIndexes(dir, "shop")
	require.NoError(t, err)
	assert.Empty(t, indexes, "order_line was read again, with no index")
}
