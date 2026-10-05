// Package plainfs writes, reads, renames and removes files and folders inside a project
// folder only as plain files in plain folders.
//
// A project folder may have been cloned from someone else, so nothing below it can be trusted
// to be what it looks like: a command writes only plain files in plain folders of the project,
// and does not write through a link. Every method of a Tree takes a path inside the project
// folder (the root of the tree, trusted as the person gave it), walks from the root down to the
// path with Lstat, and refuses a folder that is a link or is not a plain folder, and a target
// that exists and is not a plain file. A file is opened so that a link put in its place after
// the check is not followed where the platform has O_NOFOLLOW (every Unix); on Windows, which
// has none, the Lstat check alone applies, and it also refuses a junction. A rename lands only
// on a path that was checked the same way.
//
// A refusal wraps ErrNotPlain and names the path inside the project; it never says where a
// link leads. Every error of the operating system is returned, without the absolute path it
// carries.
//
// This is the one walk of the CLI. datatug-core's file store has the same rule for what it
// writes (its helper is internal to that module), and the commands that write into a project
// without the store use this one.
package plainfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrNotPlain is wrapped by every refusal: a path outside the project, a link, or an entry of
// another kind where a plain file or folder is needed.
var ErrNotPlain = errors.New("not a plain file or folder of the project")

// refusal is the error behind ErrNotPlain: the path inside the project and what is wrong with it.
type refusal struct{ rel, reason string }

func (r *refusal) Error() string { return r.rel + ": " + r.reason + "; refusing to use it" }

func (r *refusal) Unwrap() error { return ErrNotPlain }

// File is an opened file.
type File interface {
	io.ReadWriteCloser
}

// Ops are the calls of the operating system that a Tree makes, so that a test can inject the
// failures and the entries (a link on a platform that cannot make one) that a real folder cannot
// produce on demand. OSOps has the real ones.
type Ops struct {
	Lstat     func(name string) (fs.FileInfo, error)
	Mkdir     func(name string, perm fs.FileMode) error
	Remove    func(name string) error
	RemoveAll func(name string) error
	Rename    func(oldpath, newpath string) error
	OpenFile  func(name string, flag int, perm fs.FileMode) (File, error)

	// AfterCheck, when it is set, runs between the Lstat of a file and the open of it: a test
	// uses it to put a link in the file's place at exactly that moment.
	AfterCheck func(name string)
}

// OSOps is the real operating system.
func OSOps() Ops {
	return Ops{
		Lstat:     os.Lstat,
		Mkdir:     os.Mkdir,
		Remove:    os.Remove,
		RemoveAll: os.RemoveAll,
		Rename:    os.Rename,
		OpenFile: func(name string, flag int, perm fs.FileMode) (File, error) {
			f, err := os.OpenFile(name, flag, perm)
			if err != nil {
				return nil, err // not f: a nil *os.File would be a File that is not nil
			}
			return f, nil
		},
	}
}

// A Tree is a project folder and how to change it. The project folder is trusted as it was
// given (it may itself be a link: that is the person's to choose); everything below it is not.
type Tree struct {
	root       string
	folderPerm fs.FileMode
	ops        Ops
}

// New is the project folder root, whose folders are made with folderPerm.
func New(root string, folderPerm fs.FileMode) Tree {
	return NewWithOps(root, folderPerm, OSOps())
}

// NewWithOps is New with other calls of the operating system, for a test.
func NewWithOps(root string, folderPerm fs.FileMode, ops Ops) Tree {
	return Tree{root: root, folderPerm: folderPerm, ops: ops}
}

const rootName = "the project folder"

// MkdirAll makes dir, which must be the project folder or inside it, and every missing folder
// between them, refusing any existing one that is not a plain folder.
func (t Tree) MkdirAll(dir string) error {
	_, err := t.MakeFolders(dir)
	return err
}

// MakeFolders is MkdirAll that returns the folders it made, outermost first, as full paths.
// When it fails it returns the ones it made before, so that the caller can take them back.
func (t Tree) MakeFolders(dir string) (made []string, err error) {
	rel, err := t.rel(dir)
	if err != nil {
		return nil, err
	}
	_, made, err = t.walk(rel, true)
	return made, err
}

// CheckFolders is whether dir, which must be the project folder or inside it, is there, and an
// error when a folder of the way down to it is a link or is not a plain folder. It makes nothing.
// A folder that is not there is not a link: found is false and there is no error.
func (t Tree) CheckFolders(dir string) (found bool, err error) {
	rel, err := t.rel(dir)
	if err != nil {
		return false, err
	}
	found, _, err = t.walk(rel, false)
	return found, err
}

// WriteFile writes data to the file at path, which must be inside the project folder: it makes
// the missing folders above it, refuses a target that exists and is not a plain file, and
// creates or truncates the file without following a link. It returns the error of the write, or
// else the error of the close.
func (t Tree) WriteFile(path string, data []byte, perm fs.FileMode) error {
	rel, err := t.fileRel(path)
	if err != nil {
		return err
	}
	if _, _, err = t.walk(filepath.Dir(rel), true); err != nil {
		return err
	}
	full := filepath.Join(t.root, rel)
	if _, err = t.checkFile(full, rel); err != nil {
		return err
	}
	if t.ops.AfterCheck != nil {
		t.ops.AfterCheck(full)
	}
	f, err := t.open(full, rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|noFollow, perm)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return wrap(display(rel), err)
	}
	if err = f.Close(); err != nil {
		return wrap(display(rel), err)
	}
	return nil
}

// ReadFile reads the file at path, which must be inside the project folder, without following a
// link: a link, or a folder or anything else that is not a plain file, is refused. A file that is
// not there, or a folder above it that is not there, is an error that is fs.ErrNotExist.
func (t Tree) ReadFile(path string) ([]byte, error) {
	rel, err := t.fileRel(path)
	if err != nil {
		return nil, err
	}
	found, _, err := t.walk(filepath.Dir(rel), false)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, wrap(display(rel), fs.ErrNotExist)
	}
	full := filepath.Join(t.root, rel)
	if _, err = t.checkFile(full, rel); err != nil {
		return nil, err
	}
	f, err := t.open(full, rel, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return nil, wrap(display(rel), err)
	}
	return data, nil
}

// Remove deletes the plain file at path, which must be inside the project folder. A file that
// is not there, or a folder above it that is not there, is not an error.
func (t Tree) Remove(path string) error {
	rel, err := t.fileRel(path)
	if err != nil {
		return err
	}
	found, _, err := t.walk(filepath.Dir(rel), false)
	if err != nil || !found {
		return err
	}
	full := filepath.Join(t.root, rel)
	if _, err = t.checkFile(full, rel); err != nil {
		return err
	}
	if err = t.ops.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return wrap(display(rel), err)
	}
	return nil
}

// RemoveAll deletes the plain folder dir, which must be inside the project folder, with everything
// in it. A folder that is not there is not an error; the project folder itself is never removed.
func (t Tree) RemoveAll(dir string) error {
	rel, err := t.rel(dir)
	if err != nil {
		return err
	}
	if rel == "." {
		return &refusal{rootName, "cannot be removed"}
	}
	found, _, err := t.walk(rel, false)
	if err != nil || !found {
		return err
	}
	if err = t.ops.RemoveAll(filepath.Join(t.root, rel)); err != nil {
		return wrap(display(rel), err)
	}
	return nil
}

// RemoveEmptyFolder deletes the plain folder dir, which must be inside the project folder, when
// nothing is in it. A folder that is not there is not an error, and a folder that holds something
// is left as it is: it is somebody else's now.
func (t Tree) RemoveEmptyFolder(dir string) error {
	rel, err := t.rel(dir)
	if err != nil {
		return err
	}
	if rel == "." {
		return &refusal{rootName, "cannot be removed"}
	}
	found, _, err := t.walk(rel, false)
	if err != nil || !found {
		return err
	}
	// Remove, and not RemoveAll: it refuses a folder that holds something.
	if err = t.ops.Remove(filepath.Join(t.root, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrExist) {
		return wrap(display(rel), err)
	}
	return nil
}

// Rename moves the plain file from to the path to, both inside the project folder: the folders
// above each must be plain folders that are there, the file must be there and be a plain file,
// and to must be a plain file or not be there. A rename lands only on a checked path.
func (t Tree) Rename(from, to string) error {
	relFrom, err := t.fileRel(from)
	if err != nil {
		return err
	}
	relTo, err := t.fileRel(to)
	if err != nil {
		return err
	}
	for _, rel := range []string{relFrom, relTo} {
		found, _, walkErr := t.walk(filepath.Dir(rel), false)
		if walkErr != nil {
			return walkErr
		}
		if !found {
			return wrap(display(rel), fs.ErrNotExist)
		}
	}
	fullFrom, fullTo := filepath.Join(t.root, relFrom), filepath.Join(t.root, relTo)
	exists, err := t.checkFile(fullFrom, relFrom)
	if err != nil {
		return err
	}
	if !exists {
		return wrap(display(relFrom), fs.ErrNotExist)
	}
	if _, err = t.checkFile(fullTo, relTo); err != nil {
		return err
	}
	if err = t.ops.Rename(fullFrom, fullTo); err != nil {
		return wrap(display(relTo), err)
	}
	return nil
}

// open opens the file full, whose path inside the project is rel. A link put in its place after
// the check is refused, where the platform says so.
func (t Tree) open(full, rel string, flag int, perm fs.FileMode) (File, error) {
	f, err := t.ops.OpenFile(full, flag, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, &refusal{display(rel), "is a link, not a plain file"}
		}
		return nil, wrap(display(rel), err)
	}
	return f, nil
}

// rel is p relative to the project folder, or a refusal when p is not the project folder or
// inside it. The check is on the paths as written, before any Lstat.
func (t Tree) rel(p string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(t.root), filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &refusal{filepath.ToSlash(filepath.Clean(p)), "is outside the project"}
	}
	return rel, nil
}

// fileRel is rel for a path that names a file: the project folder is not one.
func (t Tree) fileRel(p string) (string, error) {
	rel, err := t.rel(p)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", &refusal{rootName, "is a folder, not a file"}
	}
	return rel, nil
}

// walk checks that every folder from the project folder down to rel (a path relative to it, "."
// for the folder itself) is a plain folder. With create each missing one is made, and made lists
// them, outermost first, as full paths; without it the walk stops at the first missing folder and
// reports found=false. The project folder itself is not looked at.
func (t Tree) walk(rel string, create bool) (found bool, made []string, err error) {
	if rel == "." {
		return true, nil, nil
	}
	dir := t.root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, name := range parts {
		dir = filepath.Join(dir, name)
		shown := display(filepath.Join(parts[:i+1]...))
		info, err := t.ops.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return false, made, nil
			}
			switch mkErr := t.ops.Mkdir(dir, t.folderPerm); {
			case mkErr == nil:
				made = append(made, dir)
			case !errors.Is(mkErr, fs.ErrExist): // a folder another process made in the meantime is looked at, and is not ours to take back
				return false, made, wrap(shown, mkErr)
			}
			info, err = t.ops.Lstat(dir)
		}
		if err != nil {
			return false, made, wrap(shown, err)
		}
		if reason := folderIssue(info); reason != "" {
			return false, made, &refusal{shown, reason}
		}
	}
	return true, made, nil
}

// checkFile refuses full, whose path inside the project is rel, when it exists and is not a plain
// file, and says whether it exists.
func (t Tree) checkFile(full, rel string) (exists bool, err error) {
	info, err := t.ops.Lstat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, wrap(display(rel), err)
	}
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return true, &refusal{display(rel), "is a link, not a plain file"}
	case !mode.IsRegular():
		return true, &refusal{display(rel), "is not a plain file"}
	}
	return true, nil
}

// folderIssue is what is wrong with a folder of the way down, or "" when it is a plain folder.
func folderIssue(info fs.FileInfo) string {
	mode := info.Mode()
	switch {
	case mode&fs.ModeSymlink != 0:
		return "is a link, not a plain folder"
	case !mode.IsDir():
		return "is not a plain folder"
	}
	return ""
}

// display is rel as a refusal shows it: slash-separated.
func display(rel string) string { return filepath.ToSlash(rel) }

// wrap prefixes err with the path inside the project and drops the absolute path that the error
// of the operating system carries, keeping the cause for errors.Is.
func wrap(shown string, err error) error {
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	switch {
	case errors.As(err, &pathErr):
		err = pathErr.Err
	case errors.As(err, &linkErr):
		err = linkErr.Err
	}
	return fmt.Errorf("%s: %w", shown, err)
}
