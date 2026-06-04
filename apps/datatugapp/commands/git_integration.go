package commands

import (
	"fmt"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/urfave/cli/v3"
)

// gitMode is the resolved version-control behaviour for a mutating command.
type gitMode int

const (
	// gitModeNone performs no version-control action: files are written and
	// nothing is staged or committed.
	gitModeNone gitMode = iota
	// gitModeStage stages written files into the git index (staging logic is
	// implemented by a later task).
	gitModeStage
)

// gitFlag is the shared --git flag reused across mutating commands. Its value
// selects the version-control behaviour: none (default), stage, or commit.
var gitFlag = cli.StringFlag{
	Name:  "git",
	Usage: "Version-control action for written files: none, stage, or commit",
	Value: "none",
}

// resolveGitMode maps a --git flag value to a gitMode. An empty value defaults
// to none. An unknown value or "commit" yields a non-zero-exit error: unknown
// names the bad value and the supported set; commit reports it is not yet
// supported.
func resolveGitMode(value string) (gitMode, error) {
	switch value {
	case "", "none":
		return gitModeNone, nil
	case "stage":
		return gitModeStage, nil
	case "commit":
		return gitModeNone, cli.Exit("--git=commit is not yet supported", 2)
	default:
		return gitModeNone, cli.Exit(fmt.Sprintf("invalid --git value %q (supported values: none, stage, commit)", value), 2)
	}
}

// gitPreflight performs the fail-loud check that must run before any write.
// When mode is stage it verifies the project is inside a git repository (via
// openRepo), returning openRepo's "not a git repository" error otherwise. It is
// a no-op for none. (commit and unknown values are already rejected by
// resolveGitMode.)
func gitPreflight(projectDir string, mode gitMode) error {
	if mode == gitModeStage {
		if _, err := openRepo(projectDir); err != nil {
			return err
		}
	}
	return nil
}

// applyGit performs the post-write version-control action for the given mode.
// When mode is stage it stages exactly writtenPaths into the index; it is a
// no-op for none.
func applyGit(projectDir string, mode gitMode, writtenPaths []string) error {
	if mode == gitModeStage {
		return stageFiles(projectDir, writtenPaths)
	}
	return nil
}

// openRepo opens the git repository that contains projectDir, walking up
// parent directories (DetectDotGit). A directory that is not inside a git
// repository yields a clear "not a git repository" error suitable for a
// fail-loud preflight.
func openRepo(projectDir string) (*git.Repository, error) {
	repo, err := git.PlainOpenWithOptions(projectDir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, cli.Exit(fmt.Sprintf("%s is not a git repository: %v", projectDir, err), 2)
	}
	return repo, nil
}

// stageFiles stages exactly the given absolute paths into the index of the git
// repository containing projectDir. It never stages anything else (no
// `git add -A`), so unrelated staged/unstaged changes remain untouched. Each
// path is added by its location relative to the worktree root.
func stageFiles(projectDir string, absPaths []string) error {
	if len(absPaths) == 0 {
		return nil
	}
	repo, err := openRepo(projectDir)
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	// Resolve symlinks so paths relative to the worktree root compute cleanly
	// even when one side is symlinked (e.g. /tmp -> /private/tmp on macOS).
	root := wt.Filesystem.Root()
	if resolved, rErr := filepath.EvalSymlinks(root); rErr == nil {
		root = resolved
	}
	for _, p := range absPaths {
		abs := p
		if resolved, rErr := filepath.EvalSymlinks(p); rErr == nil {
			abs = resolved
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return fmt.Errorf("failed to resolve %s relative to worktree %s: %w", p, root, err)
		}
		if _, err := wt.Add(rel); err != nil {
			return fmt.Errorf("failed to stage %s: %w", rel, err)
		}
	}
	return nil
}
