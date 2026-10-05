package commands

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A command that writes into a project, and is given the project folder with -d, stops when the
// last part of that folder is a link: a folder in a repository that somebody else made may be a
// link to anywhere, so the command prints the folder it leads to and writes nothing, unless it is
// told with --follow-project-link to go on. Commands that only read are not changed.

// linkedProjectFolder is a project folder, with one scanned database in it, and a link to it:
// what a project folder cloned from someone else may be.
func linkedProjectFolder(t *testing.T) (linked, resolved string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows; the refusal there is covered with a faked Lstat")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real-project")
	require.NoError(t, os.Mkdir(real, 0o755))
	linked = filepath.Join(base, "linked-project")
	require.NoError(t, os.Symlink(real, linked))
	resolved, err := filepath.EvalSymlinks(linked)
	require.NoError(t, err)
	return linked, resolved
}

func TestScanStopsAtAProjectFolderThatIsALink(t *testing.T) {
	linked, resolved := linkedProjectFolder(t)
	db := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, db)
	args := func(dir string, more ...string) []string {
		return append([]string{"-d", dir, "-D", "sqlite3", "--path", db, "--db", "shop", "--env", "local"}, more...)
	}

	for _, dir := range []string{linked, linked + string(filepath.Separator)} {
		stderr, err := runScanCommand(t, args(dir)...)

		require.Error(t, err, "a project folder that is a link: %q", dir)
		assert.ErrorContains(t, err, resolved, "the folder it leads to is printed")
		assert.ErrorContains(t, err, "--follow-project-link", "and the flag that goes on")
		assert.Empty(t, stderr)
		assert.Empty(t, projectFiles(t, resolved, ""), "nothing was written")
	}

	_, err := runScanCommand(t, args(linked, "--follow-project-link")...)

	require.NoError(t, err, "with the flag the scan goes on")
	assert.FileExists(t, filepath.Join(resolved, "datatug-project.json"))
	assert.FileExists(t, filepath.Join(resolved, "dbmodels", "shop", "main", "tables", "Customer", "main.Customer.columns.json"))
}

func TestScanStopsAtTheWorkingDirectoryThatIsALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the working directory is not kept as the path it was entered by on Windows")
	}
	linked, resolved := linkedProjectFolder(t)
	db := filepath.Join(t.TempDir(), "shop.db")
	writeJourneyDB(t, db)
	t.Chdir(linked) // keeps the name it was entered by, as a shell does (PWD)

	_, err := runScanCommand(t, "-d", ".", "-D", "sqlite3", "--path", db, "--db", "shop", "--env", "local")

	require.Error(t, err)
	assert.ErrorContains(t, err, resolved)
	assert.ErrorContains(t, err, "--follow-project-link")
	assert.Empty(t, projectFiles(t, resolved, ""))

	_, err = runScanCommand(t, "-d", ".", "-D", "sqlite3", "--path", db, "--db", "shop", "--env", "local", "--follow-project-link")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(resolved, "datatug-project.json"))
}

func TestPostgresScanStopsAtAProjectFolderThatIsALink(t *testing.T) {
	linked, resolved := linkedProjectFolder(t)
	opener := usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })

	_, err := runScanCommand(t, pgScanArgs(linked, journeyPgVar, "shop", "local")...)

	require.Error(t, err)
	assert.ErrorContains(t, err, resolved)
	assert.Empty(t, opener.opened, "the server is not opened for a scan that will not write")
	assert.Empty(t, projectFiles(t, resolved, ""))

	_, err = runScanCommand(t, pgScanArgs(linked, journeyPgVar, "shop", "local", "--follow-project-link")...)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(resolved, "connections", "local", "shop.json"))
}

func TestEntityCommandsStopAtAProjectFolderThatIsALink(t *testing.T) {
	linked, resolved := linkedProjectFolder(t)
	definition := writeEntityDefinition(t, "id: User\nfields:\n  - id: id\n    type: string\n")

	_, _, err := runEntity(t, "entity", "add", "-d", linked, "-f", definition)
	require.Error(t, err)
	assert.ErrorContains(t, err, resolved)
	assert.Empty(t, projectFiles(t, resolved, ""), "nothing was written")

	_, _, err = runEntity(t, "entity", "add", "-d", linked, "-f", definition, "--follow-project-link")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(resolved, "entities", "User", "User.entity.json"))

	for _, args := range [][]string{
		{"entity", "field", "add", "User", "-d", linked, "-f", writeEntityDefinition(t, "id: email\ntype: string\n")},
		{"entity", "field", "set", "User", "id", "-d", linked, "--title", "Id"},
		{"entity", "field", "rm", "User", "id", "-d", linked},
	} {
		before := treeWithLinks(t, resolved)
		_, _, err = runEntity(t, args...)
		require.Error(t, err, args)
		assert.ErrorContains(t, err, resolved, args)
		assert.Equal(t, before, treeWithLinks(t, resolved), args)

		_, _, err = runEntity(t, append(args, "--follow-project-link")...)
		require.NoError(t, err, args)
		assert.NotEqual(t, before, treeWithLinks(t, resolved), "with the flag it goes on: %v", args)
	}
}

// A command that only reads is as it was.
func TestReadingCommandsDoNotStopAtAProjectFolderThatIsALink(t *testing.T) {
	linked, resolved := linkedProjectFolder(t)
	_, _, err := runEntity(t, "entity", "add", "-d", linked, "-f", writeEntityDefinition(t, "id: User\nfields:\n  - id: id\n    type: string\n"), "--follow-project-link")
	require.NoError(t, err)

	for _, args := range [][]string{{"entity", "list", "-d", linked}, {"entity", "show", "User", "-d", linked}} {
		stdout, _, readErr := runEntity(t, args...)
		require.NoError(t, readErr, args)
		assert.Contains(t, stdout.String(), "User", args)
	}
	assert.FileExists(t, filepath.Join(resolved, "entities", "User", "User.entity.json"))
	for _, name := range []string{"list", "show"} {
		cmd, _, findErr := entityCommand().Find([]string{name})
		require.NoError(t, findErr)
		assert.Nil(t, cmd.Flags().Lookup("follow-project-link"), "entity %s reads, and has no flag for writing", name)
	}
}

func TestInitStopsAtAProjectFolderThatIsALink(t *testing.T) {
	linked, resolved := linkedProjectFolder(t)

	err := runInit(t, "shop", linked)
	require.Error(t, err)
	assert.ErrorContains(t, err, resolved)
	assert.Empty(t, projectFiles(t, resolved, ""))

	require.NoError(t, runInit(t, "shop", linked, "--follow-project-link"))
	assert.FileExists(t, filepath.Join(resolved, "datatug-project.json"))
}

func runInit(t *testing.T, args ...string) error {
	t.Helper()
	root := DatatugCommand()
	root.SetArgs(append([]string{"init"}, args...))
	root.SilenceUsage, root.SilenceErrors = true, true
	return root.Execute()
}

// The flag is named in the help of every command that writes into a project, with what it does.
func TestFollowProjectLinkFlagSaysWhatItDoes(t *testing.T) {
	root := DatatugCommand()
	var writers []*cobra.Command
	for _, path := range [][]string{{"scan"}, {"init"}, {"entity", "add"}, {"entity", "field", "add"}, {"entity", "field", "set"}, {"entity", "field", "rm"}} {
		cmd, _, err := root.Find(path)
		require.NoError(t, err)
		writers = append(writers, cmd)
		flag := cmd.Flags().Lookup("follow-project-link")
		require.NotNil(t, flag, strings.Join(path, " "))
		assert.Contains(t, flag.Usage, "link")
		assert.Contains(t, flag.Usage, "folder")
		assert.Equal(t, "false", flag.DefValue)
	}
	assert.Len(t, writers, 6)
}

func TestCheckProjectFolderLink(t *testing.T) {
	boom := errors.New("cannot look")
	setLstat := func(t *testing.T, lstat func(string) (fs.FileInfo, error)) {
		t.Helper()
		original := projectFolderLstat
		projectFolderLstat = lstat
		t.Cleanup(func() { projectFolderLstat = original })
	}

	t.Run("a folder, a folder that is not there and a file are not links", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "file")
		require.NoError(t, os.WriteFile(file, nil, 0o600))
		for _, path := range []string{dir, filepath.Join(dir, "not-there"), file, filepath.Join(dir, "not-there", "deeper")} {
			assert.NoError(t, checkProjectFolderLink(path, false), path)
		}
	})
	t.Run("a folder that cannot be looked at is left to the checks that name it", func(t *testing.T) {
		setLstat(t, func(string) (fs.FileInfo, error) { return nil, boom })
		assert.NoError(t, checkProjectFolderLink("anywhere", false))
	})
	t.Run("a link that leads to a folder that is not there says so", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("making a symbolic link needs a privilege on Windows")
		}
		dir := t.TempDir()
		require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "broken")))

		err := checkProjectFolderLink(filepath.Join(dir, "broken"), false)

		require.Error(t, err)
		assert.ErrorContains(t, err, "leads to a folder that is not there")
		assert.ErrorContains(t, err, "--follow-project-link")
		assert.NoError(t, checkProjectFolderLink(filepath.Join(dir, "broken"), true))
	})
	t.Run("a Windows junction is a link: Lstat reports it as irregular", func(t *testing.T) {
		dir := t.TempDir()
		setLstat(t, func(path string) (fs.FileInfo, error) {
			info, err := os.Lstat(path)
			return junctionInfo{info}, err
		})

		err := checkProjectFolderLink(dir, false)

		require.Error(t, err)
		resolved, evalErr := filepath.EvalSymlinks(dir)
		require.NoError(t, evalErr)
		assert.ErrorContains(t, err, resolved)
	})
	t.Run("the working directory, when the folder is the working directory", func(t *testing.T) {
		original := projectFolderGetwd
		t.Cleanup(func() { projectFolderGetwd = original })
		projectFolderGetwd = func() (string, error) { return "", boom }

		err := checkProjectFolderLink(".", false)

		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "working directory")
		assert.NoError(t, checkProjectFolderLink(".", true), "a person who follows it does not need it looked at")
	})
}

// junctionInfo is what Lstat reports of a Windows junction: not a link, and not a folder.
type junctionInfo struct{ fs.FileInfo }

func (junctionInfo) Mode() fs.FileMode { return fs.ModeIrregular }
func (junctionInfo) IsDir() bool       { return false }
