package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
		return "", "", sourceLookupFailed(`environment %q not found`, "load environment %q", err, environment)
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
		return "", "", sourceLookupFailed("database %q not found in environment %q", "database %q not found in environment %q", lastErr, database, environment)
	}
	return "", "", LookupError("environment %q has no DB servers configured; cannot resolve database %q", nil, environment, database)
}

// sourceLookupError is the failure of the lookup of the environment or the database of a
// source, or of the read of the connection descriptor of its catalog (see resolveSourceURL):
// its own text is LookupError's, which names the cause when the IDs are plain names, and
// answer is the sentence for a client, built from the kind and the IDs and from nothing the
// store or the operating system said (the cause quotes the path the store built from the
// project folder, or the path of the descriptor).
type sourceLookupError struct {
	error
	answer string
}

// Unwrap lets errors.Is and errors.As see what LookupError wrapped.
func (e sourceLookupError) Unwrap() error { return e.error }

// sourceLookupFailed builds the failure of a lookup: the error as LookupError gives it for
// format, cause and ids, and the answer for a client as LookupError gives answerFormat
// with no cause.
func sourceLookupFailed(answerFormat, format string, cause error, ids ...string) error {
	return sourceLookupError{error: LookupError(format, cause, ids...), answer: LookupError(answerFormat, nil, ids...).Error()}
}

// sourceLookupAnswer is the answer of a route to the error of resolveSourceURL: the failure
// of a lookup is the one sentence it was built with, and its text goes to the log of the
// server; any other error is unchanged.
func sourceLookupAnswer(err error) error {
	var failure sourceLookupError
	if !errors.As(err, &failure) {
		return err
	}
	log.Printf("api: %s: %s", failure.answer, dbcopy.RedactText(failure.Error()))
	return errors.New(failure.answer)
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
		return LocalSQLiteSourceURL(path), nil
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
		// project file and no message ever holds the URL. The descriptor must be a
		// file inside the project folder (ResolveDescriptorPath): a project file is
		// not trusted to point a reader at any other file of the machine.
		if catalog.Path == "" {
			return "", fmt.Errorf("catalog %q has no connection descriptor path configured for its postgres driver", catalog.ID)
		}
		path, err := ResolveDescriptorPath(projDir, catalog.Path)
		if err != nil {
			return "", fmt.Errorf("catalog %q: %w", catalog.ID, err)
		}
		descriptor, err := dbcopy.ReadPostgresDescriptor(path)
		if err != nil {
			// A descriptor that is not there, is a folder or cannot be read fails with the
			// text of the operating system, which quotes the path of the file in the served
			// project: the answer for a client is built from the ID of the catalog.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				return "", sourceLookupFailed("catalog %q: the PostgreSQL connection descriptor cannot be read", "catalog %q", err, catalog.ID)
			}
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

// LocalSQLiteSourceURL is the "sqlite://" source URL of the SQLite file at path, the
// path of a catalog as ResolveCatalogPath returns it: a URL that dbcopy.Parse reads
// back to that path. A file name may hold "%", "#" and "?", which a URL does not
// read as part of a path (a file named a#b.db is the file a, and one named a%23b.db
// is a#b.db), and a control character (a tab, a newline), which dbcopy.Parse refuses
// in a URL, so each is written as percent-encoded, and then the path starts like one
// ("./") when it is relative, as the host of a URL cannot be encoded.
func LocalSQLiteSourceURL(path string) string {
	escaped := escapeSQLitePath(path)
	if escaped == path {
		return dbcopy.LocalSourceURL("sqlite", path)
	}
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "./") && !strings.HasPrefix(path, "../") {
		escaped = "./" + escaped
	}
	return dbcopy.LocalSourceURL("sqlite", escaped)
}

// escapeSQLitePath writes "%", "#", "?" and every control character of path
// (a byte below a space, and the delete character) as percent-encoded.
func escapeSQLitePath(path string) string {
	var escaped strings.Builder
	for i := 0; i < len(path); i++ {
		switch c := path[i]; {
		case c == '%' || c == '#' || c == '?' || c < ' ' || c == 0x7f:
			fmt.Fprintf(&escaped, "%%%02X", c)
		default:
			escaped.WriteByte(c)
		}
	}
	return escaped.String()
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
		return "", fmt.Errorf("no project directory configured for project %q", dbcopy.SourceIDDisplay(projectID))
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
