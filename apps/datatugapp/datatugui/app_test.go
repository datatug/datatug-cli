package datatugui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

// recorder captures what the app persists and logs.
type recorder struct {
	opened   []string
	saved    []string
	syncSave []string
	logged   []string
}

// fakeSeams replaces the telemetry, persistence and logging seams.
func fakeSeams(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	origOpened, origSave, origSync, origLog := screenOpened, saveCurrentScreenPath, saveScreenPathSync, logErrorf
	screenOpened = func(id, name string) { r.opened = append(r.opened, id+"|"+name) }
	saveCurrentScreenPath = func(path string) { r.saved = append(r.saved, path) }
	saveScreenPathSync = func(path string) { r.syncSave = append(r.syncSave, path) }
	logErrorf = func(_ context.Context, format string, args ...any) {
		r.logged = append(r.logged, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() {
		screenOpened, saveCurrentScreenPath, saveScreenPathSync, logErrorf = origOpened, origSave, origSync, origLog
	})
	return r
}

// driver runs an App the way the Bubble Tea loop does, without a terminal:
// commands are executed and the messages they produce are fed back.
type driver struct {
	t    *testing.T
	app  App
	quit bool
}

func newDriver(t *testing.T, modules []Module, opts Options) *driver {
	t.Helper()
	d := &driver{t: t, app: New(modules, opts)}
	d.run(d.app.Init())
	d.send(tea.WindowSizeMsg{Width: 120, Height: 40})
	return d
}

func (d *driver) send(msg tea.Msg) *driver {
	d.t.Helper()
	model, cmd := d.app.Update(msg)
	d.app = model.(App)
	return d.run(cmd)
}

func (d *driver) run(cmd tea.Cmd) *driver {
	d.t.Helper()
	for _, msg := range uitest.Msgs(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			d.quit = true
			continue
		}
		d.send(msg)
	}
	return d
}

func (d *driver) press(keys ...string) *driver {
	d.t.Helper()
	for _, k := range keys {
		d.send(uitest.Key(k))
	}
	return d
}

func (d *driver) screen() string { return uitest.Plain(d.app.Shell().Render()) }

func (d *driver) crumbs() string {
	var titles []string
	for _, c := range d.app.Shell().Breadcrumbs() {
		titles = append(titles, c.Title)
	}
	return strings.Join(titles, " > ")
}

func (d *driver) requireContains(s string) {
	d.t.Helper()
	if !strings.Contains(d.screen(), s) {
		d.t.Fatalf("screen does not contain %q:\n%s", s, d.screen())
	}
}

// textModule is a module whose root screen shows text.
func textModule(id, text string, shortcut rune) Module {
	return Module{
		ID: id, Text: strings.ToUpper(id[:1]) + id[1:], Shortcut: shortcut,
		Root: func() nav.Page { return nav.Page{Content: nav.Static(text, text)} },
	}
}

func testModules() []Module {
	return []Module{
		textModule(ScreenProjects, "projects body", 'p'),
		textModule(ScreenViewers, "viewers body", 'v'),
		textModule(ScreenSettings, "settings body", 's'),
	}
}

func TestAppStartsOnTheStartModule(t *testing.T) {
	r := fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenViewers})
	if got := d.crumbs(); got != RootTitle+" > Viewers" {
		t.Fatalf("breadcrumbs = %q", got)
	}
	if d.app.Shell().Zone() != nav.FocusToMenu {
		t.Errorf("focus = %v, want the menu", d.app.Shell().Zone())
	}
	d.requireContains("viewers body")
	if len(r.opened) != 1 || r.opened[0] != "viewers|Viewers" || r.saved[0] != "viewers" {
		t.Errorf("opened=%v saved=%v", r.opened, r.saved)
	}
	// The highlight follows the module.
	if got := d.screen(); !strings.Contains(got, "Exit") {
		t.Errorf("menu is missing Exit:\n%s", got)
	}
}

func TestAppUnknownStartShowsTheFirstModule(t *testing.T) {
	fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: "nope"})
	if got := d.crumbs(); got != RootTitle+" > Projects" {
		t.Fatalf("breadcrumbs = %q", got)
	}
}

func TestAppInitialPageIsPushedOverTheStartModule(t *testing.T) {
	fakeSeams(t)
	initial := nav.Page{Title: "sales.db", Content: nav.Static("db", "db body")}
	d := newDriver(t, testModules(), Options{Start: ScreenProjects, Initial: &initial})
	if got := d.crumbs(); got != RootTitle+" > Projects > sales.db" {
		t.Fatalf("breadcrumbs = %q", got)
	}
	d.requireContains("db body")
}

func TestAppHighlightOpensModuleKeepingFocusOnMenu(t *testing.T) {
	r := fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenProjects})
	d.press("down")
	if got := d.crumbs(); got != RootTitle+" > Viewers" {
		t.Fatalf("breadcrumbs = %q", got)
	}
	if d.app.Shell().Zone() != nav.FocusToMenu {
		t.Errorf("focus = %v, want the menu", d.app.Shell().Zone())
	}
	d.requireContains("viewers body")
	if want := "viewers"; r.saved[len(r.saved)-1] != want {
		t.Errorf("saved = %v", r.saved)
	}
	d.press("down")
	d.requireContains("settings body")
	d.press("up")
	d.requireContains("viewers body")
}

func TestAppEnterAndRightMoveFocusToContent(t *testing.T) {
	fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenProjects})
	d.press("enter")
	if d.app.Shell().Zone() != nav.FocusToContent {
		t.Fatalf("Enter: focus = %v, want the content", d.app.Shell().Zone())
	}
	d.press("left")
	if d.app.Shell().Zone() != nav.FocusToMenu {
		t.Fatalf("Left: focus = %v, want the menu", d.app.Shell().Zone())
	}
	d.press("right")
	if d.app.Shell().Zone() != nav.FocusToContent {
		t.Fatalf("Right: focus = %v, want the content", d.app.Shell().Zone())
	}
}

func TestAppUpAtTopOfMenuGoesToBreadcrumbs(t *testing.T) {
	fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenProjects})
	d.press("up")
	if d.app.Shell().Zone() != nav.FocusToBreadcrumbs {
		t.Fatalf("focus = %v, want the breadcrumbs", d.app.Shell().Zone())
	}
}

func TestAppExitSavesEmptyScreenAndQuits(t *testing.T) {
	r := fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenProjects})
	d.press("q") // shortcut of the Exit item
	if !d.quit {
		t.Fatal("Exit did not quit")
	}
	if len(r.syncSave) != 1 || r.syncSave[0] != "" {
		t.Errorf("synchronous saves = %q, want one empty path", r.syncSave)
	}
}

func TestAppExitByEnterOnTheLastItem(t *testing.T) {
	r := fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenProjects})
	d.press("end", "enter")
	if !d.quit || len(r.syncSave) != 1 {
		t.Errorf("quit=%v saves=%v", d.quit, r.syncSave)
	}
}

func TestAppRootBreadcrumbReturnsToTheStartModule(t *testing.T) {
	fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenSettings})
	d.run(Drill("Deeper", nav.Static("t", "deeper body")))
	if got := d.crumbs(); !strings.HasSuffix(got, "Deeper") {
		t.Fatalf("breadcrumbs = %q", got)
	}
	d.press("shift+tab", "home", "enter") // the breadcrumbs, the root crumb, activate it
	if got := d.crumbs(); got != RootTitle+" > Settings" {
		t.Fatalf("breadcrumbs = %q\n%s", got, d.screen())
	}
}

func TestAppOpenModuleFromADeepPage(t *testing.T) {
	fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenProjects})
	d.run(Drill("Deeper", nav.Static("t", "deeper body")))
	d.run(Open(ScreenSettings, nav.FocusToContent))
	if got := d.crumbs(); got != RootTitle+" > Settings" {
		t.Fatalf("breadcrumbs = %q", got)
	}
	if d.app.Shell().Zone() != nav.FocusToContent {
		t.Errorf("focus = %v, want the content", d.app.Shell().Zone())
	}
}

func TestAppOpenUnknownModuleFallsBackToTheFirst(t *testing.T) {
	fakeSeams(t)
	d := newDriver(t, testModules(), Options{Start: ScreenSettings})
	d.run(Open("nope", nav.FocusToMenu))
	if got := d.crumbs(); got != RootTitle+" > Projects" {
		t.Fatalf("breadcrumbs = %q", got)
	}
}

func TestAppWithoutModulesReportsAnError(t *testing.T) {
	r := fakeSeams(t)
	d := newDriver(t, nil, Options{})
	d.requireContains("no modules registered")
	if len(r.opened) != 0 {
		t.Errorf("opened = %v", r.opened)
	}
}

func TestAppOpenWebUIWithCtrlW(t *testing.T) {
	fakeSeams(t)
	restoreConfig, restoreState, restoreOpen := webUIReadConfigFile, webUIGetState, openURL
	t.Cleanup(func() { webUIReadConfigFile, webUIGetState, openURL = restoreConfig, restoreState, restoreOpen })
	webUIReadConfigFile = func() ([]byte, error) { return nil, errors.New("none") }
	webUIGetState = func() (state *dtstate.DatatugState, err error) { return nil, errors.New("none") }
	d := newDriver(t, testModules(), Options{Start: ScreenProjects})

	var opened []string
	openURL = func(url string) error { opened = append(opened, url); return nil }
	d.press("ctrl+w")
	if len(opened) != 1 || opened[0] != DefaultWebUIOrigin+"/home" {
		t.Fatalf("opened = %v", opened)
	}

	openURL = func(string) error { return errors.New("no browser") }
	d.press("ctrl+w")
	if !d.app.Shell().AlertOpen() {
		t.Error("a browser failure must show an alert")
	}
}

func TestAppRecoversFromAPanicInUpdate(t *testing.T) {
	fakeSeams(t)
	boom := Module{ID: "boom", Text: "Boom", Root: func() nav.Page { panic("kaboom") }}
	app := New([]Module{boom}, Options{})
	model, cmd := app.Update(startMsg{})
	if app.crash.value != "kaboom" {
		t.Fatalf("recovered = %v", app.crash.value)
	}
	if _, ok := model.(App); !ok {
		t.Fatalf("model = %T", model)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("a panic must quit the program")
	}
}

type panickingScreen struct{ nav.Screen }

func (p panickingScreen) Update(tea.Msg) (nav.Screen, tea.Cmd) { return p, nil }

func (panickingScreen) View() string { panic("view boom") }

func TestAppRecoversFromAPanicInView(t *testing.T) {
	fakeSeams(t)
	m := Module{ID: "v", Text: "V", Root: func() nav.Page { return nav.Page{Content: panickingScreen{nav.Static("", "")}} }}
	d := newDriver(t, []Module{m}, Options{})
	if v := d.app.View(); v.Content != "" {
		t.Errorf("view after a panic is not empty")
	}
	if d.app.crash.value != "view boom" {
		t.Errorf("recovered = %v", d.app.crash.value)
	}
}

func TestAppViewIsTheShellView(t *testing.T) {
	fakeSeams(t)
	d := newDriver(t, testModules(), Options{})
	if !d.app.View().AltScreen {
		t.Error("the app runs full screen")
	}
}

func stubProgram(t *testing.T, fn func(p *tea.Program) (tea.Model, error)) {
	t.Helper()
	previous := runProgram
	runProgram = fn
	t.Cleanup(func() { runProgram = previous })
}

func TestRunWrapsProgramErrors(t *testing.T) {
	want := errors.New("no tty")
	stubProgram(t, func(*tea.Program) (tea.Model, error) { return nil, want })
	if err := Run(testModules(), Options{}); !errors.Is(err, want) {
		t.Fatalf("Run = %v", err)
	}
}

func TestRunReturnsNilAfterQuit(t *testing.T) {
	fakeSeams(t)
	stubProgram(t, func(*tea.Program) (tea.Model, error) { return nil, nil })
	if err := Run(testModules(), Options{}); err != nil {
		t.Fatal(err)
	}
}

// runHeadless runs the real Bubble Tea program on pipes; quitAfter is when
// Ctrl+Q is typed into it.
func runHeadless(t *testing.T, modules []Module, quitAfter time.Duration) (err error) {
	t.Helper()
	input, release := io.Pipe()
	t.Cleanup(func() { _ = release.Close() })
	go func() {
		time.Sleep(quitAfter)
		_, _ = release.Write([]byte{0x11})
	}()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- errors.New("panic: " + r.(string))
			}
		}()
		done <- Run(modules, Options{}, tea.WithInput(input), tea.WithOutput(io.Discard), tea.WithWindowSize(100, 30))
	}()
	select {
	case err = <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("program did not exit")
		return nil
	}
}

func TestRunHeadlessProgramEndToEnd(t *testing.T) {
	fakeSeams(t)
	if err := runHeadless(t, testModules(), 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestRunRaisesAgainAPanicAfterTheProgramQuit(t *testing.T) {
	fakeSeams(t)
	boom := Module{ID: "boom", Text: "Boom", Root: func() nav.Page { panic("kaboom") }}
	err := runHeadless(t, []Module{boom}, time.Minute)
	if err == nil || err.Error() != "panic: kaboom" {
		t.Fatalf("Run = %v, want the panic raised again once the program quit", err)
	}
}
