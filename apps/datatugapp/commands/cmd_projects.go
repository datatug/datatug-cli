package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/datatug/cliformat"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/urfave/cli/v3"
)

type projectEntry struct {
	ID     string `json:"id"     yaml:"id"`
	Title  string `json:"title"  yaml:"title,omitempty"`
	Origin string `json:"origin" yaml:"origin,omitempty"`
}

func projectsCommandAction(_ context.Context, cmd *cli.Command) error {
	format := cmd.String("format")
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

func projectsCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "projects",
		Usage:       "List & manage DataTug projects",
		Description: "",
		Action:      projectsCommandAction,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Value: "name", Usage: "output format: name, yaml, json"},
		},
		Commands: []*cli.Command{
			projectsAddCommandArgs(),
		},
	}
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
