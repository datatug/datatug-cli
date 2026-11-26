package demogit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

// New returns a client that authenticates with token. token is never logged,
// persisted, or put in a URL or git config.
func New(cfg Config, token string) *Client {
	if cfg.Branch == "" {
		cfg.Branch = "datatug-demo"
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://api.github.com/"
	}
	if cfg.GitBaseURL == "" {
		cfg.GitBaseURL = "https://github.com/"
	}
	cfg.APIBaseURL = strings.TrimRight(cfg.APIBaseURL, "/") + "/"
	cfg.GitBaseURL = strings.TrimRight(cfg.GitBaseURL, "/") + "/"
	if cfg.ForkWait == 0 {
		cfg.ForkWait = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 45 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		}
	}
	cfg.Limits = defaultLimits(cfg.Limits)
	return &Client{cfg: cfg, token: token, sleep: waitForPoll}
}

func (c *Client) apiClient() (*github.Client, error) {
	options := []github.ClientOptionsFunc{
		github.WithURLs(&c.cfg.APIBaseURL, &c.cfg.APIBaseURL),
		github.WithHTTPClient(c.cfg.HTTPClient),
		github.WithDisableRateLimitCheck(),
	}
	if c.token != "" {
		options = append(options, github.WithAuthToken(c.token))
	}
	api, err := github.NewClient(options...)
	if err != nil {
		return nil, c.operationError("configure GitHub API", err)
	}
	return api, nil
}

// User returns the login of the token's owner.
func (c *Client) User(ctx context.Context) (string, error) {
	api, err := c.apiClient()
	if err != nil {
		return "", err
	}
	user, err := c.getUser(ctx, api)
	return user.GetLogin(), err
}

func (c *Client) getUser(ctx context.Context, api *github.Client) (*github.User, error) {
	user, resp, err := api.Users.Get(ctx, "")
	if err != nil {
		return nil, c.apiError("get GitHub user", resp, err)
	}
	if user.GetLogin() == "" {
		return nil, errors.New("GitHub user response has no login")
	}
	return user, nil
}

// FindFork returns the visitor's fork of the canonical repository, or
// ErrNoFork. It does not create one.
func (c *Client) FindFork(ctx context.Context) (Repo, error) {
	api, err := c.apiClient()
	if err != nil {
		return Repo{}, err
	}
	user, err := c.getUser(ctx, api)
	if err != nil {
		return Repo{}, err
	}
	return c.findFork(ctx, api, user.GetLogin())
}

func (c *Client) findFork(ctx context.Context, api *github.Client, login string) (Repo, error) {
	candidate := Repo{Owner: login, Name: c.cfg.Canonical.Name}
	repo, resp, err := api.Repositories.Get(ctx, candidate.Owner, candidate.Name)
	if err != nil {
		if statusIs(resp, http.StatusNotFound) {
			return Repo{}, ErrNoFork
		}
		return Repo{}, c.apiError("find GitHub fork", resp, err)
	}
	canonical := c.cfg.Canonical.FullName()
	if !repo.GetFork() || !(strings.EqualFold(repo.GetParent().GetFullName(), canonical) ||
		strings.EqualFold(repo.GetSource().GetFullName(), canonical)) {
		return Repo{}, ErrNoFork
	}
	if fork, ok := repoFromResponse(repo); ok {
		return fork, nil
	}
	return candidate, nil
}

// EnsureFork returns the visitor's fork, creating it when there is none and
// waiting until GitHub serves it. created reports whether it was made now.
// Callers ask the visitor before calling it.
func (c *Client) EnsureFork(ctx context.Context) (fork Repo, created bool, err error) {
	api, err := c.apiClient()
	if err != nil {
		return Repo{}, false, err
	}
	user, err := c.getUser(ctx, api)
	if err != nil {
		return Repo{}, false, err
	}
	fork, err = c.findFork(ctx, api, user.GetLogin())
	if !errors.Is(err, ErrNoFork) {
		return fork, false, err
	}

	canonical := c.cfg.Canonical
	started := c.cfg.Now().Truncate(time.Second)
	repo, resp, err := api.Repositories.CreateFork(ctx, canonical.Owner, canonical.Name, github.CreateForkRequest{
		DefaultBranchOnly: new(true),
	})
	_, accepted := errors.AsType[*github.AcceptedError](err)
	if err != nil && !accepted {
		if statusIs(resp, http.StatusNotFound) {
			return Repo{}, false, fmt.Errorf("demo repository not found: %s", canonical.FullName())
		}
		return Repo{}, false, c.apiError("create GitHub fork", resp, err)
	}
	fork, ok := repoFromResponse(repo)
	if !ok {
		return Repo{}, false, errors.New("GitHub fork response has no repository name")
	}

	// The endpoint also returns existing forks. A different name or older
	// creation timestamp identifies a fork that was already there.
	createdAt := repo.GetCreatedAt().Time
	created = strings.EqualFold(fork.Name, canonical.Name) &&
		(createdAt.IsZero() || !createdAt.Before(started))
	if err := c.waitForFork(ctx, api, fork); err != nil {
		return Repo{}, false, err
	}
	return fork, created, nil
}

func repoFromResponse(repo *github.Repository) (Repo, bool) {
	owner, name, ok := strings.Cut(repo.GetFullName(), "/")
	if !ok {
		owner, name = repo.GetOwner().GetLogin(), repo.GetName()
	}
	return Repo{Owner: owner, Name: name}, owner != "" && name != ""
}

func (c *Client) waitForFork(ctx context.Context, api *github.Client, fork Repo) (err error) {
	ctx, cancel := context.WithTimeoutCause(ctx, c.cfg.ForkWait, ErrForkNotReady)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = context.Cause(ctx)
		}
	}()
	for delay := 250 * time.Millisecond; ; delay = min(2*delay, 2*time.Second) {
		repo, resp, err := api.Repositories.Get(ctx, fork.Owner, fork.Name)
		if err != nil && !statusIs(resp, http.StatusNotFound, http.StatusAccepted) {
			return c.apiError("wait for GitHub fork", resp, err)
		}
		if err == nil && repo.GetDefaultBranch() != "" {
			ref, resp, err := api.Git.GetRef(ctx, fork.Owner, fork.Name, "heads/"+repo.GetDefaultBranch())
			if err == nil && ref.GetObject().GetSHA() != "" {
				return nil
			}
			if err != nil && !statusIs(resp, http.StatusNotFound, http.StatusConflict, http.StatusAccepted) {
				return c.apiError("wait for GitHub fork default branch", resp, err)
			}
		}
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

func waitForPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func statusIs(resp *github.Response, codes ...int) bool {
	for _, code := range codes {
		if resp != nil && resp.StatusCode == code {
			return true
		}
	}
	return false
}

func (c *Client) apiError(operation string, resp *github.Response, err error) error {
	if statusIs(resp, http.StatusForbidden, http.StatusTooManyRequests) {
		retryAfter := resp.Header.Get("Retry-After")
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || retryAfter != "" {
			return fmt.Errorf("%s: %w", operation, &RateLimitError{Reset: c.rateLimitReset(resp.Header, retryAfter)})
		}
	}
	if statusIs(resp, http.StatusUnauthorized, http.StatusForbidden) {
		return fmt.Errorf("%s: %w", operation, ErrAuth)
	}
	return c.operationError(operation, err)
}

func (c *Client) rateLimitReset(headers http.Header, retryAfter string) time.Time {
	now := c.cfg.Now()
	if seconds, err := strconv.ParseInt(retryAfter, 10, 32); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if reset, err := http.ParseTime(retryAfter); err == nil {
		return reset
	}
	if epoch, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		return time.Unix(epoch, 0)
	}
	return now.Add(time.Minute)
}

// API and Git servers can put arbitrary text in their errors. Discard a
// credential-bearing cause so it is redacted even when the caller unwraps it.
func (c *Client) operationError(operation string, err error) error {
	if c.token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(gitAuthUser + ":" + c.token))
		message := err.Error()
		for _, secret := range []string{c.token, url.PathEscape(c.token), url.QueryEscape(c.token), auth} {
			if strings.Contains(message, secret) {
				err = errors.New("request failed (credential redacted)")
				break
			}
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
