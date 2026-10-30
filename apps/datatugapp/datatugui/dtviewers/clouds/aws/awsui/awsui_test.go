package awsui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
)

func TestViewer(t *testing.T) {
	v := Viewer()
	assert.Equal(t, viewerID, v.ID)
	assert.Equal(t, "Amazon Web Services", v.Name)
	assert.Equal(t, 'a', v.Shortcut)
	navtest.New(t, v.Root()).RequireContains("AWS is not implemented yet.")
}
