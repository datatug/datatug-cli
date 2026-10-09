package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/spf13/cobra"
)

// exitCodeNoProject is the exit code of a command that could not resolve a
// project (spec/features/cli: project-or-dir-resolution).
const exitCodeNoProject = 3

// Seams, replaced in tests: the working directory and the listing of queries.
var (
	queriesGetwd = os.Getwd
	queriesIndex = api.QueryIDIndex
	// queriesReadFile reads a query file for --format json.
	queriesReadFile = os.ReadFile
	// queriesLstat tells whether a query file is a regular file, without following a link.
	queriesLstat = os.Lstat
)

// queryListItem is one object of the --format json array.
type queryListItem struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Type  string `json:"type,omitempty"`
}

// queriesCommand returns the CLI command that lists the saved queries of a
// project, one ID per line (spec/features/cli/queries).
func queriesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queries",
		Short: "Lists the saved queries of a project",
		Long:  "Prints the ID of every saved query of the project in the current folder (or of --project or --dir), one per line. An ID that is in a folder starts with the folder's name and a slash. --format json prints one JSON document with each query's id, title and type.",
		RunE:  queriesCommandAction,
	}
	cmd.Flags().StringP("project", "p", "", "project ID or folder")
	cmd.Flags().StringP("dir", "d", "", "project folder")
	cmd.Flags().String("format", "text", "output format: text or json")
	return cmd
}

func queriesCommandAction(cmd *cobra.Command, _ []string) error {
	project, _ := cmd.Flags().GetString("project")
	dir, _ := cmd.Flags().GetString("dir")
	format, _ := cmd.Flags().GetString("format")
	if format != "text" && format != "json" {
		return Exit(fmt.Sprintf("unsupported --format %q: use text or json", format), exitCodeUsage)
	}
	switch {
	case project != "" && dir != "":
		return Exit("--project and --dir cannot be used together", exitCodeUsage)
	case project != "":
		var err error
		if dir, _, err = resolveQueryProject(project); err != nil {
			return Exit(err.Error(), exitCodeNoProject)
		}
	case dir == "":
		var err error
		if dir, err = queriesGetwd(); err != nil {
			return Exit("cannot find the current folder: "+err.Error(), exitCodeNoProject)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, storage.ProjectSummaryFileName)); err != nil {
		return Exit(fmt.Sprintf("the folder is not a DataTug project (it has no %s): run the command in a project folder or pass --project or --dir", storage.ProjectSummaryFileName), exitCodeNoProject)
	}
	index, err := queriesIndex(dir)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out bytes.Buffer
	skipped := 0
	unreadable := 0
	items := make([]queryListItem, 0, len(ids))
	for _, id := range ids {
		if !plainQueryID(id) {
			skipped++
			continue
		}
		if format == "json" {
			item, ok := readQueryListItem(dir, id)
			if !ok {
				unreadable++
			}
			items = append(items, item)
			continue
		}
		_, _ = fmt.Fprintln(&out, id)
	}
	if format == "json" {
		data, _ := json.MarshalIndent(items, "", "  ") // plain strings cannot fail to marshal
		_, _ = fmt.Fprintln(&out, string(data))
	}
	if _, err = cmd.OutOrStdout().Write(out.Bytes()); err != nil {
		return err
	}
	if skipped > 0 {
		noun := "queries"
		if skipped == 1 {
			noun = "query"
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%d saved %s skipped: their IDs are not plain names (letters, digits, '.', '_' and '-', with '/' between folders)\n", skipped, noun)
	}
	if unreadable > 0 {
		noun := "files"
		if unreadable == 1 {
			noun = "file"
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%d query %s could not be read as a query\n", unreadable, noun)
	}
	return nil
}

// readQueryListItem returns the item of the query id, with the title and type
// of its file. When the file cannot be read as a query the item holds the ID
// only and ok is false.
func readQueryListItem(projectDir, id string) (item queryListItem, ok bool) {
	item = queryListItem{ID: id}
	file := filepath.Join(projectDir, storage.QueriesFolder, filepath.FromSlash(id)+"."+storage.QueryFileSuffix+".json")
	// A link, a folder or a device is not read: the project store refuses them too.
	info, err := queriesLstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return item, false
	}
	data, err := queriesReadFile(file)
	if err != nil || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return item, false
	}
	var def struct {
		Title string `json:"title"`
		Type  string `json:"type"`
	}
	if err = json.Unmarshal(data, &def); err != nil {
		return item, false
	}
	item.Title, item.Type = def.Title, def.Type
	return item, true
}

// plainQueryID reports whether id, the folders and name of a saved query joined
// by "/", is made of plain names. Such an ID is safe to print and to pass on to
// `query run`; anything else is a file name a person chose, which may hold a
// path or a secret and which no command can address. (dbcopy.QueryIDDisplay is
// for naming an ID a client sent in a message; its placeholder is not an ID.)
func plainQueryID(id string) bool {
	for _, part := range strings.Split(id, "/") {
		if !dbcopy.IsPlainSourceID(part) {
			return false
		}
	}
	return true
}
