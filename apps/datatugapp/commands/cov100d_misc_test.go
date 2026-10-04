package commands

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

func TestCovDStageFilesErrors(t *testing.T) {
	t.Run("repository without a worktree", func(t *testing.T) {
		dir := t.TempDir()
		_, err := git.PlainInit(dir, false)
		require.NoError(t, err)
		covDSetVar(t, &repoWorktree, func(*git.Repository) (*git.Worktree, error) { return nil, git.ErrIsBareRepository })
		err = stageFiles(dir, []string{filepath.Join(dir, "f")})
		require.ErrorIs(t, err, git.ErrIsBareRepository)
	})
	t.Run("path relative to nothing", func(t *testing.T) {
		dir := t.TempDir()
		_, err := git.PlainInit(dir, false)
		require.NoError(t, err)
		err = stageFiles(dir, []string{"relative/missing.txt"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to resolve")
	})
	t.Run("file that does not exist cannot be staged", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		require.NoError(t, err)
		_, err = git.PlainInit(dir, false)
		require.NoError(t, err)
		err = stageFiles(dir, []string{filepath.Join(dir, "missing.txt")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to stage")
	})
}

// covDCaptureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func covDCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	func() {
		defer func() { os.Stdout = orig }()
		fn()
	}()
	require.NoError(t, w.Close())
	out := <-done
	require.NoError(t, r.Close())
	return out
}

func TestCovDDatasetDataCommand(t *testing.T) {
	sample := datatug.Recordset{
		Columns: []datatug.RecordsetColumn{{Name: "id"}, {Name: "meta"}},
		Rows:    [][]any{{1, map[string]any{"k": "v"}}},
	}
	run := func(t *testing.T, format, indent string) (string, error) {
		t.Helper()
		covDSetVar(t, &datasetDataInitProject, func(v *datasetDataCommand) error {
			v.File, v.Format, v.Indent = "x", format, indent
			v.store = cov100fStore{proj: cov100fRS{data: sample}}
			return nil
		})
		var err error
		out := covDCaptureStdout(t, func() { err = datasetDataCommandAction(nil, nil) })
		return out, err
	}
	t.Run("default project initialisation needs a project", func(t *testing.T) {
		require.Error(t, datasetDataInitProject(&datasetDataCommand{}))
	})
	t.Run("yaml indent", func(t *testing.T) {
		def, err := run(t, "", "") // empty format means yaml
		require.NoError(t, err)
		assert.Equal(t, "- id: 1\n  meta:\n    k: v\n", def)

		for indent, want := range map[string]string{
			"8":   "- id: 1\n  meta:\n        k: v\n",
			"3":   "- id: 1\n  meta:\n   k: v\n",
			"TAB": "- id: 1\n  meta:\n    k: v\n",
			"tab": "- id: 1\n  meta:\n    k: v\n",
			"x":   def, // not a digit: default indentation
		} {
			out, err := run(t, "YAML", indent)
			require.NoError(t, err, indent)
			assert.Equal(t, want, out, "indent %q", indent)
		}
	})
	t.Run("json indent", func(t *testing.T) {
		def, err := run(t, "json", "")
		require.NoError(t, err)
		assert.Equal(t, "[\n {\n  \"id\": 1,\n  \"meta\": {\n   \"k\": \"v\"\n  }\n }\n]\n", def)

		out, err := run(t, "json", "3")
		require.NoError(t, err)
		assert.Equal(t, "[\n   {\n      \"id\": 1,\n      \"meta\": {\n         \"k\": \"v\"\n      }\n   }\n]\n", out)

		out, err = run(t, "json", "TAB")
		require.NoError(t, err)
		assert.Equal(t, "[\n\t{\n\t\t\"id\": 1,\n\t\t\"meta\": {\n\t\t\t\"k\": \"v\"\n\t\t}\n\t}\n]\n", out)

		out, err = run(t, "json", "12") // multi-digit counts are honoured for json
		require.NoError(t, err)
		assert.Contains(t, out, "\n"+strings.Repeat(" ", 12)+"{\n")

		_, err = run(t, "json", "x") // not a number: the indent is used verbatim
		require.NoError(t, err)
	})
	t.Run("unknown format is rejected", func(t *testing.T) {
		out, err := run(t, "xml", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown or unsopported format")
		assert.Empty(t, out)
	})
	t.Run("grid format opens the grid shell", func(t *testing.T) {
		called := false
		covDSetVar(t, &runShell, func(nav.Model, ...tea.ProgramOption) error { called = true; return nil })
		_, err := run(t, "grid", "")
		require.NoError(t, err)
		assert.True(t, called, "grid format must open the shell")
	})
}

func TestCovDProjectsAddSettingsErrors(t *testing.T) {
	t.Run("unreadable settings", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		require.NoError(t, os.WriteFile(filepath.Join(home, ".datatug.yaml"), []byte("projects: ["), 0o600))
		v := &addProjectCommand{}
		v.ProjectName, v.ProjectDir = "p", "/x"
		err := v.Execute(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read settings file")
	})
	t.Run("settings cannot be saved", func(t *testing.T) {
		covDSkipIfRoot(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		covDChmod(t, home, 0o500)
		v := &addProjectCommand{}
		v.ProjectName, v.ProjectDir = "p", "/x"
		err := v.Execute(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to save settings")
	})
}

func TestCovDInitProjectCommandStoreError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".datatug.yaml"), []byte("projects:\n  - id: p1\n    path: /x\n"), 0o600))
	covDSetVar(t, &newProjectsStore, func(string, map[string]string) (storage.Store, error) {
		return nil, errors.New("store boom")
	})
	v := &projectBaseCommand{ProjectName: "p1"}
	require.Error(t, v.initProjectCommand(projectCommandOptions{projNameRequired: true}))
}

func TestCovDRootCommandDefaultsToUI(t *testing.T) {
	modules, opts := stubUI(t, nil, errors.New("no state"))
	root := DatatugCommand()
	require.NoError(t, root.RunE(root, nil))
	assert.NotEmpty(t, *modules, "a bare datatug invocation must launch the UI with its modules")
	assert.Nil(t, opts.Initial, "the default UI opens no file")
}

func TestCovDShowProjectLoadError(t *testing.T) {
	v := &showProjectCommand{}
	v.ProjectDir = t.TempDir() // no project file in it
	err := v.Execute(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load project")
}

func TestCovDProjectsCommandSettingsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".datatug.yaml"), 0o755))
	err := projectsCommandAction(projectsCommandArgs(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get settings")
}

func TestCovDDBCommandRejectsUnparseableURL(t *testing.T) {
	cmd := dbCommand()
	cmd.SetOut(&bytes.Buffer{})
	var err error
	out := covDCaptureStdout(t, func() { err = cmd.RunE(cmd, []string{"a", "b"}) }) // no usable URL
	require.Error(t, err)
	// The error is returned and says nothing about the arguments: a database
	// URL can hold a password and the parser's own text quotes it.
	assert.Equal(t, "db url parse error: the argument is not a valid database URL", err.Error())
	assert.Empty(t, out)
}

func TestCovDExecuteSQLColumnTypesError(t *testing.T) {
	cov100eSetup(t)
	cov100eRowsClosed = true
	cov100eResults["q"] = cov100eResult{
		cols:  []string{"id"},
		types: []string{"INT"},
		data:  [][]driver.Value{{int64(1)}},
	}
	covDSetVar(t, &executeSQLColumnTypes, func(*sql.Rows) ([]*sql.ColumnType, error) { return nil, errors.New("ct failed") })
	v := cov100eCmd()
	v.CommandText = "q"
	err := v.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ct failed")
}
