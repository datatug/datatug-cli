package dtproject

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/uitest"
)

// stub replaces a seam for the duration of the test.
func stub[T any](t *testing.T, target *T, value T) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

// mount sizes a screen and gives or takes its focus, as the shell does.
func mount(s nav.Screen, w, h int, focused bool) nav.Screen {
	s, _ = s.Update(tea.WindowSizeMsg{Width: w, Height: h})
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: focused})
	return s
}

// step sends msg to a screen and returns the updated screen with the messages
// its command produced.
func step(s nav.Screen, msg tea.Msg) (nav.Screen, []tea.Msg) {
	s, cmd := s.Update(msg)
	return s, uitest.Msgs(cmd)
}

// press sends key presses and collects every message produced.
func press(s nav.Screen, keys ...string) (nav.Screen, []tea.Msg) {
	var all []tea.Msg
	for _, k := range keys {
		var msgs []tea.Msg
		s, msgs = step(s, uitest.Key(k))
		all = append(all, msgs...)
	}
	return s, all
}

// feed delivers messages to a screen one by one, and follows the messages its
// commands produce, as the Bubble Tea loop would. It returns all of them.
func feed(s nav.Screen, msgs ...tea.Msg) (nav.Screen, []tea.Msg) {
	var out []tea.Msg
	for len(msgs) > 0 {
		var produced []tea.Msg
		s, produced = step(s, msgs[0])
		out = append(out, produced...)
		msgs = append(msgs[1:], produced...)
	}
	return s, out
}

// only returns the single message of type T among msgs.
func only[T any](t *testing.T, msgs []tea.Msg) T {
	t.Helper()
	var found []T
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one %T among %#v, got %d", *new(T), msgs, len(found))
	}
	return found[0]
}

// view is the rendered text of a screen without styling.
func view(s nav.Screen) string { return uitest.Plain(s.View()) }

// fakeStore is a datatug.ProjectStore with just what the screens read.
type fakeStore struct {
	datatug.ProjectStore
	fileErr error
	loadErr error
	title   string
	envs    datatug.Environments
	envsErr error
	dbs     datatug.ProjDbDrivers
	dbsErr  error
}

func (f *fakeStore) LoadProjectFile(context.Context) (datatug.ProjectFile, error) {
	return datatug.ProjectFile{}, f.fileErr
}

func (f *fakeStore) LoadProject(context.Context, ...datatug.StoreOption) (*datatug.Project, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	p := datatug.NewProjectWithStore("p1", f)
	p.Title = f.title
	return p, nil
}

func (f *fakeStore) LoadEnvironments(context.Context, ...datatug.StoreOption) (datatug.Environments, error) {
	return f.envs, f.envsErr
}

func (f *fakeStore) LoadProjDbDrivers(context.Context, ...datatug.StoreOption) (datatug.ProjDbDrivers, error) {
	return f.dbs, f.dbsErr
}

func newEnv(id, title string) *datatug.Environment {
	return &datatug.Environment{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id, Title: title}}}
}

func newDB(id, title string) *datatug.ProjDbDriver {
	return &datatug.ProjDbDriver{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id, Title: title}}}
}

// datatugStore is the store type the seams traffic in.
type datatugStore = datatug.ProjectStore

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// runCmd runs a command and returns the messages it produced.
func runCmd(cmd tea.Cmd) []tea.Msg { return uitest.Msgs(cmd) }

// recorder is a screen that remembers the messages it was sent.
type recorder struct{ msgs *[]tea.Msg }

func newRecorder() recorder { return recorder{msgs: new([]tea.Msg)} }

func (r recorder) Init() tea.Cmd { return nil }

func (r recorder) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	*r.msgs = append(*r.msgs, msg)
	return r, nil
}

func (r recorder) View() string { return "" }

// received reports whether the recorder got a message of type T.
func received[T any](r recorder) bool {
	for _, m := range *r.msgs {
		if _, ok := m.(T); ok {
			return true
		}
	}
	return false
}

// instantTick makes the clock fire at once.
func instantTick(t *testing.T) {
	t.Helper()
	stub(t, &tick, func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(time.Time{}) }
	})
}
