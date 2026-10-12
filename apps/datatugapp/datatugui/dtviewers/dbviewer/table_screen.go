package dbviewer

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// writeClipboard is a seam over the system clipboard.
var writeClipboard = clipboard.WriteAll

const (
	tabsID      = "dbviewer.tabs"
	cellModalID = "dbviewer.cell"
)

// Tab order of the table screen: the content, then one tab per kind of schema
// information.
var tabPanes = [...]paneKind{paneColumns, paneForeignKeys, paneReferrers}

var (
	copyKey    = key.NewBinding(key.WithKeys("ctrl+c", "alt+c", "super+c"), key.WithHelp("ctrl+c", "copy cell"))
	detailKey  = key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "view row"))
	previewKey = key.NewBinding(key.WithKeys("alt+down"), key.WithHelp("alt+↓", "foreign key row"))
)

// copyFailed reports that the clipboard could not be written.
type copyFailed struct{ err error }

// tableScreen shows one table or view: the tab strip, and under it the records or
// the columns, foreign keys or referrers.
type tableScreen struct {
	ctx       dtviewers.CollectionContext
	schema    schemer.SchemaProvider
	tabs      widgets.Tabs
	tab       int
	inTabs    bool
	content   contentPane
	panes     [paneKinds]detailPane
	modal     widgets.Modal
	modalOpen bool
	w, h      int
	focused   bool
}

var (
	_ nav.Screen       = tableScreen{}
	_ nav.Titled       = tableScreen{}
	_ nav.KeyCapturer  = tableScreen{}
	_ nav.ShortHelper  = tableScreen{}
	_ widgets.Boundary = tableScreen{}
)

func newTableScreen(collCtx dtviewers.CollectionContext) tableScreen {
	s := tableScreen{ctx: collCtx, schema: collCtx.Schema(), content: newContentPane(collCtx.DbContext)}
	s.tabs = widgets.NewTabs(tabsID, widgets.UnderlineTabsStyle, widgets.Tab{ID: "content", Title: "Content"})
	if s.schema != nil {
		titles := [paneKinds]string{paneColumns: "Columns", paneReferrers: "Referrers", paneForeignKeys: "Foreign keys"}
		for _, k := range tabPanes {
			s.tabs.AddTab(widgets.Tab{ID: paneSpecs[k].id, Title: titles[k]})
		}
		for k := range paneKinds {
			s.panes[k] = newDetailPane(k).show(collCtx.CollectionRef.Name())
		}
	}
	return s
}

// Init implements nav.Screen: everything is loaded up front.
func (s tableScreen) Init() tea.Cmd {
	cmds := []tea.Cmd{loadContent(s.ctx)}
	if s.schema != nil {
		for k := range paneKinds {
			cmds = append(cmds, loadPane(k, s.schema, s.ctx.CollectionRef))
		}
	}
	return tea.Batch(cmds...)
}

// Update implements nav.Screen.
func (s tableScreen) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.w, s.h = msg.Width, msg.Height
		s.modal.SetMaxSize(msg.Width, msg.Height)
		return s.relayout(), nil
	case nav.ScreenFocusMsg:
		s.focused = msg.Focused
		return s.syncFocus(), nil
	case contentLoaded:
		if msg.coll != s.ctx.CollectionRef.Name() {
			return s, nil
		}
		var cmd tea.Cmd
		s.content, cmd = s.content.apply(msg)
		return s, cmd
	case previewLoaded:
		s.content = s.content.applyPreview(msg)
		return s, nil
	case paneLoaded:
		for k := range s.panes {
			s.panes[k] = s.panes[k].apply(msg)
		}
		return s, nil
	case grid.SelectionChangedMsg:
		if msg.ID != contentGridID {
			return s, nil
		}
		var cmd tea.Cmd
		s.content, cmd = s.content.previewFor(msg.Index, msg.Column)
		return s, cmd
	case grid.RowActivatedMsg:
		return s.activated(msg)
	case widgets.TabChangedMsg:
		if msg.ID == tabsID {
			s.tab = msg.Index
			return s.syncFocus(), nil
		}
		return s, nil
	case copyFailed:
		return s, nav.Alert("Copy failed", msg.err.Error(), 3*time.Second, nav.FocusToContent)
	case tea.KeyPressMsg:
		return s.key(msg)
	}
	return s, nil
}

// activated handles Enter on a row: follow a foreign key or a referrer, or show
// the row.
func (s tableScreen) activated(msg grid.RowActivatedMsg) (nav.Screen, tea.Cmd) {
	switch msg.ID {
	case contentGridID:
		if target := s.content.followTable(msg.Column); target != "" {
			return s, s.open(target)
		}
		return s.openDetail(msg.Row, msg.Column), nil
	case previewGridID:
		return s.openDetail(msg.Row, msg.Column), nil
	case paneSpecs[paneReferrers].id, paneSpecs[paneForeignKeys].id:
		return s, s.open(msg.Row.Ref.(string))
	}
	return s, nil
}

// open drills into another table of the same database.
func (s tableScreen) open(table string) tea.Cmd {
	return openTable(s.ctx.DbContext, dal.NewCollectionRef(table, "", s.ctx.CollectionRef.Parent()))
}

// openDetail shows the dialog with the whole row.
func (s tableScreen) openDetail(row grid.Row, column int) tableScreen {
	title, text := s.content.detail(row, column)
	s.modal = widgets.NewModal(cellModalID)
	s.modal.SetTitle(title)
	s.modal.SetText(text)
	s.modal.SetButtons("OK")
	s.modal.SetMaxSize(s.w, s.h)
	s.modalOpen = true
	return s.syncFocus()
}

// key handles a key press: the dialog swallows all of them while open and closes
// on Enter or Esc, the tab strip takes the horizontal ones while it holds the
// keyboard.
func (s tableScreen) key(msg tea.KeyPressMsg) (nav.Screen, tea.Cmd) {
	var cmd tea.Cmd
	switch {
	case s.modalOpen:
		if key.Matches(msg, s.modal.KeyMap.Confirm) || key.Matches(msg, s.modal.KeyMap.Cancel) {
			s.modalOpen = false
			return s.syncFocus(), nil
		}
	case s.inTabs && (msg.String() == "down" || msg.String() == "enter"):
		s.inTabs = false
		return s.syncFocus(), nil
	case s.inTabs || key.Matches(msg, s.tabs.KeyMap.Jump):
		s.tabs.Focus() // the strip only reacts to keys while it holds the keyboard
		s.tabs, cmd = s.tabs.Update(msg)
		if !s.inTabs {
			s.tabs.Blur()
		}
	case msg.String() == "up" && s.bodyAtTop():
		s.inTabs = true
		return s.syncFocus(), nil
	case s.tab == 0:
		return s.contentKey(msg)
	default:
		s.panes[tabPanes[s.tab-1]], cmd = s.panes[tabPanes[s.tab-1]].update(msg)
	}
	return s, cmd
}

// bodyAtTop tells whether Up would leave the body for the tab strip.
func (s tableScreen) bodyAtTop() bool {
	if s.tab == 0 {
		return !s.content.previewFocus && s.content.atEdge(widgets.Up)
	}
	return s.panes[tabPanes[s.tab-1]].atEdge(widgets.Up)
}

// contentKey handles the keys of the Content tab.
func (s tableScreen) contentKey(msg tea.KeyPressMsg) (nav.Screen, tea.Cmd) {
	switch {
	case key.Matches(msg, previewKey):
		s.content = s.content.focusPreview(true)
		return s, nil
	case msg.String() == "up" && s.content.previewFocus && s.content.atEdge(widgets.Up):
		s.content = s.content.focusPreview(false)
		return s, nil
	case key.Matches(msg, detailKey):
		if g := s.content.current(); g != nil {
			if row, ok := g.CurrentRow(); ok {
				return s.openDetail(row, g.SelectedColumn()), nil
			}
		}
		return s, nil
	case key.Matches(msg, copyKey):
		if g := s.content.current(); g != nil {
			return s, copyCell(g.Cell(g.CurrentIndex(), g.SelectedColumn()))
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.content, cmd = s.content.update(msg)
	return s, cmd
}

// copyCell puts text on the clipboard; there is nothing to copy from an empty cell.
func copyCell(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	return func() tea.Msg {
		if err := writeClipboard(text); err != nil {
			return copyFailed{err: err}
		}
		return nil
	}
}

// syncFocus tells the strip, the content and the panes whether they hold the
// keyboard.
func (s tableScreen) syncFocus() tableScreen {
	body := s.focused && !s.inTabs && !s.modalOpen
	if s.focused && s.inTabs {
		s.tabs.Focus()
	} else {
		s.tabs.Blur()
	}
	s.content = s.content.focus(body && s.tab == 0)
	for k := range s.panes {
		s.panes[k] = s.panes[k].focus(body && s.tab > 0 && tabPanes[s.tab-1] == paneKind(k))
	}
	return s
}

// relayout gives the strip one row and the body the rest.
func (s tableScreen) relayout() tableScreen {
	s.tabs.SetSize(s.w, 1)
	bodyH := max(s.h-1, 0)
	s.content = s.content.resize(s.w, bodyH)
	for k := range s.panes {
		s.panes[k] = s.panes[k].resize(s.w, bodyH)
	}
	return s
}

// View implements nav.Screen.
func (s tableScreen) View() string {
	body := s.content.view()
	if s.tab > 0 {
		body = s.panes[tabPanes[s.tab-1]].view()
	}
	view := widgets.Fit(s.tabs.View()+"\n"+body, s.w, s.h)
	if s.modalOpen {
		return widgets.Center(view, s.modal.View(), s.w, s.h)
	}
	return view
}

// Title implements nav.Titled.
func (s tableScreen) Title() string { return "Table: " + s.ctx.CollectionRef.Name() }

// AtEdge implements widgets.Boundary. Up from the body is handled by the screen
// itself (it goes to the tab strip); from the strip it leaves the screen.
func (s tableScreen) AtEdge(dir widgets.Direction) bool {
	switch {
	case s.modalOpen:
		return false
	case s.inTabs:
		return s.tabs.AtEdge(dir)
	case dir != widgets.Left:
		return false
	case s.tab == 0:
		return s.content.atEdge(dir)
	}
	return s.panes[tabPanes[s.tab-1]].atEdge(dir)
}

// CapturesKey implements nav.KeyCapturer: Ctrl+C copies the current cell instead
// of quitting.
func (s tableScreen) CapturesKey(msg tea.KeyPressMsg) bool {
	return !s.modalOpen && !s.inTabs && s.tab == 0 && key.Matches(msg, copyKey)
}

// ShortHelp implements nav.ShortHelper.
func (s tableScreen) ShortHelp() []key.Binding {
	return []key.Binding{s.tabs.KeyMap.Jump, detailKey, copyKey, previewKey}
}
