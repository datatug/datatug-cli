package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// resolveSourceURL turns a project's environment + database (an EnvDbServer
// catalog ID) into the pkg/dbcopy source URL secureread.Executor opens
// (REQ:server-acl-all-reads: "open sources through pkg/dbcopy/url.go" —
// sqlite:// via dalgo2sqlite, ingitdb://), and the resolved catalog's own
// driver name (S101: so a caller building a structured query's collection
// reference can derive that driver's default schema — see
// PolicyCollectionName — from real catalog metadata instead of assuming
// one). It walks the environment's configured DB servers looking for one
// whose catalog matches database, mirroring how a human would pick a
// database within an environment. projDir is the project's own on-disk
// directory, needed to resolve a catalog path that is relative rather than
// "~"/"$HOME"-prefixed or already absolute — see ResolveCatalogPath.
func resolveSourceURL(ctx context.Context, projStore datatug.ProjectStore, environment, database, projDir string) (sourceURL, driver string, err error) {
	env, err := projStore.LoadEnvironment(ctx, environment)
	if err != nil {
		return "", "", LookupError("load environment %q", err, environment)
	}
	var lastErr error
	for _, server := range env.DbServers {
		if server == nil {
			continue
		}
		catalog, catalogErr := projStore.LoadEnvDbCatalog(ctx, environment, server.GetID(), database)
		if catalogErr != nil {
			lastErr = catalogErr
			continue
		}
		sourceURL, err = sourceURLFromCatalog(catalog, projDir)
		return sourceURL, catalog.Driver, err
	}
	// environment and database are what a client sent, and a source string can be
	// sent where an ID belongs: see LookupError.
	if lastErr != nil {
		return "", "", LookupError("database %q not found in environment %q", lastErr, database, environment)
	}
	return "", "", LookupError("environment %q has no DB servers configured; cannot resolve database %q", nil, environment, database)
}

// sourceURLFromCatalog maps a resolved DbCatalog to the pkg/dbcopy URL
// scheme its driver corresponds to. Only the schemes secureread.Executor
// (via pkg/dbcopy) actually opens are supported here; anything else fails
// with a descriptive error rather than silently building an unusable URL.
// catalog.Path is expanded through ResolveCatalogPath first (S58: this used
// to build "sqlite://" + catalog.Path with no expansion at all, unopenable
// against demo-project-1's real catalog data, which declares paths like
// "~/datatug/dbs/chinook-local.sqlite").
func sourceURLFromCatalog(catalog datatug.DbCatalog, projDir string) (string, error) {
	switch catalog.Driver {
	case "openvaultdb":
		if catalog.Path == "" {
			return "", fmt.Errorf("OpenVaultDB connection descriptor path is required")
		}
		path, err := ResolveCatalogPath(projDir, catalog.Path)
		if err != nil {
			return "", err
		}
		return dbcopy.LocalSourceURL("openvaultdb", path), nil
	case "sqlite3", "sqlite":
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no path configured for its sqlite driver", catalog.ID)
		}
		path, err := ResolveCatalogPath(projDir, catalog.Path)
		if err != nil {
			return "", fmt.Errorf("catalog %q: %w", catalog.ID, err)
		}
		return dbcopy.LocalSourceURL("sqlite", path), nil
	case "ingitdb":
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no path configured for its ingitdb driver", catalog.ID)
		}
		path, err := ResolveCatalogPath(projDir, catalog.Path)
		if err != nil {
			return "", fmt.Errorf("catalog %q: %w", catalog.ID, err)
		}
		return dbcopy.LocalSourceURL("ingitdb", path), nil
	case DriverPostgres:
		// The catalog's path names a connection descriptor, a project file that
		// names an environment variable and nothing else; the variable holds the
		// connection URL, password included. The source is "env:NAME", so no
		// project file and no message ever holds the URL.
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no connection descriptor path configured for its postgres driver", catalog.ID)
		}
		path, err := ResolveCatalogPath(projDir, catalog.Path)
		if err != nil {
			return "", fmt.Errorf("catalog %q: %w", catalog.ID, err)
		}
		descriptor, err := dbcopy.ReadPostgresDescriptor(path)
		if err != nil {
			return "", fmt.Errorf("catalog %q: %w", catalog.ID, err)
		}
		// The variable must hold a postgres URL: a catalog labelled postgres whose
		// variable held a sqlite or http source would otherwise be opened as that
		// engine while policy naming still used the postgres label. Resolving it
		// also refuses a variable that is not set. No error names the value, only
		// the variable and the scheme.
		source := descriptor.SourceURL()
		ref, err := dbcopyParse(source)
		if err != nil {
			return "", fmt.Errorf("catalog %q: %w", catalog.ID, err)
		}
		if ref.Scheme != DriverPostgres {
			return "", fmt.Errorf("catalog %q: environment variable %s must hold a postgres:// URL, not a %s source", catalog.ID, descriptor.DSNEnv, ref.Scheme)
		}
		return source, nil
	default:
		return "", fmt.Errorf("database driver %q is not supported for policy-enforced reads (want sqlite3, ingitdb, openvaultdb or postgres)", catalog.Driver)
	}
}

// loadQueryDocument reads a saved query's text/document sidecar file
// directly off disk: "<id>.<QueryFileSuffix>.<lowercase(queryType)>" beside
// "<id>.query.json", the same convention filestore's saveQuery already
// writes with (store_queries_saver.go). It exists because
// fsQueriesStore.LoadQuery does not hydrate QueryDef.Text back from that
// file (the read-back half of REQ:dtql-query-type's sidecar rule lands with
// datatug-core PR #302 / the S1b module swap — see this stream's PR body).
// LoadQueryDocument exports loadQueryDocument for pkg/server/endpoints'
// exec/run_query rewrite (Task 12), which needs the same sidecar-text read
// this package's own RunQuery/ExecuteSelect already used.
func LoadQueryDocument(projectID, queryID string, queryType datatug.QueryType) (string, error) {
	return loadQueryDocument(projectID, queryID, queryType)
}

func loadQueryDocument(projectID, queryID string, queryType datatug.QueryType) (string, error) {
	dir, ok := projectDir(projectID)
	if !ok || dir == "" {
		return "", fmt.Errorf("no project directory configured for project %q", projectID)
	}
	folder, id := queryFolderAndID(queryID)
	fileName := fmt.Sprintf("%s.%s.%s", id, storage.QueryFileSuffix, strings.ToLower(string(queryType)))
	filePath := filepath.Join(dir, storage.QueriesFolder, folder, fileName)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read query document %s: %w", filePath, err)
	}
	return string(content), nil
}

// queryFolderAndID splits a "folder/subfolder/id"-shaped query ID into its
// folder path and bare ID, mirroring fsQueriesStore.LoadQuery's own split
// (store_queries.go) so the sidecar path lines up with where LoadQuery reads
// the "<id>.query.json" metadata file from.
func queryFolderAndID(queryID string) (folder, id string) {
	parts := strings.Split(queryID, "/")
	id = parts[len(parts)-1]
	folder = filepath.Join(parts[:len(parts)-1]...)
	return folder, id
}
