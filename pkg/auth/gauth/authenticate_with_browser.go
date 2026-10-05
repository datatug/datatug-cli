package gauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/pkg/browser"
	"golang.org/x/oauth2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var openBrowser = browser.OpenURL
var isTesting = testing.Testing
var authListen = net.Listen
var authServerServe = func(srv *http.Server, listener net.Listener) error { return srv.Serve(listener) }
var srvShutdown = func(ctx context.Context, srv *http.Server) error { return srv.Shutdown(ctx) }
var configExchangeFn = func(ctx context.Context, config *oauth2.Config, code string, options ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	return config.Exchange(ctx, code, options...)
}
var ErrInteractiveLoginUnavailable = errors.New("gauth: interactive Google login is unavailable in this process (go test or DATATUG_NO_BROWSER set)")
var errAuthServerStopped = errors.New("gauth: local auth server stopped before receiving the OAuth redirect")
var errAuthFlow = errors.New("gauth: Google consent failed; explicitly reconnect when ready")

const authFlowTimeout = 5 * time.Minute
const authDrainTimeout = 2 * time.Second

func interactiveLoginAllowed() bool {
	if isTesting() {
		return false
	}
	if v := os.Getenv("DATATUG_NO_BROWSER"); v != "" && v != "0" && v != "false" {
		return false
	}
	return true
}

type authResponse struct {
	code string
	err  error
}

// getTokenFromWeb owns one finite, loopback-only consent flow. Bind before the
// browser opens, bind state/PKCE to this flow, and stop its callback before token
// exchange. No callback/code/token URL or upstream error is logged.
func getTokenFromWeb(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	if !interactiveLoginAllowed() {
		return nil, ErrInteractiveLoginUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, authFlowTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, errAuthFlow
	}
	redirect, err := url.Parse(config.RedirectURL)
	if err != nil || redirect.Scheme != "http" || (redirect.Hostname() != "localhost" && redirect.Hostname() != "127.0.0.1") || redirect.Port() == "" || redirect.Path != "/oauth2callback" || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.User != nil {
		return nil, errAuthFlow
	}
	seed := make([]byte, 32)
	if _, err = rand.Read(seed); err != nil {
		return nil, errAuthFlow
	}
	state := base64.RawURLEncoding.EncodeToString(seed)
	verifier := oauth2.GenerateVerifier()
	// Listen only on IPv4 loopback; a busy port is an error, never another owner
	// to kill or reuse. The existing application's redirect URL stays exact.
	listener, err := authListen("tcp", net.JoinHostPort("127.0.0.1", redirect.Port()))
	if err != nil {
		return nil, errAuthFlow
	}
	result := make(chan authResponse, 1)
	done := make(chan error, 1)
	var accepted atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if e != nil || ip == nil || !ip.IsLoopback() || r.Method != http.MethodGet || r.URL.Path != redirect.Path || (r.Host != redirect.Host && r.Host != listener.Addr().String()) || len(r.URL.RawQuery) > 8192 {
			http.Error(w, "Invalid OAuth callback", http.StatusBadRequest)
			return
		}
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || len(q["code"]) > 1 || len(q["error"]) > 1 {
			http.Error(w, "Invalid OAuth callback", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		denial := q.Get("error")
		if (code == "" && denial == "") || (code != "" && denial != "") || len(code) > 4096 || strings.TrimSpace(code) != code {
			http.Error(w, "Invalid OAuth callback", http.StatusBadRequest)
			return
		}
		if !accepted.CompareAndSwap(false, true) {
			http.Error(w, "OAuth callback already consumed", http.StatusConflict)
			return
		}
		response := authResponse{code: code}
		if denial != "" {
			response.err = errAuthFlow
		}
		result <- response
		_, _ = fmt.Fprintln(w, "Consent received. You can close this window.")
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	serve, shutdown := authServerServe, srvShutdown
	go func() { done <- serve(srv, listener) }()
	closeFlow := func() {
		finish, stop := context.WithTimeout(context.Background(), authDrainTimeout)
		_ = shutdown(finish, srv)
		stop()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(authDrainTimeout):
		}
	}
	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier))
	if err = openBrowser(authURL); err != nil {
		closeFlow()
		return nil, errAuthFlow
	}
	var response authResponse
	select {
	case response = <-result:
	case <-done:
		_ = listener.Close()
		return nil, errAuthServerStopped
	case <-ctx.Done():
		closeFlow()
		return nil, errAuthFlow
	}
	closeFlow()
	if response.err != nil || ctx.Err() != nil {
		return nil, errAuthFlow
	}
	token, err := configExchangeFn(ctx, config, response.code, oauth2.VerifierOption(verifier))
	if err != nil || token == nil {
		return nil, errAuthFlow
	}
	return token, nil
}
