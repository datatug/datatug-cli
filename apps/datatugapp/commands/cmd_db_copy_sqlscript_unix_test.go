//go:build unix

package commands

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The script has the mode of a new file under the user's umask, and a file that is replaced keeps its own mode.
func TestWriteSQLScriptRespectsTheUmask(t *testing.T) {
	src := scriptSource(t)
	db, err := sql.Open("sqlite3", strings.TrimPrefix(src, "sqlite://"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	req := faithful.ScriptRequest{Dialect: "mysql", DB: db}
	dir := t.TempDir()
	mode := func(path string) os.FileMode {
		info, err := os.Stat(path)
		require.NoError(t, err)
		return info.Mode().Perm()
	}
	for _, c := range []struct {
		umask int
		want  os.FileMode
	}{{0o022, 0o644}, {0o077, 0o600}, {0o002, 0o664}} {
		old := syscall.Umask(c.umask)
		out := filepath.Join(dir, "u"+c.want.String()+".sql")
		_, err := writeSQLScript(context.Background(), req, out, false)
		syscall.Umask(old)
		require.NoError(t, err)
		assert.Equal(t, c.want, mode(out), "umask %o", c.umask)
	}
	// A file that is replaced keeps its mode; a path that is no file (a link to nothing) gets the umask's.
	kept := filepath.Join(dir, "kept.sql")
	require.NoError(t, os.WriteFile(kept, []byte("old"), 0o600))
	require.NoError(t, os.Chmod(kept, 0o640))
	_, err = writeSQLScript(context.Background(), req, kept, true)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), mode(kept))
	dangling := filepath.Join(dir, "dangling.sql")
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), dangling))
	old := syscall.Umask(0o022)
	_, err = writeSQLScript(context.Background(), req, dangling, true)
	syscall.Umask(old)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), mode(dangling))
}

// A FIFO as the notice file is refused at once, whether it is one when the command looks or becomes one between a look
// and the open: the file is opened without blocking and the open handle is checked.
func TestSQLScriptNoticeFileFIFOIsRefusedWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	regular := filepath.Join(dir, "regular")
	require.NoError(t, os.WriteFile(regular, []byte("MIT"), 0o600))
	swapped := filepath.Join(dir, "swapped")
	require.NoError(t, os.WriteFile(swapped, []byte("MIT"), 0o600))

	orig := openNoticeFile
	defer func() { openNoticeFile = orig }()
	// the path is a regular file until the moment of the open, then a FIFO
	openNoticeFile = func(path string) (*os.File, error) {
		if path == swapped {
			if err := os.Remove(swapped); err != nil {
				return nil, err
			}
			if err := syscall.Mkfifo(swapped, 0o600); err != nil {
				return nil, err
			}
		}
		return orig(path)
	}
	for name, path := range map[string]string{"fifo": fifo, "swapped to a fifo at the open": swapped} {
		done := make(chan error, 1)
		go func() { _, err := readNoticeFile(path); done <- err }()
		select {
		case err := <-done:
			require.Error(t, err, name)
			assert.Contains(t, err.Error(), "not a regular file", name)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: reading the notice file hangs", name)
		}
	}
	got, err := readNoticeFile(regular)
	require.NoError(t, err)
	assert.Equal(t, "MIT", got)
}
