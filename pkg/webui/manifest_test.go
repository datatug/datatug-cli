package webui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseManifest(t *testing.T) {
	good := manifestFor("1.2.3", ">=1.0 <2", appFiles())
	m, err := ParseManifest([]byte(manifestJSON(t, good)))
	require.NoError(t, err)
	assert.Equal(t, "1.2.3", m.Version)

	_, err = ParseManifest([]byte("{"))
	assert.ErrorContains(t, err, "not valid JSON")
}

func TestManifestValidate(t *testing.T) {
	entry := func(path string) ManifestFileEntry {
		return ManifestFileEntry{Path: path, Size: 1, SHA256: strings.Repeat("a", 64)}
	}
	index := entry(IndexFile)
	base := func() Manifest {
		return Manifest{Schema: ManifestSchema, Name: ManifestName, Version: "1.0.0", Requires: Requirements{LocalAPI: ">=1.0"}, Files: []ManifestFileEntry{index}}
	}
	require.NoError(t, base().Validate())
	for name, tc := range map[string]struct {
		edit func(*Manifest)
		want string
	}{
		"schema":         {func(m *Manifest) { m.Schema = "datatug-web-ui/2" }, "schema"},
		"name":           {func(m *Manifest) { m.Name = "evil" }, "name"},
		"version":        {func(m *Manifest) { m.Version = "v1.0.0" }, "semantic version"},
		"range":          {func(m *Manifest) { m.Requires.LocalAPI = "1.0" }, "requires.localApi"},
		"traversal":      {func(m *Manifest) { m.Files = append(m.Files, entry("browser/../x")) }, ". or .."},
		"manifest":       {func(m *Manifest) { m.Files = append(m.Files, entry(ManifestFile)) }, "not a content file"},
		"signature":      {func(m *Manifest) { m.Files = append(m.Files, entry(SignatureFile)) }, "not a content file"},
		"duplicate":      {func(m *Manifest) { m.Files = append(m.Files, index) }, "twice"},
		"negative size":  {func(m *Manifest) { m.Files[0].Size = -1 }, "valid size"},
		"uppercase hash": {func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("A", 64) }, "lowercase sha256"},
		"no index":       {func(m *Manifest) { m.Files = []ManifestFileEntry{entry("browser/a.js")} }, "does not list"},
	} {
		m := base()
		tc.edit(&m)
		assert.ErrorContains(t, m.Validate(), tc.want, name)
	}
}

func TestCheckEntryName(t *testing.T) {
	for _, ok := range []string{ManifestFile, SignatureFile, IndexFile, "browser/media/a-ABCD1234.woff2", "browser/store/x:y"} {
		assert.NoError(t, checkEntryName(ok), ok)
	}
	for name, want := range map[string]string{
		"":                 "empty",
		`browser\a.js`:     "backslash",
		"browser/a\x00.js": "NUL",
		"/etc/passwd":      "absolute",
		"browser//a.js":    "empty, . or ..",
		"browser/./a.js":   "empty, . or ..",
		"../browser/a.js":  "empty, . or ..",
		"browser/.env":     "dot-file",
		"browser/.git/x":   "dot-file",
		"other/a.js":       "outside browser/",
		"browser":          "outside browser/",
		"browser/main.map": "source maps",
	} {
		assert.ErrorContains(t, checkEntryName(name), want, "%q", name)
	}
}

func TestVersions(t *testing.T) {
	for v, ok := range map[string]bool{
		"1.0.0": true, "1.0.0-rc.1": true, "1.0.0+build.5": true, "0.1.0": true,
		"": false, "v1.0.0": false, "1.0": false, "1": false, "01.0.0": false, "x": false,
	} {
		assert.Equal(t, ok, ValidVersion(v), "%q", v)
	}
	assert.Equal(t, 1, CompareVersions("1.10.0", "1.9.0"))
	assert.True(t, IsPrerelease("1.0.0-rc.1"))
	assert.False(t, IsPrerelease("1.0.0"))
}

func TestCompatible(t *testing.T) {
	m := func(version, rng string) Manifest {
		return Manifest{Version: version, Requires: Requirements{LocalAPI: rng}}
	}
	assert.NoError(t, Compatible(m("1.0.0", ">=1.0 <2")))
	for rng, ok := range map[string]bool{
		">=1.0 <2": true, ">=1.1": false, ">=1": true, "<=1.0": true, "<=0.9": false,
		">0.9": true, ">1.0": false, "<1.1": true, "<1.0": false, "=1.0": true, "=1": true, "=1.1": false,
		">=2": false, ">=0.5 <1": false, ">1 <3": false, ">0 <3": true,
	} {
		err := compatibleWith(m("1.0.0", rng), "1.0", "0.1.0")
		if ok {
			assert.NoError(t, err, rng)
		} else {
			assert.ErrorIs(t, err, ErrIncompatible, rng)
			assert.ErrorContains(t, err, "needs local API", rng)
		}
	}
	err := compatibleWith(m("0.0.9", ">=1"), "1.0", "0.1.0")
	assert.ErrorIs(t, err, ErrIncompatible)
	assert.ErrorContains(t, err, "older than 0.1.0")

	err = compatibleWith(m("1.0.0", "~1"), "1.0", "0.1.0")
	assert.ErrorIs(t, err, ErrIncompatible)
	assert.ErrorContains(t, err, "unreadable")

	err = compatibleWith(m("1.0.0", ">=1"), "x", "0.1.0")
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrIncompatible))
}

func TestParseRangeAndAPIVersion(t *testing.T) {
	for _, bad := range []string{"", "  ", "1.0", ">=1.0.0", ">=a", ">=01", ">=-1", ">=1.x", "=>1"} {
		_, err := parseRange(bad)
		assert.Error(t, err, "%q", bad)
	}
	r, err := parseRange(">=1.2 <2")
	require.NoError(t, err)
	assert.Equal(t, apiRange{{">=", apiVersion{1, 2}}, {"<", apiVersion{2, 0}}}, r)
	assert.Equal(t, -1, apiVersion{1, 0}.cmp(apiVersion{1, 1}))
	assert.Equal(t, 1, apiVersion{2, 0}.cmp(apiVersion{1, 9}))
	assert.Equal(t, 0, apiVersion{1, 1}.cmp(apiVersion{1, 1}))
}
