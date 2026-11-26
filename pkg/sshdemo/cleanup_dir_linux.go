package sshdemo

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

const (
	// AppArmor checks full paths even for descriptor-relative operations.
	cleanupPathBudget = 2048
	cleanupWindowSize = 62 // Leave descriptors for the root and a temporary pin.
)

var (
	cleanupOpenat = unix.Openat
	cleanupStat   = unix.Fstat
	cleanupChmod  = os.Chmod
	cleanupFchmod = unix.Fchmod
	cleanupUnlink = unix.Unlinkat
	cleanupRename = unix.Renameat2
	cleanupRead   = (*os.File).Readdirnames
	cleanupSeek   = (*os.File).Seek
)

type cleanupIdentity struct {
	dev, ino, mount uint64
}

type cleanupFrame struct {
	file    *os.File
	id      cleanupIdentity
	name    string
	batch   []string
	scanned bool
}

func (f *cleanupFrame) close() {
	if f.file != nil {
		_ = f.file.Close()
		*f = cleanupFrame{}
	}
}

func (f *cleanupFrame) readBatch() error {
	names, err := cleanupRead(f.file, 64)
	if errors.Is(err, io.EOF) && f.scanned {
		// Some filesystems compact directory entries after unlink. Start
		// from zero before accepting EOF so a shifted entry cannot survive.
		f.scanned = false
		if _, err := cleanupSeek(f.file, 0, io.SeekStart); err != nil {
			return fmt.Errorf("rewind cleanup directory: %w", err)
		}
		names, err = cleanupRead(f.file, 64)
	}
	if err != nil {
		return err
	}
	f.batch = names
	f.scanned = true
	return nil
}

// CleanWorkspaceContents empties a drained workspace using only directory
// descriptors and single entry names. The caller removes the workspace itself.
func CleanWorkspaceContents(path string, progress func()) error {
	return cleanWorkspaceContents(path, cleanupPathBudget, progress)
}

func cleanWorkspaceContents(path string, budget int, progress func()) error {
	root, err := openCleanupFrame(unix.AT_FDCWD, path, nil, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open cleanup workspace: %w", err)
	}
	defer root.close()
	return walkCleanup(&root, len(path), budget, progress)
}

func walkCleanup(root *cleanupFrame, rootPathLen, budget int, progress func()) error {
	var frames [cleanupWindowSize]cleanupFrame
	defer func() {
		for i := range frames {
			frames[i].close()
		}
	}()
	// Every descent adds at least two bytes. Keep lengths for evicted frames.
	var pathLengths [cleanupPathBudget/2 + 1]int
	pathLengths[0] = rootPathLen
	rootFD := int(root.file.Fd())
	current := root
	for depth := 0; ; {
		if len(current.batch) == 0 {
			err := current.readBatch()
			if errors.Is(err, io.EOF) {
				if depth == 0 {
					return nil
				}
				parent := root
				if depth > 1 {
					parent = &frames[(depth-2)%len(frames)]
				}
				if err := ascendCleanup(current, parent, root.id, depth, progress); err != nil {
					return err
				}
				current = parent
				depth--
				continue
			}
			if err != nil {
				return fmt.Errorf("read cleanup directory: %w", err)
			}
		}
		name := current.batch[0]
		current.batch = current.batch[1:]
		fd := int(current.file.Fd())
		descend, err := unlinkCleanupEntry(fd, name, progress)
		if err != nil {
			return err
		}
		if !descend {
			continue
		}
		nextPathLen := pathLengths[depth] + len(name) + 1
		next := &frames[depth%len(frames)]
		next.close()
		if nextPathLen > budget {
			// Moving a directory to a new parent needs write permission on
			// the directory itself: check its identity and restore owner
			// permissions the way a descent does, then move it.
			pin, err := openCleanupFrame(fd, name, &root.id, false)
			if err != nil {
				return fmt.Errorf("prepare cleanup directory %q: %w", name, err)
			}
			pin.close()
			if err := hoistCleanupDirectory(fd, name, rootFD, budget-rootPathLen, progress); err != nil {
				return err
			}
			continue
		}
		*next, err = openCleanupFrame(fd, name, &root.id, false)
		if err != nil {
			return fmt.Errorf("open cleanup directory %q: %w", name, err)
		}
		next.name = name
		current = next
		depth++
		pathLengths[depth] = nextPathLen
	}
}

func hoistCleanupDirectory(fd int, name string, rootFD, rootSpace int, progress func()) error {
	for {
		// Unpredictable names: a visitor cannot pre-create the targets.
		target := ".cleanup-" + rand.Text()
		// The root rescan must be able to descend into the new name.
		if len(target)+1 > rootSpace {
			return fmt.Errorf("hoist cleanup directory %q: workspace path leaves no room within the cleanup path budget", name)
		}
		err := cleanupRename(fd, name, rootFD, target, unix.RENAME_NOREPLACE)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return fmt.Errorf("hoist cleanup directory %q: %w", name, err)
		}
		// A hoist moves the directory to depth 1 and the tree is drained and
		// finite, so hoists cannot repeat forever: count them as progress so
		// a starved run of hoists does not read as a stuck cleanup.
		progress()
		return nil
	}
}

func unlinkCleanupEntry(fd int, name string, progress func()) (descend bool, err error) {
	err = cleanupUnlink(fd, name, 0)
	if errors.Is(err, unix.EISDIR) {
		err = cleanupUnlink(fd, name, unix.AT_REMOVEDIR)
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
			return true, nil
		}
	}
	return false, cleanupUnlinkResult(name, err, progress)
}

func cleanupUnlinkResult(name string, err error, progress func()) error {
	switch {
	case err == nil:
		progress()
	case errors.Is(err, unix.ENOENT):
		// A retry can encounter entries already removed.
	case errors.Is(err, unix.EBUSY):
		return fmt.Errorf("%w: mounted cleanup entry %q", ErrCleanupBoundary, name)
	default:
		return fmt.Errorf("unlink cleanup entry %q: %w", name, err)
	}
	return nil
}

func ascendCleanup(current, parent *cleanupFrame, root cleanupIdentity, depth int, progress func()) error {
	if parent.file == nil {
		var err error
		*parent, err = openCleanupFrame(int(current.file.Fd()), "..", &root, false)
		if err != nil {
			return fmt.Errorf("reopen cleanup parent: %w", err)
		}
	}
	if (parent.id == root) != (depth == 1) || parent.id == current.id {
		return fmt.Errorf("%w: unexpected cleanup parent", ErrCleanupBoundary)
	}

	name := current.name
	current.close()
	if name == "" {
		// The parent was reopened without a saved entry name. Its fresh
		// batch will pick up the now-empty child and unlink it directly.
		return nil
	}
	err := cleanupUnlink(int(parent.file.Fd()), name, unix.AT_REMOVEDIR)
	return cleanupUnlinkResult(name, err, progress)
}

func openCleanupFrame(parent int, name string, root *cleanupIdentity, expectRoot bool) (cleanupFrame, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := cleanupOpenat(parent, name, flags, 0)
	pinned := errors.Is(err, unix.EACCES)
	if pinned {
		// O_PATH can pin mode-000 directories. Verify the inode before
		// restoring permissions through the kernel's descriptor link.
		fd, err = cleanupOpenat(parent, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return cleanupFrame{}, err
	}
	id, err := prepareCleanupFD(fd, pinned, root, expectRoot)
	if err == nil && pinned {
		readFD, openErr := cleanupOpenat(unix.AT_FDCWD, cleanupFDPath(fd), flags&^unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		fd, err = readFD, openErr
	}
	if err != nil {
		_ = unix.Close(fd)
		return cleanupFrame{}, err
	}
	return cleanupFrame{file: os.NewFile(uintptr(fd), "cleanup directory"), id: id}, nil
}

func prepareCleanupFD(fd int, pinned bool, root *cleanupIdentity, expectRoot bool) (cleanupIdentity, error) {
	var info unix.Stat_t
	if err := cleanupStat(fd, &info); err != nil {
		return cleanupIdentity{}, err
	}
	mount, err := mountIDAt(fd, "", unix.AT_EMPTY_PATH)
	if err != nil {
		return cleanupIdentity{}, err
	}
	id := cleanupIdentity{dev: uint64(info.Dev), ino: info.Ino, mount: mount}
	if root != nil && (id.dev != root.dev || id.mount != root.mount || (id == *root) != expectRoot) {
		return cleanupIdentity{}, fmt.Errorf("%w: unexpected cleanup directory identity", ErrCleanupBoundary)
	}
	if pinned {
		return id, cleanupChmod(cleanupFDPath(fd), 0o700)
	}
	if info.Mode&0o700 != 0o700 {
		return id, cleanupFchmod(fd, 0o700)
	}
	return id, nil
}

func cleanupFDPath(fd int) string {
	return "/proc/self/fd/" + strconv.Itoa(fd)
}
