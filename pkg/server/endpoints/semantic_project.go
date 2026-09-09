package endpoints

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	moduledatatug "github.com/datatug/datatug-core/pkg/datatug"
)

// semanticProjectDir resolves the "project" query-param value to the
// project's directory on disk, via filestore.GetProjectPath (populated at
// `datatug serve` startup — see http_server.go's newDatatugStoreFactory).
//
// The three semantic/applicable-queries endpoints read a project's
// entities/queries/recordsets/data trees directly off disk rather than
// through datatug.ProjectStore, for two reasons:
//
//  1. pkg/semantic (github.com/datatug/datatug-core/pkg/semantic, imported
//     from the module per this stream's brief since datatug-cli's own S1b
//     module-dependency swap had not landed when this was written — see the
//     PR body) operates on that MODULE's own datatug.Entity/QueryDef types.
//     The vendored pkg/datatug-core/datatug.Entity used everywhere else in
//     this repo is missing EntityField.Mappings entirely (confirmed by
//     diffing the two trees while building this): unmarshaling a project's
//     entity JSON through the vendored type silently drops any "mappings"
//     key, which would make every resolution "inferred" or absent, never
//     "declared", regardless of what the project file actually says. Reading
//     the same JSON bytes into the module's own Entity type (which does
//     have Mappings) avoids that silently-wrong translation.
//  2. datatug.ProjectStore's LoadQueries(ctx, folderPath) does not recurse
//     into subfolders (verified against project_items_store.go's loadDir,
//     which reads exactly one directory level) and QueriesFolder.Folders is
//     never populated by it either — so it cannot return "every query in the
//     project" the way Applicable needs in one call. Direct traversal is
//     both simpler and correct for this specific need.
func semanticProjectDir(projectID string) (string, error) {
	if projectID == "" {
		return "", newFieldError("project", "is required")
	}
	dir := filestore.GetProjectPath(projectID)
	if dir == "" {
		return "", newFieldError("project", fmt.Sprintf("unknown project %q (is datatug serve running with this project loaded?)", projectID))
	}
	return dir, nil
}

// loadModuleEntities reads every entities/**/*.entity.json file under dir
// into the module's datatug.Entity type (see semanticProjectDir's doc for
// why not the vendored one).
func loadModuleEntities(dir string) ([]*moduledatatug.Entity, error) {
	entitiesDir := filepath.Join(dir, storage.EntitiesFolder)
	suffix := "." + storage.EntityFileSuffix + ".json"
	var entities []*moduledatatug.Entity
	err := walkJSONFiles(entitiesDir, suffix, func(path string, data []byte) error {
		var entity moduledatatug.Entity
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
func loadModuleQueries(dir string) ([]*moduledatatug.QueryDef, error) {
	queriesDir := filepath.Join(dir, storage.QueriesFolder)
	suffix := "." + storage.QueryFileSuffix + ".json"
	var queries []*moduledatatug.QueryDef
	err := walkJSONFiles(queriesDir, suffix, func(path string, data []byte) error {
		var query moduledatatug.QueryDef
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

// semanticIngitdbPath returns the project's shared inGitDB store directory
// (data/ingitdb, per REQ:http-reference-source's sibling recordsets/README.md
// documentation of the support-notes collection) as a pkg/dbcopy-parseable
// ingitdb:// URL.
func semanticIngitdbPath(projectDir string) string {
	return "ingitdb://" + filepath.Join(projectDir, storage.DataFolder, "ingitdb")
}

// newFieldError builds the structured {code, message, field} error this
// feature's API contracts require (core-investigation-loop's "Security"
// section: "Errors are structured (code, message, field); access refusals
// use code: ACCESS_DENIED with the policy name and never the hidden
// value."). code defaults to "BAD_REQUEST"; use newAccessDeniedError for
// ACCESS_DENIED. Write it to the response with writeSemanticError (see
// semantic_columns.go), not the package's generic handleError/returnJSON —
// those pre-date this contract and always emit {"error": "..."}.
func newFieldError(field, message string) error {
	return &structuredError{Code: "BAD_REQUEST", Message: message, Field: field}
}

// newAccessDeniedError builds an ACCESS_DENIED structured error. message
// MUST name the policy, and MUST NOT echo the hidden value/field the
// refusal is protecting (REQ's own words: "never the hidden value").
func newAccessDeniedError(message string) error {
	return &structuredError{Code: "ACCESS_DENIED", Message: message}
}

// structuredError is the JSON error shape core-investigation-loop's API
// contracts require: {code, message, field}.
type structuredError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func (e *structuredError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// queryValues is a tiny url.Values convenience wrapper used by the semantic
// handlers to read repeated query parameters (role=/group=) alongside plain
// ones, without importing net/url in every file.
type queryValues = url.Values
