package localprojects

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

type registryLock interface {
	TryLockContext(context.Context, time.Duration) (bool, error)
	Close() error
}

// Keep failures at actual I/O admission and durability boundaries reproducible.
var registryIO = struct {
	mkdirAll     func(string, os.FileMode) error
	eval         func(string) (string, error)
	abs          func(string) (string, error)
	readDir      func(string) ([]os.DirEntry, error)
	openRoot     func(string) (*os.Root, error)
	rootOpenRoot func(*os.Root, string) (*os.Root, error)
	rootMkdir    func(*os.Root, string, os.FileMode) error
	rootLstat    func(*os.Root, string) (os.FileInfo, error)
	rootOpenFile func(*os.Root, string, int, os.FileMode) (*os.File, error)
	rootOpen     func(*os.Root, string) (*os.File, error)
	rootRename   func(*os.Root, string, string) error
	read         func([]byte) (int, error)
	marshal      func(any) ([]byte, error)
	unmarshal    func([]byte, any) error
	readAll      func(*os.File) ([]byte, error)
	lstat        func(string) (os.FileInfo, error)
	lock         func(string) registryLock
	command      func(context.Context, string, ...string) *exec.Cmd
	now          func() time.Time
	platform     string
}{
	mkdirAll: os.MkdirAll, eval: filepath.EvalSymlinks, abs: filepath.Abs,
	readDir: os.ReadDir, openRoot: os.OpenRoot, rootOpenRoot: (*os.Root).OpenRoot,
	rootMkdir: (*os.Root).Mkdir, rootLstat: (*os.Root).Lstat, rootOpenFile: (*os.Root).OpenFile,
	rootOpen: (*os.Root).Open, rootRename: (*os.Root).Rename, read: rand.Read,
	marshal: json.Marshal, unmarshal: json.Unmarshal, lstat: os.Lstat,
	lock: func(path string) registryLock {
		return flock.New(path, flock.SetFlag(os.O_CREATE|os.O_RDWR|noFollowFlags), flock.SetPermissions(0600))
	},
	command: exec.CommandContext, now: time.Now, platform: runtime.GOOS,
}
