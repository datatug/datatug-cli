package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// This file is what a second scan does to the files of the first: it merges the
// state of one environment into the columns file of a table that other environments
// of the model have scanned, and takes back what an environment scanned earlier of a
// table that its database no longer has. The layout is written down in "Project
// layout written by a scan" of spec/features/cli/scan/README.md, and the rules of a
// rescan in its REQ: idempotent-rescan, REQ: rescan-removes-dropped-tables and REQ:
// rescan-keeps-other-environments.

// existsInEnvironment is the state of a column, a table or a view in an environment
// whose scan found it.
func existsInEnvironment() *datatug.EnvState { return &datatug.EnvState{Status: "exists"} }

// parseColumnsFile is the columns of the columns file whose content is data, and an
// error when it is not a columns file.
func parseColumnsFile(data []byte) (datatug.ColumnModels, error) {
	var file filestore.TableModelColumnsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	return file.Columns, nil
}

// mergeColumns is the columns of a table or view after a scan of environment found
// scanned, given the columns its file held before (previous).
//
// Each scanned column is there, in the order the scan found them, and lists every
// environment that has it: environment, and the ones that the column listed before.
// A column of previous that the scan did not find is kept when another environment has
// it, listing those environments only, after the scanned columns; it is gone when
// environment was the only one that had it. A column that no environment is listed for
// (none of the scans that wrote the file listed one) is not this scan's to remove, and
// stays. What a column holds besides its state by environment, its checks, is kept.
func mergeColumns(previous datatug.ColumnModels, scanned []*datatug.ColumnInfo, environment string) datatug.ColumnModels {
	before := make(map[string]*datatug.ColumnModel, len(previous))
	for _, column := range previous {
		before[column.Name] = column
	}
	merged := make(datatug.ColumnModels, 0, len(scanned)+len(previous))
	found := make(map[string]bool, len(scanned))
	for _, column := range scanned {
		model := &datatug.ColumnModel{ColumnInfo: *column, ByEnv: datatug.StateByEnv{}}
		if earlier := before[column.Name]; earlier != nil {
			model.Checks = earlier.Checks
			for env, state := range earlier.ByEnv {
				model.ByEnv[env] = state
			}
		}
		model.ByEnv[environment] = existsInEnvironment()
		merged = append(merged, model)
		found[column.Name] = true
	}
	for _, column := range previous {
		if found[column.Name] {
			continue
		}
		if kept, ok := withoutEnvironment(column, environment); ok {
			merged = append(merged, kept)
		}
	}
	return merged
}

// withoutEnvironment is column as it is when the scan of environment no longer finds
// it, and whether it stays: it stays when another environment has it, or none is
// listed for it.
func withoutEnvironment(column *datatug.ColumnModel, environment string) (*datatug.ColumnModel, bool) {
	if _, listed := column.ByEnv[environment]; !listed {
		return column, true
	}
	if len(column.ByEnv) == 1 {
		return nil, false
	}
	kept := *column
	kept.ByEnv = make(datatug.StateByEnv, len(column.ByEnv)-1)
	for env, state := range column.ByEnv {
		if env != environment {
			kept.ByEnv[env] = state
		}
	}
	return &kept, true
}

// retraction is what a scan takes back of a table or view that its database no
// longer has: the folder of it is removed, or its columns file is written again
// without the environment of the scan, because another environment has it.
type retraction struct {
	dir     string // the folder of the table or view, which is removed when content is nil
	file    string // its columns file
	content []byte // what the file holds after, or nil when the folder is removed
}

// planRetractions finds what the scan of environment takes back, and changes nothing.
// It looks at the folder of every table and view that model has on disk, in any
// schema, that the scan does not write (layout), and:
//
//   - leaves it, saying nothing, when it is not a folder, or no column of its file
//     lists environment: what the scan did not write is not the scan's to remove;
//   - leaves it, naming it, when it holds anything but one columns file, or the file
//     cannot be read, or the model is also fed by another catalog in environment
//     (otherCatalogs), whose tables this scan does not know;
//   - refuses, with an error, when the folder, or any folder above it from the
//     dbmodels folder down, is a symbolic link: a folder is never removed through one;
//   - otherwise takes it back: its file without environment, or the folder when no
//     environment is left.
func planRetractions(projectDir, model, environment string, layout scannedLayout, otherCatalogs []string, warnings io.Writer) ([]retraction, error) {
	modelDir := filepath.Join(projectDir, storage.DbModelsFolder, model)
	schemas, err := os.ReadDir(modelDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // nothing was scanned into this model yet
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list the schemas of database model %q: %w", model, err)
	}
	var retractions []retraction
	for _, schemaEntry := range schemas {
		schema := schemaEntry.Name()
		if !isFolder(filepath.Join(modelDir, schema)) {
			continue
		}
		for _, kind := range []string{"tables", "views"} {
			kindDir := filepath.Join(modelDir, schema, kind)
			entries, err := os.ReadDir(kindDir)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("failed to list the %s of schema %q of database model %q: %w", kind, schema, model, err)
			}
			for _, entry := range entries {
				name := entry.Name()
				dir := filepath.Join(kindDir, name)
				if layout.has(schema, kind, name) || !isFolder(dir) {
					continue
				}
				what := fmt.Sprintf("%s %q of schema %q", relationKind(kind), name, schema)
				if err = refuseSymbolicLinks(projectDir, what, storage.DbModelsFolder, model, schema, kind, name); err != nil {
					return nil, err
				}
				retracted, err := planRetraction(dir, what, model, environment, otherCatalogs, warnings)
				if err != nil {
					return nil, err
				}
				if retracted != nil {
					retractions = append(retractions, *retracted)
				}
			}
		}
	}
	return retractions, nil
}

// planRetraction is planRetractions for the one folder dir of what (a table or view,
// said as in a message), or nil when the scan leaves it.
func planRetraction(dir, what, model, environment string, otherCatalogs []string, warnings io.Writer) (*retraction, error) {
	entries, err := scanReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list the folder of %s: %w", what, err)
	}
	leaves := func(reason string) (*retraction, error) {
		_, _ = fmt.Fprintf(warnings, "warning: %s is no longer in the database, and its folder stays: %s\n", what, reason)
		return nil, nil
	}
	var columnsFiles []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), "."+storage.ColumnsFileSuffix+".json") {
			return leaves(fmt.Sprintf("it holds %q, which a scan did not write", entry.Name()))
		}
		columnsFiles = append(columnsFiles, filepath.Join(dir, entry.Name()))
	}
	if len(columnsFiles) != 1 {
		return leaves(fmt.Sprintf("it holds %d columns files, and a table or view has one", len(columnsFiles)))
	}
	file := columnsFiles[0]
	data, err := os.ReadFile(file)
	var previous datatug.ColumnModels
	if err == nil {
		previous, err = parseColumnsFile(data)
	}
	if err != nil {
		return leaves("its columns file cannot be read")
	}
	if !anyColumnListsEnvironment(previous, environment) {
		return nil, nil
	}
	if len(otherCatalogs) > 0 {
		return leaves(fmt.Sprintf("database model %q is also fed by catalog %s in environment %q, and this scan does not know what that database has", model, strings.Join(quoted(otherCatalogs), ", "), environment))
	}
	remaining := mergeColumns(previous, nil, environment)
	if len(remaining) == 0 {
		return &retraction{dir: dir, file: file}, nil
	}
	// A column that is left lists the environments that have it: the file is the one
	// mergeColumns writes, as the scan of an environment that has the table would.
	content, _ := json.MarshalIndent(filestore.TableModelColumnsFile{Columns: remaining}, "", "\t")
	return &retraction{dir: dir, file: file, content: append(content, '\n')}, nil
}

// anyColumnListsEnvironment is whether the state of any column in columns lists environment.
func anyColumnListsEnvironment(columns datatug.ColumnModels, environment string) bool {
	for _, column := range columns {
		if _, listed := column.ByEnv[environment]; listed {
			return true
		}
	}
	return false
}

// quoted is each of names in double quotes, as %q writes them.
func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = fmt.Sprintf("%q", name)
	}
	return out
}

// isFolder is whether path is a folder, or a link to one.
func isFolder(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// refuseSymbolicLinks is an error when any of parts, taken one below the other from
// projectDir down (projectDir itself may be, or be reached through, a link: it is the
// project), is a symbolic link or cannot be inspected: what is removed there is not
// known to be in the project.
func refuseSymbolicLinks(projectDir, what string, parts ...string) error {
	path := projectDir
	for _, part := range parts {
		path = filepath.Join(path, part)
		if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is no longer in the database, but its folder is not removed: %s is a symbolic link, or cannot be inspected, and a scan never removes a folder through a link; remove the folder yourself if it is to go, and scan again", what, path)
		}
	}
	return nil
}

// applyRetractions does what planRetractions found.
func applyRetractions(retractions []retraction) error {
	for _, r := range retractions {
		if r.content == nil {
			if err := scanRemoveAll(r.dir); err != nil {
				return fmt.Errorf("failed to remove the folder %s of a table or view that the database no longer has: %w", r.dir, err)
			}
			continue
		}
		if err := scanWriteFile(r.file, r.content, 0o644); err != nil {
			return fmt.Errorf("failed to update the columns file %s of a table or view that the database no longer has: %w", r.file, err)
		}
	}
	return nil
}
