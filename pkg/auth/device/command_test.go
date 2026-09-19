package device

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strongo/deviceauth"
)

type memoryStore struct {
	credential deviceauth.Credential
	err        error
}

func (s *memoryStore) Save(value deviceauth.Credential) error {
	if s.err != nil {
		return s.err
	}
	s.credential = value
	return nil
}
func (s *memoryStore) Load() (deviceauth.Credential, error) {
	if s.credential.AccessToken == "" {
		return deviceauth.Credential{}, deviceauth.ErrCredentialNotFound
	}
	return s.credential, nil
}
func (s *memoryStore) Delete() error { s.credential = deviceauth.Credential{}; return nil }

func TestLoginStatusLogout_UsesSharedDeviceFlowWithoutSecrets(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := &memoryStore{}
	deps := testDependencies(server, store)
	cmd := newCommand(deps)
	var output, errorOutput bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&errorOutput)
	cmd.SetArgs([]string{"login", "--auth-host", server.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if store.credential.AccessToken != "firebase-id" || store.credential.RefreshToken != "firebase-refresh" {
		t.Fatalf("stored credential = %+v", store.credential)
	}
	if strings.Contains(output.String(), "firebase-id") || strings.Contains(output.String(), "firebase-refresh") || strings.Contains(output.String(), "custom-token") {
		t.Fatalf("login exposed a secret: %q", output.String())
	}

	store.credential.Expiry = time.Now().Add(-time.Minute)
	output.Reset()
	cmd.SetArgs([]string{"status", "--auth-host", server.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(output.String(), "firebase-id") || strings.Contains(output.String(), "firebase-refresh") {
		t.Fatalf("status exposed a secret: %q", output.String())
	}
	if store.credential.AccessToken != "refreshed-id" || store.credential.RefreshToken != "refreshed-refresh" {
		t.Fatalf("expired session was not refreshed by authenticated status command: %+v", store.credential)
	}

	output.Reset()
	cmd.SetArgs([]string{"logout", "--auth-host", server.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, deviceauth.ErrCredentialNotFound) {
		t.Fatalf("credential retained after logout: %v", err)
	}
}

func TestLogin_BrowserFailureLeavesManualFallback(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := &memoryStore{}
	deps := testDependencies(server, store)
	deps.openBrowser = func(string) error { return errors.New("no browser") }
	cmd := newCommand(deps)
	var output, errorOutput bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&errorOutput)
	cmd.SetArgs([]string{"login", "--auth-host", server.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(output.String(), "ABCD-EFGH") || !strings.Contains(errorOutput.String(), "Continue with the URL above") {
		t.Fatalf("manual fallback missing: out=%q err=%q", output.String(), errorOutput.String())
	}
}

func TestLogin_RejectsWrongAudienceAndStorageFailure(t *testing.T) {
	server := authServer(t, "sneat-cli")
	defer server.Close()
	store := &memoryStore{}
	deps := testDependencies(server, store)
	cmd := newCommand(deps)
	cmd.SetArgs([]string{"login", "--auth-host", server.URL})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("wrong audience error = %v", err)
	}

	serverWrongStore := authServer(t, clientID)
	defer serverWrongStore.Close()
	store.err = errors.New("keyring unavailable")
	cmd = newCommand(testDependencies(serverWrongStore, store))
	cmd.SetArgs([]string{"login", "--auth-host", serverWrongStore.URL})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "save device credential") {
		t.Fatalf("storage failure error = %v", err)
	}
}

func TestPrintWarningsDoesNotExposeProviderDetail(t *testing.T) {
	var output bytes.Buffer
	printWarnings(&output, []error{errors.New("provider response included firebase-refresh")})
	if !strings.Contains(output.String(), "previous device login") || strings.Contains(output.String(), "firebase-refresh") {
		t.Fatalf("warning output=%q", output.String())
	}
}

func testDependencies(server *httptest.Server, store *memoryStore) dependencies {
	return dependencies{
		httpClient: server.Client(), openBrowser: func(string) error { return nil },
		exchange: func(context.Context, string) (session, error) {
			return session{IDToken: "firebase-id", RefreshToken: "firebase-refresh", UID: "user-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
		},
		refresh: func(context.Context, string) (session, error) {
			return session{IDToken: "refreshed-id", RefreshToken: "refreshed-refresh", UID: "user-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
		},
		newClient: func(issuer string, _ bool) (*deviceauth.Client, deviceauth.Store, string, error) {
			client, err := deviceauth.NewClient(deviceauth.ClientConfig{Issuer: issuer, ClientID: clientID, Scopes: scopes, RequiredScopes: scopes})
			return client, store, "test keyring", err
		},
	}
}

func authServer(t *testing.T, audience string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device/code":
			_, _ = io.WriteString(w, `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://verify.example/device","expires_in":300,"interval":1}`)
		case "/oauth/token":
			_, _ = io.WriteString(w, `{"access_token":"custom-token","token_type":"urn:ietf:params:oauth:token-type:firebase-custom-token","expires_in":300}`)
		case "/oauth/userinfo":
			if got := r.Header.Get("Authorization"); got != "Bearer firebase-id" && got != "Bearer refreshed-id" {
				t.Errorf("authorization = %q", got)
			}
			_, _ = io.WriteString(w, `{"sub":"user-1","aud":"`+audience+`","scope":"openid profile datatug:projects:read datatug:projects:write"}`)
		case "/oauth/revoke":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}
