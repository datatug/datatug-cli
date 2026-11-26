package dtdemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/datatug/datatug-cli/pkg/demogit"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"golang.org/x/oauth2"
)

func stub[T any](t *testing.T, target *T, val T) {
	t.Helper()
	orig := *target
	*target = val
	t.Cleanup(func() { *target = orig })
}

type fakeGitClient struct {
	userLogin  string
	userErr    error
	findFork   demogit.Repo
	findErr    error
	ensureFork demogit.Repo
	ensureNew  bool
	ensureErr  error
	ensureHits int
	saveRes    demogit.SaveResult
	saveErr    error
	saveFn     func(repoDir string, fork demogit.Repo, opts demogit.SaveOptions) (demogit.SaveResult, error)
	savedOpts  []demogit.SaveOptions
	openBranch string
	openErr    error
	openDir    string
	openProj   string
	newBranch  string
}

func (f *fakeGitClient) User(context.Context) (string, error) {
	return f.userLogin, f.userErr
}

func (f *fakeGitClient) FindFork(context.Context) (demogit.Repo, error) {
	return f.findFork, f.findErr
}

func (f *fakeGitClient) EnsureFork(context.Context) (demogit.Repo, bool, error) {
	f.ensureHits++
	return f.ensureFork, f.ensureNew, f.ensureErr
}

func (f *fakeGitClient) Save(_ context.Context, repoDir string, fork demogit.Repo, opts demogit.SaveOptions) (demogit.SaveResult, error) {
	f.savedOpts = append(f.savedOpts, opts)
	if f.saveFn != nil {
		return f.saveFn(repoDir, fork, opts)
	}
	return f.saveRes, f.saveErr
}

func (f *fakeGitClient) Open(_ context.Context, _ demogit.Repo, destDir, projectDir string) (string, error) {
	f.openDir = destDir
	f.openProj = projectDir
	return f.openBranch, f.openErr
}

func (f *fakeGitClient) NewBranchName() string { return f.newBranch }

func testConfig(auth *AuthState, pollTok *oauth2.Token, pollErr error) Config {
	return Config{
		Canonical:      demogit.Repo{Owner: "datatug", Name: "datatug-demo-project"},
		RepoDir:        "/tmp/demo-repo",
		ProjectDir:     "demo-project-1",
		SaveBranch:     "datatug-demo",
		GitHubClientID: "test-client-id",
		Deadline:       time.Date(2026, 11, 26, 14, 30, 0, 0, time.UTC),
		Auth:           auth,
		RequestDeviceCode: func(_ context.Context, clientID, scope string) (*ghauth.DeviceCodeResponse, error) {
			return &ghauth.DeviceCodeResponse{
				DeviceCode:      "dev-" + clientID + "-" + scope,
				UserCode:        "DEMO-1234",
				VerificationURI: "https://github.com/login/device",
				Interval:        5,
			}, nil
		},
		PollForToken: func(_ context.Context, _, _, _ string, _ int, onAttempt func(int)) (*oauth2.Token, error) {
			if onAttempt != nil {
				onAttempt(1)
			}
			return pollTok, pollErr
		},
		SaveToken: func(*oauth2.Token) error { return nil },
	}
}

func feedScreen(s nav.Screen, msgs ...tea.Msg) (nav.Screen, []tea.Msg) {
	var all []tea.Msg
	for len(msgs) > 0 {
		var cmd tea.Cmd
		s, cmd = s.Update(msgs[0])
		produced := uitest.Msgs(cmd)
		all = append(all, produced...)
		msgs = append(msgs[1:], produced...)
	}
	return s, all
}

func feedModel(m tea.Model, msgs ...tea.Msg) (tea.Model, []tea.Msg) {
	var all []tea.Msg
	for len(msgs) > 0 {
		var cmd tea.Cmd
		m, cmd = m.Update(msgs[0])
		produced := uitest.Msgs(cmd)
		all = append(all, produced...)
		msgs = append(msgs[1:], produced...)
	}
	return m, all
}

func TestAuthStateAndConfig(t *testing.T) {
	deleted := false
	stub(t, &deleteToken, func() error { deleted = true; return nil })

	a := NewAuthState()
	if a.SignedIn() || a.Token() != "" || a.Login() != "" {
		t.Fatal("new AuthState must be empty")
	}
	a.Set("secret-token-xyz", "octocat")
	if !a.SignedIn() || a.Token() != "secret-token-xyz" || a.Login() != "octocat" {
		t.Fatalf("unexpected state: signedIn=%v login=%q", a.SignedIn(), a.Login())
	}
	if strings.Contains(a.String(), "secret-token-xyz") {
		t.Fatalf("String() leaked token: %q", a.String())
	}
	a.SetLogin("alice")
	if a.Login() != "alice" {
		t.Fatalf("login = %q", a.Login())
	}
	a.Clear()
	if a.SignedIn() || a.Token() != "" || a.Login() != "" || !deleted {
		t.Fatalf("Clear failed: signedIn=%v deleted=%v", a.SignedIn(), deleted)
	}

	// Default Config normalization & default newGitClient seam.
	cfg := (Config{Canonical: demogit.Repo{Owner: "datatug", Name: "datatug-demo-project"}}).normalized()
	if cfg.Auth == nil || cfg.SaveBranch != DefaultSaveBranch || cfg.GitHubClientID == "" || !cfg.GitHubEnabled() {
		t.Fatalf("unexpected normalized config: %+v", cfg)
	}
	if (Config{}).GitHubEnabled() || (Config{Canonical: demogit.Repo{Owner: "datatug"}}).GitHubEnabled() {
		t.Fatal("empty or partial canonical repo must not enable GitHub")
	}
	if c := cfg.gitClient(); c == nil {
		t.Fatal("default gitClient returned nil")
	}
}

func TestScopeExplanationAndFormatError(t *testing.T) {
	canon := demogit.Repo{Owner: "datatug", Name: "datatug-demo-project"}
	exp := ScopeExplanation(canon)
	for _, want := range []string{
		"public_repo",
		"read and write your public repositories",
		"datatug/datatug-demo-project",
		"forgets it when you disconnect",
		"https://github.com/settings/applications",
	} {
		if !strings.Contains(exp, want) {
			t.Fatalf("ScopeExplanation missing %q in %q", want, exp)
		}
	}

	if got := FormatErrorFor(nil, canon); got != "" {
		t.Fatalf("nil error formatted as %q", got)
	}
	reset := time.Date(2026, 11, 26, 15, 4, 5, 0, time.UTC)
	cases := []struct {
		err  error
		want string
	}{
		{&demogit.RateLimitError{Reset: reset}, "15:04:05 UTC"},
		{demogit.ErrAuth, "Sign in again"},
		{demogit.ErrNoFork, "No fork of datatug/datatug-demo-project"},
		{demogit.ErrForkNotReady, "creating your fork"},
		{demogit.ErrUntrustedContent, "cannot be opened"},
		{demogit.ErrDiverged, "never force-pushes"},
		{demogit.ErrNothingToSave, "Nothing to save"},
		{errors.New("authentication failed: github error: access_denied (denied)"), "denied"},
		{errors.New("authentication failed: github error: expired_token (expired)"), "expired"},
		{errors.New("failed to send request: dial tcp"), "network error"},
		{context.DeadlineExceeded, "network error"},
		{errors.New("something unexpected"), "something unexpected"},
	}
	for _, tc := range cases {
		if got := FormatErrorFor(tc.err, canon); !strings.Contains(got, tc.want) {
			t.Errorf("FormatErrorFor(%v) = %q, want substring %q", tc.err, got, tc.want)
		}
	}
}

func TestClassifyChanges(t *testing.T) {
	repoDir := t.TempDir()
	// Create a real inGitDB directory on disk: demo-project-1/geo/.ingitdb/
	ingitDir := filepath.Join(repoDir, "demo-project-1", "geo", ".ingitdb")
	if err := os.MkdirAll(ingitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	changes := []demogit.Change{
		{Path: "demo-project-1/queries/top.sql", Kind: "modified"},
		{Path: "demo-project-1/datatug-project.json", Kind: "modified"},
		{Path: "demo-project-1/.demo-data/chinook.sqlite", Kind: "modified"},
		{Path: "demo-project-1/data/orders.db", Kind: "added"},
		{Path: "demo-project-1/geo/.ingitdb/root-collections.yaml", Kind: "modified"},
		{Path: "demo-project-1/geo/countries/us.yaml", Kind: "added"},
	}
	proj, dbs := ClassifyChanges(repoDir, changes)
	if len(proj) != 2 {
		t.Fatalf("expected 2 project files, got %+v", proj)
	}
	if len(dbs) != 4 {
		t.Fatalf("expected 4 db files, got %+v", dbs)
	}

	// Also cover empty repoDir branch.
	proj2, dbs2 := ClassifyChanges("", []demogit.Change{
		{Path: "queries/a.sql", Kind: "added"},
		{Path: "data/local.sqlite", Kind: "modified"},
	})
	if len(proj2) != 1 || len(dbs2) != 1 {
		t.Fatalf("empty repoDir: proj=%+v dbs=%+v", proj2, dbs2)
	}
}
