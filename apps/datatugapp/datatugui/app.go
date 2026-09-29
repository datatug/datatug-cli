package datatugui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/strongo-tui/pkg/nav"
)

// RootTitle is the root breadcrumb.
const RootTitle = "⛴ DataTug"

// Options customise the app.
type Options struct {
	// Start is the ID of the module shown first; see StartScreen. An unknown or
	// empty ID shows the first module.
	Start string
	// Initial, when not nil, is a page pushed on top of the start module, for
	// example the database opened with `datatug ui -f file.db`.
	Initial *nav.Page
}

// startMsg opens the start module once the program runs.
type startMsg struct{}

// crash keeps the panic recovered by an App, shared by its copies.
type crash struct{ value any }

// App is the root tea.Model of the DataTug terminal UI. It hosts the nav shell,
// handles the application-wide messages (OpenModuleMsg, OpenWebUIMsg) that must
// work whichever menu is showing, and turns a panic in Update or View into an
// orderly exit: the program quits, Bubble Tea restores the terminal, and Run
// raises the panic again so the caller's recovery reports it.
type App struct {
	shell   nav.Model
	modules []Module
	opts    Options
	crash   *crash
}

var _ tea.Model = App{}

// RootPage returns the root page of the app: the breadcrumb, the shared main
// menu and an empty content until the start module opens. It panics for an
// invalid or duplicate module.
func RootPage(modules []Module, opts Options) nav.Page {
	start := opts.Start
	if indexOf(modules, start) < 0 && len(modules) > 0 {
		start = modules[0].ID
	}
	return nav.Page{
		Title:   RootTitle,
		Menu:    NewMainMenu(modules),
		Content: nav.Static("DataTug", ""),
		Focus:   nav.FocusToMenu,
		OnCrumb: Open(start, nav.FocusToMenu),
	}
}

// New creates the app.
func New(modules []Module, opts Options, navOptions ...nav.Option) App {
	root := RootPage(modules, opts)
	navOptions = append([]nav.Option{nav.WithActions(webUIAction())}, navOptions...)
	return App{
		shell:   nav.New(root, navOptions...),
		modules: slices.Clone(modules),
		opts:    opts,
		crash:   &crash{},
	}
}

// Init implements tea.Model.
func (a App) Init() tea.Cmd {
	return tea.Batch(a.shell.Init(), func() tea.Msg { return startMsg{} })
}

// Update implements tea.Model.
func (a App) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			a.crash.value = r
			model, cmd = a, tea.Quit
		}
	}()
	switch msg := msg.(type) {
	case startMsg:
		return a.start()
	case OpenModuleMsg:
		return a.open(msg)
	case OpenWebUIMsg:
		return a, OpenCurrentScreenInWebUI()
	}
	return a.forward(msg)
}

// forward hands a message to the shell.
func (a App) forward(msg tea.Msg) (App, tea.Cmd) {
	shell, cmd := a.shell.Update(msg)
	a.shell = shell.(nav.Model)
	return a, cmd
}

// start opens the start module and then the initial page.
func (a App) start() (App, tea.Cmd) {
	a, cmd := a.open(OpenModuleMsg{ID: a.opts.Start, Focus: nav.FocusToMenu})
	if a.opts.Initial == nil {
		return a, cmd
	}
	a, push := a.forward(nav.PushMsg{Page: *a.opts.Initial})
	return a, tea.Batch(cmd, push)
}

// open shows a module: the stack goes back to the root page, the module's page
// is pushed and its menu item is highlighted.
func (a App) open(msg OpenModuleMsg) (App, tea.Cmd) {
	i := indexOf(a.modules, msg.ID)
	if i < 0 {
		if len(a.modules) == 0 {
			return a, ReportError("open module", fmt.Errorf("no modules registered"))
		}
		i = 0
	}
	module := a.modules[i]
	a, popped := a.forward(nav.PopToMsg{Depth: 1})
	a, pushed := a.forward(nav.PushMsg{Page: module.page(msg.Focus)})
	a, selected := a.forward(selectModuleMsg{id: module.ID})
	return a, tea.Batch(popped, pushed, selected, ScreenOpened(module.ID, module.Text))
}

// View implements tea.Model.
func (a App) View() (view tea.View) {
	defer func() {
		if r := recover(); r != nil {
			a.crash.value = r
			view = tea.NewView("")
		}
	}()
	return a.shell.View()
}

// Shell returns the navigation shell, for tests that inspect the screens.
func (a App) Shell() nav.Model { return a.shell }

// runProgram runs a program to completion; it is the app's only path to a
// terminal, so tests replace it.
var runProgram = func(p *tea.Program) (tea.Model, error) { return p.Run() }

// Run assembles the app from modules and runs it full-screen until it exits.
// programOptions go to tea.NewProgram. A panic inside the app quits the program
// first, so the terminal is restored, and is then raised again from Run.
func Run(modules []Module, opts Options, programOptions ...tea.ProgramOption) error {
	app := New(modules, opts)
	_, err := runProgram(tea.NewProgram(app, programOptions...))
	if app.crash.value != nil {
		panic(app.crash.value)
	}
	if err != nil {
		return fmt.Errorf("terminal UI: %w", err)
	}
	return nil
}
