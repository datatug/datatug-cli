package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCommandIsNotNil(t *testing.T) {
	cmd := DatatugCommand()
	assert.NotNil(t, cmd)
}

func TestWhoamiCommandIsRegistered(t *testing.T) {
	cmd, _, err := DatatugCommand().Find([]string{"whoami"})
	assert.NoError(t, err)
	assert.Equal(t, "whoami", cmd.Name())
}
