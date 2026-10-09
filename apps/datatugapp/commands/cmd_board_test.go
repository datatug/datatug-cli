package commands

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runBoard(t *testing.T, args ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	return cov100fRun(t, func(r *cobra.Command) { r.AddCommand(boardCommand()) }, append([]string{"board"}, args...)...)
}

func writeBoardFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// boardProject builds a project with no board.
func boardProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeBoardFile(t, filepath.Join(dir, "datatug-project.json"), `{"id":"p"}`)
	return dir
}

// boardProjectWithBoards adds a nested board "alpha" (titled), a flat board "beta"
// (untitled) and a board whose ID is not a plain name.
func boardProjectWithBoards(t *testing.T) string {
	t.Helper()
	dir := boardProject(t)
	writeBoardFile(t, filepath.Join(dir, "boards", "alpha", "board.json"), `{"title":"Alpha board"}`)
	writeBoardFile(t, filepath.Join(dir, "boards", "beta.board.json"), `{}`)
	writeBoardFile(t, filepath.Join(dir, "boards", "not plain", "board.json"), `{"title":"x"}`)
	return dir
}

// snapshotTree lists every path of dir with its size and content.
func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		info, err := d.Info()
		require.NoError(t, err)
		sb.WriteString(p + "|" + info.Mode().String() + "|")
		if !d.IsDir() {
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			sb.WriteString(string(data))
		}
		sb.WriteString("\n")
		return nil
	}))
	return sb.String()
}

func TestBoardListListsBoards(t *testing.T) {
	dir := boardProjectWithBoards(t)
	before := snapshotTree(t, dir)

	stdout, stderr, err := runBoard(t, "list", "-d", dir)
	require.NoError(t, err)
	assert.Equal(t, "alpha\nbeta\n", stdout.String())
	assert.Equal(t, "1 board skipped: their IDs are not plain names (letters, digits, '.', '_' and '-')\n", stderr.String())

	stdout2, stderr2, err := runBoard(t, "list", "-d", dir)
	require.NoError(t, err)
	assert.Equal(t, stdout.String(), stdout2.String())
	assert.Equal(t, stderr.String(), stderr2.String())
	assert.Equal(t, before, snapshotTree(t, dir), "board list must not change any file")
}

func TestBoardListCountsSeveralSkippedBoards(t *testing.T) {
	dir := boardProjectWithBoards(t)
	writeBoardFile(t, filepath.Join(dir, "boards", "also bad", "board.json"), `{}`)
	_, stderr, err := runBoard(t, "list", "-d", dir)
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "2 boards skipped")
}

func TestBoardListJSONListsBoards(t *testing.T) {
	dir := boardProjectWithBoards(t)
	stdout, _, err := runBoard(t, "list", "-d", dir, "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, "[\n  {\n    \"id\": \"alpha\",\n    \"title\": \"Alpha board\"\n  },\n  {\n    \"id\": \"beta\"\n  }\n]\n", stdout.String())

	empty := boardProject(t)
	stdout, _, err = runBoard(t, "list", "-d", empty, "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, "[]\n", stdout.String())

	stdout, stderr, err := runBoard(t, "list", "-d", empty)
	require.NoError(t, err)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())

	stdout, _, err = runBoard(t, "list", "-d", empty, "--format", "yaml")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, `"yaml"`)
	assert.Empty(t, stdout.String())
}

func TestBoardListNotAProjectIsNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // nothing is registered
	folder := t.TempDir()
	stdout, _, err := runBoard(t, "list", "-d", folder)
	require.Error(t, err)
	assert.Equal(t, 3, exitCodeOf(t, err))
	assert.ErrorContains(t, err, folder)
	assert.ErrorContains(t, err, "not a DataTug project")
	assert.Empty(t, stdout.String())

	// a path that is a file holds no project file either
	file := filepath.Join(folder, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	_, _, err = runBoard(t, "list", "-d", file)
	assert.Equal(t, 3, exitCodeOf(t, err))

	stdout, _, err = runBoard(t, "list", "-p", "p", "-d", folder)
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.ErrorContains(t, err, "--project")
	assert.ErrorContains(t, err, "--directory")
	assert.Empty(t, stdout.String())

	stdout, _, err = runBoard(t, "list", "-p", "no-such-project")
	require.Error(t, err)
	assert.Equal(t, 3, exitCodeOf(t, err))
	assert.ErrorContains(t, err, `"no-such-project"`)
	assert.Empty(t, stdout.String())
}

func TestBoardListRegisteredProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := boardProjectWithBoards(t)
	registerProjectForTest(t, "shop", dir)
	stdout, _, err := runBoard(t, "list", "-p", "Shop")
	require.NoError(t, err)
	assert.Equal(t, "alpha\nbeta\n", stdout.String())

	_, _, err = runBoard(t, "list", "-p", "other")
	assert.Equal(t, 3, exitCodeOf(t, err), "a name that is not among the registered ones")
}

func TestBoardListSettingsFailureIsNotAnUnknownProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".datatug.yaml"), 0o755))
	_, _, err := runBoard(t, "list", "-p", "anything")
	require.Error(t, err)
	var ec interface{ ExitCode() int }
	assert.False(t, errors.As(err, &ec) && ec.ExitCode() == 3)
}

func TestBoardListUnloadableBoardFails(t *testing.T) {
	dir := boardProjectWithBoards(t)
	writeBoardFile(t, filepath.Join(dir, "boards", "broken", "board.json"), `not json`)
	stdout, _, err := runBoard(t, "list", "-d", dir)
	require.Error(t, err)
	assert.Equal(t, 1, exitCodeOrOne(err))
	assert.ErrorContains(t, err, "broken")
	assert.Empty(t, stdout.String())
}

// exitCodeOrOne is the exit code of err: that of an ExitCoder, or 1 for any other error.
func exitCodeOrOne(err error) int {
	var ec ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

func TestBoardBareResourceShowsHelp(t *testing.T) {
	stdout, _, err := runBoard(t)
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "list")
	assert.Contains(t, stdout.String(), "board")
}

func TestBoardListDirIsTheFlagDirectory(t *testing.T) {
	dir := boardProjectWithBoards(t)
	stdout, _, err := runBoard(t, "list", "--dir", dir)
	require.NoError(t, err)
	assert.Equal(t, "alpha\nbeta\n", stdout.String())
}

func TestBoardListUsesTheCurrentFolder(t *testing.T) {
	t.Chdir(boardProjectWithBoards(t))
	stdout, _, err := runBoard(t, "list")
	require.NoError(t, err)
	assert.Equal(t, "alpha\nbeta\n", stdout.String())
}

func TestBoardListFailsWhenStdoutCannotBeWritten(t *testing.T) {
	root := &cobra.Command{Use: "datatug", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(boardCommand())
	root.SetOut(failingWriter{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"board", "list", "-d", boardProjectWithBoards(t)})
	err := root.Execute()
	require.ErrorIs(t, err, errWriteFailed)
	assert.Equal(t, 1, exitCodeOrOne(err))
}
