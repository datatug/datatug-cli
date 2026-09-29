package datatugui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/strongo/logus"
	"github.com/strongo/strongo-tui/pkg/nav"
)

// Seams over telemetry and persisted state, replaced in tests.
var (
	screenOpened          = dtlog.ScreenOpened
	saveCurrentScreenPath = dtstate.SaveCurrentScreePath
	saveScreenPathSync    = dtstate.SaveCurrentScreePathSync
	logErrorf             = logus.Errorf
)

// OpenModuleMsg opens the root screen of a module: the stack is reduced to the
// root page and the module's page is pushed, its menu item highlighted.
type OpenModuleMsg struct {
	ID    string
	Focus nav.FocusTo
}

// Open returns the command that opens the module with the given ID, for a
// screen that has to go back to a root screen, such as after a project was
// created.
func Open(id string, focus nav.FocusTo) tea.Cmd {
	return func() tea.Msg { return OpenModuleMsg{ID: id, Focus: focus} }
}

// ScreenOpened returns the command that records that a screen was opened:
// telemetry and the persisted screen path, whose first element (the module ID)
// is where the next start resumes. The app runs it for every module; a screen
// deeper than the root may run it with a longer path such as "viewers/sql".
func ScreenOpened(path, name string) tea.Cmd {
	return func() tea.Msg {
		screenOpened(path, name)
		saveCurrentScreenPath(path)
		return nil
	}
}

// Drill returns the command that opens a screen one level deeper: a page whose
// title is added to the breadcrumbs and whose content takes focus. The page
// keeps the menu that is showing.
func Drill(title string, content nav.Screen) tea.Cmd {
	return nav.Push(nav.Page{Title: title, Content: content})
}

// ReportError returns the command that logs a failed action and shows the error
// in the content panel, where the previous imperative shell panicked. what says
// what was being done. A nil err yields no command.
func ReportError(what string, err error) tea.Cmd {
	if err == nil {
		return nil
	}
	logErrorf(context.Background(), "%s: %v", what, err)
	return nav.ShowError(fmt.Errorf("%s: %w", what, err))
}
