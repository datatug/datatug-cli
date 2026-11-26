// Package demogit keeps the workspace of an SSH demo session on GitHub: it
// finds or creates the visitor's fork of the canonical demo repository, saves
// the workspace to a dedicated branch of that fork without ever forcing a
// push, and clones a saved branch back into a fresh workspace.
//
// Design: spec/features/ssh-demo/README.md (D6, D7, D8). Everything here runs
// in the per-session child process; the GitHub token is held by the caller in
// memory and handed to each call, never written anywhere. Git is go-git only:
// no git binary, no hooks, no filters, no submodules.
package demogit

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Repo names a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

// FullName returns "owner/name".
func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// CloneLimits bound what Open accepts from an untrusted fork.
type CloneLimits struct {
	// MaxBytes is the total checkout byte budget, including Git metadata (default 200 MiB).
	MaxBytes int64
	// MaxFiles is the largest number of checked-out files (default 20 000).
	MaxFiles int
	// Depth is the clone depth (default 1).
	Depth int
}

// Config configures a Client. Zero fields take the documented defaults.
type Config struct {
	// Canonical is the demo repository visitors fork (required).
	Canonical Repo
	// Branch is the branch saves go to (default "datatug-demo").
	Branch string
	// APIBaseURL is the GitHub REST API root (default "https://api.github.com/").
	APIBaseURL string
	// GitBaseURL is the root of clone/push URLs (default "https://github.com/");
	// tests point it at a local server or a file:// directory.
	GitBaseURL string
	// HTTPClient is used for the API and for git over HTTP (default: a client
	// whose transport honours HTTPS_PROXY).
	HTTPClient *http.Client
	// ForkWait bounds how long EnsureFork waits for a new fork (default 60s).
	ForkWait time.Duration
	// Limits bound Open.
	Limits CloneLimits
	// Now is the clock, for branch names (default time.Now).
	Now func() time.Time
}

// Change is one changed path of the workspace, relative to the repository root.
type Change struct {
	Path string
	// Kind is "added", "modified" or "deleted".
	Kind string
}

// SaveOptions customise Save.
type SaveOptions struct {
	// Branch overrides Config.Branch, e.g. for the new branch offered after
	// ErrDiverged.
	Branch string
	// Message is the commit message (default "Save DataTug demo workspace").
	Message string
}

// SaveResult reports a successful save.
type SaveResult struct {
	Fork      Repo
	Branch    string
	CommitSHA string
	// CommitURL is the commit's page on GitHub.
	CommitURL string
}

// Errors callers branch on. Returned errors wrap these; test with errors.Is.
var (
	// ErrNothingToSave: the workspace has no change against its base commit.
	ErrNothingToSave = errors.New("nothing to save: the workspace has no changes")
	// ErrDiverged: the branch on the fork moved since the workspace's base
	// commit; nothing was pushed. Offer NewBranchName.
	ErrDiverged = errors.New("the branch on your fork has changes this workspace does not have")
	// ErrAuth: GitHub refused the token (401, or 403 without rate limiting).
	ErrAuth = errors.New("GitHub refused the sign-in")
	// ErrRateLimited: GitHub rate limited the request; see RateLimitError.
	ErrRateLimited = errors.New("GitHub rate limit reached")
	// ErrForkNotReady: the fork did not become available within ForkWait.
	ErrForkNotReady = errors.New("GitHub has not finished creating the fork")
	// ErrNoFork: the visitor has no fork of the canonical repository.
	ErrNoFork = errors.New("no fork of the demo repository found")
	// ErrForkSearchIncomplete indicates more GitHub repository pages remain to be searched.
	ErrForkSearchIncomplete = errors.New("fork search stopped after 50 pages")
	// ErrGitRedirect indicates a Git HTTP redirect outside the validated repository path or origin.
	ErrGitRedirect = errors.New("git redirect changes repository path or origin")
	// ErrUntrustedContent: the demo could not safely open the workspace.
	ErrUntrustedContent = errors.New("the repository cannot be opened in the demo")
)

// RateLimitError carries when GitHub accepts requests again.
type RateLimitError struct{ Reset time.Time }

func (e *RateLimitError) Error() string {
	return ErrRateLimited.Error() + " until " + e.Reset.UTC().Format(time.RFC3339)
}
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Client talks to GitHub with one visitor's token.
type Client struct {
	cfg   Config
	token string
	sleep func(context.Context, time.Duration) error
}
