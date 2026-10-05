package gauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"golang.org/x/oauth2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type callbackListener struct {
	closed chan struct{}
	once   sync.Once
}

func (l *callbackListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *callbackListener) Close() error              { l.once.Do(func() { close(l.closed) }); return nil }
func (l *callbackListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}
}
func callbackFixture(t *testing.T) (*callbackListener, chan *http.Server) {
	t.Helper()
	listen, serve, shutdown, browser, exchange, testingFn := authListen, authServerServe, srvShutdown, openBrowser, configExchangeFn, isTesting
	t.Cleanup(func() {
		authListen, authServerServe, srvShutdown, openBrowser, configExchangeFn, isTesting = listen, serve, shutdown, browser, exchange, testingFn
	})
	isTesting = func() bool { return false }
	t.Setenv("DATATUG_NO_BROWSER", "")
	l := &callbackListener{closed: make(chan struct{})}
	ready := make(chan *http.Server, 1)
	authListen = func(network, address string) (net.Listener, error) {
		if network != "tcp" || address != "127.0.0.1:8080" {
			t.Fatal(network, address)
		}
		return l, nil
	}
	authServerServe = func(srv *http.Server, _ net.Listener) error { ready <- srv; <-l.closed; return http.ErrServerClosed }
	srvShutdown = func(ctx context.Context, _ *http.Server) error {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > authDrainTimeout {
			t.Error("unbounded shutdown")
		}
		return l.Close()
	}
	return l, ready
}
func invokeCallback(srv *http.Server, method, target, remote, host string) int {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remote
	req.Host = host
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec.Code
}
func TestGoogleCallbackStatePKCEAndSingleDelivery(t *testing.T) {
	l, ready := callbackFixture(t)
	exchanges := 0
	seenState := ""
	challenge := ""
	openBrowser = func(raw string) error {
		srv := <-ready
		select {
		case <-l.closed:
			t.Fatal("listener closed before browser")
		default:
		}
		u, e := url.Parse(raw)
		if e != nil {
			t.Fatal(e)
		}
		seenState = u.Query().Get("state")
		challenge = u.Query().Get("code_challenge")
		if len(seenState) < 40 || seenState == "state-token" || challenge == "" || u.Query().Get("code_challenge_method") != "S256" {
			t.Fatal(u.Query())
		}
		target := "http://localhost:8080/oauth2callback?state=" + url.QueryEscape(seenState) + "&code=fixture-code"
		invalid := []struct{ method, target, remote, host string }{{"GET", strings.Replace(target, seenState, "wrong", 1), "127.0.0.1:40000", "localhost:8080"}, {"POST", target, "127.0.0.1:40000", "localhost:8080"}, {"GET", target, "192.0.2.1:40000", "localhost:8080"}, {"GET", target, "127.0.0.1:40000", "untrusted:8080"}, {"GET", target + "&state=duplicate", "127.0.0.1:40000", "localhost:8080"}, {"GET", target + "&code=duplicate", "127.0.0.1:40000", "localhost:8080"}, {"GET", target + "&error=denied", "127.0.0.1:40000", "localhost:8080"}}
		for _, bad := range invalid {
			if status := invokeCallback(srv, bad.method, bad.target, bad.remote, bad.host); status != 400 {
				t.Fatal(status, bad)
			}
		}
		if status := invokeCallback(srv, "GET", target, "127.0.0.1:40000", "localhost:8080"); status != 200 {
			t.Fatal(status)
		}
		if status := invokeCallback(srv, "GET", target, "127.0.0.1:40000", "localhost:8080"); status != 409 {
			t.Fatal("duplicate accepted", status)
		}
		return nil
	}
	configExchangeFn = func(_ context.Context, config *oauth2.Config, code string, options ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
		exchanges++
		if code != "fixture-code" {
			t.Fatal(code)
		}
		select {
		case <-l.closed:
		default:
			t.Fatal("listener active during exchange")
		}
		raw := config.AuthCodeURL("fixture", options...)
		u, _ := url.Parse(raw)
		verifier := u.Query().Get("code_verifier")
		h := sha256.Sum256([]byte(verifier))
		if verifier == "" || base64.RawURLEncoding.EncodeToString(h[:]) != challenge {
			t.Fatal("PKCE verifier not bound")
		}
		return &oauth2.Token{AccessToken: "fixture"}, nil
	}
	config := &oauth2.Config{RedirectURL: "http://localhost:8080/oauth2callback", Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/auth"}}
	token, e := getTokenFromWeb(context.Background(), config)
	if e != nil || token == nil || exchanges != 1 {
		t.Fatal(e, token, exchanges)
	}
}
func TestGoogleCallbackCancellationBindFailureAndDenial(t *testing.T) {
	for _, name := range []string{"cancel", "bind-failure", "browser-failure", "denial", "exchange-failure", "invalid-redirect"} {
		t.Run(name, func(t *testing.T) {
			l, ready := callbackFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			exchanges, browsers := 0, 0
			config := &oauth2.Config{RedirectURL: "http://localhost:8080/oauth2callback", Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/auth"}}
			if name == "bind-failure" {
				authListen = func(string, string) (net.Listener, error) { return nil, errors.New("private listener failure") }
			}
			if name == "invalid-redirect" {
				config.RedirectURL = "http://0.0.0.0:8080/oauth2callback"
			}
			openBrowser = func(raw string) error {
				browsers++
				srv := <-ready
				u, _ := url.Parse(raw)
				state := u.Query().Get("state")
				if name == "browser-failure" {
					return errors.New("private browser failure")
				}
				if name == "cancel" {
					cancel()
					return nil
				}
				suffix := "&code=fixture"
				if name == "denial" {
					suffix = "&error=access_denied"
				}
				invokeCallback(srv, "GET", "http://localhost:8080/oauth2callback?state="+url.QueryEscape(state)+suffix, "127.0.0.1:40000", "localhost:8080")
				return nil
			}
			configExchangeFn = func(context.Context, *oauth2.Config, string, ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
				exchanges++
				return nil, errors.New("secret upstream response")
			}
			token, e := getTokenFromWeb(ctx, config)
			if e == nil || token != nil || strings.Contains(e.Error(), "private") || strings.Contains(e.Error(), "secret") {
				t.Fatal(e, token)
			}
			if name == "bind-failure" || name == "invalid-redirect" {
				if browsers != 0 || exchanges != 0 {
					t.Fatal(browsers, exchanges)
				}
			} else {
				select {
				case <-l.closed:
				default:
					t.Fatal("listener not closed")
				}
				if name != "exchange-failure" && exchanges != 0 {
					t.Fatal(exchanges)
				}
			}
		})
	}
}
