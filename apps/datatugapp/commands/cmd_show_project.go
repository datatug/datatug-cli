package commands

// specscore: feature/cli/show

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	showFormatText = "text"
	showFormatJSON = "json"
)

func showCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Lists what a project holds: its environments, sources, schemas, tables and columns",
		Long: "Lists what a scan wrote into a project: the project's ID; each environment; each source with its driver " +
			"(a PostgreSQL source with the name of the environment variable that holds its URL, never the URL); " +
			"each schema; each table and view with its columns, their types and their place in the primary key.\n\n" +
			"The project is the folder of --directory, or the registered project of --project, or else the current folder. " +
			"The text is stable from one run to the next, so two runs can be compared; --format json prints the same as one JSON document.",
		Args: cobra.NoArgs,
		RunE: showCommandAction,
	}
	flags := cmd.Flags()
	flags.StringP("project", "p", "", "Registered project id/name")
	flags.StringP("directory", "d", "", "Path to the project directory (alternative to --project; --dir is the same flag)")
	flags.String("format", showFormatText, "Output format: "+showFormatText+" or "+showFormatJSON)
	flags.SetNormalizeFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "dir" {
			name = "directory"
		}
		return pflag.NormalizedName(name)
	})
	return cmd
}

// showProjectCommand defines parameters for the show project command.
type showProjectCommand struct {
	projectBaseCommand
}

func showCommandAction(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	v := &showProjectCommand{}
	v.ProjectName, _ = flags.GetString("project")
	v.ProjectDir, _ = flags.GetString("directory")
	format, _ := flags.GetString("format")
	if format != showFormatText && format != showFormatJSON {
		return Exit(fmt.Sprintf("unsupported --format %q: want %s or %s", format, showFormatText, showFormatJSON), exitCodeUsage)
	}
	if v.ProjectName != "" && v.ProjectDir != "" {
		return Exit("--project and --directory cannot be used together: give the registered name or the folder", exitCodeUsage)
	}
	if v.ProjectName == "" && v.ProjectDir == "" {
		v.ProjectDir = "." // the folder it runs in
	}
	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		// A person who has registered no project has no settings file: no project of that name either.
		if errors.Is(err, ErrUnknownProjectName) || (v.ProjectName != "" && errors.Is(err, fs.ErrNotExist)) {
			return Exit(fmt.Sprintf("unknown project %q: it is not in the list of `datatug projects`", v.ProjectName), exitCodeNotFound)
		}
		return err
	}
	if strings.Contains(v.ProjectDir, "://") {
		return Exit(fmt.Sprintf("%q is an address, not a folder on this machine: show reads a project from a folder", v.ProjectDir), exitCodeUsage)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// A folder with no project file is not a project (and a project that lacks one of its other files is a
	// project that cannot be loaded, which is another failure). A path that is a file, not a folder, has no
	// project file in it either.
	_, err := os.Stat(filepath.Join(v.ProjectDir, storage.ProjectSummaryFileName))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return Exit(notAProjectSentence(v.ProjectDir), exitCodeNotFound)
	}
	projectStore := v.store.GetProjectStore(v.projectID)
	project, err := projectStore.LoadProject(ctx)
	if err != nil {
		return fmt.Errorf("failed to load project from [%v]: %w", v.ProjectDir, err)
	}
	doc, err := readShowDocument(ctx, projectStore, project, v.ProjectDir)
	if err != nil {
		return err
	}
	if format == showFormatJSON {
		return writeShowJSON(cmd.OutOrStdout(), doc)
	}
	return writeShowText(cmd.OutOrStdout(), doc)
}

// notAProjectSentence is the one sentence of a folder that holds no project: it names the folder and the command
// that makes a project. A folder with a project file one folder deeper, in the folder datatug (where the create
// screen of the terminal UI wrote it until issue 263 was fixed), is told so, and what to do about it as a whole:
// that file holds an ID and a title and nothing else, so moving it up lets `show` list the project, and a scan
// into it needs the two fields that a project file is not valid without (its access and the time it was made);
// the other way out is a new project.
func notAProjectSentence(folder string) string {
	const makeProject = `datatug scan -d "%s" -D sqlite3 --path <database file> --db <name> --env <environment>`
	if _, err := os.Stat(filepath.Join(folder, "datatug", storage.ProjectSummaryFileName)); err == nil {
		return fmt.Sprintf(`"%s" is not a DataTug project: its project file is in the folder datatug, where an earlier version of the terminal UI wrote it; `+
			`to keep this project, move datatug/%s up into "%s" and add to it "access": "private" and "created": {"at": "<a time, such as 2026-01-02T15:04:05Z>"}, which a scan needs; `+
			`or make a new project in another folder with `+makeProject,
			folder, storage.ProjectSummaryFileName, folder, "<new folder>")
	}
	return fmt.Sprintf(`"%s" is not a DataTug project: make one with `+makeProject, folder, folder)
}

// exitCodeNotFound is the exit code of a project, a dataset, a file or a database that is not there (the
// shared contract of spec/features/cli/README.md).
const exitCodeNotFound = 3

// showDocument is what `datatug show` prints: the project as a scan left it, in the order it is printed.
type showDocument struct {
	Project      string            `json:"project"`
	Environments []showEnvironment `json:"environments"`
}

type showEnvironment struct {
	ID      string       `json:"id"`
	Sources []showSource `json:"sources"`
}

// showSource is one database of an environment. DSNEnv is, for a PostgreSQL source, the name of the
// environment variable that holds its URL: the project holds nothing else of the connection.
type showSource struct {
	ID     string `json:"id"`
	Driver string `json:"driver"`
	DSNEnv string `json:"dsnEnv,omitempty"`
	// NotScanned is true for a source the project records and no scan has described (its catalog has no
	// dbModel): it has no schemas to list.
	NotScanned bool `json:"notScanned,omitempty"`
	// Empty is true for a source that was scanned and has no table and no view to list: a database with none,
	// or a catalog whose model has no files (its dbmodels folder was removed). It is not "notScanned": a scan
	// did describe the source, and what it described is nothing.
	Empty   bool         `json:"empty,omitempty"`
	Schemas []showSchema `json:"schemas"`
}

type showSchema struct {
	Name   string         `json:"name"`
	Tables []showRelation `json:"tables"`
	Views  []showRelation `json:"views"`
}

type showRelation struct {
	Name    string       `json:"name"`
	Columns []showColumn `json:"columns"`
}

// showColumn is a column as the scan stored it. PrimaryKeyPosition is its 1-based place in the primary
// key of its table, and 0 when it is not part of the key.
type showColumn struct {
	Name               string `json:"name"`
	Type               string `json:"type,omitempty"`
	PrimaryKeyPosition int    `json:"primaryKeyPosition,omitempty"`
}

// showUnknownDriver is what is printed in place of a driver that is not a plain name: the catalog file is
// read from a project that may have come from anywhere, and a driver there can hold anything (a URL with a
// password, a line break).
const showUnknownDriver = "unknown driver"

// absPath is the absolute path of a folder, a seam for the failure of the working directory.
var absPath = filepath.Abs

// showProjectID is the ID the first line shows. A project file with no ID leaves the ID the command was
// given (the single-project placeholder "."), and the folder's own name is the better answer then.
func showProjectID(project *datatug.Project, projectDir string) string {
	if project.ID != "" && project.ID != storage.SingleProjectID {
		return project.ID
	}
	abs, err := absPath(projectDir)
	if err != nil {
		abs = projectDir
	}
	return filepath.Base(abs)
}

// readShowDocument reads the project into the document. The sources of an environment are its catalogs,
// listed as chat lists them (the store's LoadEnvDbCatalogs, which lists the catalogs folder of the
// environment: a catalog the environment file does not list is a source, and a catalog the file lists that
// has no folder is not one, see pkg/api/resolver.go catalogSources). The tables and the columns of a
// source come from api.GetCatalogSchema, the reader that chat and serve use for the same files, so that
// what is listed here cannot differ from what they find. A source whose catalog has no dbModel was never
// scanned and is listed as such. Environments and sources are in the order of their IDs.
func readShowDocument(ctx context.Context, store datatug.ProjectStore, project *datatug.Project, projectDir string) (*showDocument, error) {
	doc := &showDocument{Project: showProjectID(project, projectDir), Environments: []showEnvironment{}}
	for _, env := range project.Environments {
		shown := showEnvironment{ID: env.ID, Sources: []showSource{}}
		catalogs, err := store.LoadEnvDbCatalogs(ctx, env.ID)
		if err != nil {
			// The store's text quotes a path of the project: the answer names the environment only.
			return nil, fmt.Errorf("environment %q: its catalogs cannot be read", dbcopy.SourceIDDisplay(env.ID))
		}
		for _, catalog := range catalogs {
			if catalog == nil {
				continue
			}
			source, err := readShowSource(projectDir, env.ID, catalog)
			if err != nil {
				return nil, err
			}
			shown.Sources = append(shown.Sources, source)
		}
		sort.Slice(shown.Sources, func(i, j int) bool { return shown.Sources[i].ID < shown.Sources[j].ID })
		doc.Environments = append(doc.Environments, shown)
	}
	sort.Slice(doc.Environments, func(i, j int) bool { return doc.Environments[i].ID < doc.Environments[j].ID })
	return doc, nil
}

func readShowSource(projectDir, envID string, catalog *datatug.DbCatalog) (showSource, error) {
	driver := showUnknownDriver
	if dbcopy.IsPlainSourceID(catalog.Driver) {
		driver = catalog.Driver
	}
	source := showSource{ID: catalog.ID, Driver: driver, Schemas: []showSchema{}}
	if catalog.Driver == api.DriverPostgres {
		var err error
		if source.DSNEnv, err = showDescriptorVariable(projectDir, catalog.Path); err != nil {
			return showSource{}, fmt.Errorf("source %q in environment %q: %s", dbcopy.SourceIDDisplay(catalog.ID), dbcopy.SourceIDDisplay(envID), err.Error())
		}
	}
	if catalog.DbModel == "" {
		source.NotScanned = true
		return source, nil
	}
	schema, err := api.GetCatalogSchema(projectDir, envID, catalog.ID)
	if err != nil {
		return showSource{}, err
	}
	source.Empty = len(schema.Relations) == 0
	// The relations are in the order of their schema and name: the relations of one schema are together.
	for _, relation := range schema.Relations {
		if len(source.Schemas) == 0 || source.Schemas[len(source.Schemas)-1].Name != relation.Schema {
			source.Schemas = append(source.Schemas, showSchema{Name: relation.Schema, Tables: []showRelation{}, Views: []showRelation{}})
		}
		shown := showRelation{Name: relation.Name, Columns: make([]showColumn, len(relation.Columns))}
		for i, column := range relation.Columns {
			shown.Columns[i] = showColumn{Name: column.Name, Type: column.DbType, PrimaryKeyPosition: column.PrimaryKeyPosition}
		}
		current := &source.Schemas[len(source.Schemas)-1]
		if relation.DbType == "VIEW" {
			current.Views = append(current.Views, shown)
		} else {
			current.Tables = append(current.Tables, shown)
		}
	}
	return source, nil
}

// showDescriptorVariable is the name of the environment variable that the connection descriptor of a
// PostgreSQL catalog names. The descriptor is read as the readers of the source read it (it must be a
// file inside the project that names a variable a project may name), and the variable need not be set:
// listing a project does not connect. The answer for a descriptor that cannot be read is a fixed
// sentence: the system's own text quotes a path.
func showDescriptorVariable(projectDir, descriptorPath string) (string, error) {
	file, err := api.ResolveDescriptorPath(projectDir, descriptorPath)
	if err != nil {
		return "", err
	}
	descriptor, err := dbcopy.ReadPostgresDescriptor(file)
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return "", errors.New("the PostgreSQL connection descriptor cannot be read")
	}
	if err != nil {
		return "", err
	}
	return descriptor.DSNEnv, nil
}

// showText is a value that comes from the project as the text prints it: as it is when every character is
// printable and it has no space at all and no double quote, and else as a quoted string (a name may hold a
// line break or a terminal escape sequence, which would forge lines of the output or act on the terminal; a
// name with a space or a quote in it, such as a column "Order Details" or "id INTEGER pk", could not be told
// from the name, the type and the key that follow it on the line). A name of an ordinary kind, one word
// of printable characters, is never changed. JSON is exact and is never quoted this way.
func showText(value string) string {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, ` "`) {
		return strconv.Quote(value)
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return strconv.Quote(value)
		}
	}
	return value
}

// writeShowText prints the document as plain text: one item per line, two spaces of indent for each level,
// no tab, no emoji.
func writeShowText(w io.Writer, doc *showDocument) error {
	var out strings.Builder
	line := func(depth int, format string, args ...any) {
		out.WriteString(strings.Repeat("  ", depth))
		_, _ = fmt.Fprintf(&out, format, args...)
		out.WriteByte('\n')
	}
	line(0, "Project %s", showText(doc.Project))
	sources := 0
	for _, env := range doc.Environments {
		line(0, "Environment %s", showText(env.ID))
		for _, source := range env.Sources {
			sources++
			if source.DSNEnv == "" {
				line(1, "Source %s (%s)", showText(source.ID), showText(source.Driver))
			} else {
				line(1, "Source %s (%s, URL in $%s)", showText(source.ID), showText(source.Driver), showText(source.DSNEnv))
			}
			if source.NotScanned {
				line(2, "not scanned")
			}
			if source.Empty {
				line(2, "no tables or views")
			}
			for _, schema := range source.Schemas {
				line(2, "Schema %s", showText(schema.Name))
				for _, kind := range []struct {
					label     string
					relations []showRelation
				}{{"Table", schema.Tables}, {"View", schema.Views}} {
					for _, relation := range kind.relations {
						line(3, "%s %s", kind.label, showText(relation.Name))
						writeShowColumns(line, relation)
					}
				}
			}
		}
	}
	if sources == 0 {
		line(0, "No database has been scanned into this project yet: scan one with datatug scan.")
	}
	_, err := io.WriteString(w, out.String())
	return err
}

// writeShowColumns prints the columns of a table or a view, each with its type ("-" when the scan stored
// none) and, when it is part of the primary key, "pk": followed by its place in the key when the key has
// more than one column.
func writeShowColumns(line func(depth int, format string, args ...any), relation showRelation) {
	keyColumns := 0
	for _, column := range relation.Columns {
		if column.PrimaryKeyPosition > 0 {
			keyColumns++
		}
	}
	for _, column := range relation.Columns {
		dbType := "-"
		if column.Type != "" {
			dbType = showText(column.Type)
		}
		name := showText(column.Name)
		switch {
		case column.PrimaryKeyPosition == 0:
			line(4, "%s %s", name, dbType)
		case keyColumns == 1:
			line(4, "%s %s pk", name, dbType)
		default:
			line(4, "%s %s pk %d", name, dbType, column.PrimaryKeyPosition)
		}
	}
}

// writeShowJSON prints the document as one JSON document.
func writeShowJSON(w io.Writer, doc *showDocument) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(doc)
}
