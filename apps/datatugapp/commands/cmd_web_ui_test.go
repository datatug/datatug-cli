package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/webui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const webUITestIndex = `<!doctype html><html><head><meta charset="utf-8"></head><body><script>boot()</script><button onclick="x()">x</button></body></html>`

func hexSum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// webUIArchive builds a frontend archive of version that needs localAPI.
func webUIArchive(t *testing.T, version, localAPI string) []byte {
	t.Helper()
	files := map[string]string{webui.IndexFile: webUITestIndex, webui.BrowserDir + "/main-ABCD1234.js": "main()"}
	m := webui.Manifest{Schema: webui.ManifestSchema, Name: webui.ManifestName, Version: version, Requires: webui.Requirements{LocalAPI: localAPI}}
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m.Files = append(m.Files, webui.ManifestFileEntry{Path: name, Size: int64(len(files[name])), SHA256: hexSum([]byte(files[name]))})
	}
	manifest, err := json.Marshal(m)
	require.NoError(t, err)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(name string, body []byte) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}))
		_, err := tw.Write(body)
		require.NoError(t, err)
	}
	write(webui.ManifestFile, manifest)
	for _, name := range names {
		write(name, []byte(files[name]))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func webUIArchiveFile(t *testing.T, version, localAPI string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "datatug-web-ui-"+version+".tar.gz")
	require.NoError(t, os.WriteFile(p, webUIArchive(t, version, localAPI), 0o600))
	return p
}

// useWebUIStore points the commands at a fresh store and forbids the network.
func useWebUIStore(t *testing.T) webui.Store {
	t.Helper()
	store := webui.Store{Dir: filepath.Join(t.TempDir(), "web-ui")}
	origStore, origSource := webUIStore, webUIReleaseSource
	t.Cleanup(func() { webUIStore, webUIReleaseSource = origStore, origSource })
	webUIStore = func() webui.Store { return store }
	webUIReleaseSource = func() webui.ReleaseSource {
		t.Fatal("no network call is allowed here")
		return webui.ReleaseSource{}
	}
	return store
}

func runWebUI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := webUICommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func requireExit(t *testing.T, err error, code int, contains ...string) {
	t.Helper()
	var ec ExitCoder
	require.True(t, errors.As(err, &ec), "%v", err)
	assert.Equal(t, code, ec.ExitCode(), err.Error())
	for _, c := range contains {
		assert.Contains(t, err.Error(), c)
	}
}

func TestWebUICommand_Registered(t *testing.T) {
	cmd, _, err := DatatugCommand().Find([]string{"web-ui", "install"})
	require.NoError(t, err)
	assert.Equal(t, "install", cmd.Name())
	src := webUIReleaseSource()
	assert.Equal(t, webui.DefaultReleaseRepo, src.Repo)
	assert.Equal(t, "https://api.github.com", src.APIBase)
	assert.NotNil(t, src.Client)
}

func TestWebUIInstall_File(t *testing.T) {
	useWebUIStore(t)

	_, err := runWebUI(t, "install")
	requireExit(t, err, exitCodeUsage, "datatug web-ui install ./datatug-web-ui-<version>.tar.gz", "datatug web-ui install --latest")
	_, err = runWebUI(t, "install", "--latest", "a.tar.gz")
	requireExit(t, err, exitCodeUsage, "not both")

	archive := webUIArchiveFile(t, "1.0.0", ">=1")
	out, err := runWebUI(t, "install", archive)
	require.NoError(t, err)
	assert.Contains(t, out, "web UI 1.0.0 installed (compatible with this CLI)")
	assert.Contains(t, out, "digest checked against: "+string(webui.DigestNone))
	assert.Contains(t, out, "files verified: 2")
	assert.Contains(t, out, "note: no expected digest was given")

	out, err = runWebUI(t, "install", archive)
	require.NoError(t, err)
	assert.Contains(t, out, "already installed and verified")

	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	out, err = runWebUI(t, "install", "--sha256", "sha256:"+hexSum(data), archive)
	require.NoError(t, err)
	assert.Contains(t, out, "digest checked against: --sha256 flag")
	assert.NotContains(t, out, "note:")

	_, err = runWebUI(t, "install", "--sha256", "nope", archive)
	requireExit(t, err, exitCodeUsage, "--sha256: not a SHA-256 digest")
	_, err = runWebUI(t, "install", "--sha256", strings.Repeat("0", 64), archive)
	requireExit(t, err, exitCodeUsage, "web UI not installed", "from --sha256 flag")

	require.NoError(t, os.WriteFile(archive+".sha256", []byte(hexSum(data)+"  x.tar.gz\n"), 0o600))
	out, err = runWebUI(t, "install", archive)
	require.NoError(t, err)
	assert.Contains(t, out, "digest checked against: sidecar .sha256 file")

	require.NoError(t, os.WriteFile(archive+".sha256", []byte(strings.Repeat("1", 64)), 0o600))
	_, err = runWebUI(t, "install", archive)
	requireExit(t, err, exitCodeUsage, "from sidecar .sha256 file")
	require.NoError(t, os.WriteFile(archive+".sha256", []byte("garbage"), 0o600))
	_, err = runWebUI(t, "install", archive)
	requireExit(t, err, exitCodeUsage, ".sha256")

	out, err = runWebUI(t, "install", webUIArchiveFile(t, "2.0.0", ">=2"))
	require.NoError(t, err)
	assert.Contains(t, out, "web UI 2.0.0 installed (NOT servable by this CLI:")
}

// fakeReleases serves the latest-release download path of github.com for one archive.
func fakeReleases(t *testing.T, version string) (*httptest.Server, map[string][]byte) {
	t.Helper()
	data := webUIArchive(t, version, ">=1")
	base := "/datatug/web-ui/releases/latest/download/"
	archive := "datatug-web-ui-" + version + ".tar.gz"
	var manifest []byte
	gr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	tr := tar.NewReader(gr)
	_, err = tr.Next()
	require.NoError(t, err)
	var mb bytes.Buffer
	_, err = mb.ReadFrom(tr)
	require.NoError(t, err)
	manifest = mb.Bytes()
	routes := map[string][]byte{
		base + webui.ManifestFile:  manifest,
		base + archive:             data,
		base + archive + ".sha256": []byte(hexSum(data) + "\n"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	webUIReleaseSource = func() webui.ReleaseSource {
		return webui.ReleaseSource{Repo: "datatug/web-ui", APIBase: srv.URL, DownloadBase: srv.URL, Client: srv.Client()}
	}
	return srv, routes
}

func TestWebUIInstall_Latest(t *testing.T) {
	useWebUIStore(t)
	_, routes := fakeReleases(t, "1.3.0")

	out, err := runWebUI(t, "install", "--latest")
	require.NoError(t, err)
	assert.Contains(t, out, "Looking for the newest compatible web UI in github.com/datatug/web-ui")
	assert.Contains(t, out, "Downloading web UI 1.3.0")
	assert.Contains(t, out, "web UI 1.3.0 installed")
	assert.Contains(t, out, "digest checked against: GitHub release over HTTPS")

	out, err = runWebUI(t, "update")
	require.NoError(t, err)
	assert.Contains(t, out, "already installed and verified")

	digestPath := "/datatug/web-ui/releases/latest/download/datatug-web-ui-1.3.0.tar.gz.sha256"
	routes[digestPath] = []byte(strings.Repeat("0", 64))
	_, err = runWebUI(t, "update")
	requireExit(t, err, exitCodeDatabase, "web UI not installed", "GitHub release over HTTPS")

	delete(routes, digestPath)
	_, err = runWebUI(t, "update")
	requireExit(t, err, exitCodeDatabase, "download failed")

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	_, err = runWebUI(t, "update")
	assert.Error(t, err)
}

func TestWebUIInstall_LatestUnavailable(t *testing.T) {
	useWebUIStore(t)
	_, routes := fakeReleases(t, "1.3.0")
	for k := range routes {
		delete(routes, k)
	}
	_, err := runWebUI(t, "update")
	requireExit(t, err, exitCodeDatabase, "list web UI releases", "datatug web-ui install ./datatug-web-ui-<version>.tar.gz")
}

func TestWebUIList(t *testing.T) {
	store := useWebUIStore(t)
	out, err := runWebUI(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "no DataTug web UI is installed.")
	assert.Contains(t, out, "datatug web-ui install --latest")

	for _, v := range []string{"1.0.0", "1.1.0"} {
		_, err = runWebUI(t, "install", webUIArchiveFile(t, v, ">=1"))
		require.NoError(t, err)
	}
	_, err = runWebUI(t, "install", webUIArchiveFile(t, "2.0.0", ">=2"))
	require.NoError(t, err)
	require.NoError(t, store.MarkActive("1.1.0"))
	_, err = store.Use("1.0.0")
	require.NoError(t, err)

	out, err = runWebUI(t, "list")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 4, out)
	assert.True(t, strings.HasPrefix(lines[0], "2.0.0  needs local API >=2  web UI is not compatible"), lines[0])
	assert.Equal(t, "1.1.0 [last served]  needs local API >=1  compatible", lines[1])
	assert.Equal(t, "1.0.0 [pinned]  needs local API >=1  compatible", lines[2])
	assert.Equal(t, "this CLI serves local API "+webui.LocalAPIVersion+" and web UI >= "+webui.MinFrontendVersion, lines[3])

	require.NoError(t, store.MarkActive("1.0.0"))
	out, err = runWebUI(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "1.0.0 [pinned] [last served]")

	require.NoError(t, os.WriteFile(filepath.Join(store.Dir, "state.json"), []byte("{"), 0o600))
	_, err = runWebUI(t, "list")
	assert.ErrorContains(t, err, "is damaged")

	broken := useWebUIStore(t)
	require.NoError(t, os.MkdirAll(broken.Dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(broken.Dir, "versions"), nil, 0o600))
	_, err = runWebUI(t, "list")
	assert.Error(t, err)
}

func TestWebUISelectionCommands(t *testing.T) {
	store := useWebUIStore(t)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		_, err := runWebUI(t, "install", webUIArchiveFile(t, v, ">=1"))
		require.NoError(t, err)
	}

	_, err := runWebUI(t, "use", "9.9.9")
	requireExit(t, err, exitCodeUsage, "not installed")
	_, err = runWebUI(t, "rollback")
	requireExit(t, err, exitCodeUsage, "no previous")

	out, err := runWebUI(t, "use", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "web UI 1.0.0 pinned (previous: none)\n", out)
	out, err = runWebUI(t, "use", "1.1.0")
	require.NoError(t, err)
	assert.Equal(t, "web UI 1.1.0 pinned (previous: 1.0.0)\n", out)

	_, err = runWebUI(t, "remove", "1.1.0")
	requireExit(t, err, exitCodeUsage, "is pinned")

	out, err = runWebUI(t, "rollback")
	require.NoError(t, err)
	assert.Equal(t, "web UI rolled back: 1.0.0 pinned\n", out)

	out, err = runWebUI(t, "unpin")
	require.NoError(t, err)
	assert.Contains(t, out, "web UI unpinned")

	out, err = runWebUI(t, "remove", "1.1.0")
	require.NoError(t, err)
	assert.Equal(t, "web UI 1.1.0 removed\n", out)
	_, err = runWebUI(t, "remove", "1.1.0")
	requireExit(t, err, exitCodeUsage, "not installed")

	require.NoError(t, os.WriteFile(filepath.Join(store.Dir, "state.json"), []byte("{"), 0o600))
	_, err = runWebUI(t, "unpin")
	assert.ErrorContains(t, err, "is damaged")
}
