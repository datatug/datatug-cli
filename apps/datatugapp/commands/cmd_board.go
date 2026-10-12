package commands

// specscore: feature/cli/board

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// boardListItem is one object of the --format json array.
type boardListItem struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// boardCommand returns the `board` resource: a bare invocation shows help, and
// `board list` lists the boards of a project (spec/features/cli/board).
func boardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "board",
		Short: "Read the boards of a DataTug project",
	}
	cmd.AddCommand(boardListCommand())
	return cmd
}

func boardListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Lists the boards of a project",
		Long: "Prints the ID of every board of the project, one per line, in ID order; --format json prints one JSON array that also carries each board's title.\n\n" +
			"The project is the folder of --directory, or the registered project of --project, or else the current folder. Read-only: never writes.",
		Args: cobra.NoArgs,
		RunE: boardListCommandAction,
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

func boardListCommandAction(cmd *cobra.Command, _ []string) error {
	flags := cmd.Flags()
	v := &projectBaseCommand{}
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
	_, err := os.Stat(filepath.Join(v.ProjectDir, storage.ProjectSummaryFileName))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return Exit(fmt.Sprintf("%q is not a DataTug project: it has no %s", v.ProjectDir, storage.ProjectSummaryFileName), exitCodeNotFound)
	}
	boards, err := v.store.GetProjectStore(v.projectID).LoadBoards(cmd.Context())
	if err != nil {
		return err // the store names the board it could not load
	}
	items := make([]boardListItem, 0, len(boards))
	skipped := 0
	for _, board := range boards {
		if !dbcopy.IsPlainSourceID(board.ID) {
			skipped++
			continue
		}
		items = append(items, boardListItem{ID: board.ID, Title: board.Title})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })

	var out bytes.Buffer
	if format == showFormatJSON {
		data, _ := json.MarshalIndent(items, "", "  ") // plain strings cannot fail to marshal
		out.Write(data)
		out.WriteByte('\n')
	} else {
		for _, item := range items {
			out.WriteString(item.ID)
			out.WriteByte('\n')
		}
	}
	if _, err = cmd.OutOrStdout().Write(out.Bytes()); err != nil {
		return fmt.Errorf("cannot write the list of boards: %w", err)
	}
	if skipped > 0 {
		noun := "boards"
		if skipped == 1 {
			noun = "board"
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%d %s skipped: their IDs are not plain names (letters, digits, '.', '_' and '-')\n", skipped, noun)
	}
	return nil
}
