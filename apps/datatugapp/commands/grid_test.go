package commands

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/nav/navtest"
	"github.com/strongo/strongo-tui/pkg/uitest"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

func testRecordset() datatug.Recordset {
	return datatug.Recordset{
		Columns: []datatug.RecordsetColumn{{Name: "id", DbType: "int"}, {Name: "title", DbType: "text"}, {Name: "n", DbType: "number"}},
		Rows:    [][]any{{1, "first", 1.5}, {2, "second", nil}},
	}
}

func TestShowRecordsetInGrid(t *testing.T) {
	old := runShell
	defer func() { runShell = old }()
	var got nav.Model
	runShell = func(m nav.Model, _ ...tea.ProgramOption) error { got = m; return nil }
	if err := showRecordsetInGrid(testRecordset()); err != nil {
		t.Fatal(err)
	}
	if got.Render() == "" {
		t.Fatal("empty shell")
	}

	runShell = func(nav.Model, ...tea.ProgramOption) error { return errors.New("no tty") }
	if err := showRecordsetInGrid(testRecordset()); err == nil {
		t.Fatal("expected the shell error")
	}
}

func TestRecordsetShellShowsTheData(t *testing.T) {
	page := nav.Page{Title: "Recordset", Content: newRecordsetScreen(testRecordset())}
	navtest.New(t, page).RequireContains("title").RequireContains("first").RequireContains("second")
}

func TestRecordsetScreen(t *testing.T) {
	var s nav.Screen = newRecordsetScreen(testRecordset())
	if s.Init() != nil {
		t.Fatal("Init must not start work")
	}
	s, _ = s.Update(tea.WindowSizeMsg{Width: 60, Height: 8})
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: true})
	if v := uitest.Plain(s.View()); v == "" {
		t.Fatal("empty view")
	}
	s, _ = s.Update(uitest.Key("down"))
	_ = s.(widgets.Boundary).AtEdge(widgets.Up)
	if s.(widgets.Editor).Editing() {
		t.Fatal("not editing")
	}
	if !s.(nav.KeyCapturer).CapturesKey(uitest.Key("esc")) || s.(nav.KeyCapturer).CapturesKey(uitest.Key("a")) {
		t.Fatal("only esc is captured")
	}
	_, cmd := s.Update(uitest.Key("esc"))
	msgs := uitest.Msgs(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	if _, ok := msgs[0].(tea.QuitMsg); !ok {
		t.Fatalf("esc should quit, got %T", msgs[0])
	}
}

func TestRecordsetScreenEscWhileFiltering(t *testing.T) {
	var s nav.Screen = newRecordsetScreen(testRecordset())
	s, _ = s.Update(tea.WindowSizeMsg{Width: 60, Height: 8})
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: true})
	s, _ = s.Update(uitest.Key("/"))
	if !s.(widgets.Editor).Editing() {
		t.Fatal("/ should open the filter")
	}
	_, cmd := s.Update(uitest.Key("esc"))
	for _, m := range uitest.Msgs(cmd) {
		if _, ok := m.(tea.QuitMsg); ok {
			t.Fatal("esc must close the filter, not quit")
		}
	}
}
