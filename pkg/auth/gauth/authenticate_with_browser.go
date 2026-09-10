package gauth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"testing"

	"github.com/pkg/browser"
	"golang.org/x/oauth2"
)

// openBrowser is a seam so tests can prove no browser is ever opened.
var openBrowser = browser.OpenURL

// ErrInteractiveLoginUnavailable is returned instead of opening a browser when the
// process is a `go test` binary or DATATUG_NO_BROWSER is set: an interactive Google
// consent page must never pop up on a developer's machine as a side effect of running
// the test suite or a non-interactive job.
var ErrInteractiveLoginUnavailable = errors.New("gauth: interactive Google login is unavailable in this process (go test or DATATUG_NO_BROWSER set); run `datatug auth google` interactively")

// interactiveLoginAllowed reports whether opening a browser for OAuth consent is
// acceptable in this process.
func interactiveLoginAllowed() bool {
	if testing.Testing() {
		return false
	}
	if v := os.Getenv("DATATUG_NO_BROWSER"); v != "" && v != "0" && v != "false" {
		return false
	}
	return true
}

// getTokenFromWeb runs a browser auth flow.
func getTokenFromWeb(ctx context.Context, config *oauth2.Config) (token *oauth2.Token, err error) {
	if !interactiveLoginAllowed() {
		return nil, ErrInteractiveLoginUnavailable
	}
	// Step 1: Get auth URL
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)

	// Step 2: Open browser
	if err = openBrowser(authURL); err != nil {
		return
	}

	// Step 3: Wait for redirect with code
	var authCode string
	if authCode, err = waitForAuthCode(); err != nil {
		log.Printf("Failed to get auth code: %v", err)
	}

	// Step 4: Exchange code for token
	token, err = config.Exchange(ctx, authCode)
	if err != nil {
		log.Fatalf("Token exchange error: %v", err)
	}
	return
}

// Starts HTTP server to capture OAuth redirect
func waitForAuthCode() (authCode string, err error) {
	ch := make(chan string)
	srv := &http.Server{Addr: ":8080"}

	http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		_, _ = fmt.Fprintln(w, "Login successful! You can close this window.")
		go func() {
			ch <- code
			if err := srv.Shutdown(context.Background()); err != nil {
				log.Print("Failed to shutdown:", err)
			}
		}()
	})

	go func() {
		if err = srv.ListenAndServe(); errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return <-ch, err
}
