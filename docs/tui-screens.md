# How to write a DataTug screen

The DataTug terminal UI is a Bubble Tea v2 program. Every screen is an Elm-style
model hosted by the `strongo-tui` navigation shell (`pkg/nav`); the application
layer is `apps/datatugapp/datatugui`. Read `strongo-tui/README.md`,
`pkg/nav/doc.go` and `examples/demo` first; this guide only adds what is specific
to DataTug. The reference for style and tests is `datatug chat` (`pkg/chat`).

Design rule: screens are rewritten idiomatically, not ported line by line. There
is no mutable widget graph, no callback setters and no goroutine that touches the
UI.

## The rules

1. **A screen is a model.** It implements `nav.Screen`: `Init() tea.Cmd`,
   `Update(tea.Msg) (nav.Screen, tea.Cmd)`, `View() string`. State lives in the
   value. It receives its size as `tea.WindowSizeMsg` and focus as
   `nav.ScreenFocusMsg` from the shell and forwards focus to its components.
2. **Behaviour is a message.** Components report with messages
   (`widgets.ItemSelectedMsg`, `grid.RowActivatedMsg`, `widgets.SubmitMsg`, ...);
   the screen handles them in `Update`. No callbacks, no function fields on
   models.
3. **Async work is a `tea.Cmd`** that returns a result message. Nothing calls
   `Send`, nothing mutates a model from a goroutine. Errors travel in the result
   message and are reported with `datatugui.ReportError`, never with `panic`.
4. **Navigation is a message**: `nav.Push`, `Pop`, `PopTo`, `Replace`,
   `SetPanels`, `SetFocus`, `Alert`, `ShowError`, plus the DataTug helpers below.
   A pushed page adds a breadcrumb; do not manage breadcrumbs by hand except with
   `nav.SetBreadcrumbs` for a flow that is not a stack.
5. **Keys are `key.Binding`s** matched with `key.Matches` in `Update` and listed
   through `ShortHelp() []key.Binding` (shown in the actions bar).
6. **Every table is `strongo-tui/pkg/grid`.** Lists, text, inputs, tabs and modals
   come from `strongo-tui/pkg/widgets` (built on `bubbles/v2`); trees are
   `widgets.Tree`; highlighted YAML is `pkg/highlight`. Colours and styles come
   from `strongo-tui/pkg/theme`; a screen never builds a `lipgloss` style with a
   colour literal.
7. **Optional interfaces** tell the shell more: `nav.Titled` (panel title),
   `widgets.Boundary` (`AtEdge(dir)`: may an arrow key leave the screen; without
   it the screen keeps its arrows), `widgets.Editor` (`Editing()`: a text input is
   active, so application keys such as Ctrl+W step aside), `nav.KeyCapturer`.
8. **Persisted screen.** Root modules are recorded by the app. A deeper screen that
   should be resumable returns `datatugui.ScreenOpened("viewers/sql", "SQL")` from
   its `Init`; the first path element must be a module ID.

## The app-level contracts (`apps/datatugapp/datatugui`)

```go
// A root module: one entry of the main menu.
type Module struct {
	ID       string          // datatugui.ScreenProjects | ScreenViewers | ScreenSettings | ScreenAPIMonitor
	Text     string          // menu label
	Shortcut rune            // menu shortcut
	Root     func() nav.Page // Title (defaults to Text) + Content; Menu is ignored
}

datatugui.Open(id string, focus nav.FocusTo) tea.Cmd        // go back to a root screen
datatugui.Drill(title string, content nav.Screen) tea.Cmd   // push a page one level deeper
datatugui.ReportError(what string, err error) tea.Cmd      // log + nav.ShowError; nil err -> nil
datatugui.ScreenOpened(path, name string) tea.Cmd           // telemetry + persisted screen path
datatugui.Run(modules []Module, opts Options, ...tea.ProgramOption) error
```

The shared main menu (`datatugui.MainMenu`) is built once by the app and mounted in
the root page; a module never builds or embeds it, and its pages leave `Menu` nil
so the menu stays. Highlighting a menu item opens that module with focus kept on
the menu (`OpenModuleMsg`), Enter or Right moves focus to the content, and the
last item, Exit, saves an empty screen path and quits. The app records
`ScreenOpened(module.ID, module.Text)` for every module it opens and resumes on
the persisted one at startup (viewers, settings, api_monitor, otherwise projects).

A page that needs its own menu (for example the SQL browser's Tables / Views
menu) sets `nav.Page.Menu`; the shell restores the main menu when the page is
popped. Application-wide messages (`OpenModuleMsg`, `OpenWebUIMsg`) are handled by
`datatugui.App` before the shell, so they work whichever menu is showing.

### Constructors each area exports

The lead wires them in `apps/datatugapp/commands/cmd_ui.go`; these names are fixed.

| Package | Exports |
|---|---|
| `dtproject` | `Module() datatugui.Module` (ID `projects`, "Projects", `p`) |
| `dtviewers` | `Module(viewers ...Viewer) datatugui.Module` (ID `viewers`, "Viewers", `v`); `type Viewer struct{ ID ViewerID; Name, Description string; Shortcut rune; Root func() nav.Page }`; `GetSQLiteDbContext(path string) *SqlDBContext` stays |
| `dbviewer` | `Viewer() dtviewers.Viewer` (DB viewer, `1`); `DbHomePage(dbContext dtviewers.DbContext) nav.Page` (used by `datatug ui -f file.db` for SQLite) |
| `gcloudui`, `awsui`, `azureui` | `Viewer() dtviewers.Viewer` each |
| `dtsettings` | `Module() datatugui.Module` (ID `settings`) |
| `dtapiservice` | `Module() datatugui.Module` (ID `api_monitor`) |

There is no package-level registry: a module is a value, and `cmd_ui.go` passes
the list to `datatugui.Run`. Viewer values are passed to `dtviewers.Module`. The
`dtviewers` list of viewers is a `List` screen whose selection pushes the viewer's
`Root()` page.

## Worked example: menu highlight to grid, load, drill-down, error

```go
package dtproject

// Module registers the projects screen in the main menu.
func Module() datatugui.Module {
	return datatugui.Module{
		ID: datatugui.ScreenProjects, Text: "Projects", Shortcut: 'p',
		Root: func() nav.Page { return nav.Page{Content: newProjects()} },
	}
}

// projectsLoaded is the result of loadProjects.
type projectsLoaded struct {
	refs []*dtconfig.ProjectRef
	err  error
}

// loadProjects runs off the event loop (a tea.Cmd) and reports with a message.
func loadProjects() tea.Cmd {
	return func() tea.Msg {
		refs, err := readProjectRefs()
		return projectsLoaded{refs: refs, err: err}
	}
}

type projects struct {
	grid    *grid.Model
	w, h    int
	focused bool
}

func newProjects() projects { return projects{} }

func (p projects) Init() tea.Cmd { return loadProjects() }

func (p projects) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.w, p.h = msg.Width, msg.Height
		if p.grid != nil {
			p.grid.SetSize(p.w, p.h)
		}
		return p, nil
	case nav.ScreenFocusMsg:
		p.focused = msg.Focused
		if p.grid != nil {
			p.grid.SetFocused(p.focused)
		}
		return p, nil
	case projectsLoaded:
		if msg.err != nil {
			return p, datatugui.ReportError("load projects", msg.err) // shown in the panel, logged
		}
		p.grid = newProjectsGrid(msg.refs)
		p.grid.SetSize(p.w, p.h)
		p.grid.SetFocused(p.focused)
		return p, nil
	case grid.RowActivatedMsg: // Enter on a row: drill down, adds a breadcrumb
		return p, datatugui.Drill(msg.Row.Key, newProject(msg.Row.Key)) // Row.Key = project ID
	}
	if p.grid == nil {
		return p, nil
	}
	_, cmd := p.grid.Update(msg)
	return p, cmd
}

func (p projects) View() string {
	if p.grid == nil {
		return widgets.Fit("Loading projects...", p.w, p.h)
	}
	return widgets.Fit(p.grid.View(p.w, p.focused), p.w, p.h)
}

func (p projects) Title() string                     { return "Projects" }
func (p projects) AtEdge(dir widgets.Direction) bool { return p.grid == nil || p.grid.AtEdge(dir) }
func (p projects) Editing() bool                     { return p.grid != nil && p.grid.Editing() }
```

`newProjectsGrid` builds `grid.New(columns, rows, grid.WithID("projects"),
grid.WithoutFrame(), ...)` with `grid.Row{Key: ref.ID, Values: ...}`; the screen
already sits in the shell's frame. The exact API is in `strongo-tui/pkg/grid`.

## Test pattern

Drive the shell without a terminal with `strongo-tui/pkg/nav/navtest`, and
components or screens directly with `pkg/uitest`:

```go
func TestProjectsDrillDown(t *testing.T) {
	restore := stubReadProjectRefs(t, twoProjects) // a seam, not a real config file
	defer restore()
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()}) // Init's Cmd runs, result delivered
	h.RequireContains("Demo project")
	h.Press("down", "enter")
	h.RequireContains("Second project") // pushed page; h.Model().Breadcrumbs() has one more crumb
}

func TestProjectsLoadError(t *testing.T) {
	defer stubReadProjectRefs(t, nil, errors.New("no config"))()
	h := navtest.New(t, nav.Page{Title: "T", Content: newProjects()})
	h.RequireContains("load projects: no config")
}
```

Use `h.Send(msg)` for any message, `h.Type`, `h.Click`, `h.Resize`, and
`uitest.Msgs(cmd)` to assert what a component emitted. For the app itself see
`datatugui/app_test.go` (a small driver over `datatugui.App`).

### Testing rules

- **100.0% statement coverage of every package you touch** is the exit gate of
  each port task and of `datatug` as a whole. Check with
  `wb run -- go test -cover ./<pkg>/` and, for the gaps,
  `-coverprofile` plus `go tool cover -func`.
- **Seams instead of contorted tests.** Anything that touches the network, the
  filesystem outside `t.TempDir()`, the clock, the browser, the state file or a
  terminal goes behind a package-level `var` (as in `datatugui`: `openURL`,
  `screenOpened`, `runProgram`) or an injected function, so a test replaces it. Do
  not start real processes or terminals; do not test through global state.
- **Hermetic tests.** Every test package has a `hermetic_testmain_test.go` calling
  `hermetictest.Main(m)` and must never read or write the real home, XDG config or
  cache directories. `scripts/check-hermetic-tests.sh` runs the whole suite with an
  empty `HOME` and fails when anything is left behind; run it before finishing.
- Behaviours covered by the previous tests of the area are ported, not dropped.
- Tests assert on rendered text and emitted messages, not on model internals.
- `wb run -- go vet` and `gofmt` must be clean. Bare `go build/vet/test` is blocked
  in managed worktrees.

## Mapping from the previous imperative shell (for porters)

| Previous idiom (`pkg/sneatview`, `pkg/sneatv`) | Now |
|---|---|
| `GoXxxScreen(tui, focusTo) error` builds panels and calls `tui.SetPanels` | a `Root() nav.Page` or a `nav.Push(nav.Page{...})`; `Init` starts loading |
| `datatugui.RegisterMainMenuItem` / `NewDataTugMainMenu(tui, active)` in each screen | `datatugui.Module` values; the shared menu is mounted by the app, pages leave `Menu` nil |
| breadcrumb `Clear` + `Push` per screen | `Page.Title`; `datatugui.Drill` / `nav.Push` per level |
| a list with a per-item selected function | `widgets.List` + `widgets.ItemSelectedMsg` / `ItemHighlightedMsg` |
| a table of cell objects | `grid.New(columns, rows)`; `RowActivatedMsg`, `SelectionChangedMsg` |
| a text view | `widgets.TextPane` (viewport) with ANSI text; `pkg/highlight` for YAML |
| a tree view with node objects | `widgets.Tree`; `NodeHighlightedMsg`, `NodeSelectedMsg` |
| a form with items and buttons | `widgets.Form`; `SubmitMsg`, `CancelMsg`, `FieldChangedMsg` |
| an input hook on a widget | `key.Binding` in `Update`; `widgets.Boundary` for edge navigation |
| queueing a UI update from a goroutine | a `tea.Cmd` returning a result message |
| `tui.SetFocus(...)`, focusing content/menu | `nav.SetFocus(nav.FocusToContent)` or `Page.Focus` |
| `tui.ShowAlert(...)` / an error modal | `nav.Alert(...)`, `nav.ShowError(err)`, `datatugui.ReportError` |
| a `panic(err)` on an action error | `datatugui.ReportError(what, err)`: log and show, never swallow |
| stopping the application | `tea.Quit` |
| `dtstate.SaveCurrentScreePath` + `dtlog.ScreenOpened` per screen | `datatugui.ScreenOpened(path, name)` (the app does it for modules) |
| the global application variable | none: no global UI state; the shell is the program's model |

## Panics and the terminal

`datatugui.Run` recovers a panic in `Update` or `View`, quits the program (Bubble
Tea restores the terminal) and raises the panic again, so `main`'s recovery reports
it (log, telemetry, stack, exit code 1) on a sane terminal. Panics inside a `tea.Cmd`
are caught by Bubble Tea and surface as a program error. Code that can fail returns
an error message instead of panicking; panics are for programming errors only.
