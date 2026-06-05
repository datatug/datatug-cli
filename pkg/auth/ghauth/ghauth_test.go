package ghauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// fakeTicker implements tickerIface with a writable channel.
type fakeTicker struct {
	ch chan time.Time
}

func newFakeTicker(ticks int) *fakeTicker {
	ft := &fakeTicker{ch: make(chan time.Time, ticks)}
	for i := 0; i < ticks; i++ {
		ft.ch <- time.Now()
	}
	return ft
}

func (f *fakeTicker) C() <-chan time.Time   { return f.ch }
func (f *fakeTicker) Stop()                 {}
func (f *fakeTicker) Reset(_ time.Duration) {}

// withFakeTicker replaces newTicker with one that returns ft and restores it after t.
func withFakeTicker(t *testing.T, ft *fakeTicker) {
	t.Helper()
	orig := newTicker
	newTicker = func(d time.Duration) tickerIface { return ft }
	t.Cleanup(func() { newTicker = orig })
}

// withAccessTokenURL overrides accessTokenURL for the duration of t.
func withAccessTokenURL(t *testing.T, u string) {
	t.Helper()
	orig := accessTokenURL
	accessTokenURL = u
	t.Cleanup(func() { accessTokenURL = orig })
}

// withDeviceCodeURL overrides deviceCodeURL for the duration of t.
func withDeviceCodeURL(t *testing.T, u string) {
	t.Helper()
	orig := deviceCodeURL
	deviceCodeURL = u
	t.Cleanup(func() { deviceCodeURL = orig })
}

// --- SaveToken ---

func TestSaveToken_Success(t *testing.T) {
	keyring.MockInit()
	token := &oauth2.Token{AccessToken: "abc123"}
	if err := SaveToken(token); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveToken_MarshalError(t *testing.T) {
	orig := jsonMarshal
	jsonMarshal = func(v any) ([]byte, error) { return nil, errors.New("marshal fail") }
	defer func() { jsonMarshal = orig }()

	err := SaveToken(&oauth2.Token{AccessToken: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to marshal token") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestSaveToken_KeyringError(t *testing.T) {
	keyring.MockInitWithError(errors.New("keyring unavailable"))
	token := &oauth2.Token{AccessToken: "abc123"}
	err := SaveToken(token)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to save token to keyring") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- GetToken ---

func TestGetToken_Success(t *testing.T) {
	keyring.MockInit()
	tok := &oauth2.Token{AccessToken: "mytoken"}
	data, _ := json.Marshal(tok)
	if err := keyring.Set(keyringService, keyringUser, string(data)); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got, err := GetToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AccessToken != "mytoken" {
		t.Fatalf("expected mytoken, got %s", got.AccessToken)
	}
}

func TestGetToken_KeyringError(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring"))
	_, err := GetToken()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to get token from keyring") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestGetToken_UnmarshalError(t *testing.T) {
	keyring.MockInit()
	if err := keyring.Set(keyringService, keyringUser, "not-valid-json"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := GetToken()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to unmarshal token") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- DeleteToken ---

func TestDeleteToken_Success(t *testing.T) {
	keyring.MockInit()
	tok := &oauth2.Token{AccessToken: "tok"}
	data, _ := json.Marshal(tok)
	_ = keyring.Set(keyringService, keyringUser, string(data))

	if err := DeleteToken(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteToken_KeyringError(t *testing.T) {
	keyring.MockInitWithError(errors.New("delete failed"))
	err := DeleteToken()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to delete token from keyring") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- postJSON ---

func TestPostJSON_Success_WithTarget(t *testing.T) {
	type resp struct{ OK bool }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp{OK: true})
	}))
	defer srv.Close()

	var out resp
	err := postJSON(context.Background(), srv.URL, map[string]string{"k": "v"}, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatal("expected OK=true")
	}
}

func TestPostJSON_Success_NilTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := postJSON(context.Background(), srv.URL, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPostJSON_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := postJSON(context.Background(), srv.URL, map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected status code") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPostJSON_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	var out struct{ X int }
	err := postJSON(context.Background(), srv.URL, map[string]string{}, &out)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to decode response") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPostJSON_MarshalError(t *testing.T) {
	// A channel cannot be JSON-marshaled, triggering the marshal error branch.
	ch := make(chan int)
	err := postJSON(context.Background(), "http://localhost", ch, nil)
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to marshal request body") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPostJSON_RequestBuildError(t *testing.T) {
	// Invalid URL scheme causes http.NewRequestWithContext to fail.
	err := postJSON(context.Background(), "://bad-url", map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to create request") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPostJSON_SendError(t *testing.T) {
	// Unsupported scheme causes client.Do to fail.
	err := postJSON(context.Background(), "ftp://localhost/nope", map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to send request") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- RequestDeviceCode ---

func TestRequestDeviceCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DeviceCodeResponse{
			DeviceCode:      "dc",
			UserCode:        "UC-1234",
			VerificationURI: "https://github.com/activate",
			ExpiresIn:       900,
			Interval:        5,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	withDeviceCodeURL(t, srv.URL)

	got, err := RequestDeviceCode(context.Background(), "client-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DeviceCode != "dc" {
		t.Fatalf("expected dc, got %s", got.DeviceCode)
	}
}

func TestRequestDeviceCode_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	withDeviceCodeURL(t, srv.URL)

	_, err := RequestDeviceCode(context.Background(), "client-id")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- requestToken ---

func TestRequestToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "tok123",
			"token_type":   "bearer",
		})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)

	tok, err := requestToken(context.Background(), "cid", "csecret", "devcode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "tok123" {
		t.Fatalf("expected tok123, got %s", tok.AccessToken)
	}
}

func TestRequestToken_NoClientSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["client_secret"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "noSecret"})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)

	tok, err := requestToken(context.Background(), "cid", "", "devcode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "noSecret" {
		t.Fatalf("expected noSecret, got %s", tok.AccessToken)
	}
}

func TestRequestToken_AuthorizationPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)

	_, err := requestToken(context.Background(), "cid", "csec", "devcode")
	if !errors.Is(err, errAuthorizationPending) {
		t.Fatalf("expected errAuthorizationPending, got %v", err)
	}
}

func TestRequestToken_SlowDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)

	_, err := requestToken(context.Background(), "cid", "csec", "devcode")
	if !errors.Is(err, errSlowDown) {
		t.Fatalf("expected errSlowDown, got %v", err)
	}
}

func TestRequestToken_OtherGitHubError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "expired_token",
			"error_description": "The device code has expired",
		})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)

	_, err := requestToken(context.Background(), "cid", "csec", "devcode")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "github error") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRequestToken_PostJSONError(t *testing.T) {
	withAccessTokenURL(t, "://invalid")

	_, err := requestToken(context.Background(), "cid", "csec", "devcode")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- PollForToken ---

func TestPollForToken_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled immediately

	// Ticker that never ticks; ctx.Done fires first.
	ft := &fakeTicker{ch: make(chan time.Time)} // unbuffered, never sends
	withFakeTicker(t, ft)

	_, err := PollForToken(ctx, "cid", "csec", "devcode", 1, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPollForToken_Success_WithOnAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "polled-token"})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)
	withFakeTicker(t, newFakeTicker(1))

	attempts := 0
	tok, err := PollForToken(context.Background(), "cid", "csec", "devcode", 1, func(a int) {
		attempts = a
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "polled-token" {
		t.Fatalf("expected polled-token, got %s", tok.AccessToken)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestPollForToken_AuthorizationPending_ThenSuccess(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "final-token"})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)
	withFakeTicker(t, newFakeTicker(2))

	tok, err := PollForToken(context.Background(), "cid", "csec", "devcode", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "final-token" {
		t.Fatalf("expected final-token, got %s", tok.AccessToken)
	}
}

func TestPollForToken_SlowDown_ThenSuccess(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "slow-token"})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)
	withFakeTicker(t, newFakeTicker(2))

	tok, err := PollForToken(context.Background(), "cid", "csec", "devcode", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "slow-token" {
		t.Fatalf("expected slow-token, got %s", tok.AccessToken)
	}
}

func TestPollForToken_HardError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "access_denied",
			"error_description": "The user denied access",
		})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)
	withFakeTicker(t, newFakeTicker(1))

	_, err := PollForToken(context.Background(), "cid", "csec", "devcode", 1, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPollForToken_DefaultInterval(t *testing.T) {
	// interval=0 should default to 5s; verify the ticker is constructed with 5s.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := time.Duration(0)
	origTicker := newTicker
	newTicker = func(d time.Duration) tickerIface {
		got = d
		return &fakeTicker{ch: make(chan time.Time)} // never ticks
	}
	t.Cleanup(func() { newTicker = origTicker })

	_, err := PollForToken(ctx, "cid", "csec", "devcode", 0, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Canceled, got %v", err)
	}
	if got != 5*time.Second {
		t.Fatalf("expected 5s interval, got %v", got)
	}
}

func TestPollForToken_NilOnAttempt(t *testing.T) {
	// Verify nil onAttempt doesn't panic.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "t"})
	}))
	defer srv.Close()
	withAccessTokenURL(t, srv.URL)
	withFakeTicker(t, newFakeTicker(1))

	tok, err := PollForToken(context.Background(), "cid", "csec", "devcode", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "t" {
		t.Fatalf("expected t, got %s", tok.AccessToken)
	}
}

// TestRealTicker exercises the realTicker seam wrapper methods (C, Stop, Reset)
// by using the default newTicker (not overridden) with an already-cancelled context.
func TestRealTicker_SeamMethods(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Use a real server but ctx is already done, so PollForToken returns immediately
	// after the ticker is created – which exercises newTicker default, realTicker.C(),
	// and ticker.Stop() via defer.
	_, err := PollForToken(ctx, "cid", "csec", "devcode", 1, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Canceled, got %v", err)
	}
}

// TestRealTicker_Methods directly exercises the realTicker wrapper (C, Stop, Reset).
func TestRealTicker_Methods(t *testing.T) {
	rt := &realTicker{t: time.NewTicker(time.Hour)}
	ch := rt.C()
	if ch == nil {
		t.Fatal("expected non-nil channel from C()")
	}
	rt.Reset(time.Hour) // just verify no panic
	rt.Stop()           // clean up
}

// Ensure unused import of fmt is used.
var _ = fmt.Sprintf
