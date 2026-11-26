package dtdemo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/demogit"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"golang.org/x/oauth2"
)

func welcomeView(m tea.Model) string { return uitest.Plain(m.View().Content) }

func hasQuit(msgs []tea.Msg) bool {
	for _, msg := range msgs {
		if _, ok := msg.(tea.QuitMsg); ok {
			return true
		}
	}
	return false
}

func TestWelcomeChooseRouting(t *testing.T) {
	t.Run("default enter explores", func(t *testing.T) {
		m := NewWelcomeModel(testConfig(NewAuthState(), nil, nil))
		if m.Init() != nil {
			t.Fatal("Init should be nil")
		}
		var tm tea.Model = m
		tm, _ = feedModel(tm, tea.WindowSizeMsg{Width: 90, Height: 28})
		v := welcomeView(tm)
		for _, want := range []string{"Welcome to the DataTug demo", "[1] Explore the demo project", "[2] Open my GitHub project", "14:30 UTC"} {
			if !strings.Contains(v, want) {
				t.Fatalf("welcome view missing %q:\n%s", want, v)
			}
		}
		// Non-key message is ignored.
		tm, _ = feedModel(tm, struct{}{})
		// Move down then back up, then Enter.
		tm, _ = feedModel(tm, uitest.Key("down"), uitest.Key("up"))
		var msgs []tea.Msg
		tm, msgs = feedModel(tm, uitest.Key("enter"))
		if !hasQuit(msgs) || tm.(WelcomeModel).Result().Outcome != OutcomeExplore {
			t.Fatalf("expected explore quit, got %+v msgs=%#v", tm.(WelcomeModel).Result(), msgs)
		}
	})

	t.Run("shortcut 1 explores", func(t *testing.T) {
		var tm tea.Model = NewWelcomeModel(testConfig(NewAuthState(), nil, nil))
		tm, msgs := feedModel(tm, uitest.Key("1"))
		if !hasQuit(msgs) || tm.(WelcomeModel).Result().Outcome != OutcomeExplore {
			t.Fatalf("expected explore, got %+v", tm.(WelcomeModel).Result())
		}
	})

	t.Run("q quits", func(t *testing.T) {
		var tm tea.Model = NewWelcomeModel(testConfig(NewAuthState(), nil, nil))
		tm, msgs := feedModel(tm, uitest.Key("q"))
		if !hasQuit(msgs) || tm.(WelcomeModel).Result().Outcome != OutcomeQuit {
			t.Fatalf("expected quit, got %+v", tm.(WelcomeModel).Result())
		}
	})

	t.Run("tall render clips to height", func(t *testing.T) {
		var tm tea.Model = NewWelcomeModel(testConfig(NewAuthState(), nil, nil))
		tm, _ = feedModel(tm, tea.WindowSizeMsg{Width: 60, Height: 5})
		if lines := strings.Split(welcomeView(tm), "\n"); len(lines) > 5 {
			t.Fatalf("expected <= 5 lines, got %d", len(lines))
		}
	})
}

func TestWelcomeOpenForkSuccess(t *testing.T) {
	fake := &fakeGitClient{
		findFork:   demogit.Repo{Owner: "alice", Name: "datatug-demo-project"},
		openBranch: "datatug-demo",
	}
	var gotToken string
	stub(t, &newGitClient, func(_ demogit.Config, token string) GitClient {
		gotToken = token
		return fake
	})

	auth := NewAuthState()
	cfg := testConfig(auth, &oauth2.Token{AccessToken: "gho_secret_abc"}, nil)
	var tm tea.Model = NewWelcomeModel(cfg)
	tm, _ = feedModel(tm, tea.WindowSizeMsg{Width: 90, Height: 30})

	// Pick [2] via down + enter -> explanation screen.
	tm, _ = feedModel(tm, uitest.Key("down"), uitest.Key("enter"))
	v := welcomeView(tm)
	for _, want := range []string{"public_repo", "read and write your public repositories", "https://github.com/settings/applications"} {
		if !strings.Contains(v, want) {
			t.Fatalf("explain view missing %q:\n%s", want, v)
		}
	}
	// Non-key message and unhandled key in explain are ignored.
	tm, _ = feedModel(tm, struct{}{}, uitest.Key("x"))

	// Confirm explain -> runs device flow -> FindFork -> Open -> Quit.
	tm, msgs := feedModel(tm, uitest.Key("enter"))
	if !hasQuit(msgs) {
		t.Fatalf("expected quit after Open, msgs=%#v, view=%s", msgs, welcomeView(tm))
	}
	res := tm.(WelcomeModel).Result()
	if res.Outcome != OutcomeOpenedFork || res.Fork.FullName() != "alice/datatug-demo-project" || res.Branch != "datatug-demo" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if gotToken != "gho_secret_abc" || auth.Token() != "gho_secret_abc" {
		t.Fatalf("token not propagated to git client / auth state")
	}
	if fake.openDir != "/tmp/demo-repo" || fake.openProj != "demo-project-1" {
		t.Fatalf("Open called with dir=%q proj=%q", fake.openDir, fake.openProj)
	}
}

func TestWelcomeOpenForkFailuresAndCancel(t *testing.T) {
	t.Run("no fork offers explore", func(t *testing.T) {
		fake := &fakeGitClient{findErr: demogit.ErrNoFork}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("tok", "alice") // already signed in skips explain

		var tm tea.Model = NewWelcomeModel(testConfig(auth, nil, nil))
		tm, _ = feedModel(tm, uitest.Key("2"))
		v := welcomeView(tm)
		if !strings.Contains(v, "No fork of datatug/datatug-demo-project") || !strings.Contains(v, "explore the demo project") {
			t.Fatalf("expected no-fork message:\n%s", v)
		}
		// Non-key message and unhandled key in error step are ignored.
		tm, _ = feedModel(tm, struct{}{}, uitest.Key("x"))
		// Press Enter to explore instead.
		tm, msgs := feedModel(tm, uitest.Key("enter"))
		if !hasQuit(msgs) || tm.(WelcomeModel).Result().Outcome != OutcomeExplore {
			t.Fatalf("expected explore fallback, got %+v", tm.(WelcomeModel).Result())
		}
	})

	t.Run("untrusted content refused with reason and back to welcome", func(t *testing.T) {
		fake := &fakeGitClient{
			findFork: demogit.Repo{Owner: "mallory", Name: "datatug-demo-project"},
			openErr:  errors.Join(demogit.ErrUntrustedContent, errors.New("symlink escapes repo")),
		}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("tok", "mallory")

		var tm tea.Model = NewWelcomeModel(testConfig(auth, nil, nil))
		tm, _ = feedModel(tm, uitest.Key("2"))
		v := welcomeView(tm)
		if !strings.Contains(v, "cannot be opened") || !strings.Contains(v, "symlink escapes repo") {
			t.Fatalf("expected untrusted reason:\n%s", v)
		}
		// Esc returns to welcome.
		tm, _ = feedModel(tm, uitest.Key("esc"))
		if !strings.Contains(welcomeView(tm), "Welcome to the DataTug demo") {
			t.Fatalf("esc did not return to welcome:\n%s", welcomeView(tm))
		}
	})

	t.Run("open auth error clears token and q quits", func(t *testing.T) {
		stub(t, &deleteToken, func() error { return nil })
		fake := &fakeGitClient{
			findFork: demogit.Repo{Owner: "alice", Name: "datatug-demo-project"},
			openErr:  demogit.ErrAuth,
		}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("bad-tok", "alice")

		var tm tea.Model = NewWelcomeModel(testConfig(auth, nil, nil))
		tm, _ = feedModel(tm, uitest.Key("2"))
		if auth.SignedIn() {
			t.Fatal("ErrAuth should clear auth state")
		}
		tm, msgs := feedModel(tm, uitest.Key("q"))
		if !hasQuit(msgs) || tm.(WelcomeModel).Result().Outcome != OutcomeQuit {
			t.Fatalf("expected quit, got %+v", tm.(WelcomeModel).Result())
		}
	})

	t.Run("findFork auth error clears token", func(t *testing.T) {
		stub(t, &deleteToken, func() error { return nil })
		fake := &fakeGitClient{findErr: demogit.ErrAuth}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("bad-tok", "alice")

		var tm tea.Model = NewWelcomeModel(testConfig(auth, nil, nil))
		tm, _ = feedModel(tm, uitest.Key("2"))
		if auth.SignedIn() {
			t.Fatal("ErrAuth during FindFork should clear auth state")
		}
	})

	t.Run("device auth denied and empty token", func(t *testing.T) {
		cfg := testConfig(NewAuthState(), nil, errors.New("github error: access_denied (user denied)"))
		var tm tea.Model = NewWelcomeModel(cfg)
		tm, _ = feedModel(tm, uitest.Key("2"), uitest.Key("enter"))
		if !strings.Contains(welcomeView(tm), "denied") {
			t.Fatalf("expected denied error:\n%s", welcomeView(tm))
		}

		cfgEmpty := testConfig(NewAuthState(), &oauth2.Token{}, nil)
		var tm2 tea.Model = NewWelcomeModel(cfgEmpty)
		tm2, _ = feedModel(tm2, uitest.Key("2"), uitest.Key("enter"))
		if !strings.Contains(welcomeView(tm2), "did not return an access token") {
			t.Fatalf("expected empty token error:\n%s", welcomeView(tm2))
		}
	})

	t.Run("cancel from explain, auth, finding, opening and ctrl+c", func(t *testing.T) {
		// Cancel from explain
		var tm tea.Model = NewWelcomeModel(testConfig(NewAuthState(), nil, nil))
		tm, _ = feedModel(tm, uitest.Key("2"), uitest.Key("esc"))
		if !strings.Contains(welcomeView(tm), "Welcome to the DataTug demo") {
			t.Fatalf("expected welcome after cancel from explain")
		}
		// Ctrl+C from explain quits
		tm, msgs := feedModel(tm, uitest.Key("2"), uitest.Key("ctrl+c"))
		if !hasQuit(msgs) || tm.(WelcomeModel).Result().Outcome != OutcomeQuit {
			t.Fatalf("expected quit on ctrl+c from explain")
		}

		// Cancel from device auth (block poll until ctx cancelled)
		cfg := testConfig(NewAuthState(), nil, nil)
		cfg.PollForToken = func(ctx context.Context, _, _, _ string, _ int, _ func(int)) (*oauth2.Token, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		var tmAuth tea.Model = NewWelcomeModel(cfg)
		tmAuth, _ = feedModel(tmAuth, uitest.Key("2"))
		// Step into auth without draining blocking poll:
		tmAuth, cmd := tmAuth.Update(uitest.Key("enter"))
		// First message from Init is deviceCodeIssued:
		tmAuth, _ = tmAuth.Update(cmd())
		// Resize while in auth step:
		tmAuth, _ = tmAuth.Update(tea.WindowSizeMsg{Width: 85, Height: 25})
		if !strings.Contains(welcomeView(tmAuth), "DEMO-1234") {
			t.Fatalf("expected device code in auth view:\n%s", welcomeView(tmAuth))
		}
		// Esc triggers CancelMsg -> welcomeAuthCanceledMsg -> welcome
		tmAuth, _ = feedModel(tmAuth, uitest.Key("esc"))
		if !strings.Contains(welcomeView(tmAuth), "Welcome to the DataTug demo") {
			t.Fatalf("cancel from auth did not return to welcome:\n%s", welcomeView(tmAuth))
		}

		// Ctrl+C while in auth step quits
		var tmAuthQuit tea.Model = NewWelcomeModel(cfg)
		tmAuthQuit, _ = feedModel(tmAuthQuit, uitest.Key("2"))
		tmAuthQuit, _ = tmAuthQuit.Update(uitest.Key("enter"))
		tmAuthQuit, msgs = feedModel(tmAuthQuit, uitest.Key("ctrl+c"))
		if !hasQuit(msgs) {
			t.Fatal("ctrl+c in auth must quit")
		}

		// Finding & Opening views + Esc & Ctrl+C
		mFind := NewWelcomeModel(testConfig(NewAuthState(), nil, nil))
		mFind.step = welcomeStepFindingFork
		if !strings.Contains(welcomeView(mFind), "Looking for your fork") {
			t.Fatalf("finding view = %q", welcomeView(mFind))
		}
		tmF, _ := feedModel(mFind, struct{}{}, uitest.Key("esc"))
		if tmF.(WelcomeModel).step != welcomeStepChoose {
			t.Fatal("esc in finding should return to choose")
		}
		tmFQ, msgs := feedModel(mFind, uitest.Key("ctrl+c"))
		if !hasQuit(msgs) || tmFQ.(WelcomeModel).Result().Outcome != OutcomeQuit {
			t.Fatal("ctrl+c in finding should quit")
		}

		mOpen := NewWelcomeModel(testConfig(NewAuthState(), nil, nil))
		mOpen.step = welcomeStepOpening
		mOpen.fork = demogit.Repo{Owner: "alice", Name: "datatug-demo-project"}
		if !strings.Contains(welcomeView(mOpen), "Opening alice/datatug-demo-project") {
			t.Fatalf("opening view = %q", welcomeView(mOpen))
		}
		tmO, _ := feedModel(mOpen, struct{}{}, uitest.Key("esc"))
		if tmO.(WelcomeModel).step != welcomeStepChoose {
			t.Fatal("esc in opening should return to choose")
		}
		tmOQ, msgs := feedModel(mOpen, uitest.Key("ctrl+c"))
		if !hasQuit(msgs) || tmOQ.(WelcomeModel).Result().Outcome != OutcomeQuit {
			t.Fatal("ctrl+c in opening should quit")
		}
	})

	t.Run("rate limit shows reset time", func(t *testing.T) {
		reset := time.Date(2026, 11, 26, 16, 45, 0, 0, time.UTC)
		fake := &fakeGitClient{findErr: &demogit.RateLimitError{Reset: reset}}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("tok", "alice")

		var tm tea.Model = NewWelcomeModel(testConfig(auth, nil, nil))
		tm, _ = feedModel(tm, uitest.Key("2"))
		if !strings.Contains(welcomeView(tm), "16:45:00 UTC") {
			t.Fatalf("rate limit reset missing:\n%s", welcomeView(tm))
		}
	})
}

func TestRunWelcome(t *testing.T) {
	stub(t, &runWelcomeProgram, func(m WelcomeModel, _ ...tea.ProgramOption) (WelcomeModel, error) {
		m.result = WelcomeResult{Outcome: OutcomeExplore}
		return m, nil
	})
	res, err := RunWelcome(testConfig(NewAuthState(), nil, nil))
	if err != nil || res.Outcome != OutcomeExplore {
		t.Fatalf("RunWelcome = (%+v, %v)", res, err)
	}

	stub(t, &runWelcomeProgram, func(WelcomeModel, ...tea.ProgramOption) (WelcomeModel, error) {
		return WelcomeModel{}, errors.New("tty error")
	})
	if _, err := RunWelcome(testConfig(NewAuthState(), nil, nil)); err == nil {
		t.Fatal("expected error from RunWelcome")
	}
}
