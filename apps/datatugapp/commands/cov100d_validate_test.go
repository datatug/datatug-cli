package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covDInitProject(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := initCommand()
	cmd.SetArgs([]string{id, dir})
	require.NoError(t, cmd.Execute())
	return dir
}

func covDRunValidate(dir string) error {
	cmd := testCommandArgs()
	cmd.SetArgs([]string{"--dir", dir})
	return cmd.Execute()
}

func TestCovDValidateAction(t *testing.T) {
	t.Run("unparseable root file is a real failure", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".datatug.yaml"), []byte("projects: ["), 0o600))
		err := covDRunValidate(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load root repo file")
	})
	t.Run("root file validates every listed project", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"p1", "p2"} {
			cmd := initCommand()
			cmd.SetArgs([]string{name, filepath.Join(root, name)})
			require.NoError(t, cmd.Execute())
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug.yaml"), []byte("projects:\n  - p1\n  - p2\n"), 0o600))
		require.NoError(t, covDRunValidate(root))
	})
	t.Run("root file reports the failing project", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug.yaml"), []byte("projects:\n  - missing\n"), 0o600))
		err := covDRunValidate(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to validate project #1 @ missing")
	})
}

func TestCovDValidateProject(t *testing.T) {
	t.Run("init requires a directory", func(t *testing.T) {
		require.Error(t, validateProject(""))
	})
	t.Run("nil store", func(t *testing.T) {
		dir := covDInitProject(t, "p1")
		covDSetVar(t, &validateProjectStore, func(*projectBaseCommand) datatug.ProjectStore { return nil })
		err := validateProject(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "project store is nil")
	})
	t.Run("invalid project", func(t *testing.T) {
		dir := covDInitProject(t, "p1")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"p1","access":"bogus"}`), 0o600))
		err := validateProject(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not valid")
	})
}
