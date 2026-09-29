package datatugui

import (
	"errors"
	"reflect"
	"testing"

	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func panelList(p sneatnav.Panel) *tview.List {
	panelElem := reflect.ValueOf(p).Elem()
	pwbField := panelElem.FieldByName("PrimitiveWithBox")
	if !pwbField.IsValid() || pwbField.IsNil() {
		return nil
	}
	concrete := pwbField.Elem()
	if concrete.Kind() == reflect.Pointer {
		concrete = concrete.Elem()
	}
	if concrete.Kind() != reflect.Struct {
		return nil
	}
	primField := concrete.FieldByName("Primitive")
	if !primField.IsValid() || primField.IsNil() {
		return nil
	}
	list, ok := primField.Interface().(*tview.List)
	if !ok {
		return nil
	}
	return list
}

func TestRegisterMainMenuItem(t *testing.T) {
	origItems := mainMenuItems
	defer func() { mainMenuItems = origItems }()

	mainMenuItems = nil

	item1 := MainMenuItem{
		Text:     "Item 1",
		Shortcut: '1',
		Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
			return nil
		},
	}
	RegisterMainMenuItem(RootScreen(1), item1)

	if len(mainMenuItems) != 1 || mainMenuItems[0].id != 1 {
		t.Fatalf("expected 1 item with id 1, got %+v", mainMenuItems)
	}

	// Register duplicate id should panic
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate id")
		}
	}()
	RegisterMainMenuItem(RootScreen(1), item1)
}

func TestNewDataTugMainMenu(t *testing.T) {
	origItems := mainMenuItems
	defer func() { mainMenuItems = origItems }()

	mainMenuItems = []MainMenuItem{
		{
			id:       10,
			Text:     "First",
			Shortcut: 'f',
			Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
				return nil
			},
		},
		{
			id:       20,
			Text:     "Second",
			Shortcut: 's',
			Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
				return nil
			},
		},
	}

	tui := newTestTUI(t)
	panel := NewDataTugMainMenu(tui, RootScreen(20))
	if panel == nil {
		t.Fatal("expected non-nil panel")
	}

	list := panelList(panel)
	if list == nil {
		t.Fatal("expected non-nil list from panelList")
	}

	if list.GetCurrentItem() != 1 {
		t.Fatalf("expected current item 1 for RootScreen(20), got %d", list.GetCurrentItem())
	}

	// Screen not in mainMenuItems should default to 0
	panel2 := NewDataTugMainMenu(tui, RootScreen(999))
	list2 := panelList(panel2)
	if list2.GetCurrentItem() != 0 {
		t.Fatalf("expected current item 0 for non-existent screen, got %d", list2.GetCurrentItem())
	}
}

func TestNewDataTugMainMenuInteractions(t *testing.T) {
	origItems := mainMenuItems
	defer func() { mainMenuItems = origItems }()

	actionFail := false
	mainMenuItems = []MainMenuItem{
		{
			id:       1,
			Text:     "Option1",
			Shortcut: 'o',
			Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
				if actionFail {
					return errors.New("fail action")
				}
				return nil
			},
		},
	}

	tui := newTestTUI(t)
	textView := tview.NewTextView()
	contentPanel := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(textView, textView.Box))

	menuPanel := NewDataTugMainMenu(tui, RootScreen(1))
	tui.SetPanels(menuPanel, contentPanel)

	list := panelList(menuPanel)
	if list == nil {
		t.Fatal("could not find *tview.List inside panel content")
	}

	// 1. Test KeyEnter
	capture := list.GetInputCapture()
	if capture == nil {
		t.Fatal("expected input capture on list")
	}

	evEnter := tcell.NewEventKey(tcell.KeyEnter, ' ', tcell.ModNone)
	if got := capture(evEnter); got != nil {
		t.Fatalf("expected nil for KeyEnter, got %v", got)
	}

	// 2. Test KeyUp when on item 0
	list.SetCurrentItem(0)
	evUp := tcell.NewEventKey(tcell.KeyUp, ' ', tcell.ModNone)
	if got := capture(evUp); got != nil {
		t.Fatalf("expected nil for KeyUp on item 0, got %v", got)
	}

	// 3. Test KeyUp when on item > 0
	list.SetCurrentItem(1) // Exit item is index 1
	if got := capture(evUp); got != evUp {
		t.Fatalf("expected evUp for KeyUp on item > 0, got %v", got)
	}

	// 4. Test KeyRight
	evRight := tcell.NewEventKey(tcell.KeyRight, ' ', tcell.ModNone)
	if got := capture(evRight); got != nil {
		t.Fatalf("expected nil for KeyRight, got %v", got)
	}

	// 5. Test KeyBacktab
	evBacktab := tcell.NewEventKey(tcell.KeyBacktab, ' ', tcell.ModNone)
	if got := capture(evBacktab); got != nil {
		t.Fatalf("expected nil for KeyBacktab, got %v", got)
	}

	// 6. Test Default key
	evRune := tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone)
	if got := capture(evRune); got != evRune {
		t.Fatalf("expected evRune for unhandled key, got %v", got)
	}

	// 7. Test ChangedFunc:
	list.SetCurrentItem(1)
	list.SetCurrentItem(0)

	// Test ChangedFunc error panic
	actionFail = true
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from ChangedFunc on error")
		}
	}()
	list.SetCurrentItem(1)
	list.SetCurrentItem(0)
}

func TestNewDataTugMainMenuActionAndExit(t *testing.T) {
	origItems := mainMenuItems
	defer func() { mainMenuItems = origItems }()

	actionFail := false
	mainMenuItems = []MainMenuItem{
		{
			id:       1,
			Text:     "Option1",
			Shortcut: 'o',
			Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
				if actionFail {
					return errors.New("fail action")
				}
				return nil
			},
		},
	}

	tui := newTestTUI(t)
	textView := tview.NewTextView()
	contentPanel := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(textView, textView.Box))
	panel := NewDataTugMainMenu(tui, RootScreen(1))
	tui.SetPanels(panel, contentPanel)

	list := panelList(panel)

	// List item 0 is Option1, item 1 is Exit
	handler := list.InputHandler()
	handler(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone), nil)

	// Test Exit item (shortcut 'q')
	handler(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone), nil)

	// Test Action error panic
	actionFail = true
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when action fails")
		}
	}()
	handler(tcell.NewEventKey(tcell.KeyRune, 'o', tcell.ModNone), nil)
}
