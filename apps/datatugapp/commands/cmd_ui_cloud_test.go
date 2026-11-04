package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/pkg/auth/device"
	"github.com/datatug/datatug-cli/pkg/cloudproject"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const spaceLink = "https://datatug.app/space/acme/store/firestore/project/sales"

// cloudWorld stubs the sign-in, the UI and the backend, and records what the
// backend was asked and what the UI was started with.
type cloudWorld struct {
	mu        sync.Mutex
	server    *httptest.Server
	requests  []*http.Request
	summary   func(w http.ResponseWriter)
	signIns   []device.SignInOptions
	signInErr error
	opened    *cloudproject.Project
	page      []string
}

func newCloudWorld(t *testing.T) *cloudWorld {
	t.Helper()
	w := &cloudWorld{}
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.requests = append(w.requests, r)
		w.mu.Unlock()
		switch strings.TrimPrefix(r.URL.Path, "/v0/datatug/") {
		case "projects/project_summary":
			if w.summary != nil {
				w.summary(rw)
				return
			}
			_, _ = io.WriteString(rw, `{"id":"sales","title":"Sales"}`)
		case "projects/connection_catalog":
			rw.Header().Set("ETag", `"1"`)
			_, _ = io.WriteString(rw, `{"connections":[{"id":"warehouse"}]}`)
		default:
			_, _ = io.WriteString(rw, `{}`)
		}
	}))
	t.Cleanup(w.server.Close)
	origSignIn, origRun, origOut, origErr := cloudSignIn, runCloudProjectInUIApp, cloudSignInOut, cloudSignInErr
	origLoader, origLine := dtproject.CloudLoader, dtproject.CloudErrorLine
	t.Cleanup(func() {
		cloudSignIn, runCloudProjectInUIApp, cloudSignInOut, cloudSignInErr = origSignIn, origRun, origOut, origErr
		dtproject.CloudLoader, dtproject.CloudErrorLine = origLoader, origLine
	})
	cloudSignInOut, cloudSignInErr = &bytes.Buffer{}, &bytes.Buffer{}
	cloudSignIn = func(_ context.Context, opts device.SignInOptions) (device.Session, error) {
		w.signIns = append(w.signIns, opts)
		if w.signInErr != nil {
			return device.Session{}, w.signInErr
		}
		return device.Session{Email: "ann@example.com", Token: func(context.Context) (string, error) { return "bearer-1", nil }}, nil
	}
	runCloudProjectInUIApp = func(p cloudproject.Project, page []string) error {
		w.opened, w.page = &p, page
		return nil
	}
	return w
}

func (w *cloudWorld) run(args ...string) error {
	return runUIDeepLinkCommand(append(args, "--api-url", w.server.URL+"/v0/")...)
}

func exitCode(t *testing.T, err error) (int, string) {
	t.Helper()
	var exit *exitError
	require.ErrorAs(t, err, &exit)
	return exit.ExitCode(), exit.Error()
}

func TestCloudLinkOpensProjectAfterSignInAndSummary(t *testing.T) {
	w := newCloudWorld(t)
	require.NoError(t, w.run("--deep-link", spaceLink+"/query/reports/top", "--insecure-storage", "--auth-host", "http://127.0.0.1:9"))
	require.NotNil(t, w.opened)
	assert.Equal(t, "Sales", w.opened.Title)
	assert.Equal(t, []string{"query", "reports", "top"}, w.page)
	require.Len(t, w.signIns, 1)
	assert.Equal(t, device.SignInOptions{Issuer: "http://127.0.0.1:9", Insecure: true, Interactive: true, Out: cloudSignInOut, ErrOut: cloudSignInErr}, w.signIns[0])
	for _, r := range w.requests {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer bearer-1", r.Header.Get("Authorization"))
		assert.Equal(t, "acme", r.URL.Query().Get("spaceID"))
	}
	require.NotNil(t, dtproject.CloudLoader)
	p, err := dtproject.CloudLoader(context.Background(), cloudproject.Ref{SpaceID: "acme", ProjectID: "sales"})
	require.NoError(t, err)
	assert.Equal(t, "Sales", p.Title)
	assert.Contains(t, dtproject.CloudErrorLine(cloudproject.ErrForbidden), "ann@example.com")
}

func TestCloudLinkFailuresBeforeTheUIStart(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   int
		line   string
	}{
		{http.StatusForbidden, exitCodeAccessDenied, "ann@example.com has no access to this project. Ask a Space admin to invite you."},
		{http.StatusNotFound, exitCodeNotFound, "Project not found, or it was deleted."},
		{http.StatusUnauthorized, exitCodeAccessDenied, "Sign in with: datatug auth login"},
		{http.StatusServiceUnavailable, exitCodeDatabase, "Can't reach DataTug Cloud: HTTP 503"},
		{http.StatusTeapot, 1, "unexpected answer from DataTug Cloud: HTTP 418"},
	} {
		w := newCloudWorld(t)
		w.summary = func(rw http.ResponseWriter) { rw.WriteHeader(tc.status) }
		code, line := exitCode(t, w.run("--deep-link", spaceLink))
		assert.Equal(t, tc.code, code, "status %d", tc.status)
		assert.Equal(t, tc.line, line, "status %d", tc.status)
		assert.NotContains(t, line, "bearer-1")
		assert.Nil(t, w.opened, "status %d: the UI started", tc.status)
	}
}

func TestCloudLinkSignInOutcomes(t *testing.T) {
	w := newCloudWorld(t)
	w.signInErr = device.ErrNotSignedIn
	code, line := exitCode(t, w.run("--deep-link", spaceLink, "--no-sign-in"))
	assert.Equal(t, exitCodeAccessDenied, code)
	assert.Equal(t, "Sign in with: datatug auth login", line)
	assert.False(t, w.signIns[0].Interactive)
	assert.Empty(t, w.requests)

	w = newCloudWorld(t)
	w.signInErr = errors.New("device login cancelled")
	code, line = exitCode(t, w.run("--deep-link", spaceLink))
	assert.Equal(t, 1, code)
	assert.Equal(t, "device login cancelled", line)
}

func TestCloudLinkRejectedBeforeAnyRequest(t *testing.T) {
	w := newCloudWorld(t)
	code, line := exitCode(t, w.run("--deep-link", "https://datatug.app/store/firestore/project/p1"))
	assert.Equal(t, 1, code)
	assert.Equal(t, dtproject.PersonalCloudNotice, line)

	code, line = exitCode(t, w.run("--deep-link", "https://datatug.app/space/acme/store/firestore/project/"+strings.Repeat("p", 129)))
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, line, "invalid project id")

	code, line = exitCode(t, runUIDeepLinkCommand("--deep-link", spaceLink, "--api-url", "http://example.com/v0/"))
	assert.Equal(t, exitCodeUsage, code)
	assert.Contains(t, line, "--api-url")

	assert.Empty(t, w.requests)
	assert.Empty(t, w.signIns)
	assert.Nil(t, w.opened)
}

func TestNewCloudProjectClient(t *testing.T) {
	c, err := newCloudProjectClient(cloudOptions{})
	require.NoError(t, err)
	assert.Equal(t, defaultAIAPIURLs[0], c.BaseURL)
	assert.ErrorIs(t, c.HTTP.CheckRedirect(nil, nil), http.ErrUseLastResponse)

	c, err = newCloudProjectClient(cloudOptions{apiURL: "http://127.0.0.1:1/v0/,http://127.0.0.1:2/v0/"})
	require.NoError(t, err)
	assert.NotNil(t, c.HTTP.Transport)

	orig := cloudHTTPTransport
	t.Cleanup(func() { cloudHTTPTransport = orig })
	cloudHTTPTransport = http.DefaultTransport
	c, err = newCloudProjectClient(cloudOptions{})
	require.NoError(t, err)
	assert.Equal(t, http.DefaultTransport, c.HTTP.Transport)
}

func TestConfigureCloudUILoader(t *testing.T) {
	w := newCloudWorld(t)
	configureCloudUILoader()
	assert.Equal(t, "This account has no access to this project. Ask a Space admin to invite you.", dtproject.CloudErrorLine(cloudproject.ErrForbidden))

	origURLs := defaultAIAPIURLs
	t.Cleanup(func() { defaultAIAPIURLs = origURLs })
	defaultAIAPIURLs = []string{w.server.URL + "/v0/"}
	p, err := dtproject.CloudLoader(context.Background(), cloudproject.Ref{SpaceID: "acme", ProjectID: "sales"})
	require.NoError(t, err)
	assert.Equal(t, "Sales", p.Title)
	assert.False(t, w.signIns[0].Interactive, "the UI never runs the device flow")
	assert.Contains(t, dtproject.CloudErrorLine(cloudproject.ErrForbidden), "ann@example.com")

	w.signInErr = device.ErrNotSignedIn
	_, err = dtproject.CloudLoader(context.Background(), cloudproject.Ref{SpaceID: "acme", ProjectID: "sales"})
	assert.ErrorIs(t, err, device.ErrNotSignedIn)
	assert.Equal(t, "Sign in with: datatug auth login", dtproject.CloudErrorLine(err))

	defaultAIAPIURLs = []string{"ftp://bad"}
	_, err = dtproject.CloudLoader(context.Background(), cloudproject.Ref{SpaceID: "acme", ProjectID: "sales"})
	assert.Error(t, err)
}

func TestCloudErrorLines(t *testing.T) {
	assert.Equal(t, "Your DataTug sign-in expired. Run: datatug auth login", cloudErrorLine(cloudproject.ErrUnauthorized, ""))
	assert.Equal(t, "Can't reach DataTug Cloud: timed out", cloudErrorLine(wrap(cloudproject.ErrUnavailable, "timed out"), ""))
	assert.Equal(t, "other", reasonAfter(errors.New("other"), cloudproject.ErrUnavailable))
	assert.Equal(t, "boom", cloudErrorLine(errors.New("boom"), ""))
	assert.Equal(t, 1, cloudExitCode(errors.New("boom")))
}

func wrap(sentinel error, reason string) error {
	return &wrapped{sentinel: sentinel, reason: reason}
}

type wrapped struct {
	sentinel error
	reason   string
}

func (w *wrapped) Error() string { return w.sentinel.Error() + ": " + w.reason }
func (w *wrapped) Unwrap() error { return w.sentinel }

func TestUIWithoutDeepLinkConfiguresCloudLoader(t *testing.T) {
	stubUI(t, &dtstate.DatatugState{}, nil)
	origLoader := dtproject.CloudLoader
	t.Cleanup(func() { dtproject.CloudLoader = origLoader })
	dtproject.CloudLoader = nil
	require.NoError(t, runUIDeepLinkCommand())
	assert.NotNil(t, dtproject.CloudLoader)
}

func TestProjectsCommandLabelsCloudProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := "projects:\n  - id: datatug.app/space/acme/sales\n    url: https://datatug.app/space/acme/store/firestore/project/sales\n    title: Sales\n  - id: local\n    path: ~/p\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".datatug.yaml"), []byte(settings), 0o600))
	var err error
	out := covDCaptureStdout(t, func() { err = projectsCommandAction(projectsCommandArgs(), nil) })
	require.NoError(t, err)
	assert.Contains(t, out, "datatug.app/space/acme/sales (cloud)")
	assert.Contains(t, out, "local\n")
	assert.NotContains(t, out, "local (cloud)")
}

func TestCloudProjectsAreNotFolderProjects(t *testing.T) {
	cloud := &dtconfig.ProjectRef{ID: "datatug.app/space/acme/sales", Url: "https://datatug.app/space/acme/store/firestore/project/sales"}
	local := &dtconfig.ProjectRef{ID: "local", Path: "~/p"}
	paths := getProjPathsByID(dtconfig.Settings{Projects: []*dtconfig.ProjectRef{cloud, local}})
	assert.Equal(t, map[string]string{"local": "~/p"}, paths)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	settings := "projects:\n  - id: datatug.app/space/acme/sales\n    url: https://datatug.app/space/acme/store/firestore/project/sales\n  - id: local\n    path: ~/p\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".datatug.yaml"), []byte(settings), 0o600))
	project, dir, err := projectNameOf("")
	require.NoError(t, err)
	assert.Equal(t, "local", project)
	assert.Empty(t, dir)
}

func TestUIClientHasNoFailoverNotice(t *testing.T) {
	c, err := newCloudProjectClient(cloudOptions{inUI: true, apiURL: "http://127.0.0.1:1/v0/,http://127.0.0.1:2/v0/"})
	require.NoError(t, err)
	assert.NotNil(t, c.HTTP.Transport)
}

func TestCloudLinkInterruptedDuringReadsExitsOne(t *testing.T) {
	w := newCloudWorld(t)
	orig := interruptContext
	t.Cleanup(func() { interruptContext = orig })
	interruptContext = func(parent context.Context) (context.Context, func() os.Signal, func()) {
		ctx, cancel := context.WithCancel(parent)
		cancel() // Ctrl+C already pressed
		return ctx, func() os.Signal { return os.Interrupt }, func() {}
	}
	code, line := exitCode(t, w.run("--deep-link", spaceLink))
	assert.Equal(t, 1, code)
	assert.Equal(t, "interrupted", line)
	assert.Nil(t, w.opened)
}
