package commands

import (
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
)

// queriesCommand returns the CLI command that lists the saved queries of a
// project, one ID per line (spec/features/cli/queries).
func queriesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queries",
		Short: "Lists the saved queries of a project",
		Long:  "Prints the ID of every saved query of the project in the current folder (or of --project or --dir), one per line. An ID that is in a folder starts with the folder's name and a slash.",
		RunE:  queriesCommandAction,
	}
	cmd.Flags().StringP("project", "p", "", "project ID or folder")
	cmd.Flags().StringP("dir", "d", "", "project folder")
	return cmd
}

func queriesCommandAction(cmd *cobra.Command, _ []string) error {
	project, _ := cmd.Flags().GetString("project")
	dir, _ := cmd.Flags().GetString("dir")
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
	out := cmd.OutOrStdout()
	skipped := 0
	for _, id := range ids {
		if !plainQueryID(id) {
			skipped++
			continue
		}
		_, _ = fmt.Fprintln(out, id)
	}
	if skipped > 0 {
		noun := "queries"
		if skipped == 1 {
			noun = "query"
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%d saved %s skipped: their IDs are not plain names (letters, digits, '.', '_' and '-', with '/' between folders)\n", skipped, noun)
	}
	return nil
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
