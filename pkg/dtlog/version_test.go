package dtlog

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVersion(t *testing.T) {
	assert.NotEmpty(t, version)
}

// withVersionVars overrides the package-level version/commit/date vars (and
// optionally the runtime/debug.ReadBuildInfo seam) for the duration of a
// test, restoring the originals on cleanup.
func withVersionVars(t *testing.T, v, c, d string, bi func() (*debug.BuildInfo, bool)) {
	t.Helper()
	origVersion, origCommit, origDate, origReadBuildInfo := version, commit, date, readBuildInfo
	version, commit, date = v, c, d
	if bi != nil {
		readBuildInfo = bi
	}
	t.Cleanup(func() {
		version, commit, date, readBuildInfo = origVersion, origCommit, origDate, origReadBuildInfo
	})
}

// REQ: default-placeholders, AC: dev-build-works — a binary built without
// -ldflags must report the literal placeholders exactly, on both surfaces.
func TestVersion_DefaultPlaceholders_NoBuildInfo(t *testing.T) {
	withVersionVars(t, devVersion, "none", "unknown", func() (*debug.BuildInfo, bool) {
		return nil, false
	})
	assert.Equal(t, "dev", Version())
	assert.Equal(t, "none", Commit())
	assert.Equal(t, "unknown", Date())
	assert.Equal(t, "datatug dev (none) unknown", VersionLine())
}

// REQ: runtime-debug-fallback — Go's own "(devel)" main-module placeholder
// must never leak into the flag output; it must degrade to "dev" instead.
func TestVersion_DevelBuildInfoIgnored(t *testing.T) {
	withVersionVars(t, devVersion, "none", "unknown", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	})
	assert.Equal(t, "dev", Version())
}

// A real module version surfaced by runtime/debug.ReadBuildInfo (e.g. a
// binary fetched via `go install module@vX.Y.Z`) is used as a fallback, with
// its leading "v" stripped (REQ: no-v-prefix).
func TestVersion_ReadBuildInfoFallback_StripsVPrefix(t *testing.T) {
	withVersionVars(t, devVersion, "none", "unknown", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true
	})
	assert.Equal(t, "1.2.3", Version())
}

// An empty ReadBuildInfo version is also ignored, same as "(devel)".
func TestVersion_ReadBuildInfoFallback_EmptyIgnored(t *testing.T) {
	withVersionVars(t, devVersion, "none", "unknown", func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: ""}}, true
	})
	assert.Equal(t, "dev", Version())
}

// REQ: ldflag-injection — an injected value is used verbatim and
// ReadBuildInfo is never consulted.
func TestVersion_InjectedValue(t *testing.T) {
	withVersionVars(t, "1.2.3", "abcdef0123456789abcdef0123456789abcdef01", "2026-08-30T00:00:00Z", nil)
	assert.Equal(t, "1.2.3", Version())
	assert.Equal(t, "abcdef0123456789abcdef0123456789abcdef01", Commit())
	assert.Equal(t, "2026-08-30T00:00:00Z", Date())
	assert.Equal(t, "datatug 1.2.3 (abcdef0123456789abcdef0123456789abcdef01) 2026-08-30T00:00:00Z", VersionLine())
}

// REQ: no-v-prefix — even a mistakenly "v"-prefixed injected value is
// normalized on the way out.
func TestVersion_InjectedValue_StripsVPrefix(t *testing.T) {
	withVersionVars(t, "v1.2.3", "none", "unknown", nil)
	assert.Equal(t, "1.2.3", Version())
}
