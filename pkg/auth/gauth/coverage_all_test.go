package gauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/option"
)

func TestFileStore_EnsureDir_ValidateError(t *testing.T) {
	fs := FileStore{Filepath: ""}
	err := fs.ensureDir()
	require.Error(t, err)
}

func TestInteractiveLoginAllowed(t *testing.T) {
	origIsTesting := isTesting
	defer func() { isTesting = origIsTesting }()

	// When testing, returns false
	isTesting = func() bool { return true }
	require.False(t, interactiveLoginAllowed())

	// When not testing
	isTesting = func() bool { return false }

	t.Setenv("DATATUG_NO_BROWSER", "1")
	require.False(t, interactiveLoginAllowed())

	t.Setenv("DATATUG_NO_BROWSER", "true")
	require.False(t, interactiveLoginAllowed())

	t.Setenv("DATATUG_NO_BROWSER", "")
	require.True(t, interactiveLoginAllowed())

	t.Setenv("DATATUG_NO_BROWSER", "0")
	require.True(t, interactiveLoginAllowed())

	t.Setenv("DATATUG_NO_BROWSER", "false")
	require.True(t, interactiveLoginAllowed())
}

func TestGetTokenFromWeb_Branches(t *testing.T) {
	origIsTesting := isTesting
	defer func() { isTesting = origIsTesting }()
	origOpenBrowser := openBrowser
	defer func() { openBrowser = origOpenBrowser }()
	origWaitForAuthCode := waitForAuthCodeFn
	defer func() { waitForAuthCodeFn = origWaitForAuthCode }()
	origConfigExchange := configExchangeFn
	defer func() { configExchangeFn = origConfigExchange }()

	ctx := context.Background()
	config := &oauth2.Config{}

	// 1. Not allowed
	isTesting = func() bool { return true }
	_, err := getTokenFromWeb(ctx, config)
	require.ErrorIs(t, err, ErrInteractiveLoginUnavailable)

	isTesting = func() bool { return false }
	t.Setenv("DATATUG_NO_BROWSER", "")

	// 2. openBrowser error
	openBrowser = func(string) error { return errors.New("open browser fail") }
	_, err = getTokenFromWeb(ctx, config)
	require.ErrorContains(t, err, "open browser fail")

	// 3. waitForAuthCode error
	openBrowser = func(string) error { return nil }
	waitForAuthCodeFn = func() (string, error) { return "", errors.New("wait for auth code fail") }
	configExchangeFn = func(context.Context, *oauth2.Config, string) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "token"}, nil
	}
	tok, err := getTokenFromWeb(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, tok)

	// 4. configExchange error
	waitForAuthCodeFn = func() (string, error) { return "code-123", nil }
	configExchangeFn = func(context.Context, *oauth2.Config, string) (*oauth2.Token, error) {
		return nil, errors.New("exchange failed")
	}
	_, err = getTokenFromWeb(ctx, config)
	require.ErrorContains(t, err, "exchange failed")

	// 5. Success
	configExchangeFn = func(context.Context, *oauth2.Config, string) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "token-ok", RefreshToken: "ref-ok"}, nil
	}
	tok, err = getTokenFromWeb(ctx, config)
	require.NoError(t, err)
	require.Equal(t, "token-ok", tok.AccessToken)
}

// serveUntilShutdown mimics http.Server.ListenAndServe: it delivers one
// redirect to the handler (when target is not empty), then blocks until the
// shutdown seam releases it and returns result.
func serveUntilShutdown(t *testing.T, target string, result error) (serve func(*http.Server) error, shutdown func(context.Context, *http.Server) error) {
	t.Helper()
	release := make(chan struct{})
	serve = func(srv *http.Server) error {
		if target != "" {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			srv.Handler.ServeHTTP(httptest.NewRecorder(), req)
		}
		<-release
		return result
	}
	shutdown = func(context.Context, *http.Server) error {
		close(release)
		return errors.New("simulated shutdown error")
	}
	return serve, shutdown
}

func TestWaitForAuthCode(t *testing.T) {
	origServe, origShutdown, origTimeout := authServerServe, srvShutdown, serveDrainTimeout
	defer func() { authServerServe, srvShutdown, serveDrainTimeout = origServe, origShutdown, origTimeout }()

	t.Run("code captured, shutdown error logged", func(t *testing.T) {
		authServerServe, srvShutdown = serveUntilShutdown(t, "/oauth2callback?code=captured-code", http.ErrServerClosed)
		code, err := waitForAuthCode()
		require.NoError(t, err)
		require.Equal(t, "captured-code", code)
	})

	t.Run("serve error reported after the code", func(t *testing.T) {
		authServerServe, srvShutdown = serveUntilShutdown(t, "/oauth2callback?code=code-err", errors.New("listen error"))
		_, err := waitForAuthCode()
		require.ErrorContains(t, err, "listen error")
	})

	t.Run("serve fails before any redirect", func(t *testing.T) {
		authServerServe = func(*http.Server) error { return errors.New("address already in use") }
		_, err := waitForAuthCode()
		require.ErrorContains(t, err, "address already in use")
	})

	t.Run("server closed before any redirect", func(t *testing.T) {
		authServerServe = func(*http.Server) error { return http.ErrServerClosed }
		_, err := waitForAuthCode()
		require.ErrorIs(t, err, errAuthServerStopped)
	})

	t.Run("server never reports after shutdown", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		serveDrainTimeout = 10 * time.Millisecond
		authServerServe = func(srv *http.Server) error {
			srv.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/oauth2callback?code=late", nil))
			<-release
			return nil
		}
		srvShutdown = func(context.Context, *http.Server) error { return nil }
		code, err := waitForAuthCode()
		require.NoError(t, err)
		require.Equal(t, "late", code)
	})
}

type mockTokenSource struct {
	token *oauth2.Token
	err   error
}

func (m mockTokenSource) Token() (*oauth2.Token, error) {
	return m.token, m.err
}

func TestGetGoogleCloudClient_Branches(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()

	origTokenSource := tokenSourceFromConfig
	defer func() { tokenSourceFromConfig = origTokenSource }()
	origGetTokenFromWeb := getTokenFromWebFn
	defer func() { getTokenFromWebFn = origGetTokenFromWeb }()
	origSaveRefresh := saveRefreshTokenFn
	defer func() { saveRefreshTokenFn = origSaveRefresh }()

	// 1. Refresh token in keychain, successful refresh
	require.NoError(t, keyring.Set(keyringService, keyringUser, "existing-refresh-token"))
	tokenSourceFromConfig = func(ctx context.Context, config *oauth2.Config, token *oauth2.Token) oauth2.TokenSource {
		return mockTokenSource{token: &oauth2.Token{AccessToken: "fresh-access", RefreshToken: "existing-refresh-token"}}
	}
	client, err := getGoogleCloudClient(ctx)
	require.NoError(t, err)
	require.NotNil(t, client)

	// 2. Refresh token in keychain, refresh fails -> fallback to getTokenFromWebFn
	tokenSourceFromConfig = func(ctx context.Context, config *oauth2.Config, token *oauth2.Token) oauth2.TokenSource {
		return mockTokenSource{err: errors.New("refresh expired")}
	}
	getTokenFromWebFn = func(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "web-token", RefreshToken: "web-refresh"}, nil
	}
	client, err = getGoogleCloudClient(ctx)
	require.NoError(t, err)
	require.NotNil(t, client)

	// 3. saveRefreshToken error
	saveRefreshTokenFn = func(token string) error {
		return errors.New("save refresh fail")
	}
	client, err = getGoogleCloudClient(ctx)
	require.NoError(t, err)
	require.NotNil(t, client)
	saveRefreshTokenFn = origSaveRefresh

	// 4. No refresh token in keychain, getTokenFromWebFn fails
	require.NoError(t, keyring.Delete(keyringService, keyringUser))
	getTokenFromWebFn = func(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
		return nil, errors.New("user canceled web auth")
	}
	_, err = getGoogleCloudClient(ctx)
	require.ErrorContains(t, err, "user canceled web auth")

	// 5. getTokenFromWebFn succeeds with empty refresh token
	getTokenFromWebFn = func(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "web-token-no-refresh"}, nil
	}
	client, err = getGoogleCloudClient(ctx)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestGetGCloudProjects_Branches(t *testing.T) {
	ctx := context.Background()

	origGetClient := getGoogleCloudClientFn
	defer func() { getGoogleCloudClientFn = origGetClient }()
	origNewCRM := newCRMServiceFn
	defer func() { newCRMServiceFn = origNewCRM }()

	// 1. getGoogleCloudClientFn returns error
	getGoogleCloudClientFn = func(context.Context) (*http.Client, error) {
		return nil, errors.New("oauth client failure")
	}
	_, err := GetGCloudProjects(ctx)
	require.ErrorContains(t, err, "failed to get HTTP client for Googe Cloud")

	// 2. newCRMServiceFn returns error
	getGoogleCloudClientFn = func(context.Context) (*http.Client, error) {
		return http.DefaultClient, nil
	}
	newCRMServiceFn = func(ctx context.Context, client *http.Client) (*cloudresourcemanager.Service, error) {
		return nil, errors.New("crm service creation failure")
	}
	_, err = GetGCloudProjects(ctx)
	require.ErrorContains(t, err, "unable to create Cloud Resource Manager service")

	// 3. Successful search projects
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"projects": [
				{"projectId": "proj-1", "displayName": "Project One"},
				{"projectId": "proj-2", "displayName": "Project Two"}
			]
		}`))
	}))
	defer server.Close()

	newCRMServiceFn = func(ctx context.Context, client *http.Client) (*cloudresourcemanager.Service, error) {
		return cloudresourcemanager.NewService(ctx, option.WithEndpoint(server.URL), option.WithoutAuthentication())
	}
	projects, err := GetGCloudProjects(ctx)
	require.NoError(t, err)
	require.Len(t, projects, 2)
	require.Equal(t, "proj-1", projects[0].ProjectId)
	require.Equal(t, "proj-2", projects[1].ProjectId)

	// 4. Search projects returns API error
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"code": 500, "message": "internal server error"}}`))
	}))
	defer errServer.Close()

	newCRMServiceFn = func(ctx context.Context, client *http.Client) (*cloudresourcemanager.Service, error) {
		return cloudresourcemanager.NewService(ctx, option.WithEndpoint(errServer.URL), option.WithoutAuthentication())
	}
	_, err = GetGCloudProjects(ctx)
	require.Error(t, err)
}

func TestDefaultClosures(t *testing.T) {
	// 1. tokenSourceFromConfig
	ts := tokenSourceFromConfig(context.Background(), &oauth2.Config{}, &oauth2.Token{AccessToken: "fake"})
	require.NotNil(t, ts)

	// 2. configExchangeFn
	_, err := configExchangeFn(context.Background(), &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: "http://127.0.0.1:0"}}, "code")
	require.Error(t, err)

	// 3. authServerServe
	srv := &http.Server{Addr: "127.0.0.1:-1"}
	err = authServerServe(srv)
	require.Error(t, err)

	// 4. srvShutdown
	srv2 := &http.Server{}
	err = srvShutdown(context.Background(), srv2)
	require.NoError(t, err)

	// 5. newCRMServiceFn
	crm, err := newCRMServiceFn(context.Background(), http.DefaultClient)
	require.NoError(t, err)
	require.NotNil(t, crm)
}
