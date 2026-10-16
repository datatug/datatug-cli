// Package gitpath answers whether a path lies inside a Git working tree or a
// bare Git repository. Private stores (evidence snapshots, query results)
// refuse such a location, so private bytes never end up next to files a
// person might commit or push.
package gitpath

import (
	"errors"
	"os"
	"path/filepath"
)

// Lstat is the file-information call the checks use; the stores pass their
// own seam so their tests can inject failures.
type Lstat func(name string) (os.FileInfo, error)

// Inside reports whether path, or any of its ancestors, is a Git working tree
// (holds .git) or a bare repository (holds HEAD, objects and refs). A path
// that is a file is judged by its folder; a path that does not exist yet is
// judged by its spelling.
func Inside(path string, lstat Lstat) (bool, error) {
	info, err := lstat(path)
	if err == nil && !info.IsDir() {
		path = filepath.Dir(path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for current := path; ; current = filepath.Dir(current) {
		if _, err := lstat(filepath.Join(current, ".git")); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		bare, err := IsBare(current, lstat)
		if err != nil {
			return false, err
		}
		if bare {
			return true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
	}
}

// IsBare reports whether path holds the HEAD, objects and refs of a bare
// Git repository.
func IsBare(path string, lstat Lstat) (bool, error) {
	for _, marker := range []string{"HEAD", "objects", "refs"} {
		if _, err := lstat(filepath.Join(path, marker)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}
