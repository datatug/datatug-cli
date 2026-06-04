package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	entityContinueOnErrorFlag = cli.BoolFlag{Name: "continue-on-error", Usage: "Apply items that pass and report the rest, instead of the default atomic all-or-nothing commit"}
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
		Flags:       []cli.Flag{&entityDirFlag, &entityProjectFlag, &entityFileFlag, &entityFormatFlag, &entityContinueOnErrorFlag},
		Action:      entityAddCommandAction,
	}
}

// parseEntityDocs parses one or more entity definitions from YAML or JSON bytes.
// A top-level array yields a batch of N entities; a top-level object yields a
// degenerate batch of one. It routes through JSON so the model's json struct
// tags (which flatten the embedded ProjItemBrief, unlike yaml.v3) populate
// Entity.ID correctly.
func parseEntityDocs(data []byte) ([]*datatug.Entity, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	jsonData, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if _, isList := doc.([]any); isList {
		var entities []*datatug.Entity
		if err = json.Unmarshal(jsonData, &entities); err != nil {
			return nil, err
		}
		return entities, nil
	}
	entity := &datatug.Entity{}
	if err = json.Unmarshal(jsonData, entity); err != nil {
		return nil, err
	}
	return []*datatug.Entity{entity}, nil
}

// fileWrite describes one file to write atomically: its final path and content.
type fileWrite struct {
	path    string
	content []byte
}

// atomicWriteFiles writes all entries to temp files (in their final
// directories, same filesystem) and then renames each into place. If any temp
// write fails, all staged temp files are removed and nothing is committed.
// Renames happen after all temps are staged, so a fully-validated batch is
// committed together. Reusable by other authoring commands.
func atomicWriteFiles(writes []fileWrite) error {
	type staged struct{ temp, final string }
	var stagedFiles []staged
	cleanup := func() {
		for _, s := range stagedFiles {
			_ = os.Remove(s.temp)
		}
	}
	for i, w := range writes {
		if err := os.MkdirAll(filepath.Dir(w.path), 0777); err != nil {
			cleanup()
			return fmt.Errorf("failed to create directory for %s: %w", w.path, err)
		}
		temp := fmt.Sprintf("%s.tmp-%d", w.path, i)
		if err := os.WriteFile(temp, w.content, 0666); err != nil {
			cleanup()
			return fmt.Errorf("failed to stage %s: %w", w.path, err)
		}
		stagedFiles = append(stagedFiles, staged{temp: temp, final: w.path})
	}
	for _, s := range stagedFiles {
		if err := os.Rename(s.temp, s.final); err != nil {
			cleanup()
			return fmt.Errorf("failed to commit %s: %w", s.final, err)
		}
	}
	return nil
}

// marshalEntityFile marshals an entity to the canonical on-disk JSON form,
// matching the store's saveJSONFile (tab indent + trailing newline) so batch
// and single-entity writes produce byte-identical files.
func marshalEntityFile(entity *datatug.Entity) ([]byte, error) {
	if len(entity.Fields) == 0 && entity.Fields != nil {
		entity.Fields = nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "\t")
	if err := encoder.Encode(entity); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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

	entities, err := parseEntityDocs(data)
	if err != nil {
		return fmt.Errorf("failed to parse entity definition from %s: %w", source, err)
	}
	if len(entities) == 0 {
		return cli.Exit(fmt.Sprintf("no entities found in input from %s", source), 2)
	}

	projectStore := v.store.GetProjectStore(v.projectID)

	// entityExists reports whether the entity's definition file is present on
	// disk, regardless of whether it is readable. A corrupt/unreadable existing
	// file MUST still block creation (never overwrite); only a genuine
	// not-found error means the entity is absent.
	entityExists := func(id string) bool {
		_, loadErr := projectStore.LoadEntity(ctx, id)
		return loadErr == nil || !errors.Is(loadErr, os.ErrNotExist)
	}

	entityFilePath := func(id string) string {
		return filepath.Join(v.ProjectDir, "entities", id, id+".entity.json")
	}

	w := c.Root().Writer
	if w == nil {
		w = os.Stdout
	}

	continueOnError := c.Bool(entityContinueOnErrorFlag.Name)

	if continueOnError {
		return addEntitiesContinueOnError(w, entities, entityExists, entityFilePath)
	}
	return addEntitiesAtomic(w, entities, entityExists, entityFilePath)
}

// addEntitiesAtomic implements the default all-or-nothing commit: preflight all
// items, then stage-to-temp and batch-rename. If any item fails preflight,
// nothing is written. Prints a per-item report and returns an error (non-zero
// exit) if any item failed.
func addEntitiesAtomic(
	w io.Writer,
	entities []*datatug.Entity,
	entityExists func(id string) bool,
	entityFilePath func(id string) string,
) error {
	var failed bool
	var writes []fileWrite
	type report struct {
		ok   bool
		line string
	}
	reports := make([]report, len(entities))

	for i, entity := range entities {
		switch {
		case entity.ID == "":
			failed = true
			reports[i] = report{line: "failed: <missing id>"}
		case entityExists(entity.ID):
			failed = true
			reports[i] = report{line: fmt.Sprintf("failed: %s (already exists)", entity.ID)}
		default:
			content, err := marshalEntityFile(entity)
			if err != nil {
				failed = true
				reports[i] = report{line: fmt.Sprintf("failed: %s (%v)", entity.ID, err)}
				continue
			}
			writes = append(writes, fileWrite{path: entityFilePath(entity.ID), content: content})
			reports[i] = report{ok: true, line: fmt.Sprintf("created: %s", entity.ID)}
		}
	}

	if failed {
		// Atomic mode: write nothing, print the report, exit non-zero.
		var failures []string
		for _, r := range reports {
			if r.ok {
				_, _ = fmt.Fprintf(w, "skipped: %s (rolled back)\n", strings.TrimPrefix(r.line, "created: "))
			} else {
				_, _ = fmt.Fprintln(w, r.line)
				failures = append(failures, strings.TrimPrefix(r.line, "failed: "))
			}
		}
		return fmt.Errorf("entity add failed (atomic mode, nothing written): %s", strings.Join(failures, "; "))
	}

	if err := atomicWriteFiles(writes); err != nil {
		return err
	}
	for _, r := range reports {
		_, _ = fmt.Fprintln(w, r.line)
	}
	return nil
}

// addEntitiesContinueOnError implements partial apply: each item is processed
// independently, passing items are written, failures are collected and
// reported. Returns an error (non-zero exit) if any item failed.
func addEntitiesContinueOnError(
	w io.Writer,
	entities []*datatug.Entity,
	entityExists func(id string) bool,
	entityFilePath func(id string) string,
) error {
	var failures []string
	for _, entity := range entities {
		switch {
		case entity.ID == "":
			failures = append(failures, "<missing id>")
			_, _ = fmt.Fprintln(w, "failed: <missing id>")
		case entityExists(entity.ID):
			failures = append(failures, fmt.Sprintf("%s (already exists)", entity.ID))
			_, _ = fmt.Fprintf(w, "failed: %s (already exists)\n", entity.ID)
		default:
			content, err := marshalEntityFile(entity)
			if err == nil {
				err = atomicWriteFiles([]fileWrite{{path: entityFilePath(entity.ID), content: content}})
			}
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s (%v)", entity.ID, err))
				_, _ = fmt.Fprintf(w, "failed: %s (%v)\n", entity.ID, err)
				continue
			}
			_, _ = fmt.Fprintf(w, "created: %s\n", entity.ID)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("entity add completed with failures: %s", strings.Join(failures, "; "))
	}
	return nil
}
