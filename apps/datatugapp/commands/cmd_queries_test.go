package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queriesSetup builds the three folders `datatug queries` must survive, none of
// which may panic (a panic is also a telemetry event): an empty folder, a folder
// that is not a project, and a project with no query.
func queriesSetups(t *testing.T) map[string]func() string {
	t.Helper()
	return map[string]func() string{
		"empty folder": func() string { return t.TempDir() },
		"not a project": func() string {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600))
			return dir
		},
		"project with no query": func() string { return queriesProject(t) },
	}
}

func queriesProject(t *testing.T, queryFiles ...string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"p"}`), 0o600))
	for _, name := range queryFiles {
		file := filepath.Join(dir, "queries", filepath.FromSlash(name)+".query.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(`{}`), 0o600))
	}
	return dir
}

func runQueries(t *testing.T, argv ...string) (stdout string, err error) {
	t.Helper()
	out, _, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(queriesCommand()) }, append([]string{"queries"}, argv...)...)
	return out.String(), err
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var ec ExitCoder
	require.ErrorAs(t, err, &ec)
	return ec.ExitCode()
}

// Item 5 of the telemetry task: `datatug queries` and each of its sub-commands,
// run in an empty folder, a folder that is not a project and a project with no
// query, never panic.
func TestQueries_EveryCommandSurvivesTheThreeFolders(t *testing.T) {
	names := []string{""}
	for _, sub := range queriesCommand().Commands() {
		names = append(names, sub.Name())
	}
	for setup, makeDir := range queriesSetups(t) {
		for _, sub := range names {
			t.Run(setup+"/"+sub, func(t *testing.T) {
				t.Chdir(makeDir())
				argv := []string{}
				if sub != "" {
					argv = append(argv, sub)
				}
				assert.NotPanics(t, func() { _, _ = runQueries(t, argv...) })
			})
		}
	}
}

func TestQueries_NotAProjectIsExit3(t *testing.T) {
	for _, setup := range []string{"empty folder", "not a project"} {
		t.Run(setup, func(t *testing.T) {
			t.Chdir(queriesSetups(t)[setup]())
			out, err := runQueries(t)
			require.Error(t, err)
			assert.Equal(t, 3, exitCodeOf(t, err))
			assert.Contains(t, err.Error(), "not a DataTug project")
			assert.Empty(t, out)
		})
	}
}

func TestQueries_ProjectWithNoQueryPrintsNothing(t *testing.T) {
	t.Chdir(queriesProject(t))
	out, err := runQueries(t)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestQueries_ListsOneIDPerLine(t *testing.T) {
	t.Chdir(queriesProject(t, "top", "sales/by-region", "sales/by-month/2024"))
	out, err := runQueries(t)
	require.NoError(t, err)
	assert.Equal(t, "sales/by-month/2024\nsales/by-region\ntop\n", out)
}

func TestQueries_DirAndProjectFlags(t *testing.T) {
	project := queriesProject(t, "q1")
	t.Chdir(t.TempDir())

	out, err := runQueries(t, "--dir", project)
	require.NoError(t, err)
	assert.Equal(t, "q1\n", out)

	// --project takes a directory too, as `query run` does.
	out, err = runQueries(t, "-p", project)
	require.NoError(t, err)
	assert.Equal(t, "q1\n", out)

	_, err = runQueries(t, "--dir", t.TempDir())
	assert.Equal(t, 3, exitCodeOf(t, err))

	_, err = runQueries(t, "--project", "no-such-project-registered")
	require.Error(t, err)
	assert.Equal(t, 3, exitCodeOf(t, err))

	_, err = runQueries(t, "--project", "x", "--dir", project)
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "--project")
	assert.Contains(t, err.Error(), "--dir")
}

func TestQueries_FailuresAreErrorsNotPanics(t *testing.T) {
	t.Run("no working directory", func(t *testing.T) {
		cov100fSetVar(t, &queriesGetwd, func() (string, error) { return "", errors.New("cwd boom") })
		_, err := runQueries(t)
		require.Error(t, err)
		assert.Equal(t, 3, exitCodeOf(t, err))
	})
	t.Run("the listing fails", func(t *testing.T) {
		t.Chdir(queriesProject(t))
		cov100fSetVar(t, &queriesIndex, func(string) (map[string]string, error) { return nil, errors.New("walk boom") })
		_, err := runQueries(t)
		require.EqualError(t, err, "walk boom")
	})
}

// A query whose file name is not a plain name is not echoed (it may name a
// path or hold a secret) and is not printed as an ID either: a script that pipes
// the listing into `query run` must never receive a line that names no query.
// Stdout holds only real IDs; stderr says how many were left out.
func TestQueries_ANonPlainIDIsSkippedNotPrintedAsAnID(t *testing.T) {
	t.Chdir(queriesProject(t, "fine", "folder/ok-one", "with space and $ymbols", "folder/also bad!"))
	out, errOut, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(queriesCommand()) }, "queries")
	require.NoError(t, err)
	assert.Equal(t, "fine\nfolder/ok-one\n", out.String(), "stdout holds the plain IDs and nothing else")
	assert.NotContains(t, out.String()+errOut.String(), "$ymbols")
	assert.NotContains(t, out.String()+errOut.String(), "bad!")
	assert.NotContains(t, out.String(), "not shown")
	assert.Equal(t, "2 saved queries skipped: their IDs are not plain names (letters, digits, '.', '_' and '-', with '/' between folders)\n", errOut.String())
}

func TestQueries_OneSkippedQueryIsSingular(t *testing.T) {
	t.Chdir(queriesProject(t, "with space"))
	out, errOut, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(queriesCommand()) }, "queries")
	require.NoError(t, err)
	assert.Empty(t, out.String())
	assert.Contains(t, errOut.String(), "1 saved query skipped")
}

func TestQueries_PlainIDsPrintNothingOnStderr(t *testing.T) {
	t.Chdir(queriesProject(t, "a", "b/c"))
	_, errOut, err := cov100fRun(t, func(r *cobra.Command) { r.AddCommand(queriesCommand()) }, "queries")
	require.NoError(t, err)
	assert.Empty(t, errOut.String())
}

// A query whose name is the placeholder text itself is skipped like any other
// non-plain name: only IsPlainSourceID decides.
func TestQueries_ThePlaceholderTextIsNotAPlainID(t *testing.T) {
	assert.False(t, plainQueryID(dbcopy.SourceIDNotShown))
	assert.False(t, plainQueryID("a//b"))
	assert.False(t, plainQueryID(""))
	assert.True(t, plainQueryID("a/b-c_d.e"))
}
