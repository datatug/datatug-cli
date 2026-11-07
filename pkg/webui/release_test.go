package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGitHub emulates the github.com download base and the API of one repository.
type fakeGitHub struct {
	mu     sync.Mutex
	srv    *httptest.Server
	routes map[string]func(w http.ResponseWriter)
	hits   []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{routes: map[string]func(http.ResponseWriter){}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.hits = append(g.hits, r.URL.RequestURI())
		route := g.routes[r.URL.RequestURI()]
		g.mu.Unlock()
		if route == nil {
			http.NotFound(w, r)
			return
		}
		route(w)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) body(uri, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[uri] = func(w http.ResponseWriter) { _, _ = w.Write([]byte(body)) }
}

func (g *fakeGitHub) source() ReleaseSource {
	return ReleaseSource{Repo: "datatug/frontend", APIBase: g.srv.URL + "/api/", DownloadBase: g.srv.URL + "/", Client: g.srv.Client()}
}

const latestBase = "/datatug/frontend/releases/latest/download/"
const listURI = "/api/repos/datatug/frontend/releases?per_page=50"

// release publishes version as a listed release with the three assets and returns its archive bytes.
func (g *fakeGitHub) release(t *testing.T, version, localAPI string) ghRelease {
	t.Helper()
	data := tarGz(t, archiveEntries(t, version, localAPI, appFiles())...)
	h := sha256.Sum256(data)
	base := "/dl/v" + version + "/"
	archive := "datatug-frontend-" + version + ".tar.gz"
	g.body(base+archive, string(data))
	g.body(base+archive+".sha256", hex.EncodeToString(h[:])+"  "+archive+"\n")
	g.body(base+ManifestFile, manifestJSON(t, manifestFor(version, localAPI, appFiles())))
	return ghRelease{TagName: "v" + version, Assets: []ghAsset{
		{Name: archive, URL: g.srv.URL + base + archive},
		{Name: archive + ".sha256", URL: g.srv.URL + base + archive + ".sha256"},
		{Name: ManifestFile, URL: g.srv.URL + base + ManifestFile},
		{Name: "notes.txt", URL: g.srv.URL + base + "notes.txt"},
	}}
}

func (g *fakeGitHub) list(t *testing.T, releases ...ghRelease) {
	t.Helper()
	data, err := json.Marshal(releases)
	require.NoError(t, err)
	g.body(listURI, string(data))
}

func TestReleaseSource_LatestDirect(t *testing.T) {
	g := newFakeGitHub(t)
	g.body(latestBase+ManifestFile, manifestJSON(t, manifestFor("1.2.0", ">=1", appFiles())))
	c, err := g.source().Latest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", c.Manifest.Version)
	assert.Equal(t, g.srv.URL+latestBase+"datatug-frontend-1.2.0.tar.gz", c.ArchiveURL)
	assert.Equal(t, c.ArchiveURL+".sha256", c.DigestURL)
	assert.Equal(t, []string{latestBase + ManifestFile}, g.hits, "no API call when the latest release serves")
}

func TestReleaseSource_LatestFallsBackToTheList(t *testing.T) {
	for name, latest := range map[string]string{
		"latest incompatible":  `{"schema":"datatug-frontend/1","name":"datatug-frontend","version":"3.0.0","requires":{"localApi":">=3"},"files":[{"path":"browser/index.html","size":1,"sha256":"` + strings.Repeat("a", 64) + `"}]}`,
		"latest a pre-release": `{"schema":"datatug-frontend/1","name":"datatug-frontend","version":"3.0.0-rc.1","requires":{"localApi":">=1"},"files":[{"path":"browser/index.html","size":1,"sha256":"` + strings.Repeat("a", 64) + `"}]}`,
		"latest invalid":       `{}`,
		"latest missing":       "",
	} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			if latest != "" {
				g.body(latestBase+ManifestFile, latest)
			}
			incompatible := g.release(t, "2.0.0", ">=2")
			good := g.release(t, "1.1.0", ">=1")
			older := g.release(t, "1.0.0", ">=1")
			draft := g.release(t, "2.5.0", ">=1")
			draft.Draft = true
			pre := g.release(t, "2.6.0", ">=1")
			pre.Prerelease = true
			preTag := g.release(t, "2.7.0-rc.1", ">=1")
			g.list(t, older, draft, pre, preTag, ghRelease{TagName: "nightly"}, incompatible, good)

			c, err := g.source().Latest(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "1.1.0", c.Manifest.Version, "the newest stable compatible release")
			assert.Equal(t, good.Assets[0].URL, c.ArchiveURL)
			assert.Equal(t, good.Assets[1].URL, c.DigestURL)
		})
	}
}

func TestReleaseSource_ListErrors(t *testing.T) {
	g := newFakeGitHub(t)
	_, err := g.source().Latest(context.Background())
	assert.ErrorContains(t, err, "list web UI releases: GET")
	assert.ErrorContains(t, err, "404 Not Found")

	g.body(listURI, "not json")
	_, err = g.source().Latest(context.Background())
	assert.ErrorContains(t, err, "list web UI releases")

	g.list(t, ghRelease{TagName: "v1.0.0", Draft: true})
	_, err = g.source().Latest(context.Background())
	assert.ErrorContains(t, err, "no stable web UI release found in datatug/frontend")

	noAssets := ghRelease{TagName: "v1.5.0"}
	badManifest := g.release(t, "1.4.0", ">=1")
	g.body("/dl/v1.4.0/"+ManifestFile, "{")
	wrongVersion := g.release(t, "1.3.0", ">=1")
	g.body("/dl/v1.3.0/"+ManifestFile, manifestJSON(t, manifestFor("1.2.9", ">=1", appFiles())))
	goneManifest := g.release(t, "1.2.0", ">=1")
	g.mu.Lock()
	delete(g.routes, "/dl/v1.2.0/"+ManifestFile)
	g.mu.Unlock()
	incompatible := g.release(t, "1.1.0", ">=2")
	g.list(t, noAssets, badManifest, wrongVersion, goneManifest, incompatible)
	_, err = g.source().Latest(context.Background())
	require.Error(t, err)
	for _, want := range []string{
		"no release in datatug/frontend can be served by this CLI",
		"release v1.5.0 lacks datatug-frontend-1.5.0.tar.gz",
		"release v1.4.0: manifest.json is not valid JSON",
		"release v1.3.0 carries the manifest of 1.2.9",
		"/dl/v1.2.0/manifest.json: 404",
		"web UI 1.1.0 needs local API >=2",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestReleaseSource_DownloadAndInstall(t *testing.T) {
	g := newFakeGitHub(t)
	rel := g.release(t, "1.1.0", ">=1")
	g.list(t, rel)
	src := g.source()
	c, err := src.Latest(context.Background())
	require.NoError(t, err)

	archive, digest, err := src.Download(context.Background(), c, t.TempDir())
	require.NoError(t, err)
	s := newStore(t)
	in, err := s.InstallArchive(archive, InstallOptions{ExpectedSHA256: digest, Source: DigestFromRelease})
	require.NoError(t, err)
	assert.Equal(t, "1.1.0", in.Manifest.Version)

	// a release whose .sha256 does not match its archive is refused at install
	g.body("/dl/v1.1.0/datatug-frontend-1.1.0.tar.gz.sha256", strings.Repeat("0", 64))
	archive, digest, err = src.Download(context.Background(), c, t.TempDir())
	require.NoError(t, err)
	_, err = newStore(t).InstallArchive(archive, InstallOptions{ExpectedSHA256: digest, Source: DigestFromRelease})
	assert.ErrorContains(t, err, "from GitHub release over HTTPS")

	// a digest file that is not a digest
	g.body("/dl/v1.1.0/datatug-frontend-1.1.0.tar.gz.sha256", "<html>")
	_, _, err = src.Download(context.Background(), c, t.TempDir())
	assert.ErrorContains(t, err, "release digest")

	// no digest at all
	_, _, err = src.Download(context.Background(), Candidate{DigestURL: g.srv.URL + "/nope"}, t.TempDir())
	assert.ErrorContains(t, err, "404")

	// a download directory that does not exist
	g.body("/dl/v1.1.0/datatug-frontend-1.1.0.tar.gz.sha256", strings.Repeat("0", 64))
	_, _, err = src.Download(context.Background(), c, filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)

	// an archive that is gone leaves no temp file behind
	dir := t.TempDir()
	_, _, err = src.Download(context.Background(), Candidate{DigestURL: c.DigestURL, ArchiveURL: g.srv.URL + "/nope"}, dir)
	assert.ErrorContains(t, err, "404")
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)

	// an archive larger than the limit
	orig := maxTotalBytes
	t.Cleanup(func() { maxTotalBytes = orig })
	maxTotalBytes = 10
	_, _, err = src.Download(context.Background(), c, t.TempDir())
	assert.ErrorContains(t, err, "response is larger than 10 bytes")
}

func TestReleaseSource_Get(t *testing.T) {
	g := newFakeGitHub(t)
	g.routes["/short"] = func(w http.ResponseWriter) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("only a little"))
	}
	src := g.source()
	var b strings.Builder
	assert.ErrorContains(t, src.get(context.Background(), g.srv.URL+"/short", &b, 1000), "GET "+g.srv.URL+"/short")
	assert.Error(t, src.get(context.Background(), "http://bad host/", &b, 10), "an invalid URL")

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	assert.Error(t, src.get(context.Background(), closed.URL, &b, 10), "nothing listens")

	assert.Same(t, http.DefaultClient, ReleaseSource{}.client())
	assert.Equal(t, "https://github.com", ReleaseSource{}.downloadBase())
	assert.Equal(t, "https://example.com", ReleaseSource{DownloadBase: "https://example.com/"}.downloadBase())
}
