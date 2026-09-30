package clouds

import (
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

func TestPlaceholderViewer(t *testing.T) {
	v := PlaceholderViewer("x", "Some Cloud", 's', "Not here yet.")
	assert.Equal(t, dtviewers.ViewerID("x"), v.ID)
	assert.Equal(t, "Some Cloud", v.Name)
	assert.Equal(t, "(not implemented yet)", v.Description)
	assert.Equal(t, 's', v.Shortcut)

	page := v.Root()
	require.IsType(t, Placeholder{}, page.Content)

	h := navtest.New(t, page)
	h.RequireContains("Some Cloud").RequireContains("Not here yet.")
}

func TestPlaceholder_Screen(t *testing.T) {
	p := NewPlaceholder("Title", "Body")
	assert.Nil(t, p.Init())
	assert.Equal(t, "Title", p.Title())
	assert.True(t, p.AtEdge(widgets.Up), "a short text is at the top")
	assert.True(t, p.AtEdge(widgets.Left))
}

func TestPlaceholder_ScrollsLongText(t *testing.T) {
	text := strings.Repeat("line\n", 100)
	h := navtest.New(t, nav.Page{Title: "T", Content: NewPlaceholder("Long", text)})
	h.Press("down", "down")
	// Not at the top any more: Up is kept by the text instead of leaving the screen.
	s, ok := h.Model().Content().(Placeholder)
	require.True(t, ok)
	assert.False(t, s.AtEdge(widgets.Up))
}

func TestPlaceholder_FocusFollowsTheShell(t *testing.T) {
	p := NewPlaceholder("T", "Body")
	s, cmd := p.Update(nav.ScreenFocusMsg{Focused: true})
	assert.Nil(t, cmd)
	assert.True(t, s.(Placeholder).pane.Focused())
	s, cmd = s.Update(nav.ScreenFocusMsg{Focused: false})
	assert.Nil(t, cmd)
	assert.False(t, s.(Placeholder).pane.Focused())
}
