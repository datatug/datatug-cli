package dtproject

import (
	"context"
	"fmt"
	"os"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/strongo/logus"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/theme"
	"github.com/strongo/strongo-tui/pkg/widgets"
	"golang.org/x/oauth2"
)

// githubClientID is the client ID of the DataTug OAuth app, used with GitHub's
// device flow: https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow
const githubClientID = "Ov23liAIKfguW2oYiore"

const (
	deviceFormID   = "github-device"
	buttonCopyCode = "copy"
	// copiedNoticeFor is how long the "code copied" notice stays.
	copiedNoticeFor = 2 * time.Second
)

// Messages of the device flow.
type (
	// deviceCodeIssued carries the code the user has to enter on GitHub.
	deviceCodeIssued struct{ code *ghauth.DeviceCodeResponse }
	// authAttempt reports one poll for the token.
	authAttempt struct{ n int }
	// authFinished ends the flow; the token is saved when err is nil.
	authFinished struct {
		token *oauth2.Token
		err   error
	}
	// codeCopied reports that the code is on the clipboard.
	codeCopied struct{}
	// copiedNoticeExpired removes the "code copied" notice.
	copiedNoticeExpired struct{}
)

// deviceAuthWork runs the device flow: it asks GitHub for a code, reports it,
// and polls until the user has authorised the app. The token is saved to the
// keyring; a keyring that refuses is logged and does not stop the flow.
func deviceAuthWork(ctx context.Context, report func(tea.Msg)) tea.Msg {
	code, err := requestDeviceCode(ctx, githubClientID)
	if err != nil {
		return authFinished{err: fmt.Errorf("failed to request device code: %w", err)}
	}
	report(deviceCodeIssued{code: code})
	token, err := pollForToken(ctx, githubClientID, os.Getenv("GITHUB_OAUTH_SECRET"), code.DeviceCode, code.Interval,
		func(n int) { report(authAttempt{n: n}) })
	if err != nil {
		return authFinished{err: fmt.Errorf("authentication failed: %w", err)}
	}
	if err = saveToken(token); err != nil {
		logus.Errorf(ctx, "failed to save GitHub token: %v", err)
	}
	return authFinished{token: token}
}

// deviceAuth is the GitHub device-flow screen. It is pushed above the screen
// that needs a token; when the token is saved it closes itself and tells the
// screen below with githubAuthenticated.
type deviceAuth struct {
	geo     geometry
	stream  *stream
	code    *ghauth.DeviceCodeResponse
	attempt int
	copied  bool
	form    widgets.Form
}

var (
	_ nav.Screen       = deviceAuth{}
	_ nav.Titled       = deviceAuth{}
	_ nav.ShortHelper  = deviceAuth{}
	_ widgets.Boundary = deviceAuth{}
)

func newDeviceAuth() deviceAuth {
	return deviceAuth{
		stream: newStream(deviceAuthWork),
		form: widgets.NewForm(deviceFormID, nil, []widgets.FormButton{
			{ID: buttonCopyCode, Label: "Copy Code", Role: widgets.ActionRole},
			{ID: buttonCancel, Label: "Cancel", Role: widgets.CancelRole},
		}),
	}
}

// Init implements nav.Screen.
func (d deviceAuth) Init() tea.Cmd { return d.stream.Start() }

func (d *deviceAuth) sync() {
	d.form.SetSize(d.geo.w, formRows)
	if d.geo.focused {
		d.form.Focus()
	} else {
		d.form.Blur()
	}
}

// formRows is the height of the button area under the instructions.
const formRows = 3

// Update implements nav.Screen.
func (d deviceAuth) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if d.geo.track(msg) {
		d.sync()
		return d, nil
	}
	switch msg := msg.(type) {
	case deviceCodeIssued:
		d.code = msg.code
		return d, d.stream.Next()
	case authAttempt:
		d.attempt = msg.n
		return d, d.stream.Next()
	case authFinished:
		if msg.err != nil {
			return d, datatugui.ReportError("GitHub sign-in", msg.err)
		}
		return d, tea.Sequence(nav.Pop(), widgets.Emit(githubAuthenticated{}))
	case widgets.ButtonPressedMsg:
		return d, d.copyCode()
	case codeCopied:
		d.copied = true
		return d, tick(copiedNoticeFor, func(time.Time) tea.Msg { return copiedNoticeExpired{} })
	case copiedNoticeExpired:
		d.copied = false
		return d, nil
	case widgets.CancelMsg:
		d.stream.Cancel()
		return d, nav.Pop()
	}
	form, cmd := d.form.Update(msg)
	d.form = form
	return d, cmd
}

// copyCode puts the user code on the clipboard.
func (d deviceAuth) copyCode() tea.Cmd {
	if d.code == nil {
		return nil
	}
	code := d.code.UserCode
	return func() tea.Msg {
		_ = copyToClipboard(code)
		return codeCopied{}
	}
}

// View implements nav.Screen.
func (d deviceAuth) View() string {
	text := "\nRequesting a device code from GitHub..."
	if d.code != nil {
		text = fmt.Sprintf("\nGo to %s\n\nEnter code: %s\n\nWaiting for authorization", d.code.VerificationURI, theme.YellowText(d.code.UserCode))
		if d.attempt > 0 {
			text += fmt.Sprintf(" (attempt %d)", d.attempt)
		}
		text += "..."
	}
	notice := ""
	if d.copied {
		notice = theme.GreenText("The code has been copied to clipboard.")
	}
	body := textScreen{geo: geometry{w: d.geo.w, h: max(d.geo.h-formRows-1, 0)}, text: text}
	return body.View() + "\n" + widgets.AlignIn(notice, d.geo.w, widgets.AlignCenter) + "\n" + d.form.View()
}

// Title implements nav.Titled.
func (d deviceAuth) Title() string { return "GitHub Device Activation" }

// ShortHelp implements nav.ShortHelper.
func (d deviceAuth) ShortHelp() []key.Binding { return d.form.KeyMap.ShortHelp() }

// AtEdge implements widgets.Boundary.
func (d deviceAuth) AtEdge(dir widgets.Direction) bool { return d.form.AtEdge(dir) }
