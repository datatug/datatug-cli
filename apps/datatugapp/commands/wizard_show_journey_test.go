package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The journey of a person who makes a project in the terminal UI: the project the wizard creates is
// the project the rest of the CLI reads (issue 263 of this repository: the wizard wrote its project file
// one folder deeper than every reader looked), so `datatug show` lists it, by its folder and by its name,
// and a scan into it is listed too.
func TestProjectCreatedInTheWizardIsReadByTheRestOfTheCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the settings of this test's user: the wizard registers the project there
	location := t.TempDir()

	ref, err := dtproject.CreateLocalProject("wizard-made", `Made "in" the wizard`, location)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(location, "wizard-made"), ref.Path)

	empty := "Project wizard-made\nNo database has been scanned into this project yet: scan one with datatug scan.\n"
	stdout, _, err := runShowCommand(t, "-d", ref.Path)
	require.NoError(t, err)
	assert.Equal(t, empty, stdout)
	stdout, _, err = runShowCommand(t, "-p", "wizard-made")
	require.NoError(t, err)
	assert.Equal(t, empty, stdout, "the registered name leads to the same folder")

	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	_, err = runScanCommand(t, "-d", ref.Path, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local")
	require.NoError(t, err)
	stdout, _, err = runShowCommand(t, "-d", ref.Path)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Project wizard-made\nEnvironment local\n  Source shop (sqlite3)\n")
	assert.Contains(t, stdout, "      Table Customer\n        CustomerId INTEGER pk\n")
	_, err = os.Stat(filepath.Join(ref.Path, "datatug"))
	assert.True(t, os.IsNotExist(err), "no folder datatug inside the project")
}

// A project whose project file is in a folder datatug of the project folder (what the wizard wrote
// before) is not found where the rest of the CLI looks.
func TestShowDoesNotLookOneFolderDeeper(t *testing.T) {
	folder := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(folder, "datatug"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "datatug", "datatug-project.json"), []byte(`{"id":"deep"}`), 0o644))
	_, _, err := runShowCommand(t, "-d", folder)
	assert.Equal(t, 3, exitCodeOf(t, err))
}
