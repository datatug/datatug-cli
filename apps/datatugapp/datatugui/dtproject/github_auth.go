package dtproject

import (
	"context"
	"fmt"
	"os"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/auth/ghauth"
	"github.com/strongo/logus"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/widgets"
	"golang.org/x/oauth2"
)

// DefaultGitHubClientID is the client ID of the DataTug OAuth app, used with GitHub's
// device flow: https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow
const DefaultGitHubClientID = githubClientID

const githubClientID = "Ov23liAIKfguW2oYiore"

// GitHubAuthOptions configures a reusable GitHub device-flow screen.
type GitHubAuthOptions struct {
	// ClientID overrides DefaultGitHubClientID when non-empty.
	ClientID string
	// Scope requests a specific OAuth scope when non-empty (default "repo").
	Scope string
	// Explanation is shown above the device code instructions when non-empty.
	Explanation string
	// NoSecret disables reading GITHUB_OAUTH_SECRET from the environment.
	NoSecret bool
	// OnDone, when non-nil, produces the message emitted when the flow finishes
	// (success or error) instead of the default Pop + githubAuthenticated / ReportError.
	OnDone func(token *oauth2.Token, err error) tea.Msg
	// OnCancel, when non-nil, produces the message emitted when the user cancels
	// instead of the default nav.Pop().
	OnCancel func() tea.Msg

	// Optional overrides for testing; nil uses the package's ghauth functions.
	RequestDeviceCode func(ctx context.Context, clientID, scope string) (*ghauth.DeviceCodeResponse, error)
	PollForToken      func(ctx context.Context, clientID, clientSecret, deviceCode string, interval int, onAttempt func(int)) (*oauth2.Token, error)
	SaveToken         func(token *oauth2.Token) error
}

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
	return deviceAuthWorkWithOptions(GitHubAuthOptions{})(ctx, report)
}

func deviceAuthWorkWithOptions(opts GitHubAuthOptions) func(ctx context.Context, report func(tea.Msg)) tea.Msg {
	return func(ctx context.Context, report func(tea.Msg)) tea.Msg {
		clientID := opts.ClientID
		if clientID == "" {
			clientID = githubClientID
		}
		var (
			code *ghauth.DeviceCodeResponse
			err  error
		)
		switch {
		case opts.RequestDeviceCode != nil:
			code, err = opts.RequestDeviceCode(ctx, clientID, opts.Scope)
		case opts.Scope != "":
			code, err = requestDeviceCodeWithScope(ctx, clientID, opts.Scope)
		default:
			code, err = requestDeviceCode(ctx, clientID)
		}
		if err != nil {
			return authFinished{err: fmt.Errorf("failed to request device code: %w", err)}
		}
		report(deviceCodeIssued{code: code})
		secret := ""
		if !opts.NoSecret {
			secret = os.Getenv("GITHUB_OAUTH_SECRET")
		}
		pollFn := pollForToken
		if opts.PollForToken != nil {
			pollFn = opts.PollForToken
		}
		token, err := pollFn(ctx, clientID, secret, code.DeviceCode, code.Interval,
			func(n int) { report(authAttempt{n: n}) })
		if err != nil {
			return authFinished{err: fmt.Errorf("authentication failed: %w", err)}
		}
		saveFn := saveToken
		if opts.SaveToken != nil {
			saveFn = opts.SaveToken
		}
		if err = saveFn(token); err != nil {
			logus.Errorf(ctx, "failed to save GitHub token: %v", err)
		}
		return authFinished{token: token}
	}
}

// deviceAuth is the GitHub device-flow screen. It is pushed above the screen
// that needs a token; when the token is saved it closes itself and tells the
// screen below with githubAuthenticated.
type deviceAuth struct {
	geo     geometry
	opts    GitHubAuthOptions
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
	return newDeviceAuthWithOptions(GitHubAuthOptions{})
}

// NewGitHubDeviceAuth returns a nav.Screen that runs GitHub's OAuth device flow
// with the given options.
func NewGitHubDeviceAuth(opts GitHubAuthOptions) nav.Screen {
	return newDeviceAuthWithOptions(opts)
}

func newDeviceAuthWithOptions(opts GitHubAuthOptions) deviceAuth {
	return deviceAuth{
		opts:   opts,
		stream: newStream(deviceAuthWorkWithOptions(opts)),
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
		if d.opts.OnDone != nil {
			return d, widgets.Emit(d.opts.OnDone(msg.token, msg.err))
		}
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
		if d.opts.OnCancel != nil {
			return d, widgets.Emit(d.opts.OnCancel())
		}
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
	if d.opts.Explanation != "" {
		wrapW := max(20, d.geo.w-4)
		text = "\n" + ansi.Wrap(d.opts.Explanation, wrapW, " ") + "\n" + text
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
