package dtdemo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/demogit"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
	"golang.org/x/oauth2"
)

func saveView(s nav.Screen) string { return uitest.Plain(s.View()) }

func mountSave(cfg Config) nav.Screen {
	s := NewSaveScreen(cfg)
	s, _ = s.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: true})
	return s
}

func TestSaveModuleMetadataAndPlumbing(t *testing.T) {
	stub(t, &listChanges, func(string) ([]demogit.Change, error) { return nil, nil })
	mod := SaveModule(testConfig(NewAuthState(), nil, nil))
	if mod.ID != ModuleID || mod.Text != ModuleText {
		t.Fatalf("unexpected module: %+v", mod)
	}
	page := mod.Root()
	if page.Title != ModuleText || page.Content == nil {
		t.Fatalf("unexpected page: %+v", page)
	}
	s := page.Content.(saveScreen)
	if s.Title() != ModuleText || len(s.ShortHelp()) == 0 || !s.AtEdge(widgets.Up) {
		t.Fatal("metadata/boundary check failed")
	}
}

func TestSaveScreenOverviewAndNothingToSave(t *testing.T) {
	t.Run("github disabled shows not-configured notice without loading changes", func(t *testing.T) {
		stub(t, &listChanges, func(string) ([]demogit.Change, error) {
			t.Fatal("listChanges must not be called when GitHub is disabled")
			return nil, nil
		})
		cfg := testConfig(NewAuthState(), nil, nil)
		cfg.Canonical = demogit.Repo{}
		s := mountSave(cfg)
		if cmd := s.Init(); cmd != nil {
			t.Fatal("Init must return nil when GitHub is disabled")
		}
		v := saveView(s)
		if !strings.Contains(v, "Saving to GitHub is not configured on this demo server") {
			t.Fatalf("expected not-configured notice:\n%s", v)
		}
		// Key presses are ignored cleanly.
		s, msgs := feedScreen(s, uitest.Key("enter"), uitest.Key("r"))
		if len(msgs) != 0 || !strings.Contains(saveView(s), "Saving to GitHub is not configured on this demo server") {
			t.Fatalf("unexpected state after keys: msgs=%v view=%s", msgs, saveView(s))
		}
	})

	t.Run("nothing to save", func(t *testing.T) {
		stub(t, &listChanges, func(string) ([]demogit.Change, error) { return nil, nil })
		s := mountSave(testConfig(NewAuthState(), nil, nil))
		if !strings.Contains(saveView(s), "Checking workspace changes") {
			t.Fatalf("loading view = %q", saveView(s))
		}
		// Non-changesLoadedMsg ignored during loading.
		s, _ = feedScreen(s, struct{}{})
		s, _ = feedScreen(s, s.Init()())
		v := saveView(s)
		if !strings.Contains(v, "Nothing to save") || !strings.Contains(v, "OpenVaultDB cloud databases") {
			t.Fatalf("overview missing nothing-to-save / external db note:\n%s", v)
		}
		// Enter shows nothing-to-save error state; Esc goes back to overview.
		s, _ = feedScreen(s, struct{}{}, uitest.Key("x"), uitest.Key("enter"))
		if !strings.Contains(saveView(s), "Nothing to save") {
			t.Fatalf("enter on empty changes = %q", saveView(s))
		}
		s, _ = feedScreen(s, struct{}{}, uitest.Key("x"), uitest.Key("esc"))
		if !strings.Contains(saveView(s), "Not signed in") {
			t.Fatalf("esc back to overview = %q", saveView(s))
		}
		// Refresh reloads.
		s, _ = feedScreen(s, uitest.Key("r"))
		if !strings.Contains(saveView(s), "Nothing to save") {
			t.Fatalf("refresh = %q", saveView(s))
		}
	})

	t.Run("listChanges error and retry", func(t *testing.T) {
		calls := 0
		stub(t, &listChanges, func(string) ([]demogit.Change, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("corrupt index")
			}
			return []demogit.Change{{Path: "demo-project-1/queries/q1.sql", Kind: "added"}}, nil
		})
		s := mountSave(testConfig(NewAuthState(), nil, nil))
		s, _ = feedScreen(s, s.Init()())
		if !strings.Contains(saveView(s), "corrupt index") {
			t.Fatalf("expected error in view:\n%s", saveView(s))
		}
		// Pressing enter retries when changesErr != "".
		s, _ = feedScreen(s, uitest.Key("enter"))
		v := saveView(s)
		if !strings.Contains(v, "Project files (1)") || !strings.Contains(v, "Database files (0)") || !strings.Contains(v, "(none)") {
			t.Fatalf("expected separated files after retry:\n%s", v)
		}
	})

	t.Run("separates project and database files (db-only also renders project none)", func(t *testing.T) {
		stub(t, &listChanges, func(string) ([]demogit.Change, error) {
			return []demogit.Change{
				{Path: "demo-project-1/.demo-data/chinook.sqlite", Kind: "modified"},
				{Path: "demo-project-1/geo/.ingitdb/root-collections.yaml", Kind: "added"},
			}, nil
		})
		s := mountSave(testConfig(NewAuthState(), nil, nil))
		s, _ = feedScreen(s, s.Init()())
		v := saveView(s)
		for _, want := range []string{"Project files (0)", "(none)", "Database files (2)", "chinook.sqlite", ".ingitdb", "OpenVaultDB cloud databases"} {
			if !strings.Contains(v, want) {
				t.Fatalf("missing %q in:\n%s", want, v)
			}
		}
	})
}

func TestSaveFlowNoForkConfirmThenCreateAndSave(t *testing.T) {
	verifyShared := requireSharedTokenUntouched(t)
	stub(t, &listChanges, func(string) ([]demogit.Change, error) {
		return []demogit.Change{
			{Path: "demo-project-1/queries/top.sql", Kind: "added"},
			{Path: "demo-project-1/data/orders.db", Kind: "modified"},
		}, nil
	})
	fake := &fakeGitClient{
		userLogin:  "alice",
		findErr:    demogit.ErrNoFork,
		ensureFork: demogit.Repo{Owner: "alice", Name: "datatug-demo-project"},
		ensureNew:  true,
		saveRes: demogit.SaveResult{
			Fork:      demogit.Repo{Owner: "alice", Name: "datatug-demo-project"},
			Branch:    "datatug-demo",
			CommitSHA: "abc1234",
			CommitURL: "https://github.com/alice/datatug-demo-project/commit/abc1234",
		},
	}
	stub(t, &newGitClient, func(_ demogit.Config, token string) GitClient {
		if token != "gho_save_tok" {
			t.Error("Save did not use the session token")
		}
		return fake
	})

	auth := NewAuthState()
	cfg := testConfig(auth, &oauth2.Token{AccessToken: "gho_save_tok"}, nil)
	s := mountSave(cfg)
	s, _ = feedScreen(s, s.Init()())

	// Press Enter while not signed in -> explanation screen.
	s, _ = feedScreen(s, uitest.Key("enter"))
	if !strings.Contains(saveView(s), "public_repo") {
		t.Fatalf("expected scope explanation:\n%s", saveView(s))
	}
	// Cancel from explain returns to overview, non-key and unhandled key ignored.
	s, _ = feedScreen(s, struct{}{}, uitest.Key("x"), uitest.Key("esc"))
	if !strings.Contains(saveView(s), "Project files (1)") {
		t.Fatalf("expected overview after cancel:\n%s", saveView(s))
	}

	// Sign in -> CheckFork -> ErrNoFork -> ConfirmFork (EnsureFork NOT called yet!).
	s, _ = feedScreen(s, uitest.Key("enter"), uitest.Key("enter"))
	if fake.ensureHits != 0 {
		t.Fatalf("EnsureFork must not run before confirmation; hits=%d", fake.ensureHits)
	}
	v := saveView(s)
	if !strings.Contains(v, "Create a fork of datatug/datatug-demo-project in your account?") {
		t.Fatalf("missing fork confirmation:\n%s", v)
	}
	// Decline returns to overview (now signed in as @alice).
	s, _ = feedScreen(s, struct{}{}, uitest.Key("x"), uitest.Key("n"))
	if !strings.Contains(saveView(s), "Signed in to GitHub as @alice") {
		t.Fatalf("expected signed-in overview:\n%s", saveView(s))
	}

	// Start save again (already signed in -> straight to CheckFork -> ConfirmFork) -> confirm y!
	s, _ = feedScreen(s, uitest.Key("enter"))
	s, _ = feedScreen(s, uitest.Key("y"))
	if fake.ensureHits != 1 {
		t.Fatalf("expected EnsureFork called once, got %d", fake.ensureHits)
	}
	verifyShared()
	v = saveView(s)
	for _, want := range []string{
		"Saved to GitHub",
		"alice/datatug-demo-project",
		"datatug-demo",
		"https://github.com/alice/datatug-demo-project/commit/abc1234",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("success view missing %q:\n%s", want, v)
		}
	}
	// Non-key and unhandled key in success ignored; Enter refreshes.
	s, _ = feedScreen(s, struct{}{}, uitest.Key("x"), uitest.Key("enter"))
	if !strings.Contains(saveView(s), "Project files (1)") {
		t.Fatalf("expected overview after refresh:\n%s", saveView(s))
	}
}

func TestSaveFlowDivergedOffersNewBranchNeverForce(t *testing.T) {
	stub(t, &listChanges, func(string) ([]demogit.Change, error) {
		return []demogit.Change{{Path: "demo-project-1/queries/q.sql", Kind: "modified"}}, nil
	})
	stub(t, &nowFunc, func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) })

	fake := &fakeGitClient{
		findFork:  demogit.Repo{Owner: "bob", Name: "datatug-demo-project"},
		newBranch: "datatug-demo-20261126-120000",
		saveFn: func(_ string, fork demogit.Repo, opts demogit.SaveOptions) (demogit.SaveResult, error) {
			if opts.Branch == "datatug-demo" {
				return demogit.SaveResult{}, demogit.ErrDiverged
			}
			return demogit.SaveResult{
				Fork:      fork,
				Branch:    opts.Branch,
				CommitURL: "https://github.com/bob/datatug-demo-project/commit/def5678",
			}, nil
		},
	}
	stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })

	auth := NewAuthState()
	auth.Set("tok", "bob")
	s := mountSave(testConfig(auth, nil, nil))
	s, _ = feedScreen(s, s.Init()(), uitest.Key("enter"))
	v := saveView(s)
	if !strings.Contains(v, "never force-pushes") || !strings.Contains(v, "datatug-demo-20261126-120000") {
		t.Fatalf("diverged view unexpected:\n%s", v)
	}
	// Cancel returns to overview.
	s, _ = feedScreen(s, struct{}{}, uitest.Key("x"), uitest.Key("n"))
	if !strings.Contains(saveView(s), "Project files (1)") {
		t.Fatalf("cancel from diverged failed:\n%s", saveView(s))
	}
	// Empty NewBranchName uses fallback timestamp.
	fake.newBranch = ""
	s, _ = feedScreen(s, uitest.Key("enter"))
	if !strings.Contains(saveView(s), "datatug-demo-20261126-120000") {
		t.Fatalf("fallback branch missing:\n%s", saveView(s))
	}
	// Confirm saves to the new branch.
	s, _ = feedScreen(s, uitest.Key("y"))
	v = saveView(s)
	if !strings.Contains(v, "Saved to GitHub") || !strings.Contains(v, "datatug-demo-20261126-120000") || !strings.Contains(v, "def5678") {
		t.Fatalf("new-branch save view unexpected:\n%s", v)
	}
}

func TestSaveFlowAuthRateLimitAndOtherErrors(t *testing.T) {
	stub(t, &listChanges, func(string) ([]demogit.Change, error) {
		return []demogit.Change{{Path: "demo-project-1/queries/q.sql", Kind: "modified"}}, nil
	})

	t.Run("ErrAuth offers sign in again", func(t *testing.T) {
		fake := &fakeGitClient{
			findFork: demogit.Repo{Owner: "bob", Name: "datatug-demo-project"},
			saveErr:  demogit.ErrAuth,
		}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("expired-tok", "bob")
		s := mountSave(testConfig(auth, nil, nil))
		s, _ = feedScreen(s, s.Init()(), uitest.Key("enter"))
		if auth.SignedIn() {
			t.Fatal("ErrAuth must clear token")
		}
		v := saveView(s)
		if !strings.Contains(v, "sign in again") {
			t.Fatalf("expected reauth prompt:\n%s", v)
		}
		// Enter takes user to explain screen to sign in again.
		s, _ = feedScreen(s, uitest.Key("enter"))
		if !strings.Contains(saveView(s), "public_repo") {
			t.Fatalf("expected explain screen on reauth:\n%s", saveView(s))
		}
	})

	t.Run("rate limit shows reset time and r refreshes", func(t *testing.T) {
		reset := time.Date(2026, 11, 26, 18, 22, 33, 0, time.UTC)
		fake := &fakeGitClient{userErr: &demogit.RateLimitError{Reset: reset}}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("tok", "")
		s := mountSave(testConfig(auth, nil, nil))
		s, _ = feedScreen(s, s.Init()(), uitest.Key("enter"))
		if !strings.Contains(saveView(s), "18:22:33 UTC") {
			t.Fatalf("expected rate limit reset time:\n%s", saveView(s))
		}
		s, _ = feedScreen(s, uitest.Key("r"))
		if !strings.Contains(saveView(s), "Project files (1)") {
			t.Fatalf("r should refresh overview:\n%s", saveView(s))
		}
		// Non-reauth Enter also refreshes.
		s, _ = feedScreen(s, uitest.Key("enter"))
		s, _ = feedScreen(s, uitest.Key("enter"))
		if !strings.Contains(saveView(s), "Project files (1)") {
			t.Fatalf("enter on non-reauth error should refresh:\n%s", saveView(s))
		}
	})

	t.Run("EnsureFork failure and ErrNothingToSave from Save", func(t *testing.T) {
		fake := &fakeGitClient{
			findErr:   demogit.ErrNoFork,
			ensureErr: demogit.ErrForkNotReady,
		}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
		auth := NewAuthState()
		auth.Set("tok", "alice")
		s := mountSave(testConfig(auth, nil, nil))
		s, _ = feedScreen(s, s.Init()())
		s, _ = feedScreen(s, uitest.Key("enter"))
		s, _ = feedScreen(s, uitest.Key("y"))
		if !strings.Contains(saveView(s), "creating your fork") {
			t.Fatalf("expected fork-not-ready error:\n%s", saveView(s))
		}

		fake2 := &fakeGitClient{
			findFork: demogit.Repo{Owner: "alice", Name: "datatug-demo-project"},
			saveErr:  demogit.ErrNothingToSave,
		}
		stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake2 })
		s2 := mountSave(testConfig(auth, nil, nil))
		s2, _ = feedScreen(s2, s2.Init()(), uitest.Key("enter"))
		if !strings.Contains(saveView(s2), "Nothing to save") {
			t.Fatalf("expected nothing-to-save error:\n%s", saveView(s2))
		}
	})

	t.Run("device auth cancel, error, empty token, resize/focus/boundary", func(t *testing.T) {
		// Error during device auth
		cfgErr := testConfig(NewAuthState(), nil, errors.New("github error: expired_token (expired)"))
		s := mountSave(cfgErr)
		s, _ = feedScreen(s, s.Init()(), uitest.Key("enter"), uitest.Key("enter"))
		if !strings.Contains(saveView(s), "expired") {
			t.Fatalf("expected expired error:\n%s", saveView(s))
		}

		// Empty token during device auth
		cfgEmpty := testConfig(NewAuthState(), &oauth2.Token{}, nil)
		s2 := mountSave(cfgEmpty)
		s2, _ = feedScreen(s2, s2.Init()(), uitest.Key("enter"), uitest.Key("enter"))
		if !strings.Contains(saveView(s2), "did not return an access token") {
			t.Fatalf("expected empty token error:\n%s", saveView(s2))
		}

		// Blocking poll -> resize, focus, AtEdge, view, cancel
		cfgBlock := testConfig(NewAuthState(), nil, nil)
		cfgBlock.PollForToken = func(ctx context.Context, _, _, _ string, _ int, _ func(int)) (*oauth2.Token, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		s3 := mountSave(cfgBlock)
		s3, _ = feedScreen(s3, s3.Init()(), uitest.Key("enter"))
		s3, cmd := s3.Update(uitest.Key("enter"))
		s3, _ = s3.Update(cmd()) // deviceCodeIssued
		s3, _ = s3.Update(tea.WindowSizeMsg{Width: 84, Height: 26})
		s3, _ = s3.Update(nav.ScreenFocusMsg{Focused: true})
		_ = s3.(saveScreen).AtEdge(widgets.Up)
		if !strings.Contains(saveView(s3), "DEMO-1234") {
			t.Fatalf("expected device code in save auth view:\n%s", saveView(s3))
		}
		s3, _ = feedScreen(s3, uitest.Key("esc"))
		if !strings.Contains(saveView(s3), "Project files (1)") {
			t.Fatalf("cancel from auth did not return to overview:\n%s", saveView(s3))
		}
	})

	t.Run("transient progress views and ignored messages", func(t *testing.T) {
		for _, step := range []saveStep{saveStepCheckingFork, saveStepCreatingFork, saveStepSaving} {
			s := NewSaveScreen(testConfig(NewAuthState(), nil, nil)).(saveScreen)
			s.step = step
			s.fork = demogit.Repo{Owner: "alice", Name: "datatug-demo-project"}
			if saveView(s) == "" {
				t.Fatalf("empty view for step %d", step)
			}
			_, cmd := s.Update(struct{}{})
			if cmd != nil {
				t.Fatalf("unexpected cmd for ignored msg in step %d", step)
			}
		}
	})
}

func TestSaveRetryRediscoversInvalidatedFork(t *testing.T) {
	stub(t, &listChanges, func(string) ([]demogit.Change, error) {
		return []demogit.Change{{Path: "project/query.sql", Kind: "modified"}}, nil
	})
	for _, rediscovery := range []error{demogit.ErrNoFork, demogit.ErrForkSearchIncomplete, nil} {
		name := "still invalid"
		if rediscovery != nil {
			name = rediscovery.Error()
		}
		t.Run(name, func(t *testing.T) {
			fake := &fakeGitClient{findFork: demogit.Repo{Owner: "alice", Name: "demo"}, newBranch: "alternate"}
			fake.saveFn = func(_ string, _ demogit.Repo, opts demogit.SaveOptions) (demogit.SaveResult, error) {
				if opts.Branch == "datatug-demo" {
					return demogit.SaveResult{}, demogit.ErrDiverged
				}
				fake.findErr = rediscovery
				return demogit.SaveResult{}, errors.Join(demogit.ErrNoFork, demogit.ErrGitRedirect)
			}
			stub(t, &newGitClient, func(demogit.Config, string) GitClient { return fake })
			auth := NewAuthState()
			auth.Set("tok", "alice")
			s := mountSave(testConfig(auth, nil, nil))
			s, _ = feedScreen(s, s.Init()(), uitest.Key("enter"))
			if s.(saveScreen).step != saveStepDiverged {
				t.Fatalf("expected alternate branch prompt:\n%s", saveView(s))
			}
			s, msgs := feedScreen(s, uitest.Key("y"))
			searched := false
			for _, msg := range msgs {
				if _, ok := msg.(forkCheckedMsg); ok {
					searched = true
				}
			}
			if !searched {
				t.Fatal("alternate branch refusal did not restart fork discovery")
			}
			switch rediscovery {
			case demogit.ErrNoFork:
				if s.(saveScreen).step != saveStepConfirmFork {
					t.Fatalf("expected fork creation prompt:\n%s", saveView(s))
				}
			case demogit.ErrForkSearchIncomplete:
				if !strings.Contains(saveView(s), "Could not search all your repositories for the fork") {
					t.Fatalf("expected incomplete search message:\n%s", saveView(s))
				}
			default:
				if s.(saveScreen).step != saveStepError {
					t.Fatalf("persistent invalidation did not stop retrying:\n%s", saveView(s))
				}
			}
		})
	}
}
