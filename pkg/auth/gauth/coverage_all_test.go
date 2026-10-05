package gauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

	// 4. srvShutdown
	srv2 := &http.Server{}
	err = srvShutdown(context.Background(), srv2)
	require.NoError(t, err)

	// 5. newCRMServiceFn
	crm, err := newCRMServiceFn(context.Background(), http.DefaultClient)
	require.NoError(t, err)
	require.NotNil(t, crm)
}
