package dtapiservice

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/nav/navtest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func TestModule(t *testing.T) {
	m := Module()
	if m.ID != datatugui.ScreenAPIMonitor || m.Text != "API Monitor" || m.Shortcut != 'w' {
		t.Fatalf("unexpected module: %+v", m)
	}
	page := m.Root()
	if page.Content == nil {
		t.Fatal("root page has no content")
	}
	h := navtest.New(t, page)
	h.RequireContains("Open web UI: https://datatug.app/pwa/#api=localhost:8080")
	h.RequireContains("Web UI & Local API Service Monitor")
}

func TestMonitorScreen(t *testing.T) {
	var s nav.Screen = newMonitor()
	if cmd := s.Init(); cmd != nil {
		t.Fatal("Init must not start work")
	}
	s, _ = s.Update(tea.WindowSizeMsg{Width: 60, Height: 5})
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: true})
	if s.(nav.Titled).Title() != screenTitle {
		t.Fatalf("title = %q", s.(nav.Titled).Title())
	}
	if got := s.View(); got == "" {
		t.Fatal("empty view")
	}
	b := s.(widgets.Boundary)
	if !b.AtEdge(widgets.Left) || !b.AtEdge(widgets.Up) {
		t.Fatal("a short text is at every edge")
	}
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: false})
	_, cmd := s.Update(tea.MouseWheelMsg{})
	if cmd != nil {
		t.Fatal("unexpected command")
	}
}
