package bigqueryread

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/internal/hermetictest"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func TestMain(m *testing.M) { os.Exit(hermetictest.Main(m)) }

type providerRT func(*http.Request) (*http.Response, error)

func (f providerRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func tokenWithScopes(token, scope string, expiry time.Time) *oauth2.Token {
	return (&oauth2.Token{AccessToken: token, TokenType: "Bearer", Expiry: expiry}).WithExtra(map[string]any{"scope": scope})
}
func TestGoogleVerifiedIdentityGrantAndGeneration(t *testing.T) {
	now := time.Now()
	token := "first-token-fixture"
	scope := readScope + " openid"
	sub := "stable-sub"
	calls := 0
	p := &GoogleProvider{nonce: "private-session", now: func() time.Time { return now }, tokenSource: func(ctx context.Context, scopes []string) (oauth2.TokenSource, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if strings.Join(scopes, " ") != readScope+" openid" && strings.Join(scopes, " ") != fullScope+" openid" {
			t.Fatal(scopes)
		}
		return oauth2.StaticTokenSource(tokenWithScopes(token, scope, now.Add(time.Hour))), nil
	}, transport: providerRT(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://openidconnect.googleapis.com/v1/userinfo" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal(r.URL, r.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"sub":"` + sub + `"}`)), Header: make(http.Header)}, nil
	})}
	guarded := providerRT(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("not SAME token")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	first, transport, e := p.Authorize(context.Background(), guarded)
	if e != nil || first.Principal.Subject != sub || !first.Read || first.Cancel {
		t.Fatal(first, e)
	}
	req, _ := http.NewRequest("GET", "https://bigquery.googleapis.com/bigquery/v2/projects", nil)
	if _, e = transport.RoundTrip(req); e != nil {
		t.Fatal(e)
	}
	token = "refreshed-token-fixture"
	same, _, e := p.Authorize(context.Background(), guarded)
	if e != nil || same.Principal.Generation != first.Principal.Generation {
		t.Fatal("same verified grant refresh lost session", same, e)
	}
	sub = "different-sub"
	changed, _, e := p.Authorize(context.Background(), guarded)
	if e != nil || changed.Principal.Generation == first.Principal.Generation {
		t.Fatal(changed, e)
	}
	sub = "stable-sub"
	scope += " https://www.googleapis.com/auth/userinfo.email"
	changed, _, e = p.Authorize(context.Background(), guarded)
	if e != nil || changed.Principal.Generation == first.Principal.Generation {
		t.Fatal("grant change preserved approval", changed, e)
	}
	scope = fullScope + " openid"
	p.cancel = true
	changed, _, e = p.Authorize(context.Background(), guarded)
	if e != nil || !changed.Cancel {
		t.Fatal(changed, e)
	}
	scope = "openid"
	before := calls
	_, _, e = p.Authorize(context.Background(), guarded)
	if !errors.Is(e, ErrIdentity) || calls != before {
		t.Fatal("scope manufactured", e, calls, before)
	}
}
func TestGoogleProviderRefusesMissingExpiredAndMalformedIdentity(t *testing.T) {
	now := time.Now()
	for _, name := range []string{"missing-scope", "expired", "missing-openid", "broad-cloud-only", "missing-sub", "duplicate-sub", "redirect", "overflow", "revoked"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			token := tokenWithScopes("fixture", readScope+" openid", now.Add(time.Hour))
			body := `{"sub":"stable"}`
			status := 200
			switch name {
			case "missing-scope":
				token = token.WithExtra(map[string]any{})
			case "expired":
				token.Expiry = now.Add(-time.Second)
			case "missing-openid":
				token = token.WithExtra(map[string]any{"scope": readScope})
			case "broad-cloud-only":
				token = token.WithExtra(map[string]any{"scope": "openid https://www.googleapis.com/auth/cloud-platform"})
			case "missing-sub":
				body = `{"email":"label"}`
			case "duplicate-sub":
				body = `{"sub":"x","sub":"y"}`
			case "redirect":
				status = 302
			case "overflow":
				body = strings.Repeat(" ", 64<<10+1)
			case "revoked":
				status = 401
			}
			p := &GoogleProvider{nonce: "session", now: func() time.Time { return now }, tokenSource: func(context.Context, []string) (oauth2.TokenSource, error) {
				return oauth2.StaticTokenSource(token), nil
			}, transport: providerRT(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://untrusted.example/"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			_, _, e := p.Authorize(context.Background(), p.transport)
			if !errors.Is(e, ErrIdentity) || calls > 1 {
				t.Fatal(e, calls)
			}
		})
	}
}
func TestADCExplicitUserCredentialSelection(t *testing.T) {
	now := time.Now()
	saved := findADC
	t.Cleanup(func() { findADC = saved })
	for _, kind := range []string{"authorized_user", "service_account", "external_account"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session")
			if _, e := bigquery.NewFileLedger(dir); e != nil {
				t.Fatal(e)
			}
			calls := 0
			findADC = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
				calls++
				return &google.Credentials{JSON: []byte(`{"type":"` + kind + `"}`), TokenSource: oauth2.StaticTokenSource(tokenWithScopes("fixture", readScope+" openid", now.Add(time.Hour)))}, nil
			}
			p, e := NewADCProvider(dir, false)
			if e != nil || calls != 0 {
				t.Fatal("constructor discovered credentials", e, calls)
			}
			p.transport = providerRT(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"sub":"verified"}`))}, nil
			})
			_, _, e = p.Authorize(context.Background(), p.transport)
			if calls != 1 || (kind == "authorized_user" && e != nil) || (kind != "authorized_user" && !errors.Is(e, ErrIdentity)) {
				t.Fatal(kind, e, calls)
			}
		})
	}
}
func TestPrivateAuthorizationSessionNonce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	if _, e := bigquery.NewFileLedger(dir); e != nil {
		t.Fatal(e)
	}
	one, e := NewGoogleProvider(dir, false)
	if e != nil {
		t.Fatal(e)
	}
	two, e := NewGoogleProvider(dir, false)
	if e != nil || one.nonce != two.nonce {
		t.Fatal(e)
	}
	if runtime.GOOS == "windows" {
		return
	}
	path := filepath.Join(dir, "authorization-session")
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = NewGoogleProvider(dir, false); e == nil {
		t.Fatal("public nonce accepted")
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(dir, "elsewhere"), path); e != nil {
		t.Fatal(e)
	}
	if _, e = NewGoogleProvider(dir, false); e == nil {
		t.Fatal("symlink accepted")
	}
}

func TestGoogleTransportUsesFrozenVerifiedToken(t *testing.T) {
	now := time.Now()
	supplied := tokenWithScopes("verified-token", readScope+" openid", now.Add(time.Hour))
	sourceCalls := 0
	p := &GoogleProvider{nonce: "session", now: func() time.Time { return now }, tokenSource: func(context.Context, []string) (oauth2.TokenSource, error) {
		sourceCalls++
		return oauth2.StaticTokenSource(supplied), nil
	}, transport: providerRT(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer verified-token" {
			t.Fatal("UserInfo used another token")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"sub":"verified-sub"}`))}, nil
	})}
	guarded := providerRT(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer verified-token" {
			t.Fatal("dispatch used unverified refreshed token")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	_, transport, err := p.Authorize(context.Background(), guarded)
	if err != nil {
		t.Fatal(err)
	}
	supplied.AccessToken = "unverified-next-token"
	supplied.Expiry = now.Add(-time.Second)
	req, _ := http.NewRequest("GET", "https://bigquery.googleapis.com/bigquery/v2/projects", nil)
	if _, err = transport.RoundTrip(req); err != nil || sourceCalls != 1 {
		t.Fatal(err, sourceCalls)
	}
}
