package dtproject

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
	"golang.org/x/oauth2"
)

var testDeviceCode = &ghauth.DeviceCodeResponse{
	DeviceCode: "dev", UserCode: "ABCD-1234", VerificationURI: "https://github.com/login/device", Interval: 5,
}

// stubDeviceFlow replaces the GitHub calls of the device flow. The poll reports
// one attempt and then waits for release, or returns at once when release is nil.
func stubDeviceFlow(t *testing.T, release <-chan struct{}, pollErr error) (saved *[]*oauth2.Token) {
	t.Helper()
	saved = new([]*oauth2.Token)
	stub(t, &requestDeviceCode, func(context.Context, string) (*ghauth.DeviceCodeResponse, error) { return testDeviceCode, nil })
	stub(t, &pollForToken, func(_ context.Context, _, _, code string, _ int, onAttempt func(int)) (*oauth2.Token, error) {
		if code != "dev" {
			t.Errorf("polled with device code %q", code)
		}
		onAttempt(1)
		if release != nil {
			<-release
		}
		if pollErr != nil {
			return nil, pollErr
		}
		return &oauth2.Token{AccessToken: "tok"}, nil
	})
	stub(t, &saveToken, func(token *oauth2.Token) error { *saved = append(*saved, token); return nil })
	return saved
}

func TestDeviceAuthShowsTheCodeThenSignsIn(t *testing.T) {
	release := make(chan struct{})
	saved := stubDeviceFlow(t, release, nil)

	s := mount(newDeviceAuth(), 70, 14, true)
	if got := view(s); !contains(got, "Requesting a device code") {
		t.Fatalf("view = %q", got)
	}
	s, cmd := s.Update(s.Init()()) // the code arrives
	if got := view(s); !contains(got, "Go to https://github.com/login/device") || !contains(got, "Enter code: ABCD-1234") || contains(got, "attempt") {
		t.Fatalf("view = %q", got)
	}
	s, cmd = s.Update(cmd()) // the first poll
	if got := view(s); !contains(got, "Waiting for authorization (attempt 1)...") {
		t.Fatalf("view = %q", got)
	}
	close(release)
	_, msgs := step(s, cmd()) // the token arrives
	if _, ok := msgs[0].(nav.PopMsg); !ok {
		t.Fatalf("the screen must close itself first: %#v", msgs)
	}
	only[githubAuthenticated](t, msgs)
	if len(*saved) != 1 || (*saved)[0].AccessToken != "tok" {
		t.Fatalf("saved = %+v", *saved)
	}
}

func TestDeviceAuthTellsTheScreenBelow(t *testing.T) {
	stubDeviceFlow(t, nil, nil)
	root := newRecorder()
	h := navtest.New(t, nav.Page{Title: "Root", Content: root})
	h.Send(nav.PushMsg{Page: nav.Page{Title: "GitHub sign-in", Content: newDeviceAuth()}})
	if h.Model().Depth() != 1 || !received[githubAuthenticated](root) {
		t.Fatalf("depth %d, received %v", h.Model().Depth(), *root.msgs)
	}
}

type deviceCodeTransport struct {
	http.RoundTripper
	target *url.URL
}

func (tr deviceCodeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = tr.target.Scheme, tr.target.Host
	return tr.RoundTripper.RoundTrip(req)
}

func TestNewDeviceAuthHonorsProcessScopeLimit(t *testing.T) {
	const isolated = "DATATUG_TEST_DEVICE_SCOPE_PROCESS"
	if os.Getenv(isolated) == "" {
		// The limit applies for the life of the process.
		cmd := exec.Command(os.Args[0], "-test.run=^TestNewDeviceAuthHonorsProcessScopeLimit$")
		cmd.Env = append(os.Environ(), isolated+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated device flow: %v\n%s", err, out)
		}
		return
	}

	scopes := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/login/device/code" {
			t.Errorf("device request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode device request: %v", err)
		}
		scopes <- body["scope"]
		_ = json.NewEncoder(w).Encode(testDeviceCode)
	}))
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	stub[http.RoundTripper](t, &http.DefaultTransport, deviceCodeTransport{srv.Client().Transport, target})
	stub(t, &pollForToken, func(context.Context, string, string, string, int, func(int)) (*oauth2.Token, error) {
		return nil, errors.New("stop after requesting code")
	})
	checkScope := func(want string) {
		t.Helper()
		s := newDeviceAuth()
		msg := s.Init()()
		if _, ok := msg.(deviceCodeIssued); !ok {
			t.Fatalf("device flow = %#v", msg)
		}
		if got := <-scopes; got != want {
			t.Errorf("device code requested %q, want %q", got, want)
		}
		_, cmd := s.Update(msg)
		if msg := cmd(); msg == nil {
			t.Fatal("device flow returned no completion")
		}
	}
	checkScope("repo")
	ghauth.LimitScope("public_repo")
	checkScope("public_repo")
}

func TestDeviceAuthFailures(t *testing.T) {
	t.Run("no device code", func(t *testing.T) {
		stub(t, &requestDeviceCode, func(context.Context, string) (*ghauth.DeviceCodeResponse, error) {
			return nil, errors.New("offline")
		})
		s := mount(newDeviceAuth(), 70, 14, true)
		_, msgs := step(s, s.Init()())
		if msg := only[nav.ErrorMsg](t, msgs); msg.Err.Error() != "GitHub sign-in: failed to request device code: offline" {
			t.Fatalf("error = %v", msg.Err)
		}
	})
	t.Run("the user does not authorise", func(t *testing.T) {
		stubDeviceFlow(t, nil, errors.New("access_denied"))
		s := mount(newDeviceAuth(), 70, 14, true)
		_, msgs := feed(s, s.Init()())
		if msg := only[nav.ErrorMsg](t, msgs); msg.Err.Error() != "GitHub sign-in: authentication failed: access_denied" {
			t.Fatalf("error = %v", msg.Err)
		}
	})
}

func TestDeviceAuthKeyringFailureDoesNotStopSignIn(t *testing.T) {
	stubDeviceFlow(t, nil, nil)
	stub(t, &saveToken, func(*oauth2.Token) error { return errors.New("keyring locked") })
	done := deviceAuthWork(context.Background(), func(tea.Msg) {}).(authFinished)
	if done.err != nil || done.token == nil {
		t.Fatalf("done = %+v", done)
	}
}

func TestDeviceAuthCopyCode(t *testing.T) {
	instantTick(t)
	var copied string
	stub(t, &copyToClipboard, func(text string) error { copied = text; return nil })

	s := mount(newDeviceAuth(), 70, 14, true)
	s.(deviceAuth).stream.Cancel() // no flow runs in this test: Next has nothing to wait for
	if _, msgs := step(s, widgets.ButtonPressedMsg{ButtonID: buttonCopyCode}); len(msgs) != 0 {
		t.Fatalf("there is no code to copy yet: %#v", msgs)
	}
	s, _ = step(s, deviceCodeIssued{code: testDeviceCode})
	s, msgs := step(s, widgets.ButtonPressedMsg{ButtonID: buttonCopyCode})
	only[codeCopied](t, msgs)
	if copied != "ABCD-1234" {
		t.Fatalf("copied %q", copied)
	}
	s, msgs = step(s, codeCopied{})
	if got := view(s); !contains(got, "The code has been copied to clipboard.") {
		t.Fatalf("view = %q", got)
	}
	s, _ = step(s, only[copiedNoticeExpired](t, msgs))
	if got := view(s); contains(got, "copied") {
		t.Fatalf("the notice must go away: %q", got)
	}
}

func TestDeviceAuthCancelStopsThePolling(t *testing.T) {
	stopped := make(chan struct{})
	stub(t, &requestDeviceCode, func(context.Context, string) (*ghauth.DeviceCodeResponse, error) { return testDeviceCode, nil })
	stub(t, &pollForToken, func(ctx context.Context, _, _, _ string, _ int, _ func(int)) (*oauth2.Token, error) {
		defer close(stopped)
		<-ctx.Done() // like the real poll: it ends with the context
		return nil, ctx.Err()
	})
	s := mount(newDeviceAuth(), 70, 14, true)
	s, _ = s.Update(s.Init()()) // the code arrives; the poll is now waiting
	_, msgs := step(s, widgets.CancelMsg{})
	only[nav.PopMsg](t, msgs)
	<-stopped // the poll really ended
}

func TestDeviceAuthWidgetPlumbing(t *testing.T) {
	s := mount(newDeviceAuth(), 70, 14, true)
	d := s.(deviceAuth)
	if d.Title() != "GitHub Device Activation" || len(d.ShortHelp()) == 0 {
		t.Error("title and help")
	}
	_ = d.AtEdge(widgets.Up)
	s, _ = press(s, "right", "tab")
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: false})
	if got := view(s); !contains(got, "Copy Code") || !contains(got, "Cancel") {
		t.Fatalf("view = %q", got)
	}
}

func TestNewGitHubDeviceAuth_CustomScopeAndCallbacks(t *testing.T) {
	type customDone struct {
		tok *oauth2.Token
		err error
	}
	type customCancel struct{}

	var gotClientID, gotScope, gotSecret string
	stub(t, &requestDeviceCodeWithScope, func(_ context.Context, clientID, scope string) (*ghauth.DeviceCodeResponse, error) {
		gotClientID, gotScope = clientID, scope
		return testDeviceCode, nil
	})
	stub(t, &pollForToken, func(_ context.Context, _, secret, _ string, _ int, onAttempt func(int)) (*oauth2.Token, error) {
		gotSecret = secret
		onAttempt(1)
		return &oauth2.Token{AccessToken: "scoped-tok"}, nil
	})
	var savedToken *oauth2.Token
	stub(t, &saveToken, func(tok *oauth2.Token) error { savedToken = tok; return nil })
	t.Setenv("GITHUB_OAUTH_SECRET", "must-not-be-used")

	s := mount(NewGitHubDeviceAuth(GitHubAuthOptions{
		ClientID:    "custom-client",
		Scope:       "public_repo",
		Explanation: "Only public_repo is requested.",
		NoSecret:    true,
		OnDone:      func(tok *oauth2.Token, err error) tea.Msg { return customDone{tok: tok, err: err} },
		OnCancel:    func() tea.Msg { return customCancel{} },
	}), 70, 18, true)

	if got := view(s); !contains(got, "Only public_repo is requested.") {
		t.Fatalf("explanation missing from view: %q", got)
	}
	_, msgs := feed(s, s.Init()())
	done := only[customDone](t, msgs)
	if done.err != nil || done.tok == nil || done.tok.AccessToken != "scoped-tok" {
		t.Fatalf("done = %+v", done)
	}
	if gotClientID != "custom-client" || gotScope != "public_repo" || gotSecret != "" {
		t.Fatalf("clientID=%q scope=%q secret=%q", gotClientID, gotScope, gotSecret)
	}
	if savedToken == nil || savedToken.AccessToken != "scoped-tok" {
		t.Fatalf("savedToken = %+v", savedToken)
	}

	// Custom cancel callback + explicit override seams.
	s2 := mount(NewGitHubDeviceAuth(GitHubAuthOptions{
		Scope:    "public_repo",
		NoSecret: true,
		OnCancel: func() tea.Msg { return customCancel{} },
		OnDone:   func(tok *oauth2.Token, err error) tea.Msg { return customDone{tok: tok, err: err} },
		RequestDeviceCode: func(context.Context, string, string) (*ghauth.DeviceCodeResponse, error) {
			return testDeviceCode, nil
		},
		PollForToken: func(ctx context.Context, _, _, _ string, _ int, _ func(int)) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "override-tok"}, nil
		},
		SaveToken: func(*oauth2.Token) error { return nil },
	}), 70, 18, true)
	_, msgs = step(s2, widgets.CancelMsg{})
	only[customCancel](t, msgs)

	// Exercise explicit overrides end-to-end as well.
	s3 := mount(NewGitHubDeviceAuth(GitHubAuthOptions{
		Scope:    "public_repo",
		NoSecret: true,
		OnDone:   func(tok *oauth2.Token, err error) tea.Msg { return customDone{tok: tok, err: err} },
		RequestDeviceCode: func(context.Context, string, string) (*ghauth.DeviceCodeResponse, error) {
			return testDeviceCode, nil
		},
		PollForToken: func(context.Context, string, string, string, int, func(int)) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "override-tok"}, nil
		},
		SaveToken: func(*oauth2.Token) error { return nil },
	}), 70, 18, true)
	_, msgs = feed(s3, s3.Init()())
	done3 := only[customDone](t, msgs)
	if done3.err != nil || done3.tok == nil || done3.tok.AccessToken != "override-tok" {
		t.Fatalf("done3 = %+v", done3)
	}
}
