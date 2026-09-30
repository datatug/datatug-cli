package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func covDChinook(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../../pkg/dbcopy/testdata/chinook.db")
	require.NoError(t, err)
	if _, statErr := os.Stat(p); statErr != nil {
		t.Skipf("chinook fixture unavailable: %v", statErr)
	}
	return p
}

func covDExitCode(t *testing.T, err error) int {
	t.Helper()
	ec, ok := err.(ExitCoder)
	require.True(t, ok, "want an ExitCoder, got %T: %v", err, err)
	return ec.ExitCode()
}

func TestCovDDBCopyActionBranches(t *testing.T) {
	chinook := covDChinook(t)

	t.Run("open --from fails", func(t *testing.T) {
		_, _, err := runCopy(t, "db", "copy",
			"--from", "sqlite://"+filepath.Join(t.TempDir(), "missing.db"),
			"--to", "ingitdb://"+t.TempDir())
		require.Error(t, err)
		assert.Equal(t, 4, covDExitCode(t, err))
		assert.Contains(t, err.Error(), "open --from")
	})
	t.Run("open --to fails", func(t *testing.T) {
		_, _, err := runCopy(t, "db", "copy",
			"--from", "sqlite://"+chinook,
			"--to", "sqlite://"+filepath.Join(t.TempDir(), "missing.db"))
		require.Error(t, err)
		assert.Equal(t, 4, covDExitCode(t, err))
		assert.Contains(t, err.Error(), "open --to")
	})
	t.Run("invalid filter flags exit 2", func(t *testing.T) {
		_, _, err := runCopy(t, "db", "copy",
			"--from", "sqlite://"+chinook,
			"--to", "ingitdb://"+t.TempDir(),
			"--include", "Album", "--exclude", "Artist")
		require.Error(t, err)
		assert.Equal(t, 2, covDExitCode(t, err))
	})
	t.Run("copy failure exits 1", func(t *testing.T) {
		_, _, err := runCopy(t, "db", "copy",
			"--from", "sqlite://"+chinook,
			"--to", "ingitdb://"+t.TempDir(),
			"--include", "NoSuchTable")
		require.Error(t, err)
		assert.Equal(t, 1, covDExitCode(t, err))
	})
	t.Run("progress lines are written", func(t *testing.T) {
		_, stderr, err := runCopy(t, "db", "copy",
			"--from", "sqlite://"+chinook,
			"--to", "ingitdb://"+t.TempDir(),
			"--include", "Genre", "--progress")
		require.NoError(t, err)
		assert.Contains(t, stderr.String(), "Genre")
	})
	t.Run("source without tables is not an error", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "empty.db")
		db, err := sql.Open("sqlite", src)
		require.NoError(t, err)
		_, err = db.Exec("PRAGMA user_version = 1")
		require.NoError(t, err)
		require.NoError(t, db.Close())
		_, stderr, err := runCopy(t, "db", "copy",
			"--from", "sqlite://"+src,
			"--to", "ingitdb://"+t.TempDir())
		require.NoError(t, err)
		assert.Contains(t, stderr.String(), "source has no tables")
	})
}

func TestCovDBuildDirectivesFromFlags(t *testing.T) {
	build := func(t *testing.T, flags map[string][]string) error {
		t.Helper()
		cmd := dbCopyCommand()
		for name, values := range flags {
			for _, v := range values {
				require.NoError(t, cmd.Flags().Set(name, v))
			}
		}
		_, err := buildDirectivesFromFlags(cmd)
		return err
	}
	writeConfig := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "filter.yaml")
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		return p
	}

	t.Run("config mixed with flags", func(t *testing.T) {
		err := build(t, map[string][]string{"filter-config": {writeConfig(t, "include: [A]\n")}, "include": {"B"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})
	t.Run("config file missing", func(t *testing.T) {
		err := build(t, map[string][]string{"filter-config": {filepath.Join(t.TempDir(), "absent.yaml")}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--filter-config")
	})
	t.Run("config include and exclude conflict", func(t *testing.T) {
		err := build(t, map[string][]string{"filter-config": {writeConfig(t, "include: [A]\nexclude: [B]\n")}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})
	t.Run("config ok", func(t *testing.T) {
		require.NoError(t, build(t, map[string][]string{"filter-config": {writeConfig(t, "include: [A]\n")}}))
	})
	t.Run("bad where", func(t *testing.T) {
		require.Error(t, build(t, map[string][]string{"where": {"only-one-part"}}))
	})
	t.Run("where conditions on one table compose", func(t *testing.T) {
		require.NoError(t, build(t, map[string][]string{"where": {"T:a:=:1", "T:b:>:2"}}))
	})
	t.Run("bad limit", func(t *testing.T) {
		require.Error(t, build(t, map[string][]string{"limit": {"T:zero"}}))
	})
	t.Run("duplicate limit", func(t *testing.T) {
		err := build(t, map[string][]string{"limit": {"T:1", "T:2"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate entry")
	})
	t.Run("include and exclude conflict", func(t *testing.T) {
		require.Error(t, build(t, map[string][]string{"include": {"A"}, "exclude": {"B"}}))
	})
}
