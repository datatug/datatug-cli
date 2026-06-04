package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

// runEntity invokes the entity command with the given argv slice (no "datatug"
// prefix; pass starting from "entity"). Captures stdout/stderr and returns the
// cli.Command's error (or nil) for the caller to inspect. A no-op
// ExitErrHandler lets the test observe the returned error / exit code directly.
func runEntity(t *testing.T, argv ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	return runEntityStdin(t, "", argv...)
}

// runEntityStdin is like runEntity but injects the given string as the
// command's stdin via root.Reader, letting tests exercise stdin input.
func runEntityStdin(t *testing.T, stdin string, argv ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	root := &cli.Command{
		Name:           "datatug",
		Commands:       []*cli.Command{entityCommand()},
		Reader:         strings.NewReader(stdin),
		Writer:         stdout,
		ErrWriter:      stderr,
		ExitErrHandler: func(_ context.Context, _ *cli.Command, _ error) {},
	}
	err = root.Run(context.Background(), append([]string{"datatug"}, argv...))
	return
}

// AC: add-creates-new + AC: storage-json
func TestEntityAdd_CreatesNew_StorageJSON(t *testing.T) {
	dir := t.TempDir()
	defFile := filepath.Join(dir, "user.yaml")
	require.NoError(t, os.WriteFile(defFile, []byte("id: User\nfields:\n  - id: id\n    type: string\n"), 0644))

	_, _, err := runEntity(t, "entity", "add", "-d", dir, "-f", defFile)
	assert.NoError(t, err)

	entityPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	assert.FileExists(t, entityPath)

	data, err := os.ReadFile(entityPath)
	require.NoError(t, err)
	assert.True(t, json.Valid(data), "stored file must be valid JSON")
	assert.Contains(t, string(data), `"id": "User"`)
}

// AC: add-rejects-existing
func TestEntityAdd_RejectsExisting(t *testing.T) {
	dir := t.TempDir()
	defFile := filepath.Join(dir, "user.yaml")
	require.NoError(t, os.WriteFile(defFile, []byte("id: User\nfields:\n  - id: id\n    type: string\n"), 0644))

	// First add succeeds and creates the entity.
	_, _, err := runEntity(t, "entity", "add", "-d", dir, "-f", defFile)
	require.NoError(t, err)

	entityPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	before, err := os.ReadFile(entityPath)
	require.NoError(t, err)

	// Second add for the same entity must be rejected and leave the file untouched.
	_, _, err = runEntity(t, "entity", "add", "-d", dir, "-f", defFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "User")
	assert.Contains(t, err.Error(), "exists")

	after, err := os.ReadFile(entityPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "existing entity must be left unchanged")
}

// AC: add-rejects-existing — a corrupt/unreadable existing entity file must
// still trigger the create-only guard (never overwrite curated content).
func TestEntityAdd_RejectsExisting_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	defFile := filepath.Join(dir, "user.yaml")
	require.NoError(t, os.WriteFile(defFile, []byte("id: User\nfields:\n  - id: id\n    type: string\n"), 0644))

	// Plant a corrupt (invalid-JSON) entity file at the canonical path.
	entityPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(entityPath), 0755))
	corrupt := []byte("{ this is not valid json")
	require.NoError(t, os.WriteFile(entityPath, corrupt, 0644))

	_, _, err := runEntity(t, "entity", "add", "-d", dir, "-f", defFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exists")

	after, err := os.ReadFile(entityPath)
	require.NoError(t, err)
	assert.Equal(t, corrupt, after, "corrupt existing entity must be left untouched")
}

// AC: add-reads-stdin — a YAML definition piped on stdin (no -f) is read from
// stdin and the entity is created.
func TestEntityAdd_ReadsStdin(t *testing.T) {
	dir := t.TempDir()
	stdin := "id: User\nfields:\n  - id: id\n    type: string\n"

	_, _, err := runEntityStdin(t, stdin, "entity", "add", "-d", dir)
	assert.NoError(t, err)

	entityPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	assert.FileExists(t, entityPath)
}

// AC: add-reads-stdin — explicit "-f -" also reads from stdin.
func TestEntityAdd_ReadsStdin_DashFile(t *testing.T) {
	dir := t.TempDir()
	stdin := "id: User\nfields:\n  - id: id\n    type: string\n"

	_, _, err := runEntityStdin(t, stdin, "entity", "add", "-d", dir, "-f", "-")
	assert.NoError(t, err)

	entityPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	assert.FileExists(t, entityPath)
}

// AC: add-empty-errors — empty stdin yields a non-zero "empty input" error and
// writes nothing.
func TestEntityAdd_EmptyStdin_Errors(t *testing.T) {
	dir := t.TempDir()

	_, _, err := runEntityStdin(t, "   \n\t ", "entity", "add", "-d", dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")

	_, statErr := os.Stat(filepath.Join(dir, "entities"))
	assert.True(t, os.IsNotExist(statErr), "nothing must be written on empty input")
}

// AC: add-batch-atomic-rollback — a batch with one new entity (User) and one
// existing entity (Order) in the default atomic mode writes nothing, reports
// the Order conflict, and exits non-zero.
func TestEntityAdd_BatchAtomicRollback(t *testing.T) {
	dir := t.TempDir()

	// Pre-create Order so the batch hits a create-only conflict.
	orderDef := "id: Order\nfields:\n  - id: id\n    type: string\n"
	_, _, err := runEntityStdin(t, orderDef, "entity", "add", "-d", dir)
	require.NoError(t, err)

	orderPath := filepath.Join(dir, "entities", "Order", "Order.entity.json")
	orderBefore, err := os.ReadFile(orderPath)
	require.NoError(t, err)

	batch := "- id: User\n  fields:\n    - id: id\n      type: string\n- id: Order\n  fields:\n    - id: id\n      type: string\n"
	stdout, _, runErr := runEntityStdin(t, batch, "entity", "add", "-d", dir)
	assert.Error(t, runErr)

	// Atomic rollback: User must NOT be written.
	userPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	_, statErr := os.Stat(userPath)
	assert.True(t, os.IsNotExist(statErr), "User must not be written in atomic rollback")

	// Order must be left unchanged.
	orderAfter, err := os.ReadFile(orderPath)
	require.NoError(t, err)
	assert.Equal(t, orderBefore, orderAfter, "existing Order must be left unchanged")

	// The per-item report must flag Order as the conflict.
	report := stdout.String() + runErr.Error()
	assert.Contains(t, report, "Order")
	assert.Contains(t, report, "exists")
}

// AC: add-continue-on-error — same batch with --continue-on-error creates User,
// reports Order failed, and exits non-zero.
func TestEntityAdd_BatchContinueOnError(t *testing.T) {
	dir := t.TempDir()

	orderDef := "id: Order\nfields:\n  - id: id\n    type: string\n"
	_, _, err := runEntityStdin(t, orderDef, "entity", "add", "-d", dir)
	require.NoError(t, err)

	batch := "- id: User\n  fields:\n    - id: id\n      type: string\n- id: Order\n  fields:\n    - id: id\n      type: string\n"
	stdout, _, err := runEntityStdin(t, batch, "entity", "add", "-d", dir, "--continue-on-error")
	assert.Error(t, err)

	// User IS created in partial-apply mode.
	userPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	assert.FileExists(t, userPath)

	// Report mentions Order failed.
	report := stdout.String() + err.Error()
	assert.Contains(t, report, "Order")
	assert.Contains(t, report, "exists")
}

// An all-new batch creates every entity, reports each, and exits zero.
func TestEntityAdd_BatchAllNew(t *testing.T) {
	dir := t.TempDir()

	batch := "- id: User\n  fields:\n    - id: id\n      type: string\n- id: Order\n  fields:\n    - id: id\n      type: string\n"
	stdout, _, err := runEntityStdin(t, batch, "entity", "add", "-d", dir)
	assert.NoError(t, err)

	assert.FileExists(t, filepath.Join(dir, "entities", "User", "User.entity.json"))
	assert.FileExists(t, filepath.Join(dir, "entities", "Order", "Order.entity.json"))

	out := stdout.String()
	assert.Contains(t, out, "User")
	assert.Contains(t, out, "Order")
}

// The atomic batch path must produce byte-identical on-disk JSON to the
// single-entity store path (SaveEntity), so curated files stay consistent.
func TestEntityAdd_BatchMatchesStoreFormat(t *testing.T) {
	dirBatch := t.TempDir()
	dirStore := t.TempDir()

	// single via store path (one object, not a list)
	_, _, err := runEntityStdin(t, "id: User\nfields:\n  - id: id\n    type: string\n", "entity", "add", "-d", dirStore)
	require.NoError(t, err)
	// single via batch list of one
	_, _, err = runEntityStdin(t, "- id: User\n  fields:\n    - id: id\n      type: string\n", "entity", "add", "-d", dirBatch)
	require.NoError(t, err)

	storeData, err := os.ReadFile(filepath.Join(dirStore, "entities", "User", "User.entity.json"))
	require.NoError(t, err)
	batchData, err := os.ReadFile(filepath.Join(dirBatch, "entities", "User", "User.entity.json"))
	require.NoError(t, err)
	assert.Equal(t, string(storeData), string(batchData), "batch write must match store on-disk format")
}

// loadEntityFields is a test helper: it reads the on-disk entity file and
// returns a map of field id -> type for assertions.
func loadEntityFields(t *testing.T, dir, name string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "entities", name, name+".entity.json"))
	require.NoError(t, err)
	var ent struct {
		Fields []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(data, &ent))
	out := map[string]string{}
	for _, f := range ent.Fields {
		out[f.ID] = f.Type
	}
	return out
}

// AC: field-add-additive — adding a new field to an existing entity adds it and
// leaves the pre-existing field unchanged.
func TestEntityFieldAdd_Additive(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runEntityStdin(t, "id: User\nfields:\n  - id: id\n    type: integer\n", "entity", "add", "-d", dir)
	require.NoError(t, err)

	_, _, err = runEntityStdin(t, "id: primaryCurrency\ntype: string\n", "entity", "field", "add", "User", "-d", dir)
	assert.NoError(t, err)

	fields := loadEntityFields(t, dir, "User")
	assert.Equal(t, "string", fields["primaryCurrency"], "primaryCurrency must be added")
	assert.Equal(t, "integer", fields["id"], "id must be unchanged")
}

// AC: field-add-rejects-existing — adding a field named like an existing one
// fails non-zero and leaves the entity unchanged.
func TestEntityFieldAdd_RejectsExisting(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runEntityStdin(t, "id: User\nfields:\n  - id: id\n    type: integer\n", "entity", "add", "-d", dir)
	require.NoError(t, err)

	entityPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	before, err := os.ReadFile(entityPath)
	require.NoError(t, err)

	_, _, err = runEntityStdin(t, "id: id\ntype: integer\n", "entity", "field", "add", "User", "-d", dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "id")

	after, err := os.ReadFile(entityPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "entity must be left unchanged")
}

// AC: no-implicit-override — adding a field id with a different type never
// overwrites the existing field; it fails non-zero and the type is preserved.
func TestEntityFieldAdd_NoImplicitOverride(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runEntityStdin(t, "id: User\nfields:\n  - id: id\n    type: integer\n", "entity", "add", "-d", dir)
	require.NoError(t, err)

	_, _, err = runEntityStdin(t, "id: id\ntype: string\n", "entity", "field", "add", "User", "-d", dir)
	assert.Error(t, err)

	fields := loadEntityFields(t, dir, "User")
	assert.Equal(t, "integer", fields["id"], "id type must remain integer (never overwritten)")
}

// AC: field-set-updates — setting a new type on an existing field updates the
// type and exits zero.
func TestEntityFieldSet_UpdatesType(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runEntityStdin(t, "id: User\nfields:\n  - id: primaryCurrency\n    type: string\n", "entity", "add", "-d", dir)
	require.NoError(t, err)

	_, _, err = runEntity(t, "entity", "field", "set", "User", "primaryCurrency", "--type", "currency", "-d", dir)
	assert.NoError(t, err)

	fields := loadEntityFields(t, dir, "User")
	assert.Equal(t, "currency", fields["primaryCurrency"], "primaryCurrency type must become currency")
}

// AC: field-set-key-flag — --key promotes a non-key field to a key field and
// exits zero.
func TestEntityFieldSet_KeyFlag(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runEntityStdin(t, "id: User\nfields:\n  - id: email\n    type: string\n", "entity", "add", "-d", dir)
	require.NoError(t, err)

	_, _, err = runEntity(t, "entity", "field", "set", "User", "email", "--key", "-d", dir)
	assert.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "entities", "User", "User.entity.json"))
	require.NoError(t, err)
	var ent struct {
		Fields []struct {
			ID         string `json:"id"`
			IsKeyField bool   `json:"isKeyField"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(data, &ent))
	var keyed bool
	for _, f := range ent.Fields {
		if f.ID == "email" {
			keyed = f.IsKeyField
		}
	}
	assert.True(t, keyed, "email must become a key field")
}

// AC: field-set-missing-errors — setting attributes on a non-existent field
// fails non-zero and leaves the entity unchanged.
func TestEntityFieldSet_MissingErrors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runEntityStdin(t, "id: User\nfields:\n  - id: id\n    type: string\n", "entity", "add", "-d", dir)
	require.NoError(t, err)

	entityPath := filepath.Join(dir, "entities", "User", "User.entity.json")
	before, err := os.ReadFile(entityPath)
	require.NoError(t, err)

	_, _, err = runEntity(t, "entity", "field", "set", "User", "xyz", "--title", "X", "-d", dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "xyz")

	after, err := os.ReadFile(entityPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "entity must be left unchanged")
}

// field add on a non-existent entity fails non-zero with a not-found error.
func TestEntityFieldAdd_EntityNotFound(t *testing.T) {
	dir := t.TempDir()

	_, _, err := runEntityStdin(t, "id: primaryCurrency\ntype: string\n", "entity", "field", "add", "Ghost", "-d", dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// An invalid --format value is rejected with a non-zero error.
func TestEntityAdd_InvalidFormat_Errors(t *testing.T) {
	dir := t.TempDir()
	stdin := "id: User\nfields:\n  - id: id\n    type: string\n"

	_, _, err := runEntityStdin(t, stdin, "entity", "add", "-d", dir, "--format", "xml")
	assert.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "entities"))
	assert.True(t, os.IsNotExist(statErr), "nothing must be written on invalid format")
}
