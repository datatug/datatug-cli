package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultReleaseRepo is the public repository of compiled frontend releases.
const DefaultReleaseRepo = "datatug/web-ui"

// ReleaseSource finds and downloads frontend releases on GitHub. It is used
// only when the person asks for a download (`web-ui install --latest`,
// `web-ui update`); nothing else in this package touches the network.
type ReleaseSource struct {
	Repo         string
	APIBase      string // https://api.github.com
	DownloadBase string // https://github.com
	Client       *http.Client
}

// Candidate is a release chosen for download.
type Candidate struct {
	Manifest   Manifest
	ArchiveURL string
	DigestURL  string
}

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// maxMetadataBytes bounds a release list, a manifest and a digest file.
const maxMetadataBytes = 8 << 20

func (rs ReleaseSource) client() *http.Client {
	if rs.Client != nil {
		return rs.Client
	}
	return http.DefaultClient
}

func (rs ReleaseSource) get(ctx context.Context, url string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	req.Header.Set("User-Agent", "datatug-cli")
	resp, err := rs.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	if n > limit {
		return fmt.Errorf("GET %s: response is larger than %d bytes", url, limit)
	}
	return nil
}

func (rs ReleaseSource) getBytes(ctx context.Context, url string) ([]byte, error) {
	var b strings.Builder
	if err := rs.get(ctx, url, &b, maxMetadataBytes); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// DownloadBase is where release assets are served from (https://github.com).
// Latest first reads releases/latest/download/manifest.json there: no API
// call, no rate limit. Only when that release cannot be served does it list
// releases through the API to find an older compatible one.
func (rs ReleaseSource) downloadBase() string {
	if rs.DownloadBase != "" {
		return strings.TrimSuffix(rs.DownloadBase, "/")
	}
	return "https://github.com"
}

// Latest returns the newest stable release that this CLI can serve.
func (rs ReleaseSource) Latest(ctx context.Context) (Candidate, error) {
	if c, err := rs.latestDirect(ctx); err == nil {
		return c, nil
	}
	return rs.latestListed(ctx)
}

func (rs ReleaseSource) latestDirect(ctx context.Context) (Candidate, error) {
	base := rs.downloadBase() + "/" + rs.Repo + "/releases/latest/download/"
	data, err := rs.getBytes(ctx, base+ManifestFile)
	if err != nil {
		return Candidate{}, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return Candidate{}, err
	}
	if IsPrerelease(m.Version) {
		return Candidate{}, errors.New("latest release is a pre-release")
	}
	if err := Compatible(m); err != nil {
		return Candidate{}, err
	}
	archive := base + "datatug-web-ui-" + m.Version + ".tar.gz"
	return Candidate{Manifest: m, ArchiveURL: archive, DigestURL: archive + ".sha256"}, nil
}

func (rs ReleaseSource) latestListed(ctx context.Context) (Candidate, error) {
	data, err := rs.getBytes(ctx, strings.TrimSuffix(rs.APIBase, "/")+"/repos/"+rs.Repo+"/releases?per_page=50")
	if err != nil {
		return Candidate{}, fmt.Errorf("list web UI releases: %w", err)
	}
	var releases []ghRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return Candidate{}, fmt.Errorf("list web UI releases: %w", err)
	}
	type tagged struct {
		version string
		rel     ghRelease
	}
	var stable []tagged
	for _, r := range releases {
		v := strings.TrimPrefix(r.TagName, "v")
		if r.Draft || r.Prerelease || !ValidVersion(v) || IsPrerelease(v) {
			continue
		}
		stable = append(stable, tagged{v, r})
	}
	sort.Slice(stable, func(i, j int) bool { return CompareVersions(stable[i].version, stable[j].version) > 0 })
	var reasons []error
	for _, t := range stable {
		c, err := rs.candidate(ctx, t.version, t.rel)
		if err != nil {
			reasons = append(reasons, err)
			continue
		}
		if err := Compatible(c.Manifest); err != nil {
			reasons = append(reasons, err)
			continue
		}
		return c, nil
	}
	if len(stable) == 0 {
		return Candidate{}, fmt.Errorf("no stable web UI release found in %s", rs.Repo)
	}
	return Candidate{}, fmt.Errorf("no release in %s can be served by this CLI: %w", rs.Repo, errors.Join(reasons...))
}

func (rs ReleaseSource) candidate(ctx context.Context, version string, r ghRelease) (Candidate, error) {
	archive := "datatug-web-ui-" + version + ".tar.gz"
	var c Candidate
	manifestURL := ""
	for _, a := range r.Assets {
		switch a.Name {
		case archive:
			c.ArchiveURL = a.URL
		case archive + ".sha256":
			c.DigestURL = a.URL
		case ManifestFile:
			manifestURL = a.URL
		}
	}
	if c.ArchiveURL == "" || c.DigestURL == "" || manifestURL == "" {
		return c, fmt.Errorf("release %s lacks %s, its .sha256 or %s", r.TagName, archive, ManifestFile)
	}
	data, err := rs.getBytes(ctx, manifestURL)
	if err != nil {
		return c, err
	}
	if c.Manifest, err = ParseManifest(data); err != nil {
		return c, fmt.Errorf("release %s: %w", r.TagName, err)
	}
	if c.Manifest.Version != version {
		return c, fmt.Errorf("release %s carries the manifest of %s", r.TagName, c.Manifest.Version)
	}
	return c, nil
}

// Download fetches the candidate's archive into dir and returns its path and expected digest.
func (rs ReleaseSource) Download(ctx context.Context, c Candidate, dir string) (archivePath, digest string, err error) {
	data, err := rs.getBytes(ctx, c.DigestURL)
	if err != nil {
		return "", "", err
	}
	if digest, err = ParseDigest(string(data)); err != nil {
		return "", "", fmt.Errorf("release digest: %w", err)
	}
	f, err := os.CreateTemp(dir, "datatug-web-ui-*.tar.gz")
	if err != nil {
		return "", "", err
	}
	err = rs.get(ctx, c.ArchiveURL, f, maxTotalBytes)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", "", err
	}
	return filepath.Clean(f.Name()), digest, nil
}
