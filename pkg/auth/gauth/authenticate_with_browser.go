package gauth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/pkg/browser"
	"golang.org/x/oauth2"
)

// openBrowser is a seam so tests can prove no browser is ever opened.
var openBrowser = browser.OpenURL

var isTesting = testing.Testing

var (
	waitForAuthCodeFn = waitForAuthCode
	configExchangeFn  = func(ctx context.Context, config *oauth2.Config, code string) (*oauth2.Token, error) {
		return config.Exchange(ctx, code)
	}
	authServerServe = func(srv *http.Server) error {
		return srv.ListenAndServe()
	}
	srvShutdown = func(ctx context.Context, srv *http.Server) error {
		return srv.Shutdown(ctx)
	}
)

// ErrInteractiveLoginUnavailable is returned instead of opening a browser when the
// process is a `go test` binary or DATATUG_NO_BROWSER is set: an interactive Google
// consent page must never pop up on a developer's machine as a side effect of running
// the test suite or a non-interactive job.
var ErrInteractiveLoginUnavailable = errors.New("gauth: interactive Google login is unavailable in this process (go test or DATATUG_NO_BROWSER set); run `datatug auth google` interactively")

// interactiveLoginAllowed reports whether opening a browser for OAuth consent is
// acceptable in this process.
func interactiveLoginAllowed() bool {
	if isTesting() {
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
	if authCode, err = waitForAuthCodeFn(); err != nil {
		log.Printf("Failed to get auth code: %v", err)
	}

	// Step 4: Exchange code for token
	token, err = configExchangeFn(ctx, config, authCode)
	if err != nil {
		return nil, fmt.Errorf("token exchange error: %w", err)
	}
	return
}

var errAuthServerStopped = errors.New("gauth: local auth server stopped before receiving the OAuth redirect")

// serveDrainTimeout bounds how long waitForAuthCode waits, after the code has
// arrived, for the HTTP server goroutine to report how it ended. Shutdown makes
// the server return at once, so the bound only matters if Shutdown failed.
var serveDrainTimeout = 2 * time.Second

// Starts HTTP server to capture OAuth redirect
func waitForAuthCode() (authCode string, err error) {
	ch := make(chan string, 1)
	// serveDone receives the server goroutine's outcome exactly once; the
	// goroutine never touches waitForAuthCode's named results.
	serveDone := make(chan error, 1)
	// Read the seams once, on the caller's goroutine: the goroutines below can
	// outlive this call.
	serve, shutdown := authServerServe, srvShutdown
	mux := http.NewServeMux()
	srv := &http.Server{Addr: ":8080", Handler: mux}

	mux.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		_, _ = fmt.Fprintln(w, "Login successful! You can close this window.")
		go func() {
			ch <- code
			if err := shutdown(context.Background(), srv); err != nil {
				log.Print("Failed to shutdown:", err)
			}
		}()
	})

	go func() {
		serveErr := serve(srv)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		} else if serveErr != nil {
			log.Printf("HTTP server error: %v", serveErr)
		}
		serveDone <- serveErr
	}()

	select {
	case authCode = <-ch:
		// Shutdown makes the server return, so it reports here promptly.
		select {
		case err = <-serveDone:
		case <-time.After(serveDrainTimeout):
		}
	case err = <-serveDone:
		// The server ended before any redirect arrived (for example the port
		// is busy); do not wait for a code that can no longer come.
		if err == nil {
			err = errAuthServerStopped
		}
	}
	return authCode, err
}
