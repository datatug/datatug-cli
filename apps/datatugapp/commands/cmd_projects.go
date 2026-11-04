package commands

import (
	"fmt"
	"os"

	"github.com/datatug/cliformat"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
)

type projectEntry struct {
	ID     string `json:"id"     yaml:"id"`
	Title  string `json:"title"  yaml:"title,omitempty"`
	Origin string `json:"origin" yaml:"origin,omitempty"`
	// Kind is "cloud" for a DataTug Cloud project, which has no folder.
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`
}

func projectsCommandAction(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("format")
	settings, err := dtconfig.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to get settings: %w", err)
	}
	entries := make([]projectEntry, 0, len(settings.Projects))
	for _, p := range settings.Projects {
		entry := projectEntry{ID: p.ID, Title: p.Title, Origin: p.Url}
		if dtproject.IsCloudRef(*p) {
			entry.Kind = "cloud"
		}
		entries = append(entries, entry)
	}
	return cliformat.WriteList(os.Stdout, format, entries, func(e projectEntry) string {
		if e.Kind != "" {
			return e.ID + " (" + e.Kind + ")"
		}
		return e.ID
	})
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
		if dtproject.IsCloudRef(*p) {
			continue // a DataTug Cloud project has no folder for a file store
		}
		if p.Path != "" {
			pathsByID[p.ID] = p.Path // locally-added projects store a local Path
		} else {
			pathsByID[p.ID] = p.Url
		}
	}
	return
}
