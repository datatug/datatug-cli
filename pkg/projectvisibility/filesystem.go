package projectvisibility

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

// Filesystem seams exercise admission, identity checks and durability failures
// without weakening the production root/descriptor boundaries.
type coordinatorLocker interface {
	TryLockContext(context.Context, time.Duration) (bool, error)
	Close() error
}

var journalOS = struct {
	openRoot     func(string) (*os.Root, error)
	rootOpen     func(*os.Root, string) (*os.File, error)
	rootOpenFile func(*os.Root, string, int, os.FileMode) (*os.File, error)
	rootLstat    func(*os.Root, string) (os.FileInfo, error)
	rootRename   func(*os.Root, string, string) error
	readNames    func(*os.File, int) ([]string, error)
	mkdirAll     func(string, os.FileMode) error
	lstat        func(string) (os.FileInfo, error)
	open         func(string) (*os.File, error)
	stat         func(*os.File) (os.FileInfo, error)
	chmod        func(*os.File, os.FileMode) error
	lock         func(string) coordinatorLocker
	platform     string
}{os.OpenRoot, (*os.Root).Open, (*os.Root).OpenFile, (*os.Root).Lstat, (*os.Root).Rename, (*os.File).Readdirnames, os.MkdirAll, os.Lstat, os.Open, (*os.File).Stat, (*os.File).Chmod, func(path string) coordinatorLocker { return flock.New(path) }, runtime.GOOS}
