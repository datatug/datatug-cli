package dtlog

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// version, commit and date are injected at release-build time via linker
// flags (see .goreleaser.yaml):
//
//	-X github.com/datatug/datatug-cli/pkg/dtlog.version=<semver>
//	-X github.com/datatug/datatug-cli/pkg/dtlog.commit=<full-sha>
//	-X github.com/datatug/datatug-cli/pkg/dtlog.date=<rfc3339>
//
// A build without -ldflags (e.g. `go build`, `go run`, `go install` on a
// plain checkout) keeps the literal placeholders below — see
// spec/features/cli/version REQ: default-placeholders.
var (
	version = devVersion
	commit  = "none"
	date    = "unknown"
)

// devVersion is the literal placeholder for an unstamped build.
const devVersion = "dev"

// readBuildInfo is a seam over runtime/debug.ReadBuildInfo for tests.
var readBuildInfo = debug.ReadBuildInfo

// Version returns the bare semver with no leading "v" (REQ: no-v-prefix).
//
// When the linker did not inject a value it tries
// runtime/debug.ReadBuildInfo for a real module version (e.g. a binary
// fetched via `go install module@vX.Y.Z`), but it never surfaces Go's own
// "(devel)" placeholder for the main module (REQ: runtime-debug-fallback)
// and otherwise keeps the literal "dev" placeholder
// (REQ: default-placeholders).
func Version() string {
	v := version
	if v == devVersion {
		if bi, ok := readBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
	}
	return strings.TrimPrefix(v, "v")
}

// Commit returns the full git commit SHA the binary was built from, or the
// literal placeholder "none" when it was not injected.
func Commit() string {
	return commit
}

// Date returns the RFC 3339 build timestamp, or the literal placeholder
// "unknown" when it was not injected.
func Date() string {
	return date
}

// VersionLine returns the single line of output for the `datatug version`
// subcommand (REQ: subcommand-output), without a trailing newline.
func VersionLine() string {
	return fmt.Sprintf("datatug %s (%s) %s", Version(), Commit(), Date())
}
