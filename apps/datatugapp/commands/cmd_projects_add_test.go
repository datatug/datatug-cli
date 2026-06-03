package commands

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/stretchr/testify/assert"
)

func TestProjectsAddCommand_RegistersFlags(t *testing.T) {
	cmd := projectsAddCommandArgs()
	for _, name := range []string{"project", "directory"} {
		assert.Truef(t, cmdHasFlag(cmd, name), "projects add command must register --%s flag", name)
	}
}

func TestGetProjPathsByID_PrefersLocalPathThenOrigin(t *testing.T) {
	cfg := dtconfig.Settings{
		Projects: []*dtconfig.ProjectRef{
			{ID: "local", Path: "/tmp/local"},
			{ID: "remote", Origin: "github.com/acme/remote"},
		},
	}
	paths := getProjPathsByID(cfg)
	assert.Equal(t, "/tmp/local", paths["local"], "a locally-added project must resolve by its Path")
	assert.Equal(t, "github.com/acme/remote", paths["remote"], "a project with only Origin must still resolve")
}
