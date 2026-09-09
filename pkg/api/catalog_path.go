package api

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mitchellh/go-homedir"
)

// ResolveCatalogPath expands a datatug.DbCatalog.Path field into an absolute
// filesystem path the way this repo's real catalog data actually uses it
// (see datatug-demo-projects/demo-project-1's
// environments/*/catalogs/*/*.db.json, S45's dead-layout cleanup, e.g.
// "~/datatug/dbs/chinook-local.sqlite"):
//
//   - a leading "~" or "~/..." expands to the resolved home directory
//     (github.com/mitchellh/go-homedir)
//   - a leading "$HOME" or "${HOME}" expands the same way
//   - any other relative path resolves against projectDir (the project's
//     own on-disk location), so it means the same thing regardless of the
//     caller's working directory
//   - an already-absolute path (after the above) is returned unchanged
//
// datatug-core's DbCatalogBase.Path has no documented convention for any of
// these forms; this is this repo's one shared answer, used by this
// package's own sourceURLFromCatalog and by
// apps/datatugapp/commands/cmd_query_run_saved.go's equivalent for `datatug
// query run --project/--query` (S52 found sourceURLFromCatalog building
// "sqlite://" + catalog.Path with no expansion at all, unopenable against
// demo-project-1's real catalog data — this closes that gap at its root
// instead of working around it at each call site).
func ResolveCatalogPath(projectDir, catalogPath string) (string, error) {
	if catalogPath == "" {
		return "", errors.New("empty catalog path")
	}
	expanded := catalogPath
	switch {
	case strings.HasPrefix(expanded, "~"):
		var err error
		expanded, err = homedir.Expand(expanded)
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", catalogPath, err)
		}
	case strings.HasPrefix(expanded, "${HOME}"), strings.HasPrefix(expanded, "$HOME"):
		home, err := homedir.Dir()
		if err != nil {
			return "", fmt.Errorf("resolve $HOME for %q: %w", catalogPath, err)
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(expanded, "${HOME}"), "$HOME")
		expanded = filepath.Join(home, rest)
	}
	if filepath.IsAbs(expanded) {
		return expanded, nil
	}
	if projectDir == "" {
		return "", fmt.Errorf("relative catalog path %q has no project directory to resolve against", catalogPath)
	}
	return filepath.Join(projectDir, expanded), nil
}
