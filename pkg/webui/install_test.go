package webui

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDigest(t *testing.T) {
	d := strings.Repeat("ab", 32)
	for _, in := range []string{d, " " + d + "\n", "sha256:" + d, strings.ToUpper(d), d + "  datatug-frontend-1.0.0.tar.gz\n"} {
		got, err := ParseDigest(in)
		require.NoError(t, err, in)
		assert.Equal(t, d, got)
	}
	for _, in := range []string{"", "abc", "sha256:" + d[:63], d + "0"} {
		_, err := ParseDigest(in)
		assert.Error(t, err, in)
	}
}

func TestSidecarDigest(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")

	_, ok, err := SidecarDigest(archive)
	require.NoError(t, err)
	assert.False(t, ok)

	d := strings.Repeat("0", 64)
	writeFile(t, dir, "a.tar.gz.sha256", []byte(d+"  a.tar.gz\n"))
	got, ok, err := SidecarDigest(archive)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, d, got)

	writeFile(t, dir, "a.tar.gz.sha256", []byte("nope"))
	_, _, err = SidecarDigest(archive)
	assert.ErrorContains(t, err, "a.tar.gz.sha256")

	require.NoError(t, os.Mkdir(filepath.Join(dir, "b.tar.gz.sha256"), 0o700))
	_, _, err = SidecarDigest(filepath.Join(dir, "b.tar.gz"))
	assert.Error(t, err)
}

func TestInstallArchive_Good(t *testing.T) {
	s := newStore(t)
	archive, digest := goodArchive(t, "1.0.0", ">=1.0 <2")
	in, err := s.InstallArchive(archive, InstallOptions{ExpectedSHA256: digest, Source: DigestFromFlag})
	require.NoError(t, err)
	assert.False(t, in.Reused)
	assert.Equal(t, digest, in.ArchiveSHA256)
	assert.Equal(t, DigestFromFlag, in.Source)
	assert.Equal(t, filepath.Join(s.versionsDir(), "1.0.0"), in.Dir)
	assert.Equal(t, []string{"1.0.0"}, versionDirs(t, s))
	_, err = VerifyDir(in.Dir)
	require.NoError(t, err)

	// the same version again is reused
	again, err := s.InstallArchive(archive, InstallOptions{})
	require.NoError(t, err)
	assert.True(t, again.Reused)
	assert.Equal(t, DigestNone, again.Source)
}

func TestInstallArchive_DirEntriesAndSignature(t *testing.T) {
	s := newStore(t)
	files := appFiles()
	entries := append([]tarEntry{
		{name: "./", typ: tar.TypeDir},
		{name: "browser/", typ: tar.TypeDir},
		{name: "./browser/assets/", typ: tar.TypeDir},
		file(SignatureFile, "sig"),
	}, archiveEntries(t, "1.0.0", ">=1", files)...)
	archive := writeFile(t, t.TempDir(), "a.tar.gz", tarGz(t, entries...))
	_, err := s.InstallArchive(archive, InstallOptions{})
	require.NoError(t, err)
}

// Every malicious or broken archive is refused and leaves nothing under versions/.
func TestInstallArchive_Refused(t *testing.T) {
	origEntries, origFile, origTotal := maxEntries, maxFileBytes, maxTotalBytes
	t.Cleanup(func() { maxEntries, maxFileBytes, maxTotalBytes = origEntries, origFile, origTotal })

	good := func() []tarEntry { return archiveEntries(t, "1.0.0", ">=1", appFiles()) }
	withEntries := func(extra ...tarEntry) []byte { return tarGz(t, append(good(), extra...)...) }
	withManifest := func(edit func(*Manifest)) []byte {
		m := manifestFor("1.0.0", ">=1", appFiles())
		edit(&m)
		e := good()
		e[0] = file(ManifestFile, manifestJSON(t, m))
		return tarGz(t, e...)
	}
	full := tarGz(t, good()...)

	for name, tc := range map[string]struct {
		data  []byte
		opts  InstallOptions
		limit func()
		want  string
	}{
		"dot-dot traversal":   {data: withEntries(file("browser/../../evil.js", "x")), want: ". or .."},
		"dot-dot directory":   {data: withEntries(tarEntry{name: "../evil/", typ: tar.TypeDir}), want: ". or .."},
		"absolute path":       {data: withEntries(file("/tmp/evil.js", "x")), want: "absolute"},
		"backslash":           {data: withEntries(file(`browser\..\evil.js`, "x")), want: "backslash"},
		"symlink":             {data: withEntries(tarEntry{name: "browser/link", typ: tar.TypeSymlink, link: "/etc/passwd"}), want: "not a regular file"},
		"hard link":           {data: withEntries(tarEntry{name: "browser/hard", typ: tar.TypeLink, link: IndexFile}), want: "not a regular file"},
		"character device":    {data: withEntries(tarEntry{name: "browser/dev", typ: tar.TypeChar}), want: "not a regular file"},
		"fifo":                {data: withEntries(tarEntry{name: "browser/fifo", typ: tar.TypeFifo}), want: "not a regular file"},
		"duplicate entry":     {data: withEntries(file(IndexFile, testIndex)), want: "twice"},
		"dot-file":            {data: withEntries(file("browser/.env", "SECRET=1")), want: "dot-file"},
		"source map":          {data: withEntries(file("browser/main.js.map", "{}")), want: "source maps"},
		"outside browser/":    {data: withEntries(file("server/run.sh", "x")), want: "outside browser/"},
		"extra unlisted file": {data: withEntries(file("browser/extra.js", "x")), want: "does not list"},
		"tampered file": {data: func() []byte {
			e := good()
			for i := range e {
				if e[i].name == BrowserDir+"/main-ABCD1234.js" {
					e[i].body = "console.log('evil')"
				}
			}
			return tarGz(t, e...)
		}(), want: "does not match"},
		"missing listed file": {data: func() []byte { e := good(); return tarGz(t, e[:len(e)-1]...) }(), want: "lacks"},
		"missing manifest":    {data: func() []byte { e := good(); return tarGz(t, e[1:]...) }(), want: "no manifest.json"},
		"missing index.html": {data: func() []byte {
			files := appFiles()
			delete(files, IndexFile)
			return tarGz(t, archiveEntries(t, "1.0.0", ">=1", files)...)
		}(), want: "does not list browser/index.html"},
		"bad manifest schema":  {data: withManifest(func(m *Manifest) { m.Schema = "x" }), want: "schema"},
		"bad manifest name":    {data: withManifest(func(m *Manifest) { m.Name = "x" }), want: "name"},
		"bad manifest version": {data: withManifest(func(m *Manifest) { m.Version = "1.0" }), want: "semantic version"},
		"bad manifest range":   {data: withManifest(func(m *Manifest) { m.Requires.LocalAPI = "^1" }), want: "requires.localApi"},
		"manifest not JSON":    {data: func() []byte { e := good(); e[0].body = "{"; return tarGz(t, e...) }(), want: "not valid JSON"},
		"wrong expected digest": {data: full, opts: InstallOptions{ExpectedSHA256: strings.Repeat("0", 64), Source: DigestFromSidecar},
			want: "from sidecar .sha256 file"},
		"truncated gzip":   {data: full[:len(full)/2], want: "unexpected EOF"},
		"corrupt tar":      {data: gz(t, []byte(strings.Repeat("x", 1024))), want: "damaged or truncated"},
		"not gzip":         {data: tarBytes(t, good()...), want: "not gzip"},
		"too many entries": {data: full, limit: func() { maxEntries = 3 }, want: "more than 3 entries"},
		"file too large":   {data: full, limit: func() { maxFileBytes = 10 }, want: "larger than 10 bytes"},
		"total too large":  {data: full, limit: func() { maxTotalBytes = 100 }, want: "larger than 100 bytes uncompressed"},
		"file over a file": {data: withEntries(file("browser/x", "1"), file("browser/x/y", "2")), want: "browser/x"},
		"dir over a file":  {data: withEntries(file("browser/x", "1"), tarEntry{name: "browser/x/y/", typ: tar.TypeDir}), want: "not a directory"},
		"file over a dir":  {data: withEntries(tarEntry{name: "browser/d/", typ: tar.TypeDir}, file("browser/d", "1")), want: "browser/d"},
		"truncated entry": {data: func() []byte {
			raw := tarBytes(t, good()...)
			return gz(t, raw[:512+10])
		}(), want: "manifest.json"},
	} {
		t.Run(name, func(t *testing.T) {
			maxEntries, maxFileBytes, maxTotalBytes = origEntries, origFile, origTotal
			if tc.limit != nil {
				tc.limit()
			}
			s := newStore(t)
			archive := writeFile(t, t.TempDir(), "a.tar.gz", tc.data)
			_, err := s.InstallArchive(archive, tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, versionDirs(t, s), "nothing may stay under versions/")
		})
	}
}

func TestInstallArchive_FilesystemErrors(t *testing.T) {
	s := newStore(t)
	_, err := s.InstallArchive(filepath.Join(t.TempDir(), "missing.tar.gz"), InstallOptions{})
	assert.Error(t, err)

	_, err = s.InstallArchive(t.TempDir(), InstallOptions{})
	assert.ErrorContains(t, err, "read archive")

	archive, _ := goodArchive(t, "1.0.0", ">=1")
	blocked := Store{Dir: writeFile(t, t.TempDir(), "file", nil)}
	_, err = blocked.InstallArchive(archive, InstallOptions{})
	assert.Error(t, err)

	readOnly := newStore(t)
	require.NoError(t, os.MkdirAll(readOnly.versionsDir(), 0o700))
	require.NoError(t, os.Chmod(readOnly.versionsDir(), 0o500))
	t.Cleanup(func() { _ = os.Chmod(readOnly.versionsDir(), 0o700) })
	_, err = readOnly.InstallArchive(archive, InstallOptions{})
	assert.Error(t, err)
}

func TestExtract_DirectCases(t *testing.T) {
	_, err := extract(strings.NewReader(string(tarGz(t))), filepath.Join(t.TempDir(), "missing"))
	assert.Error(t, err)

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { _ = root.Close() }()
	_, err = writeEntry(root, "short", strings.NewReader("ab"), 5)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)

	_, err = verifyExtracted(t.TempDir(), extracted{ManifestFile: {}})
	assert.Error(t, err)
}

func TestInstallArchive_ReplacesDamagedVersion(t *testing.T) {
	s := newStore(t)
	in := install(t, s, "1.0.0", ">=1")
	tampered := filepath.Join(in.Dir, BrowserDir, "main-ABCD1234.js")
	require.NoError(t, os.WriteFile(tampered, []byte("evil"), 0o600))
	_, err := VerifyDir(in.Dir)
	require.ErrorContains(t, err, "changed after install")

	again := install(t, s, "1.0.0", ">=1")
	assert.False(t, again.Reused)
	data, err := os.ReadFile(tampered)
	require.NoError(t, err)
	assert.Equal(t, "console.log('main')", string(data))
	assert.Equal(t, []string{"1.0.0"}, versionDirs(t, s), "the trash is removed")
}

func TestInstallArchive_RenameFailures(t *testing.T) {
	orig := rename
	t.Cleanup(func() { rename = orig })
	archive, _ := goodArchive(t, "1.0.0", ">=1")

	// a damaged copy that cannot be moved aside
	s := newStore(t)
	in := install(t, s, "1.0.0", ">=1")
	require.NoError(t, os.WriteFile(filepath.Join(in.Dir, IndexFile), []byte("x"), 0o600))
	rename = func(string, string) error { return errors.New("busy") }
	_, err := s.InstallArchive(archive, InstallOptions{})
	assert.ErrorContains(t, err, "replace damaged 1.0.0")

	// a publish that fails with nothing valid in place
	s = newStore(t)
	_, err = s.InstallArchive(archive, InstallOptions{})
	assert.ErrorContains(t, err, "publish 1.0.0")
	assert.Empty(t, versionDirs(t, s))

	// a publish that loses to another process which published the same version first
	rename = orig
	s = newStore(t)
	other := install(t, Store{Dir: filepath.Join(t.TempDir(), "other")}, "1.0.0", ">=1")
	rename = func(from, to string) error {
		if err := orig(other.Dir, to); err != nil {
			return err
		}
		return errors.New("exists")
	}
	got, err := s.InstallArchive(archive, InstallOptions{})
	require.NoError(t, err)
	assert.True(t, got.Reused)
}

func TestInstallArchive_Concurrent(t *testing.T) {
	s := newStore(t)
	archive, _ := goodArchive(t, "1.0.0", ">=1")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.InstallArchive(archive, InstallOptions{})
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, []string{"1.0.0"}, versionDirs(t, s))
	_, err := VerifyDir(filepath.Join(s.versionsDir(), "1.0.0"))
	require.NoError(t, err)
}

func TestInstallArchive_RemovesStaleStaging(t *testing.T) {
	s := newStore(t)
	require.NoError(t, os.MkdirAll(s.versionsDir(), 0o700))
	old := time.Now().Add(-2 * staleStagingAge)
	for _, name := range []string{".staging-old", ".trash-old"} {
		require.NoError(t, os.Mkdir(filepath.Join(s.versionsDir(), name), 0o700))
		require.NoError(t, os.Chtimes(filepath.Join(s.versionsDir(), name), old, old))
	}
	require.NoError(t, os.Mkdir(filepath.Join(s.versionsDir(), ".staging-fresh"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(s.versionsDir(), ".other-old"), 0o700))
	require.NoError(t, os.Chtimes(filepath.Join(s.versionsDir(), ".other-old"), old, old))

	install(t, s, "1.0.0", ">=1")
	assert.ElementsMatch(t, []string{".staging-fresh", ".other-old", "1.0.0"}, versionDirs(t, s))
}

func TestVerifyDir(t *testing.T) {
	_, err := VerifyDir(t.TempDir())
	assert.Error(t, err, "no manifest")

	bad := t.TempDir()
	writeFile(t, bad, ManifestFile, []byte("{}"))
	_, err = VerifyDir(bad)
	assert.ErrorContains(t, err, "schema")

	s := newStore(t)
	in := install(t, s, "1.0.0", ">=1")

	moved := filepath.Join(t.TempDir(), "2.0.0")
	require.NoError(t, os.Rename(in.Dir, moved))
	_, err = VerifyDir(moved)
	assert.ErrorContains(t, err, "directory 2.0.0 holds web UI 1.0.0")
	require.NoError(t, os.Rename(moved, in.Dir))

	// search permission only: the manifest reads, the directory does not open
	require.NoError(t, os.Chmod(in.Dir, 0o100))
	_, err = VerifyDir(in.Dir)
	assert.Error(t, err)
	require.NoError(t, os.Chmod(in.Dir, 0o700))

	logo := filepath.Join(in.Dir, BrowserDir, "assets", "logo.svg")
	require.NoError(t, os.Remove(logo))
	_, err = VerifyDir(in.Dir)
	assert.ErrorContains(t, err, "lacks browser/assets/logo.svg")

	require.NoError(t, os.Mkdir(logo, 0o700))
	_, err = VerifyDir(in.Dir)
	assert.Error(t, err)
}
