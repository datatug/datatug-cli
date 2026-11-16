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

func TestContextualArchiveReadCustodyFailures(t *testing.T) {
	for _, kind := range []string{"open-denied", "closed-open-file", "read-failure", "decode-failure", "replaced-state"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, stateName)
			require.NoError(t, os.WriteFile(name, []byte(`{"version":1,"archived":true,"operations":{}}`), 0600))
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer func() { _ = root.Close() }()
			original := registryIO
			t.Cleanup(func() { registryIO = original })
			switch kind {
			case "open-denied":
				registryIO.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, os.ErrPermission }
			case "closed-open-file":
				registryIO.rootOpenFile = func(r *os.Root, n string, flags int, mode os.FileMode) (*os.File, error) {
					f, err := original.rootOpenFile(r, n, flags, mode)
					require.NoError(t, err)
					require.NoError(t, f.Close())
					return f, nil
				}
			case "read-failure":
				registryIO.readAll = func(*os.File) ([]byte, error) { return nil, os.ErrPermission }
			case "decode-failure":
				require.NoError(t, os.WriteFile(name, []byte(`{"version":1,"archived":true,"operations":[]}`), 0600))
			case "replaced-state":
				registryIO.readAll = func(f *os.File) ([]byte, error) {
					body, err := os.ReadFile(name)
					require.NoError(t, err)
					require.NoError(t, os.Rename(name, name+"-old"))
					require.NoError(t, os.WriteFile(name, []byte(`{"version":1,"archived":false,"operations":{}}`), 0600))
					return body, nil
				}
			}
			_, err = ReadArchivedFromRoot(root)
			require.Error(t, err)
		})
	}
}
