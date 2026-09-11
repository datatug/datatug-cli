// Package personalqueries resolves the on-disk directory a principal's
// personal (never-shared) queries live under for `datatug serve` — see
// ResolveProjectDir.
package personalqueries

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DirEnv names the environment variable that overrides the default personal
// queries base directory (lead assumption, S172 — no prior art names this
// specific variable; mirrors pkg/accesspolicies.DirEnv's
// DATATUG_POLICIES_DIR pattern for a sibling per-user, home-rooted
// convention).
const DirEnv = "DATATUG_PERSONAL_DIR"

// DefaultBaseDir is the per-OS-user personal-queries base directory,
// relative to the home directory, that every project's personal root sits
// under: ResolveProjectDir(projectID) joins this (or $DATATUG_PERSONAL_DIR)
// with projectID.
const DefaultBaseDir = ".datatug/projects"

// userHomeDir is a seam for testing ResolveProjectDir without touching the
// real home directory — mirrors pkg/auth/gauth/file_store.go's
// userConfigDir seam.
var userHomeDir = os.UserHomeDir

// ErrInvalidProjectID is returned by ResolveProjectDir when projectID is not
// safe to use as a single, contained directory-name path segment.
var ErrInvalidProjectID = errors.New("invalid project id")

// ResolveProjectDir returns the on-disk directory a principal's personal
// queries for projectID live under: <base>/<projectID>, where base is
// $DATATUG_PERSONAL_DIR when set (non-blank), else ~/.datatug/projects.
// The returned directory's own "queries/" subtree is read exactly like a
// shared project's — see this package's caller, getPersonalQueries.
//
// Founder ruling 2026-09-11 (S172, verbatim): "where personal queries live
// on disk for datatug serve. - I'm Ok with your suggestion for now." The
// accepted suggestion: personal queries live OUTSIDE the shared project
// directory, under the serving OS user's home, keyed by project id —
// keeping personal content out of the project's own git repo entirely
// (nothing personal is ever committed alongside the shared project), and,
// unlike this feature's original `<project>/user:<principalID>/` on-disk
// layout, never putting a colon in a directory name (illegal in a Windows
// path segment). "for now" is explicit in the founder's own reply: this
// layout is provisional, not settled — say so wherever it is documented.
//
// This directory is per OS user and per PROJECT, not per serving
// PRINCIPAL ID (lead assumption, S172, flagged for the founder to
// overrule): two `datatug serve --as alice` / `--as bob` invocations run
// by the same OS user on the same machine read and write the very same
// files on disk here — only the wire root id
// (datatug.RootUserFolderPrefix+principalID, unchanged by this package)
// differs between the two responses. A future revision could nest
// principalID under this directory if that assumption turns out wrong.
func ResolveProjectDir(projectID string) (string, error) {
	if err := validateProjectID(projectID); err != nil {
		return "", err
	}
	base := strings.TrimSpace(os.Getenv(DirEnv))
	if base == "" {
		home, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve personal queries directory: %w", err)
		}
		base = filepath.Join(home, filepath.FromSlash(DefaultBaseDir))
	}
	return filepath.Join(base, projectID), nil
}

// validateProjectID defensively rejects a projectID that is not safe to use
// as a single, contained path segment under the personal-queries base
// directory: never empty, never containing a path separator (forward or
// backward slash — a value must never address a nested path or escape the
// base directory that way), never containing ".." (a parent-directory
// traversal, whether the whole segment or embedded in it), and never
// exactly "." (the current-directory segment).
func validateProjectID(projectID string) error {
	switch {
	case projectID == "":
		return fmt.Errorf("%w: empty", ErrInvalidProjectID)
	case strings.ContainsAny(projectID, `/\`):
		return fmt.Errorf("%w: %q contains a path separator", ErrInvalidProjectID, projectID)
	case strings.Contains(projectID, ".."):
		return fmt.Errorf("%w: %q contains a parent-directory traversal", ErrInvalidProjectID, projectID)
	case projectID == ".":
		return fmt.Errorf("%w: %q is the current-directory segment", ErrInvalidProjectID, projectID)
	}
	return nil
}
