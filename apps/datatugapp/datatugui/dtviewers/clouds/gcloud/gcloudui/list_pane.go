package gcloudui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// listPane is what the list screens of this package share: a list that takes its
// size and focus from the shell. A screen embeds it, handles the selection
// message of its list and passes everything else to update.
type listPane struct {
	list widgets.List
}

func newListPane(id string, items ...list.Item) listPane {
	return listPane{list: widgets.NewList(id, items...)}
}

// update sizes and focuses the list and forwards any other message to it.
func (p listPane) update(msg tea.Msg) (listPane, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.list.SetSize(msg.Width, msg.Height)
		return p, nil
	case nav.ScreenFocusMsg:
		if msg.Focused {
			p.list.Focus()
		} else {
			p.list.Blur()
		}
		return p, nil
	}
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, cmd
}

// View renders the list.
func (p listPane) View() string { return p.list.View() }

// AtEdge implements widgets.Boundary.
func (p listPane) AtEdge(dir widgets.Direction) bool { return p.list.AtEdge(dir) }

// Editing implements widgets.Editor.
func (p listPane) Editing() bool { return p.list.Editing() }

// ShortHelp implements nav.ShortHelper.
func (p listPane) ShortHelp() []key.Binding { return p.list.ShortHelp() }

// menuItem returns the MenuItem an ItemSelectedMsg carries.
func menuItem(msg widgets.ItemSelectedMsg) widgets.MenuItem {
	item, _ := msg.Item.(widgets.MenuItem)
	return item
}
