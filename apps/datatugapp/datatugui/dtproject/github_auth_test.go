package dtproject

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
	"github.com/strongo/strongo-tui/pkg/widgets"
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
