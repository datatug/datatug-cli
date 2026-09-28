package device

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/strongo/deviceauth"
)

func TestCommandFactories(t *testing.T) {
	cmd := Command()
	require.NotNil(t, cmd)
	require.Equal(t, "device", cmd.Use)

	login := LoginCommand()
	require.NotNil(t, login)
	require.Equal(t, "login", login.Use)

	status := StatusCommand()
	require.NotNil(t, status)
	require.Equal(t, "status", status.Use)

	logout := LogoutCommand()
	require.NotNil(t, logout)
	require.Equal(t, "logout", logout.Use)

	// directCommand with DATATUG_AUTH_HOST set
	t.Setenv("DATATUG_AUTH_HOST", "https://custom-auth.example.com")
	loginCustom := LoginCommand()
	require.NotNil(t, loginCustom)
}

func TestLoginCommand_Branches(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()

	// 1. newClient error
	deps := testDependencies(server, &memoryStore{})
	deps.newClient = func(string, bool) (*deviceauth.Client, deviceauth.Store, string, error) {
		return nil, nil, "", errors.New("client creation fail")
	}
	cmd := newCommand(deps)
	cmd.SetArgs([]string{"login", "--auth-host", server.URL})
	err := cmd.Execute()
	require.ErrorContains(t, err, "configure credential storage")

	// 2. insecure storage warning
	store := &memoryStore{}
	deps = testDependencies(server, store)
	cmd = newCommand(deps)
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"login", "--auth-host", server.URL, "--insecure-storage"})
	err = cmd.Execute()
	require.NoError(t, err)
	require.Contains(t, errBuf.String(), "Warning: --insecure-storage writes the Firebase session unencrypted")

	// 3. TokenTransformer unexpected token type
	badTypeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device/code":
			_, _ = io.WriteString(w, `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://verify.example/device","expires_in":300,"interval":1}`)
		case "/oauth/token":
			_, _ = io.WriteString(w, `{"access_token":"token","token_type":"Bearer","expires_in":300}`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer badTypeServer.Close()
	cmd = newCommand(testDependencies(badTypeServer, &memoryStore{}))
	cmd.SetArgs([]string{"login", "--auth-host", badTypeServer.URL})
	err = cmd.Execute()
	require.ErrorContains(t, err, "unexpected [redacted] type")

	// 4. TokenTransformer exchange error
	deps = testDependencies(server, &memoryStore{})
	deps.exchange = func(context.Context, string) (session, error) {
		return session{}, errors.New("exchange simulated fail")
	}
	cmd = newCommand(deps)
	cmd.SetArgs([]string{"login", "--auth-host", server.URL})
	err = cmd.Execute()
	require.ErrorContains(t, err, "exchange device login for Firebase session")
}

func TestStatusCommand_Branches(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()

	// 1. newClient error
	deps := testDependencies(server, &memoryStore{})
	deps.newClient = func(string, bool) (*deviceauth.Client, deviceauth.Store, string, error) {
		return nil, nil, "", errors.New("client creation fail")
	}
	cmd := newCommand(deps)
	cmd.SetArgs([]string{"status", "--auth-host", server.URL})
	err := cmd.Execute()
	require.ErrorContains(t, err, "configure credential storage")

	// 2. Not logged in (ErrCredentialNotFound)
	store := &memoryStore{}
	deps = testDependencies(server, store)
	cmd = newCommand(deps)
	cmd.SetArgs([]string{"status", "--auth-host", server.URL})
	err = cmd.Execute()
	require.ErrorContains(t, err, "not logged in")

	// 3. Token load error (generic error)
	store.err = errors.New("store corrupted")
	store.credential = deviceauth.Credential{Issuer: server.URL, ClientID: clientID, AccessToken: "token"}
	cmd = newCommand(testDependencies(server, store))
	cmd.SetArgs([]string{"status", "--auth-host", server.URL})
	err = cmd.Execute()
	require.ErrorContains(t, err, "load Firebase session")

	// 4. UserInfo error
	badUserServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/userinfo" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid_token"}`)
			return
		}
	}))
	defer badUserServer.Close()
	store = &memoryStore{credential: deviceauth.Credential{Issuer: badUserServer.URL, ClientID: clientID, AccessToken: "token", Expiry: time.Now().Add(time.Hour)}}
	cmd = newCommand(testDependencies(badUserServer, store))
	cmd.SetArgs([]string{"status", "--auth-host", badUserServer.URL})
	err = cmd.Execute()
	require.ErrorContains(t, err, "validate Firebase session")
}

func TestLogoutCommand_Branches(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()

	// 1. newClient error
	deps := testDependencies(server, &memoryStore{})
	deps.newClient = func(string, bool) (*deviceauth.Client, deviceauth.Store, string, error) {
		return nil, nil, "", errors.New("client creation fail")
	}
	cmd := newCommand(deps)
	cmd.SetArgs([]string{"logout", "--auth-host", server.URL})
	err := cmd.Execute()
	require.ErrorContains(t, err, "configure credential storage")

	// 2. Not logged in
	store := &memoryStore{}
	cmd = newCommand(testDependencies(server, store))
	cmd.SetArgs([]string{"logout", "--auth-host", server.URL})
	err = cmd.Execute()
	require.ErrorContains(t, err, "not logged in")

	// 3. Logout error from client.Logout (simulate bad server)
	badLogoutServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/revoke" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"server_error"}`)
			return
		}
	}))
	defer badLogoutServer.Close()
	store = &memoryStore{credential: deviceauth.Credential{Issuer: badLogoutServer.URL, ClientID: clientID, AccessToken: "token", Expiry: time.Now().Add(time.Hour)}}
	cmd = newCommand(testDependencies(badLogoutServer, store))
	cmd.SetArgs([]string{"logout", "--auth-host", badLogoutServer.URL})
	err = cmd.Execute()
	require.ErrorContains(t, err, "logout DataTug session")
}

func TestExchangeCustomToken_AllBranches(t *testing.T) {
	origHttpDo := httpDo
	defer func() { httpDo = origHttpDo }()
	origNewReq := newRequestWithContext
	defer func() { newRequestWithContext = origNewReq }()

	ctx := context.Background()

	// 0. NewRequest error
	newRequestWithContext = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("simulated request creation error")
	}
	_, err := exchangeCustomToken(ctx, "custom-tok")
	require.ErrorContains(t, err, "simulated request creation error")
	newRequestWithContext = origNewReq

	// 1. Network / Do error
	httpDo = func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network failure")
	}
	_, err = exchangeCustomToken(ctx, "custom-tok")
	require.ErrorContains(t, err, "network failure")

	// 2. HTTP non-2xx status
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 400,
			Body:       io.NopCloser(strings.NewReader(`{"error":"bad request"}`)),
		}, nil
	}
	_, err = exchangeCustomToken(ctx, "custom-tok")
	require.ErrorContains(t, err, "returned HTTP 400")

	// 3. Decode error (bad JSON)
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`not-json`)),
		}, nil
	}
	_, err = exchangeCustomToken(ctx, "custom-tok")
	require.Error(t, err)

	// 4. Invalid expiresIn
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"idToken":"id","refreshToken":"ref","expiresIn":"bad"}`)),
		}, nil
	}
	_, err = exchangeCustomToken(ctx, "custom-tok")
	require.ErrorContains(t, err, "decode Firebase session expiry")

	// 5. Success
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"idToken":"id-123","refreshToken":"ref-456","localId":"uid-789","email":"test@example.com","expiresIn":"3600"}`)),
		}, nil
	}
	sess, err := exchangeCustomToken(ctx, "custom-tok")
	require.NoError(t, err)
	require.Equal(t, "id-123", sess.IDToken)
	require.Equal(t, "ref-456", sess.RefreshToken)
	require.Equal(t, "uid-789", sess.UID)
	require.Equal(t, "test@example.com", sess.Email)
}

func TestRefreshFirebaseSession_AllBranches(t *testing.T) {
	origHttpDo := httpDo
	defer func() { httpDo = origHttpDo }()
	origNewReq := newRequestWithContext
	defer func() { newRequestWithContext = origNewReq }()

	ctx := context.Background()

	// 0. NewRequest error
	newRequestWithContext = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("simulated request creation error")
	}
	_, err := refreshFirebaseSession(ctx, "refresh-tok")
	require.ErrorContains(t, err, "simulated request creation error")
	newRequestWithContext = origNewReq

	// 1. Network / Do error
	httpDo = func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network failure")
	}
	_, err = refreshFirebaseSession(ctx, "refresh-tok")
	require.ErrorContains(t, err, "network failure")

	// 2. HTTP non-2xx status
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 401,
			Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
		}, nil
	}
	_, err = refreshFirebaseSession(ctx, "refresh-tok")
	require.ErrorContains(t, err, "returned HTTP 401")

	// 3. Decode error (bad JSON)
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`not-json`)),
		}, nil
	}
	_, err = refreshFirebaseSession(ctx, "refresh-tok")
	require.Error(t, err)

	// 4. Invalid expiresIn
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id_token":"id","refresh_token":"ref","expires_in":"bad"}`)),
		}, nil
	}
	_, err = refreshFirebaseSession(ctx, "refresh-tok")
	require.ErrorContains(t, err, "decode Firebase refreshed session expiry")

	// 5. Success
	httpDo = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"id_token":"ref-id-123","refresh_token":"ref-refresh-456","user_id":"uid-789","expires_in":"3600"}`)),
		}, nil
	}
	sess, err := refreshFirebaseSession(ctx, "refresh-tok")
	require.NoError(t, err)
	require.Equal(t, "ref-id-123", sess.IDToken)
	require.Equal(t, "ref-refresh-456", sess.RefreshToken)
	require.Equal(t, "uid-789", sess.UID)
}

func TestNewClient_Branches(t *testing.T) {
	// Invalid issuer
	_, _, _, err := newClient("::invalid-url", false)
	require.Error(t, err)

	// Insecure mode with userConfigDir error
	origConfigDir := userConfigDir
	defer func() { userConfigDir = origConfigDir }()

	userConfigDir = func() (string, error) {
		return "", errors.New("user config dir error")
	}
	_, _, _, err = newClient("https://auth.example.com", true)
	require.ErrorContains(t, err, "user config dir error")

	// Insecure mode with valid temp dir
	tmpDir := t.TempDir()
	userConfigDir = func() (string, error) {
		return tmpDir, nil
	}
	client, store, desc, err := newClient("https://auth.example.com", true)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, store)
	require.True(t, strings.HasPrefix(desc, tmpDir))

	// Insecure mode with urlParse error
	origUrlParse := urlParse
	defer func() { urlParse = origUrlParse }()
	urlParse = func(string) (*url.URL, error) {
		return nil, errors.New("url parse simulated error")
	}
	_, _, _, err = newClient("https://auth.example.com", true)
	require.ErrorContains(t, err, "url parse simulated error")
	urlParse = origUrlParse
}

func TestSavedTokenSource_And_TokenSource_Branches(t *testing.T) {
	ctx := context.Background()

	// SavedTokenSource with newClient error
	_, err := SavedTokenSource(ctx, false)
	// Keyring may or may not succeed in CI/test environment; test with invalid auth host to force error
	t.Setenv("DATATUG_AUTH_HOST", "::invalid")
	_, err = SavedTokenSource(ctx, false)
	require.Error(t, err)

	// SavedTokenSource with DATATUG_AUTH_HOST empty (uses defaultIssuer)
	t.Setenv("DATATUG_AUTH_HOST", "")
	tmpDir := t.TempDir()
	origConfigDir := userConfigDir
	defer func() { userConfigDir = origConfigDir }()
	userConfigDir = func() (string, error) { return tmpDir, nil }
	ts, err := SavedTokenSource(ctx, true)
	require.NoError(t, err)
	require.NotNil(t, ts)

	// TokenContext with unconfigured TokenSource
	emptyTS := &TokenSource{}
	_, err = emptyTS.Token()
	require.ErrorContains(t, err, "token source is not configured")

	client, err := deviceauth.NewClient(deviceauth.ClientConfig{Issuer: "https://auth.example.com", ClientID: clientID, Scopes: scopes, RequiredScopes: scopes})
	require.NoError(t, err)
	store := &memoryStore{}
	configuredTS := newTokenSource(ctx, client, store, func(context.Context, string) (session, error) {
		return session{}, errors.New("refresh failed")
	})

	// Load error
	configuredTS.store = &failingLoadStore{memoryStore: store}
	_, err = configuredTS.Token()
	require.ErrorContains(t, err, "load error")
	configuredTS.store = store

	// Save error during refresh
	store.credential = deviceauth.Credential{
		Issuer:       "https://auth.example.com",
		ClientID:     clientID,
		AccessToken:  "old-token",
		RefreshToken: "old-refresh",
		Expiry:       time.Now().Add(-time.Hour),
	}
	configuredTS.refresh = func(context.Context, string) (session, error) {
		return session{IDToken: "new-id", RefreshToken: "new-ref", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	configuredTS.store = &failingSaveStore{memoryStore: store}
	_, err = configuredTS.Token()
	require.ErrorContains(t, err, "save refreshed Firebase session")
}

type failingLoadStore struct {
	*memoryStore
}

func (f *failingLoadStore) Load() (deviceauth.Credential, error) {
	return deviceauth.Credential{}, errors.New("load error")
}

type failingSaveStore struct {
	*memoryStore
}

func (f *failingSaveStore) Save(deviceauth.Credential) error {
	return errors.New("save simulated error")
}
