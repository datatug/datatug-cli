package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage"
)

// parseSourceArg is the one place a command turns the value of a source flag (--db, --from, --to)
// into a dbcopy.BackendRef: dbcopy.Parse for every form it knows, and for a datatug:// reference
// (datatug://<project>/<db>[?env=<ENV>], or datatug:///<db> for the current project) the source of
// the project: the sqlite:// URL of the file of a SQLite source, or env:<VARIABLE> for a PostgreSQL
// one, resolved by pkg/api from the project's catalog and connection descriptor. fromProject says
// which. What a SQLite source shows as is the reference; a PostgreSQL source shows the variable
// it is read from. The URL a variable holds, and the path of a file, are never printed.
//
// A reference that does not resolve fails with an exit error: 2 for a reference that is not well
// formed, a project given no name that cannot be told, a database id several environments list (the
// message names their ids) or a source that cannot be opened; 3 for a project, a database or an
// environment that is not there. Any other value fails as dbcopy.Parse fails.
func parseSourceArg(ctx context.Context, arg string) (ref dbcopy.BackendRef, fromProject bool, err error) {
	if !dbcopy.IsDatatugRef(arg) {
		ref, err = dbcopy.Parse(arg)
		return ref, false, err
	}
	reference, err := dbcopy.ParseDatatugRef(arg)
	if err != nil {
		return ref, true, Exit(err.Error(), exitCodeUsage)
	}
	source, err := datatugRefSource(ctx, reference)
	if err != nil {
		return ref, true, err
	}
	if ref, err = dbcopy.Parse(source); err != nil {
		return ref, true, Exit(err.Error(), exitCodeUsage)
	}
	if ref.Scheme == "sqlite" {
		ref.Raw = reference.String()
	}
	return ref, true, nil
}

// keepExit is err when it already carries an exit code (a datatug:// reference that did not
// resolve), and plain(err) otherwise.
func keepExit(err error, plain func(error) error) error {
	var coder ExitCoder
	if errors.As(err, &coder) {
		return err
	}
	return plain(err)
}

// projectNameOf is the registered project a reference without a project name means: the folder the
// command runs in when it is a project, else the only registered project. "" means the folder.
func projectNameOf(name string) (project, dir string, err error) {
	if name != "" {
		return name, "", nil
	}
	if _, statErr := os.Stat(filepath.Join(".", storage.ProjectSummaryFileName)); statErr == nil {
		return "", ".", nil
	}
	config, err := dtconfig.GetSettings()
	if err != nil || len(config.Projects) != 1 {
		return "", "", Exit("the reference names no project (datatug:///<db>): run in a project folder, or have exactly one registered project, or write datatug://<project>/<db>", exitCodeUsage)
	}
	return config.Projects[0].ID, "", nil
}

// datatugRefSource resolves reference to a source (see parseSourceArg).
func datatugRefSource(ctx context.Context, reference dbcopy.DatatugRef) (string, error) {
	project, dir, err := projectNameOf(reference.Project)
	if err != nil {
		return "", err
	}
	base, err := openProjectForRead(project, dir)
	if err != nil {
		return "", err
	}
	source, err := api.ResolveProjectSourceURL(ctx, base.store.GetProjectStore(base.projectID), base.ProjectDir, reference.Env, reference.DB)
	var notFound *api.ProjectSourceNotFoundError
	var ambiguous *api.AmbiguousSourceError
	var unresolved *api.SourceUnresolvedError
	switch {
	case errors.As(err, &notFound):
		return "", Exit(err.Error(), exitCodeNotFound)
	case errors.As(err, &ambiguous):
		return "", Exit(fmt.Sprintf("database %q: %s", dbcopy.SourceIDDisplay(reference.DB), err.Error()), exitCodeUsage)
	case errors.As(err, &unresolved):
		return "", Exit(fmt.Sprintf("database %q cannot be opened: %s", dbcopy.SourceIDDisplay(reference.DB), err.Error()), exitCodeUsage)
	}
	return source, err
}
