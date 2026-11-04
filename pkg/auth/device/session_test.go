package device

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/strongo/deviceauth"
)

func idTokenWithEmail(email string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return "h." + payload + ".s"
}

func TestEnsureSignedIn_SavedLoginIsUsedWithoutDeviceFlow(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := &memoryStore{credential: deviceauth.Credential{AccessToken: idTokenWithEmail("ann@example.com"), RefreshToken: "r", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour), Issuer: server.URL, ClientID: clientID, Scopes: scopes}}
	var out bytes.Buffer
	s, err := ensureSignedIn(context.Background(), SignInOptions{Issuer: server.URL, Interactive: true, Out: &out, ErrOut: &out}, testDependencies(server, store))
	if err != nil {
		t.Fatalf("ensureSignedIn: %v", err)
	}
	if s.Email != "ann@example.com" {
		t.Fatalf("email = %q", s.Email)
	}
	if out.Len() != 0 {
		t.Fatalf("device flow ran with a saved login: %q", out.String())
	}
	token, err := s.Token(context.Background())
	if err != nil || token != store.credential.AccessToken {
		t.Fatalf("token = %q, %v", token, err)
	}
}

func TestEnsureSignedIn_MissingLoginWithoutInteraction(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	_, err := ensureSignedIn(context.Background(), SignInOptions{Issuer: server.URL}, testDependencies(server, &memoryStore{}))
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
}

func TestEnsureSignedIn_MissingLoginRunsDeviceFlowAndStores(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := &memoryStore{}
	var out, errOut bytes.Buffer
	s, err := ensureSignedIn(context.Background(), SignInOptions{Issuer: server.URL, Insecure: true, Interactive: true, Out: &out, ErrOut: &errOut}, testDependencies(server, store))
	if err != nil {
		t.Fatalf("ensureSignedIn: %v", err)
	}
	if store.credential.AccessToken != "firebase-id" {
		t.Fatalf("login not stored: %+v", store.credential)
	}
	if !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Fatalf("device code not printed: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "--insecure-storage") {
		t.Fatalf("insecure warning missing: %q", errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "firebase-id") {
		t.Fatal("output carries the token")
	}
	if s.Email != "" {
		t.Fatalf("email of a non-JWT token = %q", s.Email)
	}
}

func TestEnsureSignedIn_Errors(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	deps := testDependencies(server, &memoryStore{})
	deps.newClient = func(string, bool) (*deviceauth.Client, deviceauth.Store, string, error) {
		return nil, nil, "", errors.New("no keyring")
	}
	if _, err := ensureSignedIn(context.Background(), SignInOptions{Issuer: server.URL}, deps); err == nil || !strings.Contains(err.Error(), "configure credential storage") {
		t.Fatalf("err = %v", err)
	}

	store := &memoryStore{credential: deviceauth.Credential{AccessToken: "old", RefreshToken: "r", Expiry: time.Now().Add(-time.Hour), Issuer: server.URL, ClientID: clientID, Scopes: scopes}}
	deps = testDependencies(server, store)
	deps.refresh = func(context.Context, string) (session, error) { return session{}, errors.New("revoked") }
	if _, err := ensureSignedIn(context.Background(), SignInOptions{Issuer: server.URL}, deps); err == nil || !strings.Contains(err.Error(), "load DataTug login") {
		t.Fatalf("err = %v", err)
	}

	deps = testDependencies(server, &memoryStore{})
	deps.exchange = func(context.Context, string) (session, error) { return session{}, errors.New("exchange down") }
	if _, err := ensureSignedIn(context.Background(), SignInOptions{Issuer: server.URL, Interactive: true, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}}, deps); err == nil {
		t.Fatal("failed device login was accepted")
	}
}

func TestEnsureSignedIn_TokenRefreshFailure(t *testing.T) {
	server := authServer(t, clientID)
	defer server.Close()
	store := &memoryStore{credential: deviceauth.Credential{AccessToken: "t", RefreshToken: "r", Expiry: time.Now().Add(time.Hour), Issuer: server.URL, ClientID: clientID, Scopes: scopes}}
	deps := testDependencies(server, store)
	deps.refresh = func(context.Context, string) (session, error) { return session{}, errors.New("revoked") }
	s, err := ensureSignedIn(context.Background(), SignInOptions{Issuer: server.URL}, deps)
	if err != nil {
		t.Fatalf("ensureSignedIn: %v", err)
	}
	store.credential.Expiry = time.Now().Add(-time.Hour)
	if _, err = s.Token(context.Background()); err == nil {
		t.Fatal("refused refresh returned a token")
	}
}

func TestEnsureSignedIn_IssuerFromEnvironmentAndDefault(t *testing.T) {
	var got []string
	deps := dependencies{newClient: func(issuer string, _ bool) (*deviceauth.Client, deviceauth.Store, string, error) {
		got = append(got, issuer)
		return nil, nil, "", errors.New("stop")
	}}
	t.Setenv("DATATUG_AUTH_HOST", "http://127.0.0.1:1")
	_, _ = ensureSignedIn(context.Background(), SignInOptions{}, deps)
	t.Setenv("DATATUG_AUTH_HOST", "")
	_, _ = ensureSignedIn(context.Background(), SignInOptions{}, deps)
	if len(got) != 2 || got[0] != "http://127.0.0.1:1" || got[1] != defaultIssuer {
		t.Fatalf("issuers = %v", got)
	}
}

func TestEnsureSignedIn_PublicEntryUsesDefaults(t *testing.T) {
	_, err := EnsureSignedIn(context.Background(), SignInOptions{Issuer: "ftp://not-an-issuer"})
	if err == nil {
		t.Fatal("an invalid issuer was accepted")
	}
}

func TestEmailClaim(t *testing.T) {
	for token, want := range map[string]string{
		idTokenWithEmail("a@b.c"): "a@b.c",
		"not-a-jwt":               "",
		"h.%%%.s":                 "",
		"h." + base64.RawURLEncoding.EncodeToString([]byte("[]")) + ".s": "",
	} {
		if got := emailClaim(token); got != want {
			t.Errorf("emailClaim(%q) = %q, want %q", token, got, want)
		}
	}
}
