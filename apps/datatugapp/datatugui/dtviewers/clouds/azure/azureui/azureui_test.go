package azureui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
)

func TestViewer(t *testing.T) {
	v := Viewer()
	assert.Equal(t, viewerID, v.ID)
	assert.Equal(t, "Microsoft Azure", v.Name)
	assert.Equal(t, 'm', v.Shortcut)
	navtest.New(t, v.Root()).RequireContains("Azure is not implemented yet.")
}
