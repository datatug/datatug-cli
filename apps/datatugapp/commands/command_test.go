package commands

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/stretchr/testify/assert"
)

func TestCommandIsNotNil(t *testing.T) {
	cmd := DatatugCommand()
	assert.NotNil(t, cmd)
}

// `datatug --help` names the variables that turn telemetry off.
func TestRootHelpNamesTheTelemetrySwitches(t *testing.T) {
	help := DatatugCommand().Long
	for _, name := range []string{dtlog.EnvTelemetry + "=0", dtlog.EnvDoNotTrack + "=1", dtlog.EnvCI + "=true", "#telemetry"} {
		assert.Contains(t, help, name)
	}
}
