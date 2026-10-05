package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

	// Server is the server the scan read, as ScannedServer says it. It tells which
	// server holds the catalog when the project has more than one of the driver, such as
	// two SQL Server hosts that each have a database of this id; it is not needed
	// otherwise.
	Server datatug.ServerRef
}

// SaveScannedProject saves project, a project UpdateDbSchema returned, and then
// writes the files of the scanned catalog that datatug-core does not write: the
// catalog file and one columns file for each table and view.
//
// The project is saved without the schemas of its database models: a model file
// is an id and its environments, and the tables are the columns files. A schema, or
// a table or view, whose name cannot be a folder name, or that differs from another
// of its kind in the same folder by case only, or whose columns file would have a
// name longer than a file can have, is left out of the project and named on
// warnings; the others are still written. Nothing is written when the project is
// invalid.
//
// A scan of an environment keeps what the scans of the other environments of the same
// database model wrote (see mergeColumns), and takes back what its own earlier scans
// wrote of a table or view that the database no longer has (see planRetractions): the
// folder of one that no environment has is removed, and said on warnings, one line
// each. The first scan of a catalog in an environment (the project holds no catalog file
// of it there) has no earlier scan of its own, so it takes nothing back: what is in the
// model for that environment was written by something else. Nothing is written when a
// folder that is to be removed is, or is inside, a link.
//
// The environment, the catalog and the database model of the scan are names of folders
// of the project, and one that is not a plain name (see CheckScanName), or that differs
// only by case from one the project has (see CheckScanNamesAgainstProject), is refused
// before anything is read, written or removed: this function removes folders, and it
// does not take its ids on trust from whoever calls it. A catalog that the project
// records under another driver in the same environment is refused the same way (see
// CheckScanDriverAgainstProject).
func SaveScannedProject(ctx context.Context, store datatug.ProjectStore, projectDir string, project *datatug.Project, scanned ScannedCatalog, warnings io.Writer) error {
	server, catalog := findScannedCatalog(project, scanned)
	names := []struct{ flag, value string }{{"--env", scanned.Environment}, {"--db", scanned.ID}}
	model := ""
	if catalog != nil {
		model = catalog.DbModel
		names = append(names, struct{ flag, value string }{"--dbmodel", model})
	}
	for _, name := range names {
		if err := CheckScanName(name.flag, name.value); err != nil {
			return err
		}
	}
	if err := CheckScanNamesAgainstProject(projectDir, scanned.Environment, scanned.ID, model); err != nil {
		return err
	}
	if err := CheckScanDriverAgainstProject(projectDir, scanned.Environment, scanned.ID, scanned.Driver); err != nil {
		return err
	}
	var layout scannedLayout
	var retractions []retraction
	if catalog != nil {
		var err error
		layout = layoutOfCatalog(catalog, warnings)
		if slices.Contains(catalogIDsOf(projectDir, scanned.Environment), catalog.ID) {
			retractions, err = planRetractions(projectDir, catalog.DbModel, scanned.Environment, layout, otherCatalogsOfModel(project, catalog.DbModel, scanned.Environment, catalog.ID), warnings)
			if err != nil {
				return fmt.Errorf("failed to save datatug project [%v]: %w", project.ID, err)
			}
		}
	}

	saved := *project
	saved.DbModels = make(datatug.DbModels, len(project.DbModels))
	for i, model := range project.DbModels {
		withoutSchemas := *model
		withoutSchemas.Schemas = nil
		saved.DbModels[i] = &withoutSchemas
	}
	if err := saveKeepingReadme(ctx, store, projectDir, &saved); err != nil {
		return fmt.Errorf("failed to save datatug project [%v]: %w", project.ID, err)
	}
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

	// What the database no longer has goes before what it has is written: on a file
	// system that does not tell a folder named Customer from one named customer, the
	// folder of a renamed table is the folder of the new name.
	if err := applyRetractions(retractions, warnings); err != nil {
		return err
	}
	for _, folder := range layout.folders {
		for _, table := range folder.tables {
			if err := writeScannedColumnsFile(projectDir, catalog.DbModel, folder.schema, folder.kind, scanned.Environment, table); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveKeepingReadme saves project, and leaves the README.md of the project folder
// as it was if there was one. datatug-core writes a README.md of generated text on
// every save and replaces what is there, which `datatug init` is entitled to do to
// a folder it makes but a scan is not: it runs in a folder that holds a repository's
// README, and again after the README of the project was edited. The README is read
// before the save and put back after it, whether the save worked or not.
func saveKeepingReadme(ctx context.Context, store datatug.ProjectStore, projectDir string, project *datatug.Project) error {
	path := filepath.Join(projectDir, "README.md")
	original, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return fmt.Errorf("failed to read the README.md of the project, which a scan keeps: %w", readErr)
	}
	saveErr := store.SaveProject(ctx, project)
	if readErr != nil {
		return saveErr // there was no README: the generated one stays
	}
	var putBackErr error
	if err := readmeWriteFile(path, original, 0o644); err != nil { // the file exists, so its permissions stay
		putBackErr = fmt.Errorf("failed to put back the README.md of the project: %w", err)
	}
	return errors.Join(saveErr, putBackErr)
}

// findScannedCatalog is the catalog of project that scanned names, with the
// server that holds it, or nil. When the project has more than one server of the
// driver it is the one the scan read (scanned.Server): the catalog of another server
// that has a database of the same id is not the one the scan found.
func findScannedCatalog(project *datatug.Project, scanned ScannedCatalog) (*datatug.ProjDbServer, *datatug.DbCatalog) {
	driver := project.DbDrivers.GetByID(scanned.Driver)
	if driver == nil {
		return nil, nil
	}
	servers := driver.Servers
	if len(servers) > 1 {
		ref := scanned.Server
		ref.Driver = scanned.Driver
		servers = datatug.ProjDbServers{}
		if server := driver.Servers.GetProjDbServer(ref); server != nil {
			servers = datatug.ProjDbServers{server}
		}
	}
	for _, server := range servers {
		if catalog := server.Catalogs.GetByID(scanned.ID); catalog != nil {
			return server, catalog
		}
	}
	return nil, nil
}

// foldersThatFit is the items that can be written as folders of one parent folder,
// in name order. Each item that cannot is handed to leftOut, with the reason, and
// left out: one whose name has a problem (problem is "" when it has none), and any
// whose name differs by case only from one that is kept (the first in name order
// stays), as two such folders are one on a case-insensitive file system.
func foldersThatFit[T any](items []T, name func(T) string, problem func(name string) string, leftOut func(item T, reason string)) []T {
	sorted := append([]T(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return name(sorted[i]) < name(sorted[j]) })
	var kept []T
	keptByFoldedName := map[string]string{}
	for _, item := range sorted {
		itemName := name(item)
		if reason := problem(itemName); reason != "" {
			leftOut(item, reason)
			continue
		}
		if first, ok := keptByFoldedName[strings.ToLower(itemName)]; ok {
			leftOut(item, fmt.Sprintf("its name differs only by case from %q, which is kept, and the two would be one folder on a case-insensitive file system", first))
			continue
		}
		keptByFoldedName[strings.ToLower(itemName)] = itemName
		kept = append(kept, item)
	}
	return kept
}

// scannedFolder is the tables, or the views, of one schema that a scan writes: the
// ones that fit as folders.
type scannedFolder struct {
	schema string
	kind   string // "tables" or "views", the name of the folder
	tables []*datatug.CollectionInfo
}

// folderKey names the folder of one table or view in a model: <schema>/<kind>/<name>.
type folderKey struct{ schema, kind, name string }

// scannedLayout is what a scan writes of a catalog, in the order it writes it.
type scannedLayout struct {
	folders []scannedFolder
	written map[folderKey]bool
}

// has is whether the scan writes the folder of the table or view name, of kind
// "tables" or "views", in schema.
func (l scannedLayout) has(schema, kind, name string) bool {
	return l.written[folderKey{schema, kind, name}]
}

// layoutOfCatalog is the schemas, tables and views of catalog that can be written,
// in name order. Each one that cannot is named on warnings, with the reason.
func layoutOfCatalog(catalog *datatug.DbCatalog, warnings io.Writer) scannedLayout {
	layout := scannedLayout{written: map[folderKey]bool{}}
	schemas := foldersThatFit(catalog.Schemas,
		func(schema *datatug.DbSchema) string { return schema.ID },
		folderNameProblem,
		func(schema *datatug.DbSchema, reason string) {
			_, _ = fmt.Fprintf(warnings, "warning: schema %q is left out of the project: %s\n", schema.ID, reason)
		})
	for _, schema := range schemas {
		relations := []struct {
			folder string
			tables []*datatug.CollectionInfo
		}{{"tables", schema.Tables}, {"views", schema.Views}}
		for _, relation := range relations {
			fitting := tablesThatFit(schema.ID, relation.folder, relation.tables, warnings)
			layout.folders = append(layout.folders, scannedFolder{schema: schema.ID, kind: relation.folder, tables: fitting})
			for _, table := range fitting {
				layout.written[folderKey{schema.ID, relation.folder, table.Name()}] = true
			}
		}
	}
	return layout
}

// otherCatalogsOfModel is the ids of the catalogs, other than catalogID, that the
// database model feeds in environment: a model can map several databases of an
// environment, and then no scan of one of them knows what the others have.
func otherCatalogsOfModel(project *datatug.Project, model, environment, catalogID string) (others []string) {
	dbModel := project.DbModels.GetByID(model)
	if dbModel == nil {
		return nil
	}
	modelEnv := dbModel.Environments.GetByID(environment)
	if modelEnv == nil {
		return nil
	}
	for _, catalog := range modelEnv.DbCatalogs {
		if catalog.ID != catalogID {
			others = append(others, catalog.ID)
		}
	}
	return others
}

// tablesThatFit is the tables (or views) that can be written as folders of one
// schema's tables or views folder, with their columns files: foldersThatFit, and a
// table whose columns file would have too long a name is left out too. Each one left
// out is named on warnings.
func tablesThatFit(schema, folder string, tables []*datatug.CollectionInfo, warnings io.Writer) []*datatug.CollectionInfo {
	return foldersThatFit(tables,
		func(table *datatug.CollectionInfo) string { return table.Name() },
		func(name string) string {
			if problem := folderNameProblem(name); problem != "" {
				return problem
			}
			return columnsFileNameProblem(schema, name)
		},
		func(table *datatug.CollectionInfo, reason string) {
			_, _ = fmt.Fprintf(warnings, "warning: %s %q of schema %q is left out of the project: %s\n", relationKind(folder), table.Name(), schema, reason)
		})
}

// columnsFileNameProblem is why the columns file of a table cannot be written, or
// "" when it can: its name, <schema>.<name>.columns.json, is longer than a file name
// can be on most file systems, which two names that are each a valid folder name can
// make.
func columnsFileNameProblem(schema, name string) string {
	file := storage.JsonFileName(schema+"."+name, storage.ColumnsFileSuffix)
	if len(file) > maxFileNameBytes {
		return fmt.Sprintf("its columns file would be named \"<schema>.<name>.columns.json\", %d bytes, and a file name can have %d", len(file), maxFileNameBytes)
	}
	return ""
}

// relationKind is "table" for the tables folder and "view" for the views folder,
// for the words of a message.
func relationKind(folder string) string { return strings.TrimSuffix(folder, "s") }

// maxFolderNameBytes is the longest name of a schema, table or view that a scan
// writes as a folder: well within the 255 bytes most file systems allow for a name.
// The columns file in a table's folder is named "<schema>.<name>.columns.json", so
// its name can be longer than either, and has its own limit, maxFileNameBytes.
const maxFolderNameBytes = 200

// maxFileNameBytes is the longest name a file has on the file systems a project is
// opened on (ext4, APFS and NTFS allow 255 bytes).
const maxFileNameBytes = 255

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
// environment of the scan, as the demo's files have, and with the state in the other
// environments that an earlier scan wrote (see mergeColumns). A file that is as it
// would be written is not written again. The columns are valid: the project that
// holds them was validated by SaveProject.
func writeScannedColumnsFile(projectDir, model, schema, folder, environment string, table *datatug.CollectionInfo) error {
	name := table.Name()
	dir := filepath.Join(projectDir, storage.DbModelsFolder, model, schema, folder, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create the folder of %s %q of schema %q: %w", relationKind(folder), name, schema, err)
	}
	path := filepath.Join(dir, storage.JsonFileName(schema+"."+name, storage.ColumnsFileSuffix))
	// A file that is not there, or cannot be read or is not a columns file, has no state
	// to keep: what is written in its place says what the scan found.
	existing, _ := os.ReadFile(path)
	previous, _ := parseColumnsFile(existing)
	file := filestore.TableModelColumnsFile{Columns: mergeColumns(previous, table.Columns, environment)}
	// Columns, their state by environment and their plain fields cannot fail to marshal.
	data, _ := json.MarshalIndent(file, "", "\t")
	content := append(data, '\n')
	if bytes.Equal(existing, content) {
		return nil
	}
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
		// A path that starts with "~" or "$" may be read as a home directory
		// ("~", "$HOME", "${HOME}": ResolveCatalogPath expands each), so a file
		// in a first folder named like that is written "./<folder>/...".
		if strings.HasPrefix(rel, "~") || strings.HasPrefix(rel, "$") {
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
