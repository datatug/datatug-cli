package sshdemo

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

var (
	chmodCleanupDir = os.Chmod
	openCleanupDir  = unix.Open
)

// OpenCleanupDirectory pins a directory without following its final name.
// Child operations use the returned descriptor path while file is open.
func OpenCleanupDirectory(path string) (file *os.File, pinned string, err error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = unix.Close(fd) }()
	// O_PATH works on mode-000 directories. Restore traversal permissions
	// on the pinned inode before opening it for enumeration.
	procPath := "/proc/self/fd/" + strconv.Itoa(fd)
	if err := chmodCleanupDir(procPath, 0o700); err != nil {
		return nil, "", err
	}
	readFD, err := openCleanupDir(procPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	pinned = "/proc/self/fd/" + strconv.Itoa(readFD)
	return os.NewFile(uintptr(readFD), pinned), pinned, nil
}
