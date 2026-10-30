package api

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
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
				found = append(found, CatalogRelation{Schema: schemaDir.Name(), Name: name, DbType: kind.dbType, Columns: columns, folder: filepath.Join(kindDir, name)})
			}
		}
	}
	return found, scanned, nil
}

// readBoundedColumns reads the one columns file of a relation's folder.
func readBoundedColumns(folder string) ([]CatalogColumn, error) {
	data, err := readBoundedColumnsData(folder)
	if err != nil {
		return nil, err
	}
	var parsed catalogColumnsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, errRelationUnreadable
	}
	return parsed.Columns, nil
}

// readBoundedColumnsData is the bytes of the one columns file of a relation's folder.
func readBoundedColumnsData(folder string) ([]byte, error) {
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
	return data, nil
}

// ScannedColumnOrder is the column names of one scanned table or view in the
// order the database declares them (the columns file's ordinalPosition; the
// file's own order when it records none). schema "" matches any schema; a name
// found in several schemas, a relation that was not scanned and a columns file
// that cannot be read all answer false: the caller then has no scanned order.
func ScannedColumnOrder(projectDir, environmentID, catalogID, schema, name string) ([]string, bool) {
	found, _, err := FindCatalogRelations(projectDir, environmentID, catalogID, name)
	if err != nil || len(found) == 0 {
		return nil, false
	}
	pick := -1
	for i, relation := range found {
		if schema != "" && relation.Schema == schema {
			pick = i
			break
		}
	}
	if pick < 0 {
		if len(found) != 1 {
			return nil, false
		}
		pick = 0
	}
	var parsed struct {
		Columns []struct {
			Name            string `json:"name"`
			OrdinalPosition int    `json:"ordinalPosition"`
		} `json:"columns"`
	}
	// The file was read by FindCatalogRelations a moment ago; a failure now is
	// the same as having no scanned order.
	if data, err := readBoundedColumnsData(found[pick].folder); err != nil || json.Unmarshal(data, &parsed) != nil || len(parsed.Columns) == 0 {
		return nil, false
	}
	columns := parsed.Columns
	sort.SliceStable(columns, func(i, j int) bool { return columns[i].OrdinalPosition < columns[j].OrdinalPosition })
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = column.Name
	}
	return names, true
}
