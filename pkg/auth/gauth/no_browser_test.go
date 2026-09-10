package gauth

import (
	"context"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// TestGetTokenFromWeb_NeverOpensBrowserUnderGoTest is the regression test for the
// 2026-09-10 incident where running the suite opened a real Google consent page
// (redirect http://localhost:8080/oauth2callback) on the developer's machine because
// the keychain held no refresh token. Under `go test` the flow must fail fast, before
// any browser or local listener is touched.
func TestGetTokenFromWeb_NeverOpensBrowserUnderGoTest(t *testing.T) {
	orig := openBrowser
	openBrowser = func(url string) error {
		t.Fatalf("browser.OpenURL must not be called under go test (url=%q)", url)
		return nil
	}
	t.Cleanup(func() { openBrowser = orig })

	tok, err := getTokenFromWeb(context.Background(), &oauth2.Config{})
	if !errors.Is(err, ErrInteractiveLoginUnavailable) {
		t.Fatalf("expected ErrInteractiveLoginUnavailable, got tok=%v err=%v", tok, err)
	}
}

// TestGetGoogleCloudClient_NoTokenNoBrowser proves the production entry point
// (getGoogleCloudClient → getTokenFromWebFn) also cannot reach a browser under test
// when the keychain is empty.
func TestGetGoogleCloudClient_NoTokenNoBrowser(t *testing.T) {
	keyring.MockInit()
	orig := openBrowser
	openBrowser = func(url string) error {
		t.Fatalf("browser.OpenURL must not be called under go test (url=%q)", url)
		return nil
	}
	t.Cleanup(func() { openBrowser = orig })

	if _, err := getGoogleCloudClient(context.Background()); !errors.Is(err, ErrInteractiveLoginUnavailable) {
		t.Fatalf("expected ErrInteractiveLoginUnavailable, got %v", err)
	}
}

func TestInteractiveLoginAllowed_EnvGate(t *testing.T) {
	// Under go test the gate is always closed regardless of the env var.
	t.Setenv("DATATUG_NO_BROWSER", "")
	if interactiveLoginAllowed() {
		t.Fatal("interactive login must be disallowed under go test")
	}
}
