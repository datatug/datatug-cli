package datatugui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// MenuID identifies the main menu list in its messages.
const MenuID = "datatug.menu"

// exitID is the ID of the last menu item.
const exitID = "exit"

// selectModuleMsg moves the highlight of the main menu without opening
// anything; the app sends it after it opened a module.
type selectModuleMsg struct{ id string }

// MainMenu is the menu panel shared by every root module: one item per module
// and a final Exit. Highlighting an item opens its module with focus kept on the
// menu, Enter moves focus to the content, and Exit saves an empty screen path
// and quits. A module never builds it: the app mounts it once in the root page
// and every module page keeps it.
type MainMenu struct {
	modules []Module
	list    widgets.List
}

var (
	_ nav.Screen       = MainMenu{}
	_ widgets.Boundary = MainMenu{}
	_ nav.ShortHelper  = MainMenu{}
)

// NewMainMenu creates the menu of modules. It panics for an invalid or
// duplicate module.
func NewMainMenu(modules []Module) MainMenu {
	checkModules(modules)
	items := make([]list.Item, 0, len(modules)+1)
	for _, module := range modules {
		items = append(items, module.menuItem())
	}
	items = append(items, widgets.MenuItem{ID: exitID, Label: "Exit", Shortcut: 'q'})
	return MainMenu{modules: modules, list: widgets.NewList(MenuID, items...)}
}

// Init implements nav.Screen.
func (m MainMenu) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (m MainMenu) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
		return m, nil
	case nav.ScreenFocusMsg:
		if msg.Focused {
			m.list.Focus()
		} else {
			m.list.Blur()
		}
		return m, nil
	case selectModuleMsg:
		if i := indexOf(m.modules, msg.id); i >= 0 {
			m.list.Select(i)
		}
		return m, nil
	case widgets.ItemHighlightedMsg:
		if msg.ID == MenuID && msg.Index < len(m.modules) {
			return m, Open(m.modules[msg.Index].ID, nav.FocusToMenu)
		}
		return m, nil
	case widgets.ItemSelectedMsg:
		if msg.ID != MenuID {
			return m, nil
		}
		if msg.Index < len(m.modules) {
			return m, nav.SetFocus(nav.FocusToContent)
		}
		return m, exit()
	}
	list, cmd := m.list.Update(msg)
	m.list = list
	return m, cmd
}

// exit forgets the current screen and quits.
func exit() tea.Cmd {
	return func() tea.Msg {
		saveScreenPathSync("")
		return tea.QuitMsg{}
	}
}

// View implements nav.Screen.
func (m MainMenu) View() string { return m.list.View() }

// AtEdge implements widgets.Boundary.
func (m MainMenu) AtEdge(dir widgets.Direction) bool { return m.list.AtEdge(dir) }

// ShortHelp implements nav.ShortHelper.
func (m MainMenu) ShortHelp() []key.Binding { return m.list.ShortHelp() }
