package webui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// testIndex is an Angular-like index.html: a critical-CSS loader, one inline
// script, one external script, an empty inline script and an inline handler.
const testIndex = `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
	`<link rel="stylesheet" href="styles-ABCD1234.css" media="print" onload="this.media='all'">` +
	`</head><body><app-root></app-root><script>window.boot=1</script>` +
	`<script src="main-ABCD1234.js" type="module"></script><script></script>` +
	`<br/><button onclick="go()">go</button></body></html>`

// tarEntry is one entry of a test archive. typ 0 is a regular file; size < 0 is len(body).
type tarEntry struct {
	name string
	body string
	typ  byte
	link string
	size int64
}

func file(name, body string) tarEntry { return tarEntry{name: name, body: body, size: -1} }

func tarBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		size := e.size
		if size < 0 {
			size = int64(len(e.body))
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o644, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = size
		}
		if typ == tar.TypeDir {
			h.Mode = 0o755
		}
		require.NoError(t, tw.WriteHeader(h))
		if typ == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(data)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	return gz(t, tarBytes(t, entries...))
}

func appFiles() map[string]string {
	return map[string]string{
		IndexFile:                           testIndex,
		BrowserDir + "/main-ABCD1234.js":    "console.log('main')",
		BrowserDir + "/styles-ABCD1234.css": "body{}",
		BrowserDir + "/assets/logo.svg":     "<svg/>",
	}
}

func sum(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

func manifestFor(version, localAPI string, files map[string]string) Manifest {
	m := Manifest{Schema: ManifestSchema, Name: ManifestName, Version: version, Requires: Requirements{LocalAPI: localAPI}}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m.Files = append(m.Files, ManifestFileEntry{Path: name, Size: int64(len(files[name])), SHA256: sum(files[name])})
	}
	return m
}

func manifestJSON(t *testing.T, m Manifest) string {
	t.Helper()
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return string(data)
}

// archiveEntries is a valid archive's entries: manifest.json and every file, in a stable order.
func archiveEntries(t *testing.T, version, localAPI string, files map[string]string) []tarEntry {
	t.Helper()
	entries := []tarEntry{file(ManifestFile, manifestJSON(t, manifestFor(version, localAPI, files)))}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entries = append(entries, file(name, files[name]))
	}
	return entries
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

// goodArchive writes a valid archive of version to a temp file and returns its path and digest.
func goodArchive(t *testing.T, version, localAPI string) (string, string) {
	t.Helper()
	data := tarGz(t, archiveEntries(t, version, localAPI, appFiles())...)
	h := sha256.Sum256(data)
	return writeFile(t, t.TempDir(), "datatug-web-ui-"+version+".tar.gz", data), hex.EncodeToString(h[:])
}

func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "web-ui")}
}

func install(t *testing.T, s Store, version, localAPI string) Installed {
	t.Helper()
	archive, _ := goodArchive(t, version, localAPI)
	in, err := s.InstallArchive(archive, InstallOptions{})
	require.NoError(t, err)
	return in
}

// versionDirs lists what is under versions/, staging and trash included.
func versionDirs(t *testing.T, s Store) []string {
	t.Helper()
	entries, err := os.ReadDir(s.versionsDir())
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// tamper rewrites an installed file the way another process on the machine could: installed files
// are read-only (0400), so it makes the file writable first.
func tamper(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// sizedDirArchive is a gzip tar whose only entry is a directory header claiming a size, followed by
// that many bytes: archive/tar's writer refuses to produce one, so a regular-file header is patched.
func sizedDirArchive(t *testing.T) []byte {
	t.Helper()
	raw := tarBytes(t, file("browser/d", "xxxx"))
	raw[156] = tar.TypeDir
	for i := 148; i < 156; i++ {
		raw[i] = ' '
	}
	sum := 0
	for _, b := range raw[:512] {
		sum += int(b)
	}
	copy(raw[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return gz(t, raw)
}
