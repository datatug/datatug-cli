package sneatv

import (
	"fmt"
	"strconv"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

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

	textView *tview.TextView
	pages    *tview.Pages

	tabs   []*Tab
	active int
}

type tabsOptions struct {
	label string
	radio bool
}

type TabsOption func(*tabsOptions)

func WithRadio() TabsOption {
	return func(o *tabsOptions) {
		o.radio = true
	}
}

func WithLabel(label string) TabsOption {
	return func(o *tabsOptions) {
		o.label = label
	}
}

// NewTabs creates a new tab container.
func NewTabs(options ...TabsOption) *Tabs {
	pages := tview.NewPages()

	t := &Tabs{
		pages: pages,
		Flex:  tview.NewFlex().SetDirection(tview.FlexRow),
		textView: tview.NewTextView().
			SetDynamicColors(true).
			SetRegions(true).
			SetWrap(false),
	}
	for _, set := range options {
		set(&t.tabsOptions)
	}

	t.textView.SetInputCapture(t.handleInput)

	t.textView.SetHighlightedFunc(func(added, removed, remaining []string) {
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
		AddItem(t.textView, 1, 0, false).
		AddItem(pages, 0, 1, true)

	return t
}

// AddTab adds a new tab.
func (t *Tabs) AddTab(tab *Tab) {
	index := len(t.tabs)
	t.tabs = append(t.tabs, tab)

	t.pages.AddPage(
		tab.ID,
		tab.Primitive,
		true,
		index == 0,
	)

	if index == 0 {
		t.active = 0
		t.textView.Highlight(tab.ID)
	}

	t.renderTabs()
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
	t.textView.Highlight("tab-" + strconv.Itoa(index))
	t.pages.SwitchToPage(t.tabs[index].ID)
	t.renderTabs()
}

// renderTabs redraws the tab bar.
func (t *Tabs) renderTabs() {
	t.textView.Clear()

	if t.label != "" {
		_, _ = t.textView.Write([]byte(t.label))
	}

	for i, tab := range t.tabs {
		var title string
		if t.radio {
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
			_, _ = fmt.Fprintf(
				t.textView,
				`["%s"][blue:white] %s [-:-][""] `,
				region,
				title,
			)
		} else {
			_, _ = fmt.Fprintf(
				t.textView,
				`["%s"][lightgray:black:u] %s [-:-:U][""] `,
				region,
				title,
			)
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
		t.SwitchTo((t.active - 1 + len(t.tabs)) % len(t.tabs))
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
