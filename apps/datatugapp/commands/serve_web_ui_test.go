package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server"
	"github.com/datatug/datatug-cli/pkg/webui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveWebUIRun runs serve with flags, returning its output, error and the server it started.
func serveWebUIRun(t *testing.T, flags map[string]string) (string, *server.HttpServer, error) {
	t.Helper()
	t.Setenv(agentaccess.DirEnv, filepath.Join(t.TempDir(), "daemon"))
	orig := serveStartHTTP
	t.Cleanup(func() { serveStartHTTP = orig })
	var started *server.HttpServer
	serveStartHTTP = func(s *server.HttpServer, _ map[string]string, _ string, _ int, _ secureread.Session, _ api.Capabilities) error {
		started = s
		return nil
	}
	var err error
	out := captureStdout(t, func() {
		cmd := serveCommandArgs()
		require.NoError(t, cmd.Flags().Set(servePortFlag, "18991"))
		for k, v := range flags {
			require.NoError(t, cmd.Flags().Set(k, v))
		}
		err = serveCommandAction(cmd, nil)
	})
	return out, started, err
}

func installForServe(t *testing.T, store webui.Store, version, localAPI string) {
	t.Helper()
	_, err := store.InstallArchive(webUIArchiveFile(t, version, localAPI), webui.InstallOptions{})
	require.NoError(t, err)
}

func TestServeWebUI_NothingInstalled(t *testing.T) {
	cov100eHome(t, "")
	useWebUIStore(t) // fails the test on any network use
	_, started, err := serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto})
	requireExit(t, err, exitCodeUsage, "serve --web-ui: no DataTug web UI is installed",
		"datatug web-ui install ./datatug-web-ui-<version>.tar.gz", "datatug web-ui install --latest")
	assert.Nil(t, started)
}

func TestServeWebUI_Refusals(t *testing.T) {
	cov100eHome(t, "")
	store := useWebUIStore(t)
	installForServe(t, store, "1.0.0", ">=1")

	_, _, err := serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto, serveHostFlag: "0.0.0.0"})
	requireExit(t, err, exitCodeUsage, `--host "0.0.0.0" is not a loopback address`)

	_, _, err = serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto, serveUIHostFlag: "app.example.com"})
	requireExit(t, err, exitCodeUsage, `--ui-host "app.example.com"`)

	_, _, err = serveWebUIRun(t, map[string]string{serveWebUIFlag: "9.9.9"})
	requireExit(t, err, exitCodeUsage, "serve --web-ui: requested web UI 9.9.9 is not installed")

	_, _, err = serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto, serveUIConnectSrcFlag: "http://api.example.com"})
	requireExit(t, err, exitCodeUsage, "is not an exact https origin")

	require.NoError(t, os.Chmod(store.Dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(store.Dir, 0o700) })
	_, _, err = serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto})
	assert.Error(t, err, "the active version cannot be recorded")
}

func TestServeWebUI_Serves(t *testing.T) {
	cov100eHome(t, "")
	store := useWebUIStore(t)
	installForServe(t, store, "1.0.0", ">=1")
	installForServe(t, store, "1.1.0", ">=1")

	out, started, err := serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto, serveUIConnectSrcFlag: "https://api.example.com"})
	require.NoError(t, err)
	require.NotNil(t, started)
	assert.NotNil(t, started.WebUI)
	assert.Equal(t, defaultUIHost, started.UIHost)

	primary := regexp.MustCompile(`Web UI +http://datatug-app\.localhost:18991/store/http-datatug-app\.localhost:18991#code=(\S+)`).FindStringSubmatch(out)
	fallback := regexp.MustCompile(`Fallback +http://127\.0\.0\.1:18991/store/http-127\.0\.0\.1:18991#code=(\S+)`).FindStringSubmatch(out)
	require.Len(t, primary, 2, out)
	require.Len(t, fallback, 2, out)
	assert.NotEqual(t, primary[1], fallback[1], "a code is accepted once: each link has its own")
	assert.Contains(t, out, "Local web UI 1.1.0")
	assert.Contains(t, out, filepath.Join(store.Dir, "versions", "1.1.0"))
	assert.Contains(t, out, "localhost")
	assert.Contains(t, out, "reaches this agent and https://api.example.com")
	assert.Contains(t, out, "The web UI has 1 inline event handler(s)")
	st, err := store.LoadState()
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", st.Active)

	// a requested version, and the daemon's output carries no code
	out, started, err = serveWebUIRun(t, map[string]string{serveWebUIFlag: "1.0.0", serveIdleTimeoutFlag: time.Hour.String(), serveUIHostFlag: "LocalHost"})
	require.NoError(t, err)
	assert.Equal(t, "localhost", started.UIHost)
	assert.Contains(t, out, "http://localhost:18991/store/http-localhost:18991\n")
	assert.Regexp(t, `Fallback +http://127\.0\.0\.1:18991/store/http-127\.0\.0\.1:18991\n`, out)
	assert.NotContains(t, out, "code=")
	assert.Contains(t, out, "Local web UI 1.0.0")
	assert.NotContains(t, out, "Ctrl+C", "the daemon log has no key hint")
	assert.Contains(t, out, "reaches only this agent")

	// without --web-ui nothing changes
	out, started, err = serveWebUIRun(t, nil)
	require.NoError(t, err)
	assert.Nil(t, started.WebUI)
	assert.Contains(t, out, "https://datatug.app/store/http-")
	assert.NotContains(t, out, "Local web UI")
	assert.NotContains(t, out, "localhost ")
	assert.Contains(t, out, "Press Ctrl+C to stop")
}

func TestServeWebUI_UIHostFromSettings(t *testing.T) {
	cov100eHome(t, "server:\n  uiHost: my-app.localhost\n")
	store := useWebUIStore(t)
	installForServe(t, store, "1.0.0", ">=1")
	out, started, err := serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto})
	require.NoError(t, err)
	assert.Equal(t, "my-app.localhost", started.UIHost)
	assert.Contains(t, out, "http://my-app.localhost:18991/store/http-my-app.localhost:18991#code=")

	_, started, _ = serveWebUIRun(t, map[string]string{serveWebUIFlag: webUIAuto, serveUIHostFlag: "127.0.0.1"})
	assert.Equal(t, "127.0.0.1", started.UIHost, "the flag wins over the setting")
}

func TestValidUIHostAndLoopbackBind(t *testing.T) {
	for host, ok := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "datatug-app.localhost": true, "a.b.localhost": true,
		".localhost": false, "localhost.": false, "a..localhost": false, "-a.localhost": false, "a-.localhost": false,
		"evil.com": false, "localhost.evil.com": false, "10.0.0.1": false, "a_b.localhost": false,
		strings.Repeat("a", 64) + ".localhost": false,
	} {
		assert.Equal(t, ok, validUIHost(host), host)
	}
	for host, ok := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "127.1.2.3": true, "::1": true, "[::1]": true,
		"0.0.0.0": false, "": false, "example.com": false, "192.168.1.2": false,
	} {
		assert.Equal(t, ok, loopbackBind(host), host)
	}
}
