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
	"golang.org/x/oauth2"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteWhoami_IncludesOptionalFields(t *testing.T) {
	var out bytes.Buffer
	identity := deviceauth.Identity{Subject: "u1", Email: "a@b.c", Name: "Alex", Scopes: []string{"openid", "profile"}}
	token := &oauth2.Token{Expiry: time.Unix(1000, 0)}
	if err := writeWhoami(&out, "https://auth.sneat.co", "test keyring", identity, token); err != nil {
		t.Fatalf("writeWhoami: %v", err)
	}
	got := out.String()
	for _, want := range []string{"a@b.c", "u1", "Alex", "https://auth.sneat.co", "openid profile", "1970-01-01T00:16:40Z", "test keyring", "datatug auth logout"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestWriteWhoami_PropagatesWriteError(t *testing.T) {
	if err := writeWhoami(failingWriter{}, "iss", "storage", deviceauth.Identity{Subject: "u1"}, &oauth2.Token{}); err == nil {
		t.Fatal("expected write error")
	}
}

func TestWhoami_ReportsSignedInAccount(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := &memoryStore{}
	deps := testDependencies(server, store)

	login := newCommand(deps)
	login.SetArgs([]string{"login", "--auth-host", server.URL})
	if err := login.Execute(); err != nil {
		t.Fatalf("login: %v", err)
	}

	issuer, insecure := server.URL, false
	cmd := newCommand(deps)
	cmd.AddCommand(whoamiCommand(&issuer, &insecure, deps))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"whoami"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("whoami: %v", err)
	}

	got := out.String()
	for _, want := range []string{"datatug", "You are signed in as user-1", "UID", "user-1", "Issuer", server.URL, "datatug auth logout"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "firebase-id") || strings.Contains(got, "firebase-refresh") {
		t.Fatalf("whoami exposed a secret: %q", got)
	}
}

func TestWhoami_NotSignedIn(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	issuer, insecure := server.URL, false
	cmd := newCommand(testDependencies(server, &memoryStore{}))
	cmd.AddCommand(whoamiCommand(&issuer, &insecure, testDependencies(server, &memoryStore{})))
	cmd.SetArgs([]string{"whoami"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("error = %v, want not signed in", err)
	}
}

func TestWhoami_ConfigureStorageError(t *testing.T) {
	deps := defaultDependencies()
	deps.newClient = func(string, bool) (*deviceauth.Client, deviceauth.Store, string, error) {
		return nil, nil, "", errors.New("no keyring")
	}
	issuer, insecure := "https://auth.sneat.co", false
	err := whoamiCommand(&issuer, &insecure, deps).Execute()
	if err == nil || !strings.Contains(err.Error(), "configure credential storage") {
		t.Fatalf("error = %v, want configure credential storage", err)
	}
}

func TestWhoami_SessionLoadError(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := &memoryStore{}
	deps := testDependencies(server, store)
	login := newCommand(deps)
	login.SetArgs([]string{"login", "--auth-host", server.URL})
	if err := login.Execute(); err != nil {
		t.Fatalf("login: %v", err)
	}
	store.credential.Expiry = time.Now().Add(-time.Minute)
	deps.refresh = func(context.Context, string) (session, error) { return session{}, errors.New("refresh failed") }
	issuer, insecure := server.URL, false
	err := whoamiCommand(&issuer, &insecure, deps).Execute()
	if err == nil || !strings.Contains(err.Error(), "load Firebase session") {
		t.Fatalf("error = %v, want load Firebase session", err)
	}
}

func TestWhoami_UserInfoError(t *testing.T) {
	var userinfoCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device/code":
			_, _ = io.WriteString(w, `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://verify.example/device","expires_in":300,"interval":1}`)
		case "/oauth/token":
			_, _ = io.WriteString(w, `{"access_token":"custom-token","token_type":"urn:ietf:params:oauth:token-type:firebase-custom-token","expires_in":300}`)
		case "/oauth/userinfo":
			userinfoCalls++
			if userinfoCalls == 1 {
				_, _ = io.WriteString(w, `{"sub":"user-1","aud":"`+clientID+`","scope":"openid profile datatug:projects:read datatug:projects:write"}`)
				return
			}
			_, _ = io.WriteString(w, `{"sub":"","aud":"`+clientID+`"}`)
		case "/oauth/revoke":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	store := &memoryStore{}
	deps := testDependencies(server, store)
	login := newCommand(deps)
	login.SetArgs([]string{"login", "--auth-host", server.URL})
	if err := login.Execute(); err != nil {
		t.Fatalf("login: %v", err)
	}
	issuer, insecure := server.URL, false
	err := whoamiCommand(&issuer, &insecure, deps).Execute()
	if err == nil || !strings.Contains(err.Error(), "validate Firebase session") {
		t.Fatalf("error = %v, want validate Firebase session", err)
	}
}
