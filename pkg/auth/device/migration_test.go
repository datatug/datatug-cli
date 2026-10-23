package device

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/strongo/deviceauth"
)

// movedDependencies is testDependencies with the production issuer move wired
// to a local issuer: previousIssuer is the old host, issuer is whatever local
// server the test passes with --auth-host.
func movedDependencies(server *httptest.Server, store *memoryStore, fallbacks ...string) dependencies {
	deps := testDependencies(server, store)
	deps.newClient = func(issuer string, _ bool) (*deviceauth.Client, deviceauth.Store, string, error) {
		client, err := deviceauth.NewClient(deviceauth.ClientConfig{
			Issuer: issuer, ClientID: clientID, Scopes: scopes, RequiredScopes: scopes,
			PreviousIssuers: []string{previousIssuer}, FallbackIssuers: fallbacks,
		})
		return client, store, "test keyring", err
	}
	return deps
}

func previousIssuerLogin(expiry time.Time) *memoryStore {
	return &memoryStore{credential: deviceauth.Credential{
		AccessToken: "firebase-id", RefreshToken: "firebase-refresh", TokenType: "Bearer", Expiry: expiry,
		Issuer: previousIssuer, ClientID: clientID, AccountID: "user-1",
	}}
}

func closedLoopbackURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	rawURL := server.URL
	server.Close()
	return rawURL
}

func TestIssuerDefaultsAndOverrides(t *testing.T) {
	t.Setenv("DATATUG_AUTH_HOST", "")
	require.Equal(t, "https://oauth.sneat.co", resolveIssuer())
	t.Setenv("DATATUG_AUTH_HOST", " http://127.0.0.1:9099 ")
	require.Equal(t, "http://127.0.0.1:9099", resolveIssuer())

	config := clientConfig(defaultIssuer)
	require.Equal(t, []string{"https://auth.sneat.co"}, config.PreviousIssuers)
	require.Equal(t, []string{"https://auth.sneat.co"}, config.FallbackIssuers)
	override := clientConfig("http://127.0.0.1:9099")
	require.Empty(t, override.PreviousIssuers, "an override must never reach production")
	require.Empty(t, override.FallbackIssuers, "an override must never reach production")
}

func TestLogin_ReplacesPreviousIssuerLoginOnce(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := previousIssuerLogin(time.Now().Add(time.Hour))
	cmd := newCommand(movedDependencies(server, store))
	var output, errorOutput bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&errorOutput)
	cmd.SetArgs([]string{"login", "--auth-host", server.URL})
	require.NoError(t, cmd.Execute())
	require.Equal(t, loginMovedNotice+"\n", errorOutput.String())
	require.Equal(t, server.URL, store.credential.Issuer)

	errorOutput.Reset()
	cmd.SetArgs([]string{"login", "--auth-host", server.URL})
	require.NoError(t, cmd.Execute())
	require.Empty(t, errorOutput.String(), "the moved notice must not repeat")
}

func TestStatus_ReportsPreviousIssuerLogin(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := previousIssuerLogin(time.Now().Add(time.Hour))
	cmd := newCommand(movedDependencies(server, store))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"status", "--auth-host", server.URL})
	err := cmd.Execute()
	require.EqualError(t, err, "Sneat sign-in moved to oauth.sneat.co — please run `datatug auth login` again")
	require.ErrorIs(t, err, deviceauth.ErrCredentialIssuerMoved)
}

func TestLogout_PreviousIssuerLoginIsRevokedAtCurrentIssuer(t *testing.T) {
	var revoked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/revoke" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		revoked = append(revoked, r.FormValue("token"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store := previousIssuerLogin(time.Now().Add(-time.Hour))
	cmd := newCommand(movedDependencies(server, store))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"logout", "--auth-host", server.URL})
	require.NoError(t, cmd.Execute())
	require.Equal(t, []string{"refreshed-id"}, revoked)
	_, err := store.Load()
	require.ErrorIs(t, err, deviceauth.ErrCredentialNotFound)

	failing := previousIssuerLogin(time.Now().Add(time.Hour))
	failing.err = errors.New("keyring locked")
	cmd = newCommand(movedDependencies(server, failing))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"logout", "--auth-host", server.URL})
	require.ErrorContains(t, cmd.Execute(), "move saved login to "+server.URL)
}

func TestLogin_FallsBackWhileIssuerIsNotServing(t *testing.T) {
	fallback := authServer(t, clientID)
	defer fallback.Close()
	issuer := closedLoopbackURL(t)
	store := &memoryStore{}
	cmd := newCommand(movedDependencies(fallback, store, fallback.URL))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"login", "--auth-host", issuer})
	require.NoError(t, cmd.Execute())
	require.Equal(t, issuer, store.credential.Issuer, "credentials stay keyed to the new issuer")
	require.Contains(t, output.String(), "Logged in to "+issuer)
}

func TestNewClient_InsecureStoreFindsPreviousIssuerFile(t *testing.T) {
	tmpDir := t.TempDir()
	origConfigDir := userConfigDir
	defer func() { userConfigDir = origConfigDir }()
	userConfigDir = func() (string, error) { return tmpDir, nil }

	legacyPath := credentialFilePath(tmpDir, "auth.sneat.co")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o700))
	require.NoError(t, os.WriteFile(legacyPath, []byte(`{"access_token":"old","issuer":"https://auth.sneat.co","client_id":"datatug-cli"}`), 0o600))

	client, store, path, err := newClient(defaultIssuer, true)
	require.NoError(t, err)
	require.Equal(t, credentialFilePath(tmpDir, "oauth.sneat.co"), path)
	_, err = newTokenSource(context.Background(), client, store, refreshFirebaseSession).Token()
	require.EqualError(t, err, signInMovedMessage)
	var moved *deviceauth.IssuerMovedError
	require.ErrorAs(t, err, &moved)
	require.Equal(t, "old", moved.Credential.AccessToken)

	require.NoError(t, client.ScopedStore(store).Save(deviceauth.Credential{AccessToken: "new"}))
	_, err = os.Stat(legacyPath)
	require.True(t, errors.Is(err, os.ErrNotExist), "the previous issuer's file must be removed after a new login is saved")
}
