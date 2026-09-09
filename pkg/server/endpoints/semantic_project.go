package endpoints

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// loadModuleEntities reads every entities/**/*.entity.json file under dir
// into datatug.Entity directly, bypassing datatug.ProjectStore:
// datatug.ProjectStore's LoadQueries(ctx, folderPath) does not recurse into
// subfolders (verified against project_items_store.go's loadDir, which
// reads exactly one directory level) and QueriesFolder.Folders is never
// populated by it either — so it cannot return "every query in the
// project" the way Applicable needs in one call. loadModuleEntities reads
// the same way for symmetry with loadModuleQueries, and because this path
// is already proven and tested.
func loadModuleEntities(dir string) ([]*datatug.Entity, error) {
	entitiesDir := filepath.Join(dir, storage.EntitiesFolder)
	suffix := "." + storage.EntityFileSuffix + ".json"
	var entities []*datatug.Entity
	err := walkJSONFiles(entitiesDir, suffix, func(path string, data []byte) error {
		var entity datatug.Entity
		if err := json.Unmarshal(data, &entity); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if entity.ID == "" {
			entity.ID = strings.TrimSuffix(filepath.Base(path), suffix)
		}
		entities = append(entities, &entity)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load entities from %s: %w", entitiesDir, err)
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })
	return entities, nil
}

// loadModuleQueries reads every queries/**/*.query.json file under dir into
// the module's datatug.QueryDef type. Applicable only needs ID/Type/
// Parameters, all present in the .query.json file itself — the sibling
// .query.<ext> body (SQL/DTQL/HTTP text) is never read here.
func loadModuleQueries(dir string) ([]*datatug.QueryDef, error) {
	queriesDir := filepath.Join(dir, storage.QueriesFolder)
	suffix := "." + storage.QueryFileSuffix + ".json"
	var queries []*datatug.QueryDef
	err := walkJSONFiles(queriesDir, suffix, func(path string, data []byte) error {
		var query datatug.QueryDef
		if err := json.Unmarshal(data, &query); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if query.ID == "" {
			query.ID = strings.TrimSuffix(filepath.Base(path), suffix)
		}
		queries = append(queries, &query)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load queries from %s: %w", queriesDir, err)
	}
	sort.Slice(queries, func(i, j int) bool { return queries[i].ID < queries[j].ID })
	return queries, nil
}

// walkJSONFiles calls onFile(path, data) for every regular file under dir
// (recursively) whose name ends with suffix. A missing dir is not an error
// (an empty project tree is valid); any other stat/read/parse error stops
// the walk and is returned as-is (callers wrap with their own context).
func walkJSONFiles(dir, suffix string, onFile func(path string, data []byte) error) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), suffix) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return onFile(path, data)
	})
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
