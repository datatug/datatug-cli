package datatugui

import (
	"fmt"

	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// Identifiers of the root modules. They are also the first element of the
// screen path persisted by dtstate, which is how the app returns to the screen
// the user left.
const (
	ScreenProjects   = "projects"
	ScreenViewers    = "viewers"
	ScreenSettings   = "settings"
	ScreenAPIMonitor = "api_monitor"
)

// Module is a root screen of the app, listed in the main menu.
type Module struct {
	// ID identifies the module and is persisted as the current screen path;
	// use one of the Screen constants.
	ID string
	// Text is the menu label.
	Text string
	// Shortcut is the key that opens the module from the menu.
	Shortcut rune
	// Root builds the page of the module. Its Content is the module's root
	// screen. Menu is ignored: the shared main menu always stays. An empty
	// Title defaults to Text; Focus is set by the app.
	Root func() nav.Page
}

// page returns the page a module opens with the given focus.
func (m Module) page(focus nav.FocusTo) nav.Page {
	page := m.Root()
	if page.Title == "" {
		page.Title = m.Text
	}
	page.Menu = nil
	page.Focus = focus
	return page
}

// menuItem returns the main menu entry of the module.
func (m Module) menuItem() widgets.MenuItem {
	return widgets.MenuItem{ID: m.ID, Label: m.Text, Shortcut: m.Shortcut}
}

// checkModules panics for a module without an ID or a Root, or with an ID that
// is already used: they are programming errors caught at startup.
func checkModules(modules []Module) {
	seen := make(map[string]string, len(modules))
	for i, m := range modules {
		if m.ID == "" || m.Root == nil {
			panic(fmt.Sprintf("datatugui: modules[%d] %q needs an ID and a Root", i, m.Text))
		}
		if other, ok := seen[m.ID]; ok {
			panic(fmt.Sprintf("datatugui: duplicate module %q: adding %q, already registered %q", m.ID, m.Text, other))
		}
		seen[m.ID] = m.Text
	}
}

// indexOf returns the position of the module with the given ID, or -1.
func indexOf(modules []Module, id string) int {
	for i, m := range modules {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// StartScreen resolves the module to show at startup from the persisted screen
// path (for example "viewers/sql"): its first element when a module of that ID
// is registered, otherwise the projects screen, otherwise the first module.
func StartScreen(modules []Module, currentScreenPath string) string {
	first := currentScreenPath
	for i := 0; i < len(first); i++ {
		if first[i] == '/' {
			first = first[:i]
			break
		}
	}
	for _, id := range []string{first, ScreenProjects} {
		if indexOf(modules, id) >= 0 {
			return id
		}
	}
	if len(modules) > 0 {
		return modules[0].ID
	}
	return ""
}
