package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
)

func accessServer(t *testing.T) (base string, secret []byte) {
	t.Helper()
	secret, err := agentaccess.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return startServeHTTPWithSessionAndCapabilities(t, nil, unrestrictedTestSession(t), api.Capabilities{Access: agentaccess.New(secret)}), secret
}

func do(t *testing.T, method, url, origin, token, body string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := testHTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

// Every route but a preflight and the exchange needs the token; a code is exchanged once.
func TestServeHTTP_TokenAndOneTimeCode(t *testing.T) {
	base, secret := accessServer(t)
	const origin = "https://datatug.app"

	// a preflight carries no token and is answered by origin only
	resp := optionsWithOrigin(t, base+"/datatug/agent-info", origin, http.MethodGet)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("preflight: %d", resp.StatusCode)
	}

	// any other route refuses a request without the token, and the page may read the refusal
	// what the router answers by itself is behind the token too: no route can be probed for
	for _, path := range []string{"/datatug/nope", "/datatug/session/exchange"} {
		if status, _, _ := do(t, http.MethodGet, base+path, origin, "", ""); status != http.StatusUnauthorized {
			t.Errorf("%s without a token: %d", path, status)
		}
	}
	if status, _, _ := do(t, http.MethodGet, base+"/datatug/nope", origin, agentaccess.SessionToken(secret), ""); status != http.StatusNotFound {
		t.Errorf("an unknown path with the token: %d", status)
	}
	if status, _, _ := do(t, http.MethodGet, base+"/datatug/session/exchange", origin, agentaccess.SessionToken(secret), ""); status != http.StatusMethodNotAllowed {
		t.Errorf("the wrong method with the token: %d", status)
	}
	for _, path := range []string{"/datatug/agent-info", "/datatug/ping", "/", "/datatug/exec/select"} {
		status, body, header := do(t, http.MethodGet, base+path, origin, "", "")
		if status != http.StatusUnauthorized || header.Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("%s without a token: %d %s", path, status, body)
		}
		if status, _, _ = do(t, http.MethodGet, base+path, "", "", ""); status != http.StatusUnauthorized {
			t.Errorf("%s without a token or origin: %d", path, status)
		}
		if status, _, _ = do(t, http.MethodGet, base+path, origin, "wrong", ""); status != http.StatusUnauthorized {
			t.Errorf("%s with a wrong token: %d", path, status)
		}
	}

	code, err := agentaccess.MintCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	exchange := func(code string) (int, string) {
		status, body, header := do(t, http.MethodPost, base+agentaccess.ExchangePath, origin, "", `{"code":"`+code+`"}`)
		if header.Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("the exchange answer is not readable by the page")
		}
		return status, body
	}
	status, body := exchange(code)
	var got struct{ Token string }
	if status != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil || got.Token != agentaccess.SessionToken(secret) {
		t.Fatalf("exchange: %d %s", status, body)
	}
	if status, body = exchange(code); status != http.StatusUnauthorized || !strings.Contains(body, "link-expired") || strings.Contains(body, code) {
		t.Fatalf("second exchange: %d %s", status, body)
	}
	if status, _ = exchange(code[:len(code)-2] + "xx"); status != http.StatusUnauthorized {
		t.Fatalf("tampered: %d", status)
	}
	if status, _, _ = do(t, http.MethodPost, base+agentaccess.ExchangePath, origin, "", "not json"); status != http.StatusBadRequest {
		t.Fatalf("bad body: %d", status)
	}

	// the token opens the routes
	if status, _, _ = do(t, http.MethodGet, base+"/datatug/agent-info", origin, got.Token, ""); status != http.StatusOK {
		t.Fatalf("with the token: %d", status)
	}
	// a refused origin stays refused even with the token
	if status, _, _ = do(t, http.MethodGet, base+"/datatug/agent-info", "http://localhost:4200", got.Token, ""); status != http.StatusForbidden {
		t.Fatalf("old origin with the token: %d", status)
	}
}

func TestServeHTTP_PreflightPrivateNetwork(t *testing.T) {
	base, _ := accessServer(t)
	for origin, want := range map[string]bool{"https://datatug.app": true, "http://datatug.localhost:4200": true, "https://evil.example": false} {
		req, _ := http.NewRequest(http.MethodOptions, base+"/datatug/agent-info", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Access-Control-Request-Private-Network", "true")
		resp, err := testHTTPClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Private-Network") == "true"; got != want {
			t.Errorf("%s: allow-private-network = %v, want %v", origin, got, want)
		}
	}
	// no private-network request header, no private-network answer
	resp := optionsWithOrigin(t, base+"/datatug/agent-info", "https://datatug.app", http.MethodGet)
	_ = resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Private-Network") != "" {
		t.Error("answered a private-network permission nobody asked for")
	}
}
