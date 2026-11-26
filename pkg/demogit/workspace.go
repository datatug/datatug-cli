package demogit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func defaultLimits(limits CloneLimits) CloneLimits {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = 200 * 1024 * 1024
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = 20_000
	}
	if limits.Depth <= 0 {
		limits.Depth = 1
	}
	return limits
}

// Changes lists the workspace changes Save would commit, for the save screen.
// It needs no token.
func Changes(repoDir string) ([]Change, error) {
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return nil, fmt.Errorf("open workspace repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("open workspace worktree: %w", err)
	}
	return workspaceChanges(repo, wt)
}

func workspaceChanges(repo *git.Repository, wt *git.Worktree) ([]Change, error) {
	tree, err := workspaceBaseTree(repo)
	if err != nil {
		return nil, err
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("read workspace status: %w", err)
	}
	var changes []Change
	for path, state := range status {
		kind, err := changeKind(wt, tree, path, state)
		if err != nil {
			return nil, fmt.Errorf("compare workspace path %q: %w", path, err)
		}
		if kind != "" {
			changes = append(changes, Change{Path: path, Kind: kind})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func workspaceBaseTree(repo *git.Repository) (*object.Tree, error) {
	head, err := repo.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, nil // An unborn branch has no files in its base tree.
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace HEAD: %w", err)
	}
	base, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("read workspace base commit: %w", err)
	}
	return base.Tree()
}

func changeKind(wt *git.Worktree, tree *object.Tree, path string, state *git.FileStatus) (string, error) {
	if state.Staging == git.Added {
		if state.Worktree == git.Deleted {
			return "", nil
		}
		return "added", nil
	}
	if state.Worktree == git.Deleted || (state.Staging == git.Deleted && state.Worktree == git.Unmodified) {
		return "deleted", nil
	}
	if state.Staging == git.Unmodified || state.Worktree == git.Unmodified {
		return "modified", nil
	}

	// Git may report a staged deletion followed by a recreated file as
	// untracked. Compare with HEAD when both the worktree and index changed.
	if tree == nil {
		return "added", nil
	}
	entry, err := tree.FindEntry(path)
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) || (err == nil && entry.Mode == filemode.Dir) {
		return "added", nil
	}
	if err != nil {
		return "", err
	}
	match, err := matchesEntry(wt, path, entry)
	if err != nil || match {
		return "", err
	}
	return "modified", nil
}

func matchesEntry(wt *git.Worktree, path string, entry *object.TreeEntry) (bool, error) {
	info, err := wt.Filesystem.Lstat(path)
	if err != nil {
		return false, err
	}
	mode, err := filemode.NewFromOSFileMode(info.Mode())
	if err != nil || mode != entry.Mode {
		return false, err
	}
	hash := plumbing.NewHasher(plumbing.BlobObject, info.Size())
	if mode == filemode.Symlink {
		target, err := wt.Filesystem.Readlink(path)
		if err != nil {
			return false, err
		}
		_, _ = io.WriteString(hash, target)
	} else {
		file, err := wt.Filesystem.Open(path)
		if err != nil {
			return false, err
		}
		defer file.Close()
		if _, err := io.Copy(hash, file); err != nil {
			return false, err
		}
	}
	return hash.Sum() == entry.Hash, nil
}

// ValidateWorkspace rejects a checked-out tree that DataTug must not open: a
// symlink that resolves outside root, more than limits allow, or no
// <projectDir>/datatug-project.json. Errors wrap ErrUntrustedContent.
func ValidateWorkspace(root, projectDir string, limits CloneLimits) error {
	return validateWorkspace(context.Background(), root, projectDir, defaultLimits(limits))
}

func validateWorkspace(ctx context.Context, root, projectDir string, limits CloneLimits) error {
	if !filepath.IsLocal(projectDir) || filepath.Clean(projectDir) != projectDir {
		return untrusted(projectDir, "project directory must be a clean relative path inside the workspace")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return untrusted(".", "%v", err)
	}
	if !info.IsDir() {
		return untrusted(".", "workspace root must be a directory")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return untrusted(".", "%v", err)
	}
	defer dir.Close()

	var files int
	var bytes int64
	err = fs.WalkDir(dir.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return untrusted(path, "%v", walkErr)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return untrusted(path, "%v", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := validateLink(dir, path); err != nil {
				return untrusted(path, "%v", err)
			}
		}
		if strings.HasPrefix(path, git.GitDirName+"/") {
			return nil // Git metadata is not part of the checked-out tree.
		}
		files++
		if files > limits.MaxFiles {
			return untrusted(path, "file limit exceeded (%d files)", limits.MaxFiles)
		}
		if info.Size() > limits.MaxBytes-bytes {
			return untrusted(path, "size limit exceeded (%d bytes)", limits.MaxBytes)
		}
		bytes += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	projectFile := filepath.Join(projectDir, "datatug-project.json")
	info, err = dir.Lstat(projectFile)
	if err != nil {
		return untrusted(projectFile, "cannot read project file: %v", err)
	}
	if !info.Mode().IsRegular() {
		return untrusted(projectFile, "project file must be a regular file")
	}
	return nil
}

func untrusted(path, format string, args ...any) error {
	return fmt.Errorf("%w: %q: %s", ErrUntrustedContent, path, fmt.Sprintf(format, args...))
}

// Resolve path components in order so a symlink followed by .. is checked
// from its actual target. Dangling relative targets inside the root are safe.
func validateLink(root interface {
	Lstat(string) (fs.FileInfo, error)
	Readlink(string) (string, error)
}, path string) error {
	var resolved []string
	pending := strings.Split(path, "/")
	linksLeft := 40
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return errors.New("symlink target escapes the workspace")
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := filepath.Join(append(resolved, component)...)
		info, err := root.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			resolved = append(resolved, component)
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, component)
			continue
		}
		if linksLeft == 0 {
			return errors.New("too many symlinks in target")
		}
		linksLeft--
		target, err := root.Readlink(candidate)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
			return errors.New("symlink target is absolute")
		}
		pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
	}
	return nil
}
