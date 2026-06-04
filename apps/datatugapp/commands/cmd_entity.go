package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"
)

var (
	entityDirFlag     = cli.StringFlag{Name: "directory", Aliases: []string{"d"}, Usage: "Path to the project directory (alternative to --project)"}
	entityProjectFlag = cli.StringFlag{Name: "project", Aliases: []string{"p"}, Usage: "Registered project id/name"}
	entityFileFlag    = cli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "Path to the entity definition file (YAML or JSON); use '-' or omit to read from stdin"}
	entityFormatFlag  = cli.StringFlag{Name: "format", Usage: "Input format: yaml or json (defaults to file extension or content sniff)"}
)

func entityCommand() *cli.Command {
	return &cli.Command{
		Name:  "entity",
		Usage: "Author and read DataTug entities",
		Commands: []*cli.Command{
			entityAddCommandArgs(),
		},
	}
}

func entityAddCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "add",
		Usage:       "Create a new entity from a definition file",
		Description: "Creates a new entity from a YAML/JSON definition file. Create-only: fails if the entity already exists.",
		Flags:       []cli.Flag{&entityDirFlag, &entityProjectFlag, &entityFileFlag, &entityFormatFlag},
		Action:      entityAddCommandAction,
	}
}

// parseEntityDoc parses a single entity definition from YAML or JSON bytes.
// It routes through JSON so the model's json struct tags (which flatten the
// embedded ProjItemBrief, unlike yaml.v3) populate Entity.ID correctly.
func parseEntityDoc(data []byte) (*datatug.Entity, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	jsonData, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	entity := &datatug.Entity{}
	if err = json.Unmarshal(jsonData, entity); err != nil {
		return nil, err
	}
	return entity, nil
}

func entityAddCommandAction(ctx context.Context, c *cli.Command) error {
	v := &projectBaseCommand{}
	v.ProjectDir = c.String(entityDirFlag.Name)
	v.ProjectName = c.String(entityProjectFlag.Name)

	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	// Validate an explicit --format value up front (before reading/writing).
	format := c.String(entityFormatFlag.Name)
	switch format {
	case "", "yaml", "json":
		// ok
	default:
		return cli.Exit(fmt.Sprintf("unsupported --format %q (expected 'yaml' or 'json')", format), 2)
	}

	// Resolve the input source: a file path, or stdin when -f is '-' or absent.
	filePath := c.String(entityFileFlag.Name)
	fromStdin := filePath == "" || filePath == "-"

	var data []byte
	var source string
	var err error
	if fromStdin {
		source = "stdin"
		reader := c.Root().Reader
		if reader == nil {
			reader = os.Stdin
		}
		if data, err = io.ReadAll(reader); err != nil {
			return fmt.Errorf("failed to read entity definition from stdin: %w", err)
		}
	} else {
		source = fmt.Sprintf("file [%s]", filePath)
		if data, err = os.ReadFile(filePath); err != nil {
			return fmt.Errorf("failed to read entity definition file [%s]: %w", filePath, err)
		}
	}

	// Empty input (zero or whitespace-only bytes) is an error; write nothing.
	if len(strings.TrimSpace(string(data))) == 0 {
		return cli.Exit(fmt.Sprintf("empty input from %s", source), 2)
	}

	entity, err := parseEntityDoc(data)
	if err != nil {
		return fmt.Errorf("failed to parse entity definition from %s: %w", source, err)
	}
	if entity.ID == "" {
		return cli.Exit(fmt.Sprintf("entity definition from %s is missing required field 'id'", source), 2)
	}

	projectStore := v.store.GetProjectStore(v.projectID)

	// Create-only guard: the entity "exists" if its definition file is present
	// on disk, regardless of whether it is readable. A corrupt/unreadable
	// existing file MUST still block creation (never overwrite). Only a genuine
	// not-found error lets us proceed to create.
	if _, loadErr := projectStore.LoadEntity(ctx, entity.ID); loadErr == nil || !errors.Is(loadErr, os.ErrNotExist) {
		return fmt.Errorf("entity %q already exists", entity.ID)
	}

	if err = projectStore.SaveEntity(ctx, entity); err != nil {
		return fmt.Errorf("failed to save entity %q: %w", entity.ID, err)
	}

	_, _ = fmt.Fprintf(c.Writer, "Created entity %q\n", entity.ID)
	return nil
}
