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
// before) is not found where the rest of the CLI looks, and the answer says where the file is and where
// it belongs, by the folder and by the registered name.
func TestShowDoesNotLookOneFolderDeeper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	folder := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(folder, "datatug"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "datatug", "datatug-project.json"), []byte(`{"id":"deep"}`), 0o644))
	want := `"` + folder + `" is not a DataTug project: its project file is in the folder datatug, where an earlier version of the terminal UI wrote it; ` +
		`to keep this project, move datatug/datatug-project.json up into "` + folder + `" and add to it "access": "private" and "created": {"at": "<a time, such as 2026-01-02T15:04:05Z>"}, which a scan needs; ` +
		`or make a new project in another folder with datatug scan -d "<new folder>" -D sqlite3 --path <database file> --db <name> --env <environment>`
	stdout, _, err := runShowCommand(t, "-d", folder)
	assert.Equal(t, 3, showExitCodeOf(t, err))
	require.Error(t, err)
	assert.Equal(t, want, err.Error())
	assert.Empty(t, stdout)

	registerProjectForTest(t, "deep", folder)
	_, _, err = runShowCommand(t, "-p", "deep")
	assert.Equal(t, 3, showExitCodeOf(t, err))
	assert.EqualError(t, err, want)
}

// The remedy that show gives for a project file one folder deeper is a whole one: a person who does what it
// says (moves the file up and adds the two fields it names) can list the project and scan into it. Without
// the two fields the scan refuses the file, which is why the sentence names them.
func TestShowRemedyForTheOldWizardPathWorksWhenFollowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	folder := t.TempDir()
	deep := filepath.Join(folder, "datatug", "datatug-project.json")
	require.NoError(t, os.Mkdir(filepath.Join(folder, "datatug"), 0o755))
	require.NoError(t, os.WriteFile(deep, []byte(`{"id":"deep","title":"Deep"}`), 0o644))
	_, _, err := runShowCommand(t, "-d", folder)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"access": "private" and "created": {"at": "`)

	// Step 1 of the sentence: move the file up. The project is listed, and a scan into it is refused.
	moved := filepath.Join(folder, "datatug-project.json")
	require.NoError(t, os.Rename(deep, moved))
	require.NoError(t, os.Remove(filepath.Join(folder, "datatug")))
	stdout, _, err := runShowCommand(t, "-d", folder)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Project deep\n")
	dbPath := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, dbPath)
	scanArgs := []string{"-d", folder, "-D", "sqlite3", "--path", dbPath, "--db", "shop", "--env", "local"}
	_, err = runScanCommand(t, scanArgs...)
	require.Error(t, err, "the moved file has no access and no creation time: that is why the sentence says to add them")

	// Step 2: add the two fields, as the sentence says.
	require.NoError(t, os.WriteFile(moved, []byte(`{"id":"deep","title":"Deep","access":"private","created":{"at":"2026-01-02T15:04:05Z"}}`), 0o644))
	_, err = runScanCommand(t, scanArgs...)
	require.NoError(t, err)
	stdout, _, err = runShowCommand(t, "-d", folder)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Project deep\nEnvironment local\n  Source shop (sqlite3)\n")
}
