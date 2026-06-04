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
	"slices"
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
			entityListCommandArgs(),
			entityShowCommandArgs(),
			entityFieldCommand(),
		},
	}
}

func entityListCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "list",
		Usage:       "List the entities in a project",
		Description: "Lists the project's entities, one per line. Read-only: never writes.",
		Flags:       []cli.Flag{&entityDirFlag, &entityProjectFlag},
		Action:      entityListCommandAction,
	}
}

func entityListCommandAction(ctx context.Context, c *cli.Command) error {
	v := &projectBaseCommand{}
	v.ProjectDir = c.String(entityDirFlag.Name)
	v.ProjectName = c.String(entityProjectFlag.Name)

	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	projectStore := v.store.GetProjectStore(v.projectID)

	entities, err := projectStore.LoadEntities(ctx)
	if err != nil {
		return err
	}

	w := c.Root().Writer
	if w == nil {
		w = os.Stdout
	}

	if len(entities) == 0 {
		_, _ = fmt.Fprintln(w, "no entities")
		return nil
	}

	for _, e := range entities {
		if e.Title != "" {
			_, _ = fmt.Fprintf(w, "%s — %s\n", e.ID, e.Title)
		} else {
			_, _ = fmt.Fprintln(w, e.ID)
		}
	}
	return nil
}

func entityShowCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "show",
		Usage:       "Show an entity's fields and read-only generated mapping copy",
		Description: "Renders an entity's fields and, when present, the read-only generated copy of its table/column links. Read-only: never writes.",
		ArgsUsage:   "<Entity>",
		Flags:       []cli.Flag{&entityDirFlag, &entityProjectFlag},
		Action:      entityShowCommandAction,
	}
}

func entityShowCommandAction(ctx context.Context, c *cli.Command) error {
	name := c.Args().First()
	if name == "" {
		return cli.Exit("entity name is required: datatug entity show <Entity>", 2)
	}

	v := &projectBaseCommand{}
	v.ProjectDir = c.String(entityDirFlag.Name)
	v.ProjectName = c.String(entityProjectFlag.Name)

	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	projectStore := v.store.GetProjectStore(v.projectID)

	entity, err := projectStore.LoadEntity(ctx, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cli.Exit(fmt.Sprintf("entity %q not found", name), 1)
		}
		return err
	}

	rendered, err := renderEntityShow(entity)
	if err != nil {
		return err
	}

	w := c.Root().Writer
	if w == nil {
		w = os.Stdout
	}
	_, _ = fmt.Fprint(w, rendered)
	return nil
}

// renderEntityShow renders a loaded entity as YAML for the read-only show view,
// labelling the generated table/column mapping copy when present. It routes
// through the model's JSON tags (which flatten the embedded ProjItemBrief, so
// the id/title surface correctly) and then re-renders as YAML.
func renderEntityShow(entity *datatug.Entity) (string, error) {
	jsonData, err := json.Marshal(entity)
	if err != nil {
		return "", err
	}
	var doc map[string]any
	if err = json.Unmarshal(jsonData, &doc); err != nil {
		return "", err
	}

	// The mapping copy (tables) is a read-only generated artifact: render it in a
	// clearly-labelled, separate section so a reader never mistakes it for
	// authored content.
	tables, hasTables := doc["tables"]
	delete(doc, "tables")

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err = enc.Encode(doc); err != nil {
		return "", err
	}
	if err = enc.Close(); err != nil {
		return "", err
	}

	if hasTables {
		buf.WriteString("# generated mapping copy (read-only)\n")
		tablesEnc := yaml.NewEncoder(&buf)
		tablesEnc.SetIndent(2)
		if err = tablesEnc.Encode(map[string]any{"tables": tables}); err != nil {
			return "", err
		}
		if err = tablesEnc.Close(); err != nil {
			return "", err
		}
	}
	return buf.String(), nil
}

func entityFieldCommand() *cli.Command {
	return &cli.Command{
		Name:  "field",
		Usage: "Author entity fields",
		Commands: []*cli.Command{
			entityFieldAddCommandArgs(),
			entityFieldSetCommandArgs(),
			entityFieldRmCommandArgs(),
		},
	}
}

func entityFieldRmCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "rm",
		Usage:       "Remove a named field from an entity",
		Description: "Removes a named field from an existing entity. Fails non-zero if the field is absent and writes nothing.",
		ArgsUsage:   "<Entity> <field>",
		Flags:       []cli.Flag{&entityDirFlag, &entityProjectFlag},
		Action:      entityFieldRmCommandAction,
	}
}

func entityFieldRmCommandAction(ctx context.Context, c *cli.Command) error {
	name := c.Args().Get(0)
	fieldName := c.Args().Get(1)
	if name == "" || fieldName == "" {
		return cli.Exit("entity and field names are required: datatug entity field rm <Entity> <field>", 2)
	}

	v := &projectBaseCommand{}
	v.ProjectDir = c.String(entityDirFlag.Name)
	v.ProjectName = c.String(entityProjectFlag.Name)

	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	projectStore := v.store.GetProjectStore(v.projectID)

	entity, err := projectStore.LoadEntity(ctx, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cli.Exit(fmt.Sprintf("entity %q not found", name), 1)
		}
		return err
	}

	idx := -1
	for i, f := range entity.Fields {
		if f.ID == fieldName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return cli.Exit(fmt.Sprintf("field %q not found in entity %q", fieldName, name), 1)
	}

	entity.Fields = append(entity.Fields[:idx], entity.Fields[idx+1:]...)

	content, err := marshalEntityFile(entity)
	if err != nil {
		return err
	}
	entityPath := filepath.Join(v.ProjectDir, "entities", name, name+".entity.json")
	if err = atomicWriteFiles([]fileWrite{{path: entityPath, content: content}}); err != nil {
		return err
	}

	w := c.Root().Writer
	if w == nil {
		w = os.Stdout
	}
	_, _ = fmt.Fprintf(w, "removed field: %s\n", fieldName)
	return nil
}

var (
	entityFieldTypeFlag  = cli.StringFlag{Name: "type", Usage: "Field type"}
	entityFieldTitleFlag = cli.StringFlag{Name: "title", Usage: "Field title"}
	entityFieldKeyFlag   = cli.BoolFlag{Name: "key", Usage: "Whether the field is a key field"}
)

func entityFieldSetCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "set",
		Usage:       "Update attributes of an existing field on an entity",
		Description: "Updates an existing field's type, title, and/or key flag. Fails if the field does not exist; only attributes whose flags are passed are changed.",
		ArgsUsage:   "<Entity> <field>",
		Flags:       []cli.Flag{&entityDirFlag, &entityProjectFlag, &entityFieldTypeFlag, &entityFieldTitleFlag, &entityFieldKeyFlag},
		Action:      entityFieldSetCommandAction,
	}
}

func entityFieldSetCommandAction(ctx context.Context, c *cli.Command) error {
	name := c.Args().Get(0)
	fieldName := c.Args().Get(1)
	if name == "" || fieldName == "" {
		return cli.Exit("entity and field names are required: datatug entity field set <Entity> <field>", 2)
	}

	setType := c.IsSet(entityFieldTypeFlag.Name)
	setTitle := c.IsSet(entityFieldTitleFlag.Name)
	setKey := c.IsSet(entityFieldKeyFlag.Name)
	if !setType && !setTitle && !setKey {
		return cli.Exit("nothing to update: pass at least one of --type, --title, --key", 2)
	}

	v := &projectBaseCommand{}
	v.ProjectDir = c.String(entityDirFlag.Name)
	v.ProjectName = c.String(entityProjectFlag.Name)

	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	projectStore := v.store.GetProjectStore(v.projectID)

	entity, err := projectStore.LoadEntity(ctx, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cli.Exit(fmt.Sprintf("entity %q not found", name), 1)
		}
		return err
	}

	var field *datatug.EntityField
	for _, f := range entity.Fields {
		if f.ID == fieldName {
			field = f
			break
		}
	}
	if field == nil {
		return cli.Exit(fmt.Sprintf("field %q not found in entity %q", fieldName, name), 1)
	}

	if setType {
		newType := c.String(entityFieldTypeFlag.Name)
		if err = validateFieldType(newType); err != nil {
			return cli.Exit(fmt.Sprintf("field %q: %v", fieldName, err), 1)
		}
		field.Type = newType
	}
	if setTitle {
		field.Title = c.String(entityFieldTitleFlag.Name)
	}
	if setKey {
		field.IsKeyField = c.Bool(entityFieldKeyFlag.Name)
	}

	content, err := marshalEntityFile(entity)
	if err != nil {
		return err
	}
	entityPath := filepath.Join(v.ProjectDir, "entities", name, name+".entity.json")
	if err = atomicWriteFiles([]fileWrite{{path: entityPath, content: content}}); err != nil {
		return err
	}

	w := c.Root().Writer
	if w == nil {
		w = os.Stdout
	}
	_, _ = fmt.Fprintf(w, "updated field: %s\n", fieldName)
	return nil
}

func entityFieldAddCommandArgs() *cli.Command {
	return &cli.Command{
		Name:        "add",
		Usage:       "Add one or more new fields to an existing entity",
		Description: "Adds one or more new fields to an existing entity from a YAML/JSON definition. Additive-only: fails if any named field already exists and never overwrites existing field content.",
		ArgsUsage:   "<Entity>",
		Flags:       []cli.Flag{&entityDirFlag, &entityProjectFlag, &entityFileFlag, &entityFormatFlag, &entityContinueOnErrorFlag},
		Action:      entityFieldAddCommandAction,
	}
}

// readDefinitionInput resolves the definition input shared by entity add and
// entity field add: it validates an explicit --format, reads from the -f file
// or stdin (-f '-' or absent), and rejects empty (whitespace-only) input. It
// returns the raw bytes and a human-readable source description.
func readDefinitionInput(c *cli.Command) (data []byte, source string, err error) {
	// Validate an explicit --format value up front (before reading/writing).
	format := c.String(entityFormatFlag.Name)
	switch format {
	case "", "yaml", "json":
		// ok
	default:
		return nil, "", cli.Exit(fmt.Sprintf("unsupported --format %q (expected 'yaml' or 'json')", format), 2)
	}

	// Resolve the input source: a file path, or stdin when -f is '-' or absent.
	filePath := c.String(entityFileFlag.Name)
	fromStdin := filePath == "" || filePath == "-"

	if fromStdin {
		source = "stdin"
		reader := c.Root().Reader
		if reader == nil {
			reader = os.Stdin
		}
		if data, err = io.ReadAll(reader); err != nil {
			return nil, source, fmt.Errorf("failed to read entity definition from stdin: %w", err)
		}
	} else {
		source = fmt.Sprintf("file [%s]", filePath)
		if data, err = os.ReadFile(filePath); err != nil {
			return nil, source, fmt.Errorf("failed to read entity definition file [%s]: %w", filePath, err)
		}
	}

	// Empty input (zero or whitespace-only bytes) is an error; write nothing.
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, source, cli.Exit(fmt.Sprintf("empty input from %s", source), 2)
	}
	return data, source, nil
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

// parseFieldDocs parses one or more field definitions from YAML or JSON bytes.
// A top-level array yields a batch of N fields; a top-level object yields a
// degenerate batch of one. It routes through JSON so the model's json struct
// tags populate EntityField correctly.
func parseFieldDocs(data []byte) ([]*datatug.EntityField, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	jsonData, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if _, isList := doc.([]any); isList {
		var fields []*datatug.EntityField
		if err = json.Unmarshal(jsonData, &fields); err != nil {
			return nil, err
		}
		return fields, nil
	}
	field := &datatug.EntityField{}
	if err = json.Unmarshal(jsonData, field); err != nil {
		return nil, err
	}
	return []*datatug.EntityField{field}, nil
}

// validateFieldType rejects a non-empty field type that is neither a
// datatug-core known type nor an extends:<ref> reference. An empty type is
// permissive on purpose: untyped fields exist on disk in real projects (e.g.
// the demo Person entity) and no AC requires rejecting them.
func validateFieldType(t string) error {
	if t == "" {
		return nil
	}
	if ref, ok := strings.CutPrefix(t, "extends:"); ok {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("unknown field type %q (expected a known type or extends:<ref>)", t)
		}
		return nil
	}
	if slices.Contains(datatug.KnownTypes, t) {
		return nil
	}
	return fmt.Errorf("unknown field type %q (expected a known type or extends:<ref>)", t)
}

// validateEntityFieldTypes validates every field type of an entity, returning
// the first failure with field context.
func validateEntityFieldTypes(entity *datatug.Entity) error {
	for _, f := range entity.Fields {
		if err := validateFieldType(f.Type); err != nil {
			return fmt.Errorf("field %q: %w", f.ID, err)
		}
	}
	return nil
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

	data, source, err := readDefinitionInput(c)
	if err != nil {
		return err
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
			if err := validateEntityFieldTypes(entity); err != nil {
				failed = true
				reports[i] = report{line: fmt.Sprintf("failed: %s (%v)", entity.ID, err)}
				continue
			}
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
			if err := validateEntityFieldTypes(entity); err != nil {
				failures = append(failures, fmt.Sprintf("%s (%v)", entity.ID, err))
				_, _ = fmt.Fprintf(w, "failed: %s (%v)\n", entity.ID, err)
				continue
			}
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

func entityFieldAddCommandAction(ctx context.Context, c *cli.Command) error {
	name := c.Args().First()
	if name == "" {
		return cli.Exit("entity name is required: datatug entity field add <Entity>", 2)
	}

	v := &projectBaseCommand{}
	v.ProjectDir = c.String(entityDirFlag.Name)
	v.ProjectName = c.String(entityProjectFlag.Name)

	if err := v.initProjectCommand(projectCommandOptions{projNameOrDirRequired: true}); err != nil {
		return err
	}

	data, source, err := readDefinitionInput(c)
	if err != nil {
		return err
	}

	fields, err := parseFieldDocs(data)
	if err != nil {
		return fmt.Errorf("failed to parse field definition from %s: %w", source, err)
	}
	if len(fields) == 0 {
		return cli.Exit(fmt.Sprintf("no fields found in input from %s", source), 2)
	}

	projectStore := v.store.GetProjectStore(v.projectID)

	// field add operates only on existing entities: load it, requiring presence.
	entity, err := projectStore.LoadEntity(ctx, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cli.Exit(fmt.Sprintf("entity %q not found", name), 1)
		}
		return err
	}

	existing := make(map[string]bool, len(entity.Fields))
	for _, f := range entity.Fields {
		existing[f.ID] = true
	}

	w := c.Root().Writer
	if w == nil {
		w = os.Stdout
	}

	entityPath := filepath.Join(v.ProjectDir, "entities", name, name+".entity.json")
	continueOnError := c.Bool(entityContinueOnErrorFlag.Name)

	var toAdd []*datatug.EntityField
	var failures []string
	for _, f := range fields {
		switch {
		case f.ID == "":
			failures = append(failures, "<missing id>")
			if continueOnError {
				_, _ = fmt.Fprintln(w, "failed field: <missing id>")
			}
		case existing[f.ID]:
			failures = append(failures, fmt.Sprintf("%s (already exists)", f.ID))
			if continueOnError {
				_, _ = fmt.Fprintf(w, "failed field: %s (already exists)\n", f.ID)
			}
		default:
			if typeErr := validateFieldType(f.Type); typeErr != nil {
				failures = append(failures, fmt.Sprintf("%s (%v)", f.ID, typeErr))
				if continueOnError {
					_, _ = fmt.Fprintf(w, "failed field: %s (%v)\n", f.ID, typeErr)
				}
				continue
			}
			existing[f.ID] = true
			toAdd = append(toAdd, f)
		}
	}

	// Atomic (default) mode: any collision means write nothing and exit non-zero.
	if !continueOnError && len(failures) > 0 {
		for _, fl := range failures {
			_, _ = fmt.Fprintf(w, "failed field: %s\n", fl)
		}
		return fmt.Errorf("entity field add failed (nothing written): %s", strings.Join(failures, "; "))
	}

	if len(toAdd) > 0 {
		entity.Fields = append(entity.Fields, toAdd...)
		content, err := marshalEntityFile(entity)
		if err != nil {
			return err
		}
		if err = atomicWriteFiles([]fileWrite{{path: entityPath, content: content}}); err != nil {
			return err
		}
		for _, f := range toAdd {
			_, _ = fmt.Fprintf(w, "added field: %s\n", f.ID)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("entity field add completed with failures: %s", strings.Join(failures, "; "))
	}
	return nil
}
