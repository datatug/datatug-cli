package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// resolveSourceURL turns a project's environment + database (an EnvDbServer
// catalog ID) into the pkg/dbcopy source URL secureread.Executor opens
// (REQ:server-acl-all-reads: "open sources through pkg/dbcopy/url.go" —
// sqlite:// via dalgo2sqlite, ingitdb://). It walks the environment's
// configured DB servers looking for one whose catalog matches database,
// mirroring how a human would pick a database within an environment.
func resolveSourceURL(ctx context.Context, projStore datatug.ProjectStore, environment, database string) (string, error) {
	env, err := projStore.LoadEnvironment(ctx, environment)
	if err != nil {
		return "", fmt.Errorf("load environment %q: %w", environment, err)
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
		return sourceURLFromCatalog(catalog)
	}
	if lastErr != nil {
		return "", fmt.Errorf("database %q not found in environment %q (tried %d server(s), last error: %w)", database, environment, len(env.DbServers), lastErr)
	}
	return "", fmt.Errorf("environment %q has no DB servers configured; cannot resolve database %q", environment, database)
}

// sourceURLFromCatalog maps a resolved DbCatalog to the pkg/dbcopy URL
// scheme its driver corresponds to. Only the two schemes secureread.Executor
// (via pkg/dbcopy) actually opens are supported here; anything else fails
// with a descriptive error rather than silently building an unusable URL.
func sourceURLFromCatalog(catalog datatug.DbCatalog) (string, error) {
	switch catalog.Driver {
	case "sqlite3", "sqlite":
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no path configured for its sqlite driver", catalog.ID)
		}
		return "sqlite://" + catalog.Path, nil
	case "ingitdb":
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no path configured for its ingitdb driver", catalog.ID)
		}
		return "ingitdb://" + catalog.Path, nil
	default:
		return "", fmt.Errorf("database driver %q is not supported for policy-enforced reads (want sqlite3 or ingitdb)", catalog.Driver)
	}
}

// loadQueryDocument reads a saved query's text/document sidecar file
// directly off disk: "<id>.<QueryFileSuffix>.<lowercase(queryType)>" beside
// "<id>.query.json", the same convention filestore's saveQuery already
// writes with (store_queries_saver.go). It exists because
// fsQueriesStore.LoadQuery does not hydrate QueryDef.Text back from that
// file (the read-back half of REQ:dtql-query-type's sidecar rule lands with
// datatug-core PR #302 / the S1b module swap — see this stream's PR body).
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
