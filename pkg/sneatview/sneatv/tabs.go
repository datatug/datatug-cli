package sneatv

import (
	"fmt"
	"strconv"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type TabStyles struct {
	Foreground string
	Background string
}

type TabsStyle struct {
	Radio      bool
	Underscore bool

	ActiveFocused TabStyles
	ActiveBlur    TabStyles
	Inactive      TabStyles
}

// Tab represents a single tab.
type Tab struct {
	ID    string
	Title string
	tview.Primitive
}

// Tabs is a tab container implemented using tview.Pages.
type Tabs struct {
	*tview.Flex
	tabsOptions
	TabsStyle

	app *tview.Application

	TextView *tview.TextView // TODO(help-wanted): exported as a workaround to set focus - needs fix!
	pages    *tview.Pages

	isFocused bool

	tabs   []*Tab
	active int
}

type tabsOptions struct {
	label     string
	focusDown func(current tview.Primitive)
	focusLeft func(current tview.Primitive)
	focusUp   func(current tview.Primitive)
}

type TabsOption func(*tabsOptions)

func WithLabel(label string) TabsOption {
	return func(o *tabsOptions) {
		o.label = label
	}
}

func FocusDown(f func(current tview.Primitive)) TabsOption {
	return func(o *tabsOptions) {
		o.focusDown = f
	}
}

func FocusUp(f func(current tview.Primitive)) TabsOption {
	return func(o *tabsOptions) {
		o.focusUp = f
	}
}

func FocusLeft(f func(current tview.Primitive)) TabsOption {
	return func(o *tabsOptions) {
		o.focusLeft = f
	}
}

var DefaultTabsStyle = TabsStyle{
	Radio:      false,
	Underscore: true,
	ActiveFocused: TabStyles{
		Foreground: "black",
		Background: "lightgray",
	},
	Inactive: TabStyles{
		Foreground: "black",
		Background: "lightgray",
	},
}

var RadioTabsStyle = TabsStyle{
	Radio:      true,
	Underscore: false,
	ActiveFocused: TabStyles{
		Foreground: "white",
		Background: "black",
	},
	ActiveBlur: TabStyles{
		Foreground: "lightgray",
		Background: "black",
	},
	Inactive: TabStyles{
		Foreground: "lightgray",
		Background: "black",
	},
}

//func (t *Tabs) TakeFocus() {
//	t.app.SetFocus(t.TextView)
//}

// NewTabs creates a new tab container.
func NewTabs(app *tview.Application, style TabsStyle, options ...TabsOption) *Tabs {
	pages := tview.NewPages()

	t := &Tabs{
		app:       app,
		active:    -1,
		TabsStyle: style,
		pages:     pages,
		Flex:      tview.NewFlex().SetDirection(tview.FlexRow),
		TextView: tview.NewTextView().
			SetDynamicColors(true).
			SetRegions(true).
			SetWrap(false),
	}
	t.Flex.SetFocusFunc(func() {
		t.app.SetFocus(t.TextView)
	})
	for _, set := range options {
		set(&t.tabsOptions)
	}

	t.TextView.SetInputCapture(t.handleInput)

	setIsFocused := func(isFocused bool) {
		t.isFocused = isFocused
		go app.QueueUpdateDraw(func() {
			t.renderTabs()
		})
	}

	t.TextView.SetFocusFunc(func() {
		setIsFocused(true)
	})

	t.TextView.SetBlurFunc(func() {
		setIsFocused(false)
	})

	t.TextView.SetHighlightedFunc(func(added, removed, remaining []string) {
		if len(added) == 0 {
			return
		}

		region := added[0]

		var index int
		if _, err := fmt.Sscanf(region, "tab-%d", &index); err != nil {
			return
		}
		//t.tabs[index].Title = fmt.Sprintf("Tab %d", index)
		t.SwitchTo(index)
	})

	t.
		AddItem(t.TextView, 1, 0, false).
		AddItem(pages, 0, 1, true)

	return t
}

// AddTabs adds new tabs.
func (t *Tabs) AddTabs(tabs ...*Tab) {
	is1stTab := len(t.tabs) == 0
	t.tabs = append(t.tabs, tabs...)

	for _, tab := range tabs {
		t.pages.AddPage(
			tab.ID,
			tab.Primitive,
			true,
			is1stTab,
		)
	}

	if is1stTab {
		t.SwitchTo(0)
	}
}

// SwitchTo switches to a tab by index.
func (t *Tabs) SwitchTo(index int) {
	if index < 0 || index >= len(t.tabs) {
		return
	}
	if t.active == index {
		return
	}
	t.active = index
	t.pages.SwitchToPage(t.tabs[index].ID)
	t.renderTabs()
	t.TextView.Highlight("tab-" + strconv.Itoa(index))
}

// renderTabs redraws the tab bar.
func (t *Tabs) renderTabs() {
	t.TextView.Clear()

	if t.label != "" {
		_, _ = t.TextView.Write([]byte(t.label))
	}

	for i, tab := range t.tabs {
		var title string
		if t.Radio {
			if i == t.active {
				title = "◉ " + tab.Title
			} else {
				title = "○ " + tab.Title
			}
		} else {
			title = tab.Title
		}
		region := fmt.Sprintf("tab-%d", i)
		if i == t.active {
			if t.isFocused {
				_, _ = fmt.Fprintf(
					t.TextView,
					`["%s"][%s:%s:b] %s [-:-:B][""]`,
					region,
					t.ActiveFocused.Background,
					t.ActiveFocused.Foreground,
					title,
				)
			} else {
				_, _ = fmt.Fprintf(
					t.TextView,
					`["%s"][%s:%s] %s [-:-][""]`,
					region,
					t.ActiveBlur.Background,
					t.ActiveBlur.Foreground,
					title,
				)

			}
		} else {
			if t.TabsStyle.Underscore {
				_, _ = fmt.Fprintf(
					t.TextView,
					`["%s"][%s:%s:u] %s [-:-:U][""]`,
					region,
					t.Inactive.Background,
					t.Inactive.Foreground,
					title,
				)
			} else {
				_, _ = fmt.Fprintf(
					t.TextView,
					`["%s"][%s:%s] %s [-:-][""]`,
					region,
					t.Inactive.Foreground,
					t.Inactive.Background,
					title,
				)
			}
		}
	}
}

// handleInput handles keyboard navigation.
func (t *Tabs) handleInput(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyRight:
		t.SwitchTo((t.active + 1) % len(t.tabs))
		return nil
	case tcell.KeyLeft:
		if t.active == 0 {
			t.focusLeft(t.TextView)
			return nil
		}
		t.SwitchTo((t.active - 1 + len(t.tabs)) % len(t.tabs))
		return nil
	case tcell.KeyUp:
		t.focusUp(t.TextView)
		return nil
	case tcell.KeyDown:
		t.focusDown(t.TextView)
		return nil
	default:
		if ev.Modifiers() == tcell.ModAlt {
			if ev.Rune() >= '1' && ev.Rune() <= '9' {
				t.SwitchTo(int(ev.Rune() - '1'))
				return nil
			}
		}
		return ev
	}
}
