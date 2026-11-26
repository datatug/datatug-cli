package demogit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
)

const gitAuthUser = "x-access-token"

// go-git selects a transport through its process-wide registry. Install one
// adapter at initialization. Only demogit sessions select a custom client.
func init() {
	for _, scheme := range []string{"http", "https"} {
		client.InstallProtocol(scheme, gitHTTPTransport{Transport: client.Protocols[scheme]})
	}
}

type gitHTTPTransport struct{ transport.Transport }

type gitHTTPAuth struct {
	*githttp.BasicAuth
	client *http.Client
}

func (t gitHTTPTransport) sessionTransport(auth transport.AuthMethod) (transport.Transport, transport.AuthMethod) {
	if auth, ok := auth.(*gitHTTPAuth); ok {
		return githttp.NewClientWithOptions(auth.client, &githttp.ClientOptions{
			RedirectPolicy: githttp.FollowRedirects,
		}), auth.BasicAuth
	}
	return t.Transport, auth
}

func (t gitHTTPTransport) NewUploadPackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	backend, auth := t.sessionTransport(auth)
	return backend.NewUploadPackSession(ep, auth)
}

func (t gitHTTPTransport) NewReceivePackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	backend, auth := t.sessionTransport(auth)
	return backend.NewReceivePackSession(ep, auth)
}

func (c *Client) gitAuth() transport.AuthMethod {
	basic := &githttp.BasicAuth{Username: gitAuthUser, Password: c.token}
	base := strings.ToLower(c.cfg.GitBaseURL)
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return basic
	}
	client := *c.cfg.HTTPClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		origin := via[0].URL
		if req.URL.Scheme != origin.Scheme || !strings.EqualFold(req.URL.Host, origin.Host) || req.URL.User != nil ||
			!sameGitRequestPath(origin.EscapedPath(), req.URL.EscapedPath()) {
			return ErrGitRedirect
		}
		if check := c.cfg.HTTPClient.CheckRedirect; check != nil {
			return check(req, via)
		}
		return nil
	}
	return &gitHTTPAuth{BasicAuth: basic, client: &client}
}

func sameGitRequestPath(from, to string) bool {
	service := "/" + path.Base(from)
	if strings.HasSuffix(from, "/info/refs") {
		service = "/info/refs"
	}
	from = strings.TrimSuffix(from, service)
	to, ok := strings.CutSuffix(to, service)
	if !ok {
		return false
	}
	fromParent, fromRepo := path.Split(from)
	toParent, toRepo := path.Split(to)
	fromBase, fromOwner := path.Split(strings.TrimSuffix(fromParent, "/"))
	toBase, toOwner := path.Split(strings.TrimSuffix(toParent, "/"))
	return fromBase == toBase && strings.EqualFold(fromOwner, toOwner) &&
		strings.TrimSuffix(strings.ToLower(fromRepo), ".git") == strings.TrimSuffix(strings.ToLower(toRepo), ".git")
}

func (c *Client) gitURL(fork Repo) string { return c.cfg.GitBaseURL + fork.FullName() + ".git" }

func (c *Client) gitError(operation string, err error) error {
	if isGitRedirect(err) {
		return fmt.Errorf("%s: %w: %w", operation, ErrNoFork, ErrGitRedirect)
	}
	if isGitAuthError(err) {
		return fmt.Errorf("%s: %w", operation, ErrAuth)
	}
	return c.operationError(operation, err)
}

func isGitRedirect(err error) bool {
	// go-git's HTTP POST wrapper exposes its cause as a field.
	if wrapped, ok := errors.AsType[*plumbing.UnexpectedError](err); ok {
		err = wrapped.Err
	}
	return errors.Is(err, ErrGitRedirect)
}

func isGitAuthError(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed)
}

// uncommittedStorage lets go-git stage and write new objects while keeping
// the existing HEAD available for a retry until the remote accepts the push.
type uncommittedStorage struct{ storage.Storer }

func (uncommittedStorage) SetReference(*plumbing.Reference) error { return nil }

// Save commits every change of the workspace at repoDir (a go-git clone whose
// HEAD is the base commit) and pushes it to the branch of fork, never forcing:
// a branch that moved since the base is ErrDiverged.
func (c *Client) Save(ctx context.Context, repoDir string, fork Repo, opts SaveOptions) (SaveResult, error) {
	if err := ctx.Err(); err != nil {
		return SaveResult{}, err
	}
	if strings.EqualFold(fork.FullName(), c.cfg.Canonical.FullName()) {
		return SaveResult{}, errors.New("cannot save to the canonical demo repository")
	}
	if opts.Branch == "" {
		opts.Branch = c.cfg.Branch
	}
	if opts.Message == "" {
		opts.Message = "Save DataTug demo workspace"
	}
	ref := plumbing.NewBranchReferenceName(opts.Branch)
	if err := ref.Validate(); err != nil {
		return SaveResult{}, c.operationError("invalid save branch", err)
	}

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return SaveResult{}, c.operationError("open workspace repository", err)
	}
	base, err := repo.Head()
	if err != nil {
		return SaveResult{}, c.operationError("read workspace HEAD", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return SaveResult{}, c.operationError("open workspace worktree", err)
	}
	changes, err := workspaceChanges(repo, worktree)
	if err != nil {
		return SaveResult{}, c.operationError("read workspace status", err)
	}
	if len(changes) == 0 {
		return SaveResult{}, ErrNothingToSave
	}

	api, err := c.apiClient()
	if err != nil {
		return SaveResult{}, err
	}
	user, err := c.getUser(ctx, api)
	if err != nil {
		return SaveResult{}, err
	}
	if user.GetID() <= 0 {
		return SaveResult{}, errors.New("GitHub user response has no account ID")
	}
	login := user.GetLogin()
	when := c.cfg.Now()
	commit := &git.CommitOptions{
		Parents:   []plumbing.Hash{base.Hash()},
		Author:    &object.Signature{Name: login, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", user.GetID(), login), When: when},
		Committer: &object.Signature{Name: "DataTug demo", Email: "demo@datatug.app", When: when},
	}

	remoteConfig := &config.RemoteConfig{Name: "demo-save", URLs: []string{c.gitURL(fork)}}
	auth := c.gitAuth()
	remote := git.NewRemote(memory.NewStorage(), remoteConfig)
	if err := c.validateFork(ctx, api, fork, login); err != nil {
		return SaveResult{}, err
	}
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil && !errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return SaveResult{}, c.gitError("list Git refs on fork", err)
	}
	required, err := checkSaveBranch(repo, refs, base, ref)
	if err != nil {
		return SaveResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return SaveResult{}, err
	}
	sha, err := stageCommit(repo, worktree, opts.Message, commit)
	if err != nil {
		return SaveResult{}, c.operationError("commit workspace changes", err)
	}
	remote = git.NewRemote(repo.Storer, remoteConfig)
	if err := c.validateFork(ctx, api, fork, login); err != nil {
		return SaveResult{}, err
	}
	err = remote.PushContext(ctx, &git.PushOptions{
		RemoteName:        remoteConfig.Name,
		RefSpecs:          []config.RefSpec{config.RefSpec(sha.String() + ":" + ref.String())},
		Auth:              auth,
		RequireRemoteRefs: required,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		if !isGitAuthError(err) && !isGitRedirect(err) && pushDiverged(err) {
			return SaveResult{}, ErrDiverged
		}
		return SaveResult{}, c.gitError("push workspace to fork", err)
	}

	result := SaveResult{
		Fork: fork, Branch: opts.Branch, CommitSHA: sha.String(),
		CommitURL: c.cfg.GitBaseURL + fork.FullName() + "/commit/" + sha.String(),
	}
	if err := repo.Storer.CheckAndSetReference(plumbing.NewHashReference(base.Name(), sha), base); err != nil {
		return result, c.operationError("workspace was pushed but updating local HEAD failed", err)
	}
	return result, nil
}

func checkSaveBranch(repo *git.Repository, refs []*plumbing.Reference, base *plumbing.Reference, branch plumbing.ReferenceName) ([]config.RefSpec, error) {
	var tip *plumbing.Reference
	for _, ref := range refs {
		if ref.Name() == plumbing.HEAD && ref.Target() == branch {
			return nil, errors.New("cannot save to the fork's default branch")
		}
		if ref.Name() == branch {
			tip = ref
		}
	}
	if tip == nil {
		return nil, nil
	}
	if tip.Hash() != base.Hash() {
		ancestor, err := repo.CommitObject(tip.Hash())
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, ErrDiverged
		}
		if err != nil {
			return nil, fmt.Errorf("read fork branch commit: %w", err)
		}
		local, err := repo.CommitObject(base.Hash())
		if err != nil {
			return nil, fmt.Errorf("read workspace base commit: %w", err)
		}
		ok, err := ancestor.IsAncestor(local)
		if err != nil && !errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, fmt.Errorf("check fork branch ancestry: %w", err)
		}
		if !ok {
			return nil, ErrDiverged
		}
	}
	return []config.RefSpec{config.RefSpec(tip.Hash().String() + ":" + branch.String())}, nil
}

func stageCommit(repo *git.Repository, worktree *git.Worktree, message string, opts *git.CommitOptions) (plumbing.Hash, error) {
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return plumbing.ZeroHash, err
	}
	pending, err := git.Open(uncommittedStorage{repo.Storer}, worktree.Filesystem)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	wt, _ := pending.Worktree() // git.Open received the non-nil workspace filesystem.
	sha, err := wt.Commit(message, opts)
	if errors.Is(err, git.ErrEmptyCommit) {
		return plumbing.ZeroHash, ErrNothingToSave
	}
	return sha, err
}

func pushDiverged(err error) bool {
	if errors.Is(err, git.ErrNonFastForwardUpdate) || errors.Is(err, storage.ErrReferenceHasChanged) || errors.Is(err, plumbing.ErrObjectNotFound) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "non-fast-forward") ||
		(strings.HasPrefix(message, "remote ref ") && strings.Contains(message, " required to be ")) ||
		strings.Contains(message, "failed to update ref") || strings.Contains(message, "cannot lock ref")
}

// Open clones the saved branch of fork (Config.Branch, else the fork's default
// branch) into an absent or empty destDir. It validates the tree before
// checkout and the workspace before publishing. Errors wrap ErrUntrustedContent.
func (c *Client) Open(ctx context.Context, fork Repo, destDir, projectDir string) (branch string, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrUntrustedContent) {
			err = fmt.Errorf("%w: %w", ErrUntrustedContent, err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	destDir = filepath.Clean(destDir)
	if err := checkDestination(destDir); err != nil {
		return "", err
	}
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("prepare clone destination: %w", err)
	}
	// Publish only after validation. A private sibling directory lets cleanup
	// remove the clone even when another caller uses the same destination.
	checkout, err := os.MkdirTemp(parent, ".datatug-demo-")
	if err != nil {
		return "", fmt.Errorf("prepare temporary clone: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(checkout))
		}
	}()
	api, err := c.apiClient()
	if err != nil {
		return "", err
	}
	user, err := c.getUser(ctx, api)
	if err != nil {
		return "", err
	}
	opts := &git.CloneOptions{
		URL: c.gitURL(fork), Auth: c.gitAuth(), ReferenceName: plumbing.NewBranchReferenceName(c.cfg.Branch),
		SingleBranch: true, Depth: c.cfg.Limits.Depth, Tags: git.NoTags, RecurseSubmodules: git.NoRecurseSubmodules,
		NoCheckout: true,
	}
	metadata := filepath.Join(checkout, git.GitDirName)
	clone := func() (*git.Repository, error) {
		if err := c.validateFork(ctx, api, fork, user.GetLogin()); err != nil {
			return nil, err
		}
		fs := cloneMetadataFS{Filesystem: osfs.New(metadata), maxBytes: c.cfg.Limits.MaxBytes}
		store := filesystem.NewStorage(&fs, cache.NewObjectLRUDefault())
		repo, err := git.CloneContext(ctx, store, osfs.New(checkout), opts)
		if err != nil {
			fs.closeFiles()
		}
		return repo, err
	}
	repo, err := clone()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(err, git.NoMatchingRefSpecError{}) || errors.Is(err, plumbing.ErrReferenceNotFound) {
		if err := os.RemoveAll(metadata); err != nil {
			return "", fmt.Errorf("reset metadata before cloning the default branch: %w", err)
		}
		opts.ReferenceName = plumbing.HEAD
		repo, err = clone()
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", c.gitError("clone workspace from fork", err)
	}
	limits, err := cloneWorktreeLimits(ctx, checkout, c.cfg.Limits)
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", c.operationError("read cloned branch", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return "", c.operationError("read cloned commit", err)
	}
	if err := validateTree(ctx, repo.Storer, commit.TreeHash, limits); err != nil {
		return "", c.operationError("validate cloned tree", err)
	}
	worktree, _ := repo.Worktree() // The clone is non-bare.
	worktree.Filesystem = checkoutFilesystem{Filesystem: worktree.Filesystem, ctx: ctx}
	if err := worktree.Reset(&git.ResetOptions{Commit: head.Hash(), Mode: git.HardReset}); err != nil {
		return "", c.operationError("check out cloned workspace", err)
	}
	// Checkout writes an index. Count the final metadata size as well.
	limits, err = cloneWorktreeLimits(ctx, checkout, c.cfg.Limits)
	if err != nil {
		return "", err
	}
	if err := validateWorkspace(ctx, checkout, projectDir, limits); err != nil {
		return "", err
	}
	if err := InstallClone(ctx, checkout, destDir); err != nil {
		return "", fmt.Errorf("install cloned workspace: %w", err)
	}
	return head.Name().Short(), nil
}

// InstallClone publishes a validated checkout by moving it into an absent or
// empty destination directory on the same filesystem.
func InstallClone(ctx context.Context, checkout, destDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Rename cannot replace an existing directory through os.Rename. Remove
	// only an empty directory, leaving any files another caller added intact.
	if err := syscall.Rmdir(destDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(checkout, destDir)
}

// go-git does not take a context during checkout. Check it at the worktree
// write boundary and between chunks while copying a file.
type checkoutFilesystem struct {
	billy.Filesystem
	ctx context.Context
}

func (fs checkoutFilesystem) OpenFile(path string, flag int, mode os.FileMode) (billy.File, error) {
	if err := fs.ctx.Err(); err != nil {
		return nil, err
	}
	f, err := fs.Filesystem.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return checkoutFile{File: f, ctx: fs.ctx}, nil
}

func (fs checkoutFilesystem) Symlink(target, path string) error {
	if err := fs.ctx.Err(); err != nil {
		return err
	}
	return fs.Filesystem.Symlink(target, path)
}

type checkoutFile struct {
	billy.File
	ctx context.Context
}

func (f checkoutFile) Write(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}

func checkDestination(destDir string) error {
	info, err := os.Lstat(destDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect clone destination: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("clone destination %q is not a directory", destDir)
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return fmt.Errorf("inspect clone destination: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("clone destination %q is not empty", destDir)
	}
	return nil
}

// NewBranchName returns the branch offered after ErrDiverged:
// "<Config.Branch>-<UTC yyyymmdd-hhmmss>".
func (c *Client) NewBranchName() string {
	return c.cfg.Branch + "-" + c.cfg.Now().UTC().Format("20060102-150405")
}
