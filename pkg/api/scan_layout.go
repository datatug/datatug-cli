package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// This file writes what a scan found into a project. The files, their folders and
// their fields are written down once, in the "Project layout written by a scan"
// section of spec/features/cli/scan/README.md; this is the only writer, and the
// readers are GetCatalogTables and GetCatalogSchema (catalog_tables_api.go) and
// resolveSourceURL (source_resolver.go). Foreign keys are not stored.

// ScannedCatalog names a catalog a scan has just read, which is where its files go
// in the project.
type ScannedCatalog struct {
	Driver      string // the driver of the scan, such as "sqlite3"
	Environment string // the environment the scan was run for
	ID          string // the catalog (database) id, as given to --db
}

// SaveScannedProject saves project, a project UpdateDbSchema returned, and then
// writes the files of the scanned catalog that datatug-core does not write: the
// catalog file and one columns file for each table and view.
//
// The project is saved without the schemas of its database models: a model file
// is an id and its environments, and the tables are the columns files. A table or
// view, or a schema, whose name cannot be a folder name, or that differs from
// another by case only, is left out of the project and named on warnings; the
// others are still written. Nothing is written when the project is invalid.
func SaveScannedProject(ctx context.Context, store datatug.ProjectStore, projectDir string, project *datatug.Project, scanned ScannedCatalog, warnings io.Writer) error {
	saved := *project
	saved.DbModels = make(datatug.DbModels, len(project.DbModels))
	for i, model := range project.DbModels {
		withoutSchemas := *model
		withoutSchemas.Schemas = nil
		saved.DbModels[i] = &withoutSchemas
	}
	if err := store.SaveProject(ctx, &saved); err != nil {
		return fmt.Errorf("failed to save datatug project [%v]: %w", project.ID, err)
	}

	server, catalog := findScannedCatalog(project, scanned)
	if catalog == nil {
		return fmt.Errorf("failed to save datatug project [%v]: the scan of catalog %q (driver %q) is not in the project", project.ID, scanned.ID, scanned.Driver)
	}

	// The catalog file holds the driver, the path and the model, as the demo's does.
	// Its schemas are the columns files, so the catalog is saved without them.
	stored := *catalog
	stored.Schemas = datatug.DbSchemas{}
	if scanned.Driver == dbconnection.DriverSQLite3 {
		path, err := sqliteCatalogPath(projectDir, catalog.Path)
		if err != nil {
			return fmt.Errorf("failed to record the path of SQLite database %q in the project: %w", scanned.ID, err)
		}
		stored.Path = path
	}
	if err := store.SaveEnvDbCatalog(ctx, scanned.Environment, server.ID, catalog.ID, &stored); err != nil {
		return fmt.Errorf("failed to save the catalog file of %q: %w", scanned.ID, err)
	}

	for _, schema := range catalog.Schemas {
		if problem := folderNameProblem(schema.ID); problem != "" {
			_, _ = fmt.Fprintf(warnings, "warning: schema %q is left out of the project: %s\n", schema.ID, problem)
			continue
		}
		relations := []struct {
			folder string
			tables []*datatug.CollectionInfo
		}{{"tables", schema.Tables}, {"views", schema.Views}}
		for _, relation := range relations {
			for _, table := range folderSafeTables(schema.ID, relation.folder, relation.tables, warnings) {
				if err := writeScannedColumnsFile(projectDir, catalog.DbModel, schema.ID, relation.folder, scanned.Environment, table); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// findScannedCatalog is the catalog of project that scanned names, with the
// server that holds it, or nil.
func findScannedCatalog(project *datatug.Project, scanned ScannedCatalog) (*datatug.ProjDbServer, *datatug.DbCatalog) {
	driver := project.DbDrivers.GetByID(scanned.Driver)
	if driver == nil {
		return nil, nil
	}
	for _, server := range driver.Servers {
		if catalog := server.Catalogs.GetByID(scanned.ID); catalog != nil {
			return server, catalog
		}
	}
	return nil, nil
}

// folderSafeTables is the tables that can be written as folders of one schema's
// tables or views folder, in name order. Each table that cannot is named on
// warnings, with the reason, and left out: a name that cannot be a folder name,
// and any name that differs by case only from one that is kept (the first in name
// order stays), as two such folders are one on a case-insensitive file system.
func folderSafeTables(schema, folder string, tables []*datatug.CollectionInfo, warnings io.Writer) []*datatug.CollectionInfo {
	sorted := append([]*datatug.CollectionInfo(nil), tables...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })
	var kept []*datatug.CollectionInfo
	keptByFoldedName := map[string]string{}
	for _, table := range sorted {
		name := table.Name()
		if problem := folderNameProblem(name); problem != "" {
			_, _ = fmt.Fprintf(warnings, "warning: %s %q of schema %q is left out of the project: %s\n", relationKind(folder), name, schema, problem)
			continue
		}
		if first, ok := keptByFoldedName[strings.ToLower(name)]; ok {
			_, _ = fmt.Fprintf(warnings, "warning: %s %q of schema %q is left out of the project: its name differs only by case from %q, which is kept, and the two would be one folder on a case-insensitive file system\n", relationKind(folder), name, schema, first)
			continue
		}
		keptByFoldedName[strings.ToLower(name)] = name
		kept = append(kept, table)
	}
	return kept
}

// relationKind is "table" for the tables folder and "view" for the views folder,
// for the words of a message.
func relationKind(folder string) string { return strings.TrimSuffix(folder, "s") }

// maxFolderNameBytes keeps a folder name, and the "<schema>.<name>.columns.json"
// file inside it, within the 255 bytes most file systems allow for a name.
const maxFolderNameBytes = 200

// windowsDeviceNames are names Windows reserves, with or without an extension: a
// folder cannot have one, and a project is pushed and opened on any system.
var windowsDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// folderNameProblem is why name cannot be the name of a folder on every system a
// project is opened on, or "" when it can.
func folderNameProblem(name string) string {
	switch {
	case name == "":
		return "its name is empty"
	case name == "." || name == "..":
		return fmt.Sprintf("%q is not a folder name", name)
	case len(name) > maxFolderNameBytes:
		return fmt.Sprintf("its name is longer than %d bytes", maxFolderNameBytes)
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return "its name ends with a dot or a space, which Windows removes from a folder name"
	case windowsDeviceNames[strings.ToUpper(strings.SplitN(name, ".", 2)[0])]:
		return "its name is one Windows reserves for a device"
	}
	if i := strings.IndexFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) }); i >= 0 {
		return fmt.Sprintf("its name has the character %q, which a folder name cannot have on every system", name[i:i+1])
	}
	return ""
}

// writeScannedColumnsFile writes the columns file of one table or view, in the folder the
// readers look in: dbmodels/<model>/<schema>/<tables|views>/<name>/<schema>.<name>.columns.json.
// It is datatug-core's own file type for it, with the state of each column in the
// environment of the scan, as the demo's files have. The columns are valid: the
// project that holds them was validated by SaveProject.
func writeScannedColumnsFile(projectDir, model, schema, folder, environment string, table *datatug.CollectionInfo) error {
	columns := make(datatug.ColumnModels, len(table.Columns))
	for i, column := range table.Columns {
		columns[i] = &datatug.ColumnModel{
			ColumnInfo: *column,
			ByEnv:      datatug.StateByEnv{environment: &datatug.EnvState{Status: "exists"}},
		}
	}
	file := filestore.TableModelColumnsFile{Columns: columns}
	name := table.Name()
	dir := filepath.Join(projectDir, storage.DbModelsFolder, model, schema, folder, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create the folder of %s %q of schema %q: %w", relationKind(folder), name, schema, err)
	}
	// Columns, their state by environment and their plain fields cannot fail to marshal.
	data, _ := json.MarshalIndent(file, "", "\t")
	path := filepath.Join(dir, storage.JsonFileName(schema+"."+name, storage.ColumnsFileSuffix))
	content := append(data, '\n')
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("failed to write the columns file of %s %q of schema %q: %w", relationKind(folder), name, schema, err)
	}
	return nil
}

// sqliteCatalogPath is the path a catalog file records for the SQLite file at
// dbPath, so that the project finds the file again from wherever it is opened
// (ResolveCatalogPath is the reader): relative to the project when the file is
// inside it, relative to the home directory with a leading "~/" when it is under
// that, and absolute otherwise. A relative dbPath is the one the scan was given,
// and means the file from the working directory.
func sqliteCatalogPath(projectDir, dbPath string) (string, error) {
	absolute, err := filepathAbs(dbPath)
	if err != nil {
		return "", err
	}
	projectAbsolute, err := filepathAbs(projectDir)
	if err != nil {
		return "", err
	}
	if rel, ok := relativeInside(projectAbsolute, absolute); ok {
		// A first segment that starts like a home directory would be read as one.
		if strings.HasPrefix(rel, "~") || strings.HasPrefix(rel, "$HOME") {
			rel = "./" + rel
		}
		return rel, nil
	}
	if home, homeErr := homedirDir(); homeErr == nil {
		if rel, ok := relativeInside(home, absolute); ok {
			return "~/" + rel, nil
		}
	}
	return absolute, nil
}

// relativeInside is the slash-separated path of target below base, and whether
// target is below base at all.
func relativeInside(base, target string) (string, bool) {
	rel, err := filepathRel(base, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
