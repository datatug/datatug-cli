package datatugui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func updateMenu(t *testing.T, m MainMenu, msg tea.Msg) (MainMenu, []tea.Msg) {
	t.Helper()
	s, cmd := m.Update(msg)
	return s.(MainMenu), uitest.Msgs(cmd)
}

func TestMainMenuListsModulesAndExit(t *testing.T) {
	m := NewMainMenu(testModules())
	m, _ = updateMenu(t, m, tea.WindowSizeMsg{Width: 28, Height: 10})
	view := uitest.Plain(m.View())
	for _, want := range []string{"(p) Projects", "(v) Viewers", "(s) Settings", "(q) Exit"} {
		if !contains(view, want) {
			t.Errorf("menu view is missing %q:\n%s", want, view)
		}
	}
	if m.Init() != nil {
		t.Error("the menu has nothing to start")
	}
	if len(m.ShortHelp()) == 0 {
		t.Error("the menu lists its keys")
	}
	if !m.AtEdge(widgets.Up) || m.AtEdge(widgets.Down) {
		t.Error("the first item is at the top edge only")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestMainMenuMessages(t *testing.T) {
	m := NewMainMenu(testModules())
	exitIndex := len(testModules())

	t.Run("highlight_of_a_module_opens_it_with_focus_on_the_menu", func(t *testing.T) {
		_, msgs := updateMenu(t, m, widgets.ItemHighlightedMsg{ID: MenuID, Index: 1})
		open, ok := msgs[0].(OpenModuleMsg)
		if !ok || open.ID != ScreenViewers || open.Focus != nav.FocusToMenu {
			t.Errorf("messages = %#v", msgs)
		}
	})
	t.Run("highlight_of_exit_or_of_another_list_does_nothing", func(t *testing.T) {
		for _, msg := range []tea.Msg{
			widgets.ItemHighlightedMsg{ID: MenuID, Index: exitIndex},
			widgets.ItemHighlightedMsg{ID: "other", Index: 0},
		} {
			if _, msgs := updateMenu(t, m, msg); len(msgs) != 0 {
				t.Errorf("%#v produced %#v", msg, msgs)
			}
		}
	})
	t.Run("select_of_a_module_focuses_the_content", func(t *testing.T) {
		_, msgs := updateMenu(t, m, widgets.ItemSelectedMsg{ID: MenuID, Index: 0})
		if focus, ok := msgs[0].(nav.SetFocusMsg); !ok || focus.To != nav.FocusToContent {
			t.Errorf("messages = %#v", msgs)
		}
	})
	t.Run("select_of_another_list_does_nothing", func(t *testing.T) {
		if _, msgs := updateMenu(t, m, widgets.ItemSelectedMsg{ID: "other", Index: 0}); len(msgs) != 0 {
			t.Errorf("messages = %#v", msgs)
		}
	})
	t.Run("select_of_exit_quits", func(t *testing.T) {
		r := fakeSeams(t)
		_, msgs := updateMenu(t, m, widgets.ItemSelectedMsg{ID: MenuID, Index: exitIndex})
		if _, ok := msgs[0].(tea.QuitMsg); !ok || len(r.syncSave) != 1 || r.syncSave[0] != "" {
			t.Errorf("messages = %#v, saves = %v", msgs, r.syncSave)
		}
	})
	t.Run("select_module_moves_the_highlight_silently", func(t *testing.T) {
		moved, msgs := updateMenu(t, m, selectModuleMsg{id: ScreenSettings})
		if moved.list.Index() != 2 || len(msgs) != 0 {
			t.Errorf("index = %d, messages = %#v", moved.list.Index(), msgs)
		}
		kept, _ := updateMenu(t, moved, selectModuleMsg{id: "nope"})
		if kept.list.Index() != 2 {
			t.Errorf("an unknown module moved the highlight to %d", kept.list.Index())
		}
	})
	t.Run("focus_follows_the_shell", func(t *testing.T) {
		focused, _ := updateMenu(t, m, nav.ScreenFocusMsg{Focused: true})
		if !focused.list.Focused() {
			t.Error("the menu should be focused")
		}
		blurred, _ := updateMenu(t, focused, nav.ScreenFocusMsg{Focused: false})
		if blurred.list.Focused() {
			t.Error("the menu should be blurred")
		}
	})
}

func TestNewMainMenuRejectsDuplicateModules(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a duplicate module must panic")
		}
	}()
	NewMainMenu([]Module{testModules()[0], testModules()[0]})
}
