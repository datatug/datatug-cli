// Package webui installs, selects and serves the compiled DataTug web app (the
// public archives of github.com/datatug/frontend) from the CLI's own HTTP server.
//
// Spec: datatug/backstage spec/features/local-frontend-distribution (decision 0006).
// An archive is a gzip tar of manifest.json plus the static app under browser/;
// it is verified file by file against the manifest before it becomes visible
// under the installed-versions directory, and the CLI activates it only when
// both sides of the compatibility contract hold (see Compatible).
package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// ManifestSchema is the only manifest schema this CLI reads.
const ManifestSchema = "datatug-frontend/1"

// ManifestName is the name every DataTug frontend archive declares.
const ManifestName = "datatug-frontend"

// ManifestFile and IndexFile are the two paths every archive must hold.
const (
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.json.sig"
	BrowserDir    = "browser"
	IndexFile     = BrowserDir + "/index.html"
)

// Manifest is manifest.json of a frontend archive.
type Manifest struct {
	Schema   string              `json:"schema"`
	Name     string              `json:"name"`
	Version  string              `json:"version"`
	Requires Requirements        `json:"requires"`
	Build    BuildInfo           `json:"build"`
	Files    []ManifestFileEntry `json:"files"`
}

// Requirements is what a frontend needs of the agent it runs against.
type Requirements struct {
	// LocalAPI is a range over the agent's local API contract version, e.g. ">=1.0 <2".
	LocalAPI string `json:"localApi"`
}

// BuildInfo says where an archive was built from. It is informational.
type BuildInfo struct {
	Commit string `json:"commit,omitempty"`
	Date   string `json:"date,omitempty"`
	Source string `json:"source,omitempty"`
}

// ManifestFileEntry is one file the archive holds besides manifest.json.
type ManifestFileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseManifest reads and validates manifest.json.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest.json is not valid JSON: %w", err)
	}
	return m, m.Validate()
}

// Validate checks the manifest's own consistency (not the files it lists).
func (m Manifest) Validate() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("manifest schema %q is not supported (want %q)", m.Schema, ManifestSchema)
	}
	if m.Name != ManifestName {
		return fmt.Errorf("manifest name %q is not %q", m.Name, ManifestName)
	}
	if !ValidVersion(m.Version) {
		return fmt.Errorf("manifest version %q is not a semantic version", m.Version)
	}
	if _, err := parseRange(m.Requires.LocalAPI); err != nil {
		return fmt.Errorf("manifest requires.localApi: %w", err)
	}
	seen := make(map[string]bool, len(m.Files))
	hasIndex := false
	for _, f := range m.Files {
		if err := checkEntryName(f.Path); err != nil {
			return fmt.Errorf("manifest file %q: %w", f.Path, err)
		}
		if f.Path == ManifestFile || f.Path == SignatureFile {
			return fmt.Errorf("manifest lists %q, which is not a content file", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("manifest lists %q twice", f.Path)
		}
		seen[f.Path] = true
		if f.Size < 0 || !sha256Hex.MatchString(f.SHA256) {
			return fmt.Errorf("manifest file %q has no valid size and lowercase sha256", f.Path)
		}
		if f.Path == IndexFile {
			hasIndex = true
		}
	}
	if !hasIndex {
		return errors.New("manifest does not list " + IndexFile)
	}
	return nil
}

// ValidVersion reports whether v is a SemVer 2.0 version without a leading "v".
func ValidVersion(v string) bool {
	return v != "" && !strings.HasPrefix(v, "v") && semver.IsValid("v"+v) && semver.Canonical("v"+v) == "v"+strings.SplitN(v, "+", 2)[0]
}

// CompareVersions compares two valid versions like semver.Compare.
func CompareVersions(a, b string) int {
	return semver.Compare("v"+a, "v"+b)
}

// IsPrerelease reports whether v carries a pre-release part.
func IsPrerelease(v string) bool {
	return semver.Prerelease("v"+v) != ""
}

// checkEntryName refuses any archive path that is not a plain relative path
// inside the archive's own tree: manifest.json, manifest.json.sig, or browser/...
func checkEntryName(name string) error {
	switch {
	case name == "":
		return errors.New("empty path")
	case strings.ContainsAny(name, "\\\x00"):
		return errors.New("path holds a backslash or NUL")
	case strings.HasPrefix(name, "/"):
		return errors.New("absolute path")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("path has an empty, . or .. segment")
		}
		if strings.HasPrefix(seg, ".") {
			return errors.New("path has a dot-file or dot-directory")
		}
	}
	if name == ManifestFile || name == SignatureFile {
		return nil
	}
	if !strings.HasPrefix(name, BrowserDir+"/") {
		return errors.New("path is outside " + BrowserDir + "/")
	}
	if strings.HasSuffix(name, ".map") {
		return errors.New("source maps are not allowed in a distributed archive")
	}
	return nil
}
