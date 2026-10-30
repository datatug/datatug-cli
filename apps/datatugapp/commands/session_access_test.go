package commands

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
)

// `datatug session` against a local agent sends the agent's session token.
func TestSessionRequestCarriesTheLocalAgentToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	dir := filepath.Join(t.TempDir(), "daemon")
	t.Setenv(agentaccess.DirEnv, dir)
	secret, _ := agentaccess.NewSecret()
	if err := agentaccess.WriteKey(dir, port, secret); err != nil {
		t.Fatal(err)
	}
	cli := sessionCLI{base: srv.URL, prefix: "/datatug/session-context", query: url.Values{}, client: srv.Client()}
	if _, err := cli.request(t.Context(), http.MethodGet, "/capabilities", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer "+agentaccess.SessionToken(secret) {
		t.Fatalf("Authorization = %q", got)
	}
	// a hosted token is kept
	cli.token = "hosted"
	if _, err := cli.request(t.Context(), http.MethodGet, "/capabilities", nil, nil); err != nil || got != "Bearer hosted" {
		t.Fatalf("%q %v", got, err)
	}
}
