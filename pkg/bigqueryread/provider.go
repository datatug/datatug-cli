package bigqueryread

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const readScope = "https://www.googleapis.com/auth/bigquery.readonly"
const fullScope = "https://www.googleapis.com/auth/bigquery"

var findADC = google.FindDefaultCredentials

var ErrIdentity = errors.New("google execution identity or granted scopes unavailable; explicitly sign in with Google BigQuery read-only and openid scopes, then preview again")

// GoogleProvider uses the existing explicit Google login's refresh-token source.
// It never opens a browser or uses ADC. Granted scope comes only from the Google
// token response, and the SAME token verifies sub at fixed HTTPS UserInfo.
type GoogleProvider struct {
	mu          sync.Mutex
	nonce       string
	cancel      bool
	tokenSource func(context.Context, []string) (oauth2.TokenSource, error)
	transport   http.RoundTripper
	now         func() time.Time
}

// NewGoogleProvider must follow NewFileLedger admission of dir's private path.
// The nonce is nonsecret and contains no token, email, policy label or credential.
func NewGoogleProvider(dir string, enableCancel bool) (*GoogleProvider, error) {
	if _, err := bigquery.NewFileLedger(dir); err != nil {
		return nil, ErrIdentity
	}
	nonce, err := sessionNonce(dir)
	if err != nil {
		return nil, err
	}
	return &GoogleProvider{nonce: nonce, cancel: enableCancel, tokenSource: gauth.TokenSource, transport: http.DefaultTransport, now: time.Now}, nil
}

// NewADCProvider reads user-controlled ADC only after explicit --auth adc.
// Scope options request grants but cannot attest them. Workload ADC is refused;
// its authoritative subject requires a separate trusted operator provider.
func NewADCProvider(dir string, enableCancel bool) (*GoogleProvider, error) {
	p, err := NewGoogleProvider(dir, enableCancel)
	if err != nil {
		return nil, err
	}
	p.tokenSource = func(ctx context.Context, scopes []string) (oauth2.TokenSource, error) {
		credentials, err := findADC(ctx, scopes...)
		if err != nil {
			return nil, ErrIdentity
		}
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(credentials.JSON, &kind) != nil || kind.Type != "authorized_user" {
			return nil, ErrIdentity
		}
		return credentials.TokenSource, nil
	}
	return p, nil
}
func sessionNonce(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", ErrIdentity
	}
	name := filepath.Join(dir, "authorization-session")
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return "", ErrIdentity
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, err = f.Write([]byte(hex.EncodeToString(seed)))
		if err == nil {
			err = f.Sync()
		}
		ce := f.Close()
		if err != nil || ce != nil {
			return "", ErrIdentity
		}
	} else if !os.IsExist(err) {
		return "", ErrIdentity
	}
	before, err := os.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || (runtime.GOOS != "windows" && before.Mode().Perm()&0077 != 0) {
		return "", ErrIdentity
	}
	f, err = os.Open(name)
	if err != nil {
		return "", ErrIdentity
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return "", ErrIdentity
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil || len(raw) != 64 {
		return "", ErrIdentity
	}
	if _, err = hex.DecodeString(string(raw)); err != nil {
		return "", ErrIdentity
	}
	return string(raw), nil
}
func (p *GoogleProvider) Authorize(ctx context.Context, guarded http.RoundTripper) (bigquery.Identity, http.RoundTripper, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	scopesRequested := []string{readScope, "openid"}
	if p.cancel {
		scopesRequested[0] = fullScope
	}
	// A source's refresh HTTP context belongs to this operation, never a prior
	// invocation's expired setup context.
	source, err := p.tokenSource(ctx, scopesRequested)
	if err != nil {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	token, err := source.Token()
	if err != nil {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	return p.authorizeToken(ctx, token, guarded)
}

// AuthorizeToken attests the exact token returned by an explicit Google consent
// exchange. It is used to compare that consent with the credential store before
// connect reports success; it never stores the access token or opens consent.
func (p *GoogleProvider) AuthorizeToken(ctx context.Context, token *oauth2.Token, guarded http.RoundTripper) (bigquery.Identity, http.RoundTripper, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authorizeToken(ctx, token, guarded)
}
func (p *GoogleProvider) authorizeToken(ctx context.Context, token *oauth2.Token, guarded http.RoundTripper) (bigquery.Identity, http.RoundTripper, error) {
	if token == nil || token.AccessToken == "" || token.Expiry.IsZero() || !p.now().Before(token.Expiry) || !strings.EqualFold(token.Type(), "Bearer") {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	// Freeze the token before UserInfo and transport construction.
	snapshot := *token
	token = &snapshot
	// A requested scope or stored login configuration is NEVER grant evidence.
	granted, ok := token.Extra("scope").(string)
	if !ok || granted == "" || len(granted) > 64<<10 {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	scopes := strings.Fields(granted)
	sort.Strings(scopes)
	scopes = compact(scopes)
	has := func(s string) bool {
		for _, x := range scopes {
			if x == s {
				return true
			}
		}
		return false
	}
	read := has(readScope) || has(fullScope)
	canCancel := p.cancel && has(fullScope)
	if !read || !has("openid") {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	client := &http.Client{Transport: p.transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrIdentity }}
	res, err := client.Do(req)
	if err != nil {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	v, err := bigquery.ParseJSON(raw, 64<<10)
	if err != nil {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	m, ok := v.(map[string]any)
	if !ok {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	sub, ok := m["sub"].(string)
	if !ok || sub == "" || len(sub) > 4096 || strings.TrimSpace(sub) != sub {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	// Go authorization-session generation survives token refresh only when the
	// independently verified stable identity and exact current grants agree.
	binding, _ := json.Marshal([]any{"datatug-go-authorization-session-v1", p.nonce, "google-user", sub, scopes})
	generation := sha256.Sum256(binding)
	identity := bigquery.Identity{Principal: bigquery.Principal{Kind: "google-user", Subject: sub, Generation: hex.EncodeToString(generation[:])}, ExpiresAt: token.Expiry, Read: read, Cancel: canCancel}
	if !p.now().Before(token.Expiry) {
		return bigquery.Identity{}, nil, ErrIdentity
	}
	// Static SAME token over the supplied guard; middleware cannot refresh/replay.
	return identity, &oauth2.Transport{Source: oauth2.StaticTokenSource(token), Base: guarded}, nil
}
func compact(values []string) []string {
	out := values[:0]
	for _, x := range values {
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	return out
}
