package main

import (
	"io"
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/posthog/posthog-go"
)

// TestMain keeps every test in this package from reading or writing the
// real developer's home, XDG config, or XDG cache directories — see
// internal/hermetictest for why and scripts/check-hermetic-tests.sh for the
// CI gate that catches a regression here.
//
// It also makes sure no test of main() can send telemetry: the senders main
// calls are replaced by ones that do nothing, and a test that wants the real
// ones (to prove that telemetry off sends nothing) restores them itself, with
// the variables that turn telemetry off set. A test binary that creates a real
// PostHog client panics (see dtlog), so a missed stub fails loudly.
func TestMain(m *testing.M) {
	for _, name := range []string{dtlog.EnvTelemetry, dtlog.EnvDoNotTrack, dtlog.EnvCI} {
		_ = os.Unsetenv(name)
	}
	dtlogStart = func() {}
	dtlogEnqueue = func(posthog.Message) {}
	dtlogNotice = func(io.Writer) {}
	os.Exit(hermetictest.Main(m))
}
