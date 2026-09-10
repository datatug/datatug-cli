package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/datatug/datatug-core/pkg/storage"
)

// ErrCatalogNotFound is returned by GetCatalogTables when environmentID
// names no catalog with a catalogID.db.json file under the project's
// environments/<environmentID>/catalogs/<catalogID>/ directory — the
// contract-route translation for this is NOT_FOUND (404), the same
// treatment ErrQueryNotFound already gets (see
// pkg/server/endpoints/util_error_handling.go), never a raw filesystem
// error.
var ErrCatalogNotFound = errors.New("catalog not found")

// CatalogTable is the minimal {schema, name, dbType} identity
// datatug-apps' ITableFull
// (libs/datatug/main/src/lib/models/definition/apis/database.ts) already
// declares — no column/key detail, which env-db-table.page.ts's own
// /exec/select-backed row fetch remains the source of. Defined here rather
// than reusing datatug-core's own datatug.TableModel: that type embeds
// DBCollectionKey, whose schema/catalog/name fields are unexported with no
// custom MarshalJSON, so it serializes to "{}" over JSON — a
// file-storage/lookup-key type, not a wire DTO.
type CatalogTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	DbType string `json:"dbType,omitempty"`
}

// CatalogTables is GET /datatug/catalog-tables's response (Task 17 item
// A.2, S121): the catalog table/view list datatug-apps' EnvDbPageComponent
// (the catalog overview page one level above env-db-table.page.ts's own
// /table/<type> route) never had any way to populate before this — S120's
// report found nothing in this app fed it (no in-app link even targeted the
// route, and no endpoint returned this shape). Verified live, against a
// real `datatug serve` agent, that none of the three existing candidates
// api-contract.md-adjacent code already calls carries it:
//   - dbserver-databases needs a live driver/host/port to introspect an
//     actual DB *connection* (the "add a new server" flow) — unrelated to
//     an already-registered project catalog.
//   - projects/project_full's dbModels only ever carry
//     {id, environments[].DbCatalogs[].id}: Project.LoadProject never
//     populates DbModel.Schemas (confirmed against a live response).
//   - datatug-core's own DbModelsStore/fsDbModelsStore.LoadDbModel is
//     entirely commented out (storage/filestore/store_dbmodels.go) — the
//     same class of "schemer providers moved out" gap the sibling
//     queries/all_queries route (this same task) restores.
//
// So this reads the project's already-scanned dbmodel files directly, the
// same direct-filesystem-walk pattern loadModuleQueries/loadModuleEntities
// (pkg/server/endpoints/semantic_project.go) already use for an identical
// ProjectStore-incompleteness gap.
type CatalogTables struct {
	Tables []CatalogTable `json:"tables"`
	Views  []CatalogTable `json:"views"`
}

// catalogDbModelFile is the minimal shape this reads out of
// environments/<env>/catalogs/<catalog>/<catalog>.db.json — a
// datatug.DbCatalogBase-shaped file every demo/real project already writes
// (e.g. datatug-demo-projects' chinook-local.db.json:
// {"driver":"sqlite3","path":"...","dbModel":"chinook"}). Decoded locally,
// into only the one field this needs, rather than via
// datatug.DbCatalogBase itself, so this stays independent of that struct's
// own (stricter) Validate() rules.
type catalogDbModelFile struct {
	DbModel string `json:"dbModel"`
}

// GetCatalogTables resolves environmentID+catalogID to their dbModel (via
// the catalog's own <id>.db.json file) and lists every table/view file
// under that dbModel's dbmodels/<dbModel>/<schema>/{tables,views}/ tree.
// Read-only; unlike exec/select or dbserver-databases this touches no live
// database connection.
func GetCatalogTables(projectDir, environmentID, catalogID string) (*CatalogTables, error) {
	dbModelID, err := catalogDbModel(projectDir, environmentID, catalogID)
	if err != nil {
		return nil, err
	}
	dbModelDir := filepath.Join(projectDir, storage.DbModelsFolder, dbModelID)
	// "tables"/"views": datatug-core's own TablesFolder/ViewsFolder
	// constants are commented out (pkg/storage/file_names.go) — these are
	// the literal directory names datatug-cli's scan/demo tooling already
	// writes (see datatug-demo-projects/demo-project-1/dbmodels/chinook/main/tables/*).
	tables, err := listCatalogTables(dbModelDir, "tables", "BASE TABLE")
	if err != nil {
		return nil, err
	}
	views, err := listCatalogTables(dbModelDir, "views", "VIEW")
	if err != nil {
		return nil, err
	}
	// Never nil (-> JSON `null`) even when empty: the client
	// (EnvDbPageComponent) reads `.tables.length`/`.views.length`
	// unconditionally once envDb is set, the same way `IDatabaseFull`
	// callers elsewhere in datatug-apps already assume a present array.
	if tables == nil {
		tables = []CatalogTable{}
	}
	if views == nil {
		views = []CatalogTable{}
	}
	return &CatalogTables{Tables: tables, Views: views}, nil
}

func catalogDbModel(projectDir, environmentID, catalogID string) (string, error) {
	path := filepath.Join(
		projectDir, storage.EnvironmentsFolder, environmentID, storage.EnvDbCatalogsFolder, catalogID,
		storage.JsonFileName(catalogID, storage.DbCatalogFileSuffix),
	)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: catalog %q in environment %q", ErrCatalogNotFound, catalogID, environmentID)
		}
		return "", fmt.Errorf("read catalog file %s: %w", path, err)
	}
	var file catalogDbModelFile
	if err := json.Unmarshal(data, &file); err != nil {
		return "", fmt.Errorf("parse catalog file %s: %w", path, err)
	}
	if file.DbModel == "" {
		return "", fmt.Errorf("catalog %q (environment %q) has no dbModel set", catalogID, environmentID)
	}
	return file.DbModel, nil
}

// listCatalogTables walks dbModelDir/<schema>/<kind>/<TableName>/ — one
// directory per table/view, matching loadModuleQueries/loadModuleEntities'
// own "directory/file name IS the id" convention — for every schema
// subdirectory dbModelDir has. A missing dbModelDir or a schema with no
// <kind> subdirectory is not an error (an empty/partially-scanned dbmodel
// is valid, same tolerance walkJSONFiles gives a missing queries/ dir).
func listCatalogTables(dbModelDir, kind, dbType string) ([]CatalogTable, error) {
	var out []CatalogTable
	schemaDirs, err := os.ReadDir(dbModelDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("list schemas under %s: %w", dbModelDir, err)
	}
	for _, schemaDir := range schemaDirs {
		if !schemaDir.IsDir() {
			continue
		}
		schema := schemaDir.Name()
		kindDir := filepath.Join(dbModelDir, schema, kind)
		entries, err := os.ReadDir(kindDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("list %s under %s: %w", kind, kindDir, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			out = append(out, CatalogTable{Schema: schema, Name: e.Name(), DbType: dbType})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
