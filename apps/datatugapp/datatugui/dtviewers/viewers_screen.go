package dtviewers

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// Title is the title of the Viewers module and of its list.
const Title = "Viewers"

// listID identifies the viewers list in its messages.
const listID = "dtviewers.list"

// screenOpened is a seam over the persisted screen path and telemetry.
var screenOpened = datatugui.ScreenOpened

// Module returns the Viewers module of the main menu: a list of the given
// viewers whose selection pushes the viewer's page.
func Module(viewers ...Viewer) datatugui.Module {
	return datatugui.Module{
		ID: datatugui.ScreenViewers, Text: Title, Shortcut: 'v',
		Root: func() nav.Page { return nav.Page{Content: newViewersScreen(viewers)} },
	}
}

// viewersScreen lists the viewers.
type viewersScreen struct {
	viewers []Viewer
	list    widgets.List
}

var (
	_ nav.Screen       = viewersScreen{}
	_ nav.Titled       = viewersScreen{}
	_ nav.ShortHelper  = viewersScreen{}
	_ widgets.Boundary = viewersScreen{}
	_ widgets.Editor   = viewersScreen{}
)

func newViewersScreen(viewers []Viewer) viewersScreen {
	items := make([]list.Item, len(viewers))
	for i, v := range viewers {
		items[i] = widgets.MenuItem{ID: string(v.ID), Label: v.Name, Detail: v.Description, Shortcut: v.Shortcut}
	}
	return viewersScreen{viewers: viewers, list: widgets.NewList(listID, items...)}
}

// Init implements nav.Screen.
func (s viewersScreen) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (s viewersScreen) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case nav.ScreenFocusMsg:
		if msg.Focused {
			s.list.Focus()
		} else {
			s.list.Blur()
		}
		return s, nil
	case widgets.ItemSelectedMsg:
		if msg.ID == listID {
			return s, open(s.viewers[msg.Index])
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.list, cmd = s.list.Update(msg)
	return s, cmd
}

// open pushes the page of a viewer and records the screen.
func open(v Viewer) tea.Cmd {
	page := v.Root()
	if page.Title == "" {
		page.Title = v.Name
	}
	return tea.Batch(nav.Push(page), screenOpened(datatugui.ScreenViewers+"/"+string(v.ID), v.Name))
}

// View implements nav.Screen.
func (s viewersScreen) View() string { return s.list.View() }

// Title implements nav.Titled.
func (viewersScreen) Title() string { return Title }

// AtEdge implements widgets.Boundary.
func (s viewersScreen) AtEdge(dir widgets.Direction) bool { return s.list.AtEdge(dir) }

// Editing implements widgets.Editor.
func (s viewersScreen) Editing() bool { return s.list.Editing() }

// ShortHelp implements nav.ShortHelper.
func (s viewersScreen) ShortHelp() []key.Binding { return s.list.ShortHelp() }
