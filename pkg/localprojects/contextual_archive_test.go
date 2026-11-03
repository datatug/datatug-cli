package localprojects

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContextualArchiveOpenedRootMetadata(t *testing.T) {
	for _, body := range []string{"missing", `{"version":1,"archived":true,"operations":{}}`, `{"version":1,"archived":false,"operations":{}}`, "null", "{}", "{", `{"version":2,"archived":true}`, `{"version":1,"archived":true,"operations":null}`, "directory", "symlink", "oversize"} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer func() { _ = root.Close() }()
			name := filepath.Join(dir, stateName)
			switch body {
			case "missing":
			case "directory":
				require.NoError(t, os.Mkdir(name, 0700))
			case "symlink":
				outside := filepath.Join(t.TempDir(), "private.json")
				require.NoError(t, os.WriteFile(outside, []byte(`{"version":1,"archived":false}`), 0600))
				require.NoError(t, os.Symlink(outside, name))
			case "oversize":
				require.NoError(t, os.WriteFile(name, make([]byte, maxMetadata+1), 0600))
			default:
				require.NoError(t, os.WriteFile(name, []byte(body), 0600))
			}
			archived, err := ReadArchivedFromRoot(root)
			switch body {
			case "missing", `{"version":1,"archived":false,"operations":{}}`:
				require.NoError(t, err)
				require.False(t, archived)
			case `{"version":1,"archived":true,"operations":{}}`:
				require.NoError(t, err)
				require.True(t, archived)
			default:
				require.Error(t, err)
			}
		})
	}
}
func TestContextualArchiveCannotReopenReplacementRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateName), []byte(`{"version":1,"archived":true,"operations":{}}`), 0600))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { _ = root.Close() }()
	require.NoError(t, os.Rename(dir, dir+"-old"))
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, stateName), []byte(`{"version":1,"archived":false,"operations":{}}`), 0600))
	archived, err := ReadArchivedFromRoot(root)
	require.NoError(t, err)
	require.True(t, archived)
}
func TestContextualArchiveIOFailureIsNotActive(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { _ = root.Close() }()
	old := registryIO.rootLstat
	t.Cleanup(func() { registryIO.rootLstat = old })
	registryIO.rootLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errors.New("private IO detail") }
	_, err = ReadArchivedFromRoot(root)
	require.Error(t, err)
}
