package api

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// saveModelForeignKeys is filestore.SaveModelForeignKeys, a seam so a test can make the write fail.
var saveModelForeignKeys = filestore.SaveModelForeignKeys

// loadModelForeignKeys is filestore.LoadModelForeignKeys, a seam as saveModelForeignKeys is.
var loadModelForeignKeys = filestore.LoadModelForeignKeys

// saveModelIndexes and loadModelIndexes are filestore.SaveModelIndexes and LoadModelIndexes, seams as the
// foreign keys' are.
var (
	saveModelIndexes = filestore.SaveModelIndexes
	loadModelIndexes = filestore.LoadModelIndexes
)

// The foreign keys and the indexes of a db model are in one file each, beside the model's own file
// (datatug-core's spec/features/model-foreign-keys-file and model-indexes-file). A model is shared by the
// catalogs of every environment, as its columns files are, so a scan of one environment says what it read
// for the tables it read, and leaves what the file holds for a table it did not read, as long as the table
// is in the model (its folder is there: an earlier scan, of this or another environment, wrote it, and no
// scan retracted it). A scan that found nothing, of a model whose file holds nothing, writes no file.

// writeScannedForeignKeys writes the foreign keys of the tables of the layout into
// dbmodels/<model>/<model>.refs.json, each table under the schema folder it is written in. A view has no
// keys.
func writeScannedForeignKeys(projectDir, model string, layout scannedLayout) error {
	var tables []datatug.TableForeignKeys
	for _, folder := range layout.folders {
		if folder.kind != "tables" {
			continue
		}
		for _, table := range folder.tables {
			if len(table.ForeignKeys) == 0 {
				continue
			}
			tables = append(tables, datatug.TableForeignKeys{
				Table:       datatug.NewTableKey(table.Name(), folder.schema, "", nil),
				ForeignKeys: table.ForeignKeys,
			})
		}
	}
	stored, err := loadModelForeignKeys(projectDir, model)
	if err == nil && len(stored) == 0 && len(tables) == 0 {
		return nil
	}
	for _, table := range stored {
		if keptByTheScan(projectDir, model, layout, table.Table) {
			tables = append(tables, table)
		}
	}
	if err := saveModelForeignKeys(projectDir, model, tables); err != nil {
		return fmt.Errorf("failed to write the foreign keys of db model %q: %w", model, err)
	}
	return nil
}

// writeScannedIndexes writes the indexes of the tables of the layout into
// dbmodels/<model>/<model>.indexes.json, as writeScannedForeignKeys writes the keys. The file holds
// indexes of columns only, so an index on an expression (a column without a name) is left out, and so is
// the index SQLite makes for a primary key, which the columns file already says.
func writeScannedIndexes(projectDir, model string, layout scannedLayout) error {
	var tables []datatug.TableIndexes
	for _, folder := range layout.folders {
		if folder.kind != "tables" {
			continue
		}
		for _, table := range folder.tables {
			var indexes []*datatug.Index
			for _, index := range table.Indexes {
				if storableIndex(index) {
					indexes = append(indexes, index)
				}
			}
			if len(indexes) == 0 {
				continue
			}
			tables = append(tables, datatug.TableIndexes{
				Table:   datatug.NewTableKey(table.Name(), folder.schema, "", nil),
				Indexes: indexes,
			})
		}
	}
	stored, err := loadModelIndexes(projectDir, model)
	if err == nil && len(stored) == 0 && len(tables) == 0 {
		return nil
	}
	for _, table := range stored {
		if keptByTheScan(projectDir, model, layout, table.Table) {
			tables = append(tables, table)
		}
	}
	if err := saveModelIndexes(projectDir, model, tables); err != nil {
		return fmt.Errorf("failed to write the indexes of db model %q: %w", model, err)
	}
	return nil
}

// keptByTheScan reports whether what the model's file holds for table stays: the scan did not read the
// table, and the table is in the model, its folder there.
func keptByTheScan(projectDir, model string, layout scannedLayout, table datatug.DBCollectionKey) bool {
	if layout.has(table.Schema(), "tables", table.Name()) {
		return false
	}
	info, err := os.Lstat(filepath.Join(projectDir, storage.DbModelsFolder, model, table.Schema(), "tables", table.Name()))
	return err == nil && info.IsDir()
}

// storableIndex reports whether the indexes file can hold index: one of named columns, that is not the
// index SQLite makes for a primary key.
func storableIndex(index *datatug.Index) bool {
	if index == nil || index.Origin == "pk" || len(index.Columns) == 0 {
		return false
	}
	for _, column := range index.Columns {
		if column == nil || column.Name == "" {
			return false
		}
	}
	return true
}
