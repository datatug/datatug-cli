// Package dtdemo provides the SSH demo's welcome screen, Save-to-GitHub
// workspace module, and in-memory per-session GitHub authentication state.
package dtdemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/datatug/datatug-cli/pkg/demogit"
	"golang.org/x/oauth2"
)

const (
	// ModuleID is the datatugui root module ID for Save-to-GitHub.
	ModuleID = "demo_save"
	// ModuleText is the menu label for Save-to-GitHub.
	ModuleText = "Save to GitHub"
	// GitHubScope is the minimum OAuth scope required to fork a public repo
	// and push to the visitor's fork.
	GitHubScope = "public_repo"
	// DefaultSaveBranch is the default branch name on the visitor's fork.
	DefaultSaveBranch = "datatug-demo"
	// SaveNotConfiguredNotice is displayed in the Save module when the demo
	// server was started against a non-GitHub repository.
	SaveNotConfiguredNotice = "Saving to GitHub is not configured on this demo server."
	// ExternalDatabasesNotice states that external databases are not part of the repo.
	ExternalDatabasesNotice = "Note: OpenVaultDB cloud databases and any other external databases are not part of this repository, so changes made there are neither saved nor undone."
)

// GitClient is the subset of *demogit.Client used by dtdemo screens.
// Production uses demogit.New; tests substitute a fake through newGitClient.
type GitClient interface {
	User(ctx context.Context) (string, error)
	FindFork(ctx context.Context) (demogit.Repo, error)
	EnsureFork(ctx context.Context) (fork demogit.Repo, created bool, err error)
	Save(ctx context.Context, repoDir string, fork demogit.Repo, opts demogit.SaveOptions) (demogit.SaveResult, error)
	Open(ctx context.Context, fork demogit.Repo, destDir, projectDir string) (branch string, err error)
	NewBranchName() string
}

var _ GitClient = (*demogit.Client)(nil)

// Seams over demogit and ghauth, replaced in tests.
var (
	newGitClient = func(cfg demogit.Config, token string) GitClient {
		return demogit.New(cfg, token)
	}
	listChanges = demogit.Changes
	deleteToken = ghauth.DeleteToken
	nowFunc     = time.Now
)

// AuthState holds one visitor's GitHub sign-in state in process memory only.
// The token is never persisted, logged, written to git config, or printed.
type AuthState struct {
	mu    sync.RWMutex
	token string
	login string
}

// NewAuthState creates an empty in-memory GitHub sign-in state.
func NewAuthState() *AuthState { return &AuthState{} }

// Set records the GitHub access token and optional login in memory.
func (a *AuthState) Set(token, login string) {
	a.mu.Lock()
	a.token = token
	if login != "" {
		a.login = login
	}
	a.mu.Unlock()
}

// SetLogin records the authenticated GitHub username.
func (a *AuthState) SetLogin(login string) {
	a.mu.Lock()
	a.login = login
	a.mu.Unlock()
}

// Token returns the in-memory access token, or "" when not signed in.
func (a *AuthState) Token() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.token
}

// Login returns the authenticated GitHub username if known.
func (a *AuthState) Login() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.login
}

// SignedIn reports whether an access token is held in memory.
func (a *AuthState) SignedIn() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.token != ""
}

// Clear forgets the in-memory token and login and deletes the in-memory ghauth copy.
func (a *AuthState) Clear() {
	a.mu.Lock()
	a.token = ""
	a.login = ""
	a.mu.Unlock()
	_ = deleteToken()
}

// String redacts the token so AuthState can never leak secrets through fmt.
func (a *AuthState) String() string { return "AuthState{redacted}" }

// Config holds the session-wide parameters shared by the welcome screen and the
// Save-to-GitHub workspace module.
type Config struct {
	Canonical      demogit.Repo
	RepoDir        string
	ProjectDir     string
	SaveBranch     string
	GitHubClientID string
	Deadline       time.Time
	Auth           *AuthState

	// Optional overrides for the device-flow screen in tests.
	RequestDeviceCode func(ctx context.Context, clientID, scope string) (*ghauth.DeviceCodeResponse, error)
	PollForToken      func(ctx context.Context, clientID, clientSecret, deviceCode string, interval int, onAttempt func(int)) (*oauth2.Token, error)
	SaveToken         func(token *oauth2.Token) error
}

// GitHubEnabled reports whether GitHub sign-in, fork opening, and saving are
// configured for this demo session (i.e. Canonical names a GitHub owner/repo).
func (c Config) GitHubEnabled() bool {
	return c.Canonical.Owner != "" && c.Canonical.Name != ""
}

func (c Config) normalized() Config {
	if c.Auth == nil {
		c.Auth = NewAuthState()
	}
	if c.SaveBranch == "" {
		c.SaveBranch = DefaultSaveBranch
	}
	if c.GitHubClientID == "" {
		c.GitHubClientID = dtproject.DefaultGitHubClientID
	}
	return c
}

func (c Config) demogitConfig() demogit.Config {
	return demogit.Config{
		Canonical: c.Canonical,
		Branch:    c.SaveBranch,
	}
}

func (c Config) gitClient() GitClient {
	return newGitClient(c.demogitConfig(), c.Auth.Token())
}

// ScopeExplanation explains what the public_repo OAuth scope allows and how
// DataTug uses and discards the token.
func ScopeExplanation(canonical demogit.Repo) string {
	return fmt.Sprintf(
		"GitHub sign-in requests only the %s scope.\n\n"+
			"What %s allows: read and write your public repositories; DataTug uses it only to find or create your fork of %s and to read or save your saved branch; DataTug forgets it when you disconnect; revoke any time at https://github.com/settings/applications.",
		GitHubScope, GitHubScope, canonical.FullName(),
	)
}

// FormatErrorFor turns a demogit or device-flow error into plain words for the visitor.
func FormatErrorFor(err error, canonical demogit.Repo) string {
	if err == nil {
		return ""
	}
	var rle *demogit.RateLimitError
	if errors.As(err, &rle) {
		return fmt.Sprintf("GitHub rate limit reached. Try again after %s.", rle.Reset.UTC().Format("15:04:05 UTC"))
	}
	switch {
	case errors.Is(err, demogit.ErrAuth):
		return "GitHub refused your sign-in (the token was revoked or expired). Sign in again to continue."
	case errors.Is(err, demogit.ErrNoFork):
		return fmt.Sprintf("No fork of %s was found in your GitHub account.", canonical.FullName())
	case errors.Is(err, demogit.ErrForkNotReady):
		return "GitHub has not finished creating your fork yet. Wait a moment and try again."
	case errors.Is(err, demogit.ErrUntrustedContent):
		return fmt.Sprintf("Your fork cannot be opened in the demo: %v", err)
	case errors.Is(err, demogit.ErrDiverged):
		return "The branch on your fork has changes this workspace does not have. DataTug never force-pushes."
	case errors.Is(err, demogit.ErrNothingToSave):
		return "Nothing to save: the workspace has no changes."
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "access_denied"):
		return "GitHub sign-in was denied. You can continue in the temporary demo workspace without signing in."
	case strings.Contains(msg, "expired_token"):
		return "The GitHub sign-in code expired before authorization completed. Try again or continue exploring the demo project."
	case strings.Contains(msg, "failed to send request"), errors.Is(err, context.DeadlineExceeded):
		return "Could not reach GitHub right now (network error). Try again or continue exploring the demo project."
	default:
		return msg
	}
}

// ClassifyChanges splits workspace changes into project files and database files
// (*.sqlite, *.db, SQLite sidecars, and inGitDB data directories).
func ClassifyChanges(repoDir string, changes []demogit.Change) (projectFiles, dbFiles []demogit.Change) {
	for _, ch := range changes {
		if isDatabaseChange(repoDir, ch.Path) {
			dbFiles = append(dbFiles, ch)
		} else {
			projectFiles = append(projectFiles, ch)
		}
	}
	return projectFiles, dbFiles
}

func isDatabaseChange(repoDir, relPath string) bool {
	slash := filepath.ToSlash(relPath)
	lower := strings.ToLower(slash)
	for _, suffix := range []string{".sqlite", ".sqlite3", ".db", ".sqlite-wal", ".sqlite-shm", ".db-wal", ".db-shm"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	for _, seg := range strings.Split(lower, "/") {
		if seg == ".ingitdb" || seg == "ingitdb" {
			return true
		}
	}
	if repoDir == "" {
		return false
	}
	cleanRoot := filepath.Clean(repoDir)
	dir := filepath.Dir(filepath.Join(cleanRoot, filepath.FromSlash(slash)))
	for {
		if info, err := os.Stat(filepath.Join(dir, ".ingitdb")); err == nil && info.IsDir() {
			return true
		}
		if dir == cleanRoot || !strings.HasPrefix(dir, cleanRoot+string(filepath.Separator)) {
			break
		}
		dir = filepath.Dir(dir)
	}
	return false
}
