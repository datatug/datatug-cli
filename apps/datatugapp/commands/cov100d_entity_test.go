package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covDCorruptIndexRepo returns a project directory that is a git repository
// whose index is unreadable: the --git=stage preflight (which only opens the
// repository) passes, but staging a written file fails.
func covDCorruptIndexRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o600))
	cov100cSeedEntity(t, dir, "A", cov100cEntityA)
	return dir
}

func TestCovDEntityStageFailsAfterWrite(t *testing.T) {
	t.Run("field rm", func(t *testing.T) {
		_, _, err := runEntity(t, "entity", "field", "rm", "A", "f1", "-d", covDCorruptIndexRepo(t), "--git", "stage")
		require.Error(t, err)
	})
	t.Run("field set", func(t *testing.T) {
		_, _, err := runEntity(t, "entity", "field", "set", "A", "f1", "--title", "New", "-d", covDCorruptIndexRepo(t), "--git", "stage")
		require.Error(t, err)
	})
	t.Run("entity add", func(t *testing.T) {
		_, _, err := runEntityStdin(t, "id: N\n", "entity", "add", "-d", covDCorruptIndexRepo(t), "--git", "stage")
		require.Error(t, err)
	})
	t.Run("field add", func(t *testing.T) {
		_, _, err := runEntityStdin(t, "id: f2\ntype: string\n", "entity", "field", "add", "A", "-d", covDCorruptIndexRepo(t), "--git", "stage")
		require.Error(t, err)
	})
	t.Run("field add continue-on-error", func(t *testing.T) {
		_, _, err := runEntityStdin(t, "- id: f2\n  type: string\n", "entity", "field", "add", "A", "--continue-on-error", "-d", covDCorruptIndexRepo(t), "--git", "stage")
		require.Error(t, err)
	})
}

func TestCovDParseDocsMarshalErrors(t *testing.T) {
	covDSetVar(t, &entityJSONMarshal, func(any) ([]byte, error) { return nil, errors.New("marshal boom") })
	_, err := parseEntityDocs([]byte("id: A\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal boom")
	_, err = parseFieldDocs([]byte("id: f\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal boom")
}
