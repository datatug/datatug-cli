package commands

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-cli/pkg/cloudapi"
	"github.com/datatug/datatug-cli/pkg/cloudproject"
	"github.com/datatug/datatug-cli/pkg/deeplink"
)

// cloudOptions are the flags of `datatug ui --deep-link` for DataTug Cloud
// projects.
type cloudOptions struct {
	noSignIn bool
	insecure bool
	authHost string
	apiURL   string
	// inUI leaves out the failover notice, which would print over the UI.
	inUI bool
}

// Seams for tests: the sign-in, the HTTP client and where the device flow
// prints its URL and code.
var (
	cloudSignIn            = device.EnsureSignedIn
	cloudHTTPTransport     http.RoundTripper
	cloudSignInOut         io.Writer = os.Stdout
	cloudSignInErr         io.Writer = os.Stderr
	cloudOpenTimeout                 = 30 * time.Second
	runCloudProjectInUIApp           = func(p cloudproject.Project, page []string) error {
		return runApp(uiModules(), datatugui.Options{Start: datatugui.ScreenProjects, Then: dtproject.OpenCloudProject(p, page)})
	}
)

// openCloudLink signs in when needed, reads the project's summary and lists,
// and only then starts the UI on it (spec/features/cli/cloud-projects REQ
// open-before-tui). A failure before the UI starts is an exit code.
func openCloudLink(link deeplink.Link, opts cloudOptions) error {
	if link.SpaceID == "" {
		return Exit(dtproject.PersonalCloudNotice, 1)
	}
	ref := cloudproject.Ref{SpaceID: link.SpaceID, ProjectID: link.ProjectID}
	if err := ref.Validate(); err != nil {
		return Exit("--deep-link: "+err.Error(), exitCodeUsage)
	}
	client, err := newCloudProjectClient(opts)
	if err != nil {
		return Exit("--api-url: "+err.Error(), exitCodeUsage)
	}
	// Ctrl+C while waiting for the device flow or the reads exits 1 with
	// nothing saved; the UI handles its own keys once it starts.
	ctx, _, stop := interruptContext(context.Background())
	defer stop()
	session, err := cloudSignIn(ctx, device.SignInOptions{
		Issuer: opts.authHost, Insecure: opts.insecure, Interactive: !opts.noSignIn,
		Out: cloudSignInOut, ErrOut: cloudSignInErr,
	})
	if errors.Is(err, device.ErrNotSignedIn) {
		return Exit("Sign in with: datatug auth login", exitCodeAccessDenied)
	}
	if err != nil {
		return Exit(err.Error(), 1)
	}
	client.Token = session.Token
	uiOpts := opts
	uiOpts.inUI = true
	uiClient, _ := newCloudProjectClient(uiOpts) // opts were validated above
	uiClient.Token = session.Token
	setCloudUILoader(uiClient, session.Email)
	readCtx, cancel := context.WithTimeout(ctx, cloudOpenTimeout)
	defer cancel()
	project, err := cloudproject.Load(readCtx, client, ref)
	if err != nil {
		line := cloudErrorLine(err, session.Email)
		if errors.Is(err, cloudproject.ErrUnauthorized) {
			line = "Sign in with: datatug auth login"
		}
		return Exit(line, cloudExitCode(err))
	}
	stop()
	return runCloudProjectInUIApp(project, link.Page)
}

// newCloudProjectClient is a client for the cloud API host list chat uses,
// with its failover, and redirects never followed so a bearer only reaches
// the API host.
func newCloudProjectClient(opts cloudOptions) (*cloudproject.Client, error) {
	baseURLs := cloudapi.SplitBaseURLs(opts.apiURL)
	if len(baseURLs) == 0 {
		baseURLs = defaultAIAPIURLs
	}
	for _, u := range baseURLs {
		if err := validateAIAPIURL(u); err != nil {
			return nil, errors.New("must be an HTTPS /v0/ URL, or HTTP to a loopback host")
		}
	}
	httpClient := &http.Client{Timeout: cloudOpenTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	if cloudHTTPTransport != nil {
		httpClient.Transport = cloudHTTPTransport
	}
	if len(baseURLs) > 1 {
		notice := cloudFailoverNotice
		if opts.inUI {
			notice = nil
		}
		httpClient.Transport = cloudapi.NewTransport(cloudapi.Config{BaseURLs: baseURLs, Notice: notice})
	}
	return &cloudproject.Client{BaseURL: baseURLs[0], HTTP: httpClient}, nil
}

// setCloudUILoader lets the projects screen open a recent cloud project again
// with the same login, and words its errors like the exit lines.
func setCloudUILoader(client *cloudproject.Client, email string) {
	dtproject.CloudLoader = func(ctx context.Context, ref cloudproject.Ref) (cloudproject.Project, error) {
		return cloudproject.Load(ctx, client, ref)
	}
	dtproject.CloudErrorLine = func(err error) string { return cloudErrorLine(err, email) }
}

// uiSessionEmail is the email of the login the bare UI's loader last used.
// The loader runs in a UI command goroutine while error lines are worded in
// others, so it is kept atomically rather than in a reassigned function.
var uiSessionEmail atomic.Value

// configureCloudUILoader sets the projects screen's loader for a UI started
// without --deep-link, using the saved login only (no device flow inside the
// UI).
func configureCloudUILoader() {
	uiSessionEmail.Store("")
	dtproject.CloudErrorLine = func(err error) string {
		email, _ := uiSessionEmail.Load().(string)
		return cloudErrorLine(err, email)
	}
	dtproject.CloudLoader = func(ctx context.Context, ref cloudproject.Ref) (cloudproject.Project, error) {
		client, err := newCloudProjectClient(cloudOptions{inUI: true})
		if err != nil {
			return cloudproject.Project{}, err
		}
		session, err := cloudSignIn(ctx, device.SignInOptions{})
		if err != nil {
			return cloudproject.Project{}, err
		}
		client.Token = session.Token
		uiSessionEmail.Store(session.Email)
		return cloudproject.Load(ctx, client, ref)
	}
}

// cloudErrorLine is the one line shown for a failed cloud read. It never
// carries a token or an internal path.
func cloudErrorLine(err error, email string) string {
	switch {
	case errors.Is(err, device.ErrNotSignedIn):
		return "Sign in with: datatug auth login"
	case errors.Is(err, cloudproject.ErrNotFound):
		return "Project not found, or it was deleted."
	case errors.Is(err, cloudproject.ErrUnauthorized):
		return "Your DataTug sign-in expired. Run: datatug auth login"
	case errors.Is(err, cloudproject.ErrForbidden):
		who := "This account"
		if email != "" {
			who = email
		}
		return who + " has no access to this project. Ask a Space admin to invite you."
	case errors.Is(err, cloudproject.ErrUnavailable):
		return "Can't reach DataTug Cloud: " + reasonAfter(err, cloudproject.ErrUnavailable)
	}
	return err.Error()
}

// reasonAfter is the text of err after the sentinel's own text.
func reasonAfter(err, sentinel error) string {
	text, prefix := err.Error(), sentinel.Error()+": "
	if len(text) > len(prefix) && text[:len(prefix)] == prefix {
		return text[len(prefix):]
	}
	return text
}

// cloudExitCode maps a failed read to the CLI's exit codes.
func cloudExitCode(err error) int {
	switch {
	case errors.Is(err, cloudproject.ErrNotFound):
		return exitCodeNotFound
	case errors.Is(err, cloudproject.ErrUnavailable):
		return exitCodeDatabase
	case errors.Is(err, cloudproject.ErrUnauthorized), errors.Is(err, cloudproject.ErrForbidden):
		return exitCodeAccessDenied
	}
	return 1
}
