package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// The app reads GET /datatug/environments/state from the local agent (datatug-apps
// ProjectEnvironmentStateService). Through the real served router the route keeps the agent's
// guarantees: a listed origin with the session token gets the environment-state/1 contract with
// CORS headers, no token is a 401, and an origin that is not listed is refused without one.
func TestServeHTTP_EnvironmentStateRoute(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`{"id":"p","environmentOrder":["prod"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"dev", "prod"} {
		dir := filepath.Join(root, "environments", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".env.json"), []byte(`{"id":"`+id+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secret, err := agentaccess.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	base := startServeHTTPWithSessionAndCapabilities(t, map[string]string{"p": root}, session, api.Capabilities{Access: agentaccess.New(secret)})
	token := agentaccess.SessionToken(secret)
	url := base + "/datatug/environments/state?storage=local&project=p"

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://datatug.app")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := testHTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://datatug.app" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	var state struct {
		Contract     string   `json:"contract"`
		Order        []string `json:"order"`
		Capabilities struct {
			CanReadOrder  bool `json:"canReadOrder"`
			CanWriteOrder bool `json:"canWriteOrder"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if state.Contract != "environment-state/1" || len(state.Order) != 2 || state.Order[0] != "prod" || state.Order[1] != "dev" {
		t.Errorf("state = %s", body)
	}
	if !state.Capabilities.CanReadOrder || state.Capabilities.CanWriteOrder {
		t.Errorf("capabilities without --allow-writes = %s", body)
	}
	for _, name := range []string{".datatug-environments.lock", ".datatug-environments-owner.json"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("a read created %s: %v", name, err)
		}
	}

	if resp := corsRequest(t, http.MethodGet, url, "https://datatug.app", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", resp.StatusCode)
	}
	if resp := corsRequest(t, http.MethodGet, url, "https://evil.example", token, ""); resp.StatusCode != http.StatusForbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("unlisted origin: status = %d ACAO = %q, want 403 and none", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
}
