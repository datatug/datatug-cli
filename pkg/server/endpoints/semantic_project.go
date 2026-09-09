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
//
// canonicalIDs maps each returned *datatug.QueryDef pointer to its
// canonical, folder-qualified id (its path relative to queries/, "/"-joined
// — the same convention api.ResolveQueryID/QueryIDIndex use, and the id
// fsQueriesStore.LoadQuery actually needs for a query in a subfolder). It is
// keyed by pointer, not by QueryDef.ID (always bare — datatug.QueryDef.ID
// is never folder-qualified, per fsQueriesStore.LoadQuery's own SetID
// convention), because two queries in different folders may share the same
// bare id (S97's own "ambiguous" case for input resolution), which a
// bare-id-keyed map could not distinguish between.
func loadModuleQueries(dir string) ([]*datatug.QueryDef, map[*datatug.QueryDef]string, error) {
	queriesDir := filepath.Join(dir, storage.QueriesFolder)
	suffix := "." + storage.QueryFileSuffix + ".json"
	var queries []*datatug.QueryDef
	canonicalIDs := map[*datatug.QueryDef]string{}
	err := walkJSONFiles(queriesDir, suffix, func(path string, data []byte) error {
		var query datatug.QueryDef
		if err := json.Unmarshal(data, &query); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		bareID := strings.TrimSuffix(filepath.Base(path), suffix)
		if query.ID == "" {
			query.ID = bareID
		}
		rel, relErr := filepath.Rel(queriesDir, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		canonicalID := bareID
		if rel != "." {
			canonicalID = filepath.ToSlash(filepath.Join(rel, bareID))
		}
		queries = append(queries, &query)
		canonicalIDs[&query] = canonicalID
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("load queries from %s: %w", queriesDir, err)
	}
	sort.Slice(queries, func(i, j int) bool { return canonicalIDs[queries[i]] < canonicalIDs[queries[j]] })
	return queries, canonicalIDs, nil
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
