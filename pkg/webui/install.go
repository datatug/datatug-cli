package webui

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Limits of one archive. A real frontend is about 50 MB in a few thousand files.
var (
	maxEntries    = 20000
	maxFileBytes  = int64(256 << 20)
	maxTotalBytes = int64(1 << 30)
)

// staleStagingAge is how old an abandoned staging directory must be before an
// install removes it; a younger one may belong to a concurrent install.
var staleStagingAge = time.Hour

// rename is os.Rename; a test replaces it to fail a publish.
var rename = os.Rename

// DigestSource says where the expected archive digest came from: the honest
// answer to "who vouches for this archive" until releases are signed.
type DigestSource string

const (
	DigestFromFlag    DigestSource = "--sha256 flag"
	DigestFromSidecar DigestSource = "sidecar .sha256 file"
	DigestFromRelease DigestSource = "GitHub release over HTTPS"
	DigestNone        DigestSource = "none — integrity of contents only"
)

// InstallOptions are the expectations an install verifies.
type InstallOptions struct {
	// ExpectedSHA256 is the archive's expected digest, lowercase hex. "" checks none.
	ExpectedSHA256 string
	Source         DigestSource
	// AllowUnsigned installs an archive with no signature from a trusted release key (a local or test build). A
	// signature that does not verify is refused regardless.
	AllowUnsigned bool
}

// Installed is the outcome of an install.
type Installed struct {
	Manifest      Manifest
	Dir           string
	ArchiveSHA256 string
	Source        DigestSource
	// Reused is true when the same version was already installed and verified.
	Reused bool
	// Signature says which trusted release key signed the archive.
	Signature Signature
}

// ParseDigest reads a digest in any of the forms people paste: bare hex,
// "sha256:<hex>", or a sha256sum line "<hex>  <file>".
func ParseDigest(s string) (string, error) {
	s = strings.TrimSpace(s)
	if fields := strings.Fields(s); len(fields) > 0 {
		s = fields[0]
	}
	s = strings.ToLower(strings.TrimPrefix(s, "sha256:"))
	if !sha256Hex.MatchString(s) {
		return "", errors.New("not a SHA-256 digest (64 hex characters)")
	}
	return s, nil
}

// SidecarDigest reads <archive>.sha256 when it exists. ok is false when there is none.
func SidecarDigest(archive string) (digest string, ok bool, err error) {
	data, err := os.ReadFile(archive + ".sha256")
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	digest, err = ParseDigest(string(data))
	if err != nil {
		return "", false, fmt.Errorf("%s.sha256: %w", archive, err)
	}
	return digest, true, nil
}

// InstallArchive verifies the archive at archivePath and installs it as
// versions/<version>. Nothing appears under versions/ unless every check passes.
func (s Store) InstallArchive(archivePath string, opts InstallOptions) (Installed, error) {
	out := Installed{Source: opts.Source}
	if out.Source == "" {
		out.Source = DigestNone
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return out, err
	}
	defer func() { _ = f.Close() }()
	digest, err := fileDigest(f)
	if err != nil {
		return out, fmt.Errorf("read archive: %w", err)
	}
	out.ArchiveSHA256 = digest
	if opts.ExpectedSHA256 != "" && digest != opts.ExpectedSHA256 {
		return out, fmt.Errorf("archive SHA-256 is %s, expected %s (from %s): the file is corrupt, incomplete or not the expected release", digest, opts.ExpectedSHA256, out.Source)
	}
	versions := s.versionsDir()
	if err := os.MkdirAll(versions, 0o700); err != nil {
		return out, err
	}
	s.removeStaleStaging()
	staging, err := os.MkdirTemp(versions, ".staging-")
	if err != nil {
		return out, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(staging)
		}
	}()
	extracted, err := extract(io.NewSectionReader(f, 0, 1<<62), staging)
	if err != nil {
		return out, err
	}
	m, manifestData, err := verifyExtracted(staging, extracted)
	if err != nil {
		return out, err
	}
	if out.Signature, err = checkSignature(staging, extracted, manifestData, opts.AllowUnsigned); err != nil {
		return out, err
	}
	out.Manifest = m
	target := filepath.Join(versions, m.Version)
	out.Dir = target
	if _, err := os.Stat(target); err == nil {
		if sameInstalled(target, staging) {
			out.Reused = true
			return out, nil
		}
		// A damaged or different copy of the same version (for example one installed unsigned): move it aside, then
		// publish the one just verified.
		trash := filepath.Join(versions, ".trash-"+randomSuffix())
		if err := rename(target, trash); err != nil {
			return out, fmt.Errorf("replace existing %s: %w", m.Version, err)
		}
		defer func() { _ = os.RemoveAll(trash) }()
	}
	if err := rename(staging, target); err != nil {
		// Another process published the same version first: accept it if it is exactly what was just verified.
		if sameInstalled(target, staging) {
			out.Reused = true
			return out, nil
		}
		return out, fmt.Errorf("publish %s: %w", m.Version, err)
	}
	keep = true
	return out, nil
}

// sameInstalled reports whether the installed dir verifies and holds the same manifest.json and manifest.json.sig as
// staging, the archive just verified. Only then may the install keep dir: a copy that verifies against its own,
// different manifest (installed with --allow-unsigned, or by a CLI that did not check signatures) is not the archive
// whose signature was checked.
func sameInstalled(dir, staging string) bool {
	if _, err := VerifyDir(dir); err != nil {
		return false
	}
	for _, name := range []string{ManifestFile, SignatureFile} {
		have, haveErr := os.ReadFile(filepath.Join(dir, name))
		want, wantErr := os.ReadFile(filepath.Join(staging, name))
		if (haveErr == nil) != (wantErr == nil) || !bytes.Equal(have, want) {
			return false
		}
	}
	return true
}

// extracted is what extract wrote: each content file's size and digest.
type extracted map[string]ManifestFileEntry

func extract(r io.Reader, dir string) (extracted, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("archive is not gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(gz)
	files := extracted{}
	var total int64
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive is damaged or truncated: %w", err)
		}
		entries++
		if entries > maxEntries {
			return nil, fmt.Errorf("archive has more than %d entries", maxEntries)
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			// A directory carries no data. One that claims a size would make the reader inflate (and
			// skip) that many bytes, uncounted by the limits below: refused.
			if h.Size != 0 {
				return nil, fmt.Errorf("archive entry %q is a directory with a size", h.Name)
			}
			name = strings.TrimSuffix(name, "/")
			if name == "." || name == "" {
				continue
			}
			if name != BrowserDir {
				if err := checkEntryName(name); err != nil {
					return nil, fmt.Errorf("archive entry %q: %w", h.Name, err)
				}
			}
			if err := root.MkdirAll(name, 0o700); err != nil {
				return nil, err
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("archive entry %q is not a regular file (links and devices are refused)", h.Name)
		}
		if err := checkEntryName(name); err != nil {
			return nil, fmt.Errorf("archive entry %q: %w", h.Name, err)
		}
		if _, dup := files[name]; dup {
			return nil, fmt.Errorf("archive holds %q twice", name)
		}
		if h.Size > maxFileBytes {
			return nil, fmt.Errorf("archive entry %q is larger than %d bytes", name, maxFileBytes)
		}
		total += h.Size
		if total > maxTotalBytes {
			return nil, fmt.Errorf("archive is larger than %d bytes uncompressed", maxTotalBytes)
		}
		if parent := path.Dir(name); parent != "." {
			if err := root.MkdirAll(parent, 0o700); err != nil {
				return nil, err
			}
		}
		entry, err := writeEntry(root, name, tr, h.Size)
		if err != nil {
			return nil, fmt.Errorf("archive entry %q: %w", name, err)
		}
		files[name] = entry
	}
	return files, nil
}

func writeEntry(root *os.Root, name string, r io.Reader, size int64) (ManifestFileEntry, error) {
	// Read-only once written: an installed version is never edited in place (the open handle can still write it).
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return ManifestFileEntry{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, size))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return ManifestFileEntry{}, err
	}
	return ManifestFileEntry{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// checkSignature verifies the extracted manifest.json.sig, if the archive had one, against the trusted keys.
func checkSignature(dir string, files extracted, manifest []byte, allowUnsigned bool) (Signature, error) {
	var sig []byte
	_, present := files[SignatureFile]
	if present {
		var err error
		if sig, err = os.ReadFile(filepath.Join(dir, SignatureFile)); err != nil {
			return Signature{}, err
		}
	}
	return verifySignature(manifest, sig, present, allowUnsigned, trustedKeys)
}

// verifyExtracted compares what was extracted with manifest.json, exactly.
func verifyExtracted(dir string, files extracted) (Manifest, []byte, error) {
	if _, ok := files[ManifestFile]; !ok {
		return Manifest{}, nil, errors.New("archive has no " + ManifestFile)
	}
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return Manifest{}, nil, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return m, nil, err
	}
	listed := make(map[string]bool, len(m.Files))
	for _, want := range m.Files {
		listed[want.Path] = true
		got, ok := files[want.Path]
		if !ok {
			return m, nil, fmt.Errorf("archive lacks %q, which the manifest lists", want.Path)
		}
		if got.Size != want.Size || got.SHA256 != want.SHA256 {
			return m, nil, fmt.Errorf("archive file %q does not match the manifest (size or SHA-256 differ)", want.Path)
		}
	}
	for name := range files {
		if name != ManifestFile && name != SignatureFile && !listed[name] {
			return m, nil, fmt.Errorf("archive holds %q, which the manifest does not list", name)
		}
	}
	return m, data, nil
}

// VerifyDir re-checks an installed version against its manifest.
func VerifyDir(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return Manifest{}, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return m, err
	}
	if filepath.Base(dir) != m.Version {
		return m, fmt.Errorf("directory %s holds web UI %s", filepath.Base(dir), m.Version)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return m, err
	}
	defer func() { _ = root.Close() }()
	for _, want := range m.Files {
		f, err := root.Open(want.Path)
		if err != nil {
			return m, fmt.Errorf("installed web UI %s lacks %s: %w", m.Version, want.Path, err)
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return m, err
		}
		if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return m, fmt.Errorf("installed web UI %s: %s was changed after install", m.Version, want.Path)
		}
	}
	return m, nil
}

func fileDigest(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s Store) removeStaleStaging() {
	entries, _ := os.ReadDir(s.versionsDir())
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".staging-") && !strings.HasPrefix(e.Name(), ".trash-") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleStagingAge {
			_ = os.RemoveAll(filepath.Join(s.versionsDir(), e.Name()))
		}
	}
}

func randomSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
