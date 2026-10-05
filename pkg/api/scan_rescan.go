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
	what    string // the table or view, said as in a message
	rel     string // dir, below the project folder, with "/" separators, for the message of its removal
}

// planRetractions finds what the scan of environment takes back, and changes nothing.
// It looks at the folder of every table and view that model has on disk, in any
// schema, that the scan does not write (layout).
//
// A folder is the scan's only when it holds exactly one columns file that lists
// environment: any other is a person's, or another environment's, and is left, saying
// nothing, and it never makes the scan fail, a link or not, and a folder that cannot
// be listed cannot be shown to be the scan's. Of a folder that is the scan's, the scan
//
//   - leaves it, naming it, when it holds anything but that one columns file, or the
//     model is also fed by another catalog in environment (otherCatalogs), whose
//     tables this scan does not know;
//   - refuses, with an error, when it is going to take it back and the folder, or any
//     folder above it from the dbmodels folder down, is a link (see refuseLinks);
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
				if layout.has(schema, kind, name) || !isFolder(filepath.Join(kindDir, name)) {
					continue
				}
				what := fmt.Sprintf("%s %q of schema %q", relationKind(kind), name, schema)
				parts := []string{storage.DbModelsFolder, model, schema, kind, name}
				retracted, err := planRetraction(projectDir, parts, what, model, environment, otherCatalogs, warnings)
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

// planRetraction is planRetractions for the one folder below projectDir that parts
// name, of what (a table or view, said as in a message), or nil when the scan leaves it.
func planRetraction(projectDir string, parts []string, what, model, environment string, otherCatalogs []string, warnings io.Writer) (*retraction, error) {
	dir := filepath.Join(append([]string{projectDir}, parts...)...)
	entries, err := scanReadDir(dir)
	if err != nil {
		return nil, nil // a folder that cannot be listed cannot be shown to be the scan's
	}
	file, previous := columnsFileListing(dir, entries, environment)
	if file == "" {
		return nil, nil // not the scan's: nothing to say of it
	}
	leaves := func(reason string) (*retraction, error) {
		_, _ = fmt.Fprintf(warnings, "warning: %s is no longer in the database, and its folder stays: %s\n", what, reason)
		return nil, nil
	}
	columnsFiles := 0
	for _, entry := range entries {
		if !isColumnsFile(entry) {
			return leaves(fmt.Sprintf("it holds %q, which a scan did not write", entry.Name()))
		}
		columnsFiles++
	}
	if columnsFiles != 1 {
		return leaves(fmt.Sprintf("it holds %d columns files, and a table or view has one", columnsFiles))
	}
	if len(otherCatalogs) > 0 {
		return leaves(fmt.Sprintf("database model %q is also fed by catalog %s in environment %q, and this scan does not know what that database has", model, strings.Join(quoted(otherCatalogs), ", "), environment))
	}
	if err = refuseLinks(projectDir, what, parts...); err != nil {
		return nil, err
	}
	rel := filepath.ToSlash(filepath.Join(parts...))
	remaining := mergeColumns(previous, nil, environment)
	if len(remaining) == 0 {
		return &retraction{dir: dir, file: file, what: what, rel: rel}, nil
	}
	// A column that is left lists the environments that have it: the file is the one
	// mergeColumns writes, as the scan of an environment that has the table would.
	content, _ := json.MarshalIndent(filestore.TableModelColumnsFile{Columns: remaining}, "", "\t")
	return &retraction{dir: dir, file: file, content: append(content, '\n'), what: what, rel: rel}, nil
}

// columnsFileListing is the one columns file among entries (the entries of dir) whose
// columns list environment, and its columns; both are zero when no file, or more than
// one, does. A file that cannot be read, or is not a columns file, lists nothing.
func columnsFileListing(dir string, entries []os.DirEntry, environment string) (string, datatug.ColumnModels) {
	var listing string
	var columns datatug.ColumnModels
	count := 0
	for _, entry := range entries {
		if !isColumnsFile(entry) {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(file)
		var parsed datatug.ColumnModels
		if err == nil {
			parsed, err = parseColumnsFile(data)
		}
		if err != nil || !anyColumnListsEnvironment(parsed, environment) {
			continue
		}
		listing, columns = file, parsed
		count++
	}
	if count != 1 {
		return "", nil
	}
	return listing, columns
}

// isColumnsFile is whether entry is a regular file named as a columns file is.
func isColumnsFile(entry os.DirEntry) bool {
	return entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), "."+storage.ColumnsFileSuffix+".json")
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

// refuseLinks is an error when any of parts, taken one below the other from projectDir
// down (projectDir itself may be, or be reached through, a link: it is the project), is
// not a plain folder as Lstat reports it, which a symbolic link is not, nor a Windows
// junction (Go reports it as irregular, and not as a link), nor any other reparse point,
// or when it cannot be inspected: what is removed or written there is not known to be in
// the project.
func refuseLinks(projectDir, what string, parts ...string) error {
	path := projectDir
	for _, part := range parts {
		path = filepath.Join(path, part)
		if info, err := scanLstat(path); err != nil || !info.IsDir() {
			return fmt.Errorf("%s is no longer in the database, but its folder is not removed: %s is a link (a symbolic link or a junction), or is not a plain folder, or cannot be inspected, and a scan never removes a folder through a link; remove the folder yourself if it is to go, and scan again", what, path)
		}
	}
	return nil
}

// applyRetractions does what planRetractions found, and says on removed the folder
// of each table or view it removes, one line each.
func applyRetractions(retractions []retraction, removed io.Writer) error {
	for _, r := range retractions {
		if r.content == nil {
			if err := scanRemoveAll(r.dir); err != nil {
				return fmt.Errorf("failed to remove the folder %s of a table or view that the database no longer has: %w", r.dir, err)
			}
			_, _ = fmt.Fprintf(removed, "removed: %s: %s is no longer in the database\n", r.rel, r.what)
			continue
		}
		if err := scanWriteFile(r.file, r.content, 0o644); err != nil {
			return fmt.Errorf("failed to update the columns file %s of a table or view that the database no longer has: %w", r.file, err)
		}
	}
	return nil
}
