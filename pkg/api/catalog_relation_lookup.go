package api

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-core/pkg/storage"
)

// MaxColumnsFileSize bounds the columns file a lookup reads: a larger one is not read.
const MaxColumnsFileSize = 4 << 20

// errRelationUnreadable is the fixed failure of a lookup: the system's own text quotes a path.
var errRelationUnreadable = errors.New("the scanned metadata cannot be read")

// FindCatalogRelations is the scanned relations named name (in any schema, tables and views) of the
// catalog, reading only their columns files: unlike GetCatalogSchema it does not read every
// table. scanned is false when the dbModel holds no relation at all (never scanned, or a model
// folder that is not there). A columns file that is not a regular file (a link is never followed)
// or is larger than MaxColumnsFileSize is a failure, as is any unreadable file.
func FindCatalogRelations(projectDir, environmentID, catalogID, name string) (found []CatalogRelation, scanned bool, err error) {
	dbModelID, err := catalogDbModel(projectDir, environmentID, catalogID)
	if err != nil {
		return nil, false, err
	}
	dbModelDir := filepath.Join(projectDir, storage.DbModelsFolder, dbModelID)
	schemaDirs, err := os.ReadDir(dbModelDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, errRelationUnreadable
	}
	for _, schemaDir := range schemaDirs {
		if !schemaDir.IsDir() { // a link is not a directory here: it is not followed
			continue
		}
		for _, kind := range []struct{ dir, dbType string }{{"tables", "BASE TABLE"}, {"views", "VIEW"}} {
			kindDir := filepath.Join(dbModelDir, schemaDir.Name(), kind.dir)
			tableDirs, err := os.ReadDir(kindDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, false, errRelationUnreadable
			}
			for _, tableDir := range tableDirs {
				if !tableDir.IsDir() {
					continue
				}
				scanned = true
				if tableDir.Name() != name {
					continue
				}
				columns, err := readBoundedColumns(filepath.Join(kindDir, name))
				if err != nil {
					return nil, false, err
				}
				found = append(found, CatalogRelation{Schema: schemaDir.Name(), Name: name, DbType: kind.dbType, Columns: columns})
			}
		}
	}
	return found, scanned, nil
}

// readBoundedColumns reads the one columns file of a relation's folder.
func readBoundedColumns(folder string) ([]CatalogColumn, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, errRelationUnreadable
	}
	suffix := "." + storage.ColumnsFileSuffix + ".json"
	var file os.DirEntry
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), suffix) {
			if file != nil {
				return nil, errRelationUnreadable
			}
			file = entry
		}
	}
	if file == nil || !file.Type().IsRegular() {
		return nil, errRelationUnreadable
	}
	f, err := os.Open(filepath.Join(folder, file.Name()))
	if err != nil {
		return nil, errRelationUnreadable
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, MaxColumnsFileSize+1))
	if err != nil || len(data) > MaxColumnsFileSize {
		return nil, errRelationUnreadable
	}
	var parsed catalogColumnsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, errRelationUnreadable
	}
	return parsed.Columns, nil
}
