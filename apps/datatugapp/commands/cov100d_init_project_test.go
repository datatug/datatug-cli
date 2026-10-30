package commands

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covDRunInit(args ...string) error {
	cmd := initCommand()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return cmd.Execute()
}

func TestCovDInitCommandActionErrors(t *testing.T) {
	t.Run("project directory cannot be created", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
		require.Error(t, covDRunInit("p1", filepath.Join(blocker, "sub")))
	})
	t.Run("stat failure other than not-exist", func(t *testing.T) {
		covDSetVar(t, &initStat, func(string) (os.FileInfo, error) { return nil, errors.New("stat boom") })
		err := covDRunInit("p1", t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get info about")
	})
	t.Run("directory already holds a project", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "datatug"), 0o755))
		err := covDRunInit("p1", dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already contains datatug project")
	})
	t.Run("current user lookup fails", func(t *testing.T) {
		covDSetVar(t, &initCurrentUser, func() (*user.User, error) { return nil, errors.New("no user") })
		require.Error(t, covDRunInit("p1", t.TempDir()))
	})
	t.Run("project cannot be saved", func(t *testing.T) {
		covDSkipIfRoot(t)
		dir := t.TempDir()
		covDChmod(t, dir, 0o500)
		require.Error(t, covDRunInit("p1", dir))
	})
}
