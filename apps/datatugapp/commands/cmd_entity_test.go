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

// An invalid --format value is rejected with a non-zero error.
func TestEntityAdd_InvalidFormat_Errors(t *testing.T) {
	dir := t.TempDir()
	stdin := "id: User\nfields:\n  - id: id\n    type: string\n"

	_, _, err := runEntityStdin(t, stdin, "entity", "add", "-d", dir, "--format", "xml")
	assert.Error(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "entities"))
	assert.True(t, os.IsNotExist(statErr), "nothing must be written on invalid format")
}
