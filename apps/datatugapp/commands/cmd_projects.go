package commands

import (
	"fmt"
	"os"

	"github.com/datatug/cliformat"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/spf13/cobra"
)

type projectEntry struct {
	ID     string `json:"id"     yaml:"id"`
	Title  string `json:"title"  yaml:"title,omitempty"`
	Origin string `json:"origin" yaml:"origin,omitempty"`
}

func projectsCommandAction(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("format")
	settings, err := dtconfig.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to get settings: %w", err)
	}
	entries := make([]projectEntry, 0, len(settings.Projects))
	for _, p := range settings.Projects {
		entries = append(entries, projectEntry{ID: p.ID, Title: p.Title, Origin: p.Origin})
	}
	return cliformat.WriteList(os.Stdout, format, entries, func(e projectEntry) string { return e.ID })
}

func projectsCommandArgs() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "List & manage DataTug projects",
		RunE:  projectsCommandAction,
	}
	cmd.Flags().StringP("format", "o", "name", "output format: name, yaml, json")
	cmd.AddCommand(projectsAddCommandArgs())
	return cmd
}

func getProjPathsByID(config dtconfig.Settings) (pathsByID map[string]string) {
	pathsByID = make(map[string]string, len(config.Projects))
	for _, p := range config.Projects {
		if p.Path != "" {
			pathsByID[p.ID] = p.Path // locally-added projects store a local Path
		} else {
			pathsByID[p.ID] = p.Origin
		}
	}
	return
}
