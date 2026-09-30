package dbviewer

import (
	"context"
	"errors"
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/strongo/strongo-tui/pkg/grid"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// collectionKind tells the Tables screen from the Views screen.
type collectionKind struct {
	title string
	typ   datatug.CollectionType
}

var (
	kindTables = collectionKind{"Tables", datatug.CollectionTypeTable}
	kindViews  = collectionKind{"Views", datatug.CollectionTypeView}
)

const collectionsGridID = "dbviewer.collections"

// Which pane of the collections screen holds the keyboard. The detail panes
// are paneColumns, paneReferrers and paneForeignKeys shifted by one.
const (
	focusList = iota
	focusColumns
	focusReferrers
	focusForeignKeys
)

// collectionsLoaded is the result of loadCollections.
type collectionsLoaded struct {
	kind  collectionKind
	items []*datatug.CollectionInfo
	err   error
}

// loadCollections reads the collections of one kind off the event loop.
func loadCollections(schema schemer.CollectionsProvider, kind collectionKind) tea.Cmd {
	return func() tea.Msg {
		reader, err := schema.GetCollections(context.Background(), nil)
		if err != nil {
			return collectionsLoaded{kind: kind, err: err}
		}
		var items []*datatug.CollectionInfo
		for {
			item, err := reader.NextCollection()
			if err != nil && !errors.Is(err, io.EOF) {
				return collectionsLoaded{kind: kind, err: err}
			}
			if err != nil || item == nil {
				return collectionsLoaded{kind: kind, items: items}
			}
			if item.Type() == kind.typ {
				items = append(items, item)
			}
		}
	}
}

// collections is the Tables (or Views) screen: the names in a filterable list
// and, when the database has a schema, the columns, referrers and foreign keys of
// the highlighted one next to it.
type collections struct {
	db      dtviewers.DbContext
	schema  schemer.SchemaProvider
	kind    collectionKind
	items   []*datatug.CollectionInfo
	list    *grid.Model
	panes   [paneKinds]detailPane
	cur     dal.CollectionRef
	focus   int
	ready   bool
	focused bool
	w, h    int
}

var (
	_ nav.Screen       = collections{}
	_ nav.Borderless   = collections{}
	_ nav.KeyCapturer  = collections{}
	_ nav.ShortHelper  = collections{}
	_ widgets.Boundary = collections{}
	_ widgets.Editor   = collections{}
)

var collectionsHelp = []key.Binding{
	key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←/→", "switch pane")),
	key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter")),
}

func newCollections(db dtviewers.DbContext, kind collectionKind) collections {
	s := collections{db: db, schema: db.Schema(), kind: kind}
	s.list = grid.New([]grid.Column{{Name: "Name"}}, nil, grid.WithID(collectionsGridID),
		grid.WithoutFrame(), grid.WithRowSelection(), grid.WithFilterOnType())
	if s.schema != nil {
		for k := range paneKinds {
			s.panes[k] = newDetailPane(k)
		}
	}
	return s
}

func (s collections) hasDetail() bool { return s.schema != nil }

// Init implements nav.Screen.
func (s collections) Init() tea.Cmd {
	if !s.hasDetail() {
		return nil
	}
	return loadCollections(s.schema, s.kind)
}

// Update implements nav.Screen.
func (s collections) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.w, s.h = msg.Width, msg.Height
		return s.relayout(), nil
	case nav.ScreenFocusMsg:
		s.focused = msg.Focused
		return s.syncFocus(), nil
	case collectionsLoaded:
		return s.loaded(msg)
	case paneLoaded:
		for k := range s.panes {
			s.panes[k] = s.panes[k].apply(msg)
		}
		return s, nil
	case grid.SelectionChangedMsg:
		if msg.ID == collectionsGridID {
			return s.selected(msg.Row)
		}
		return s, nil
	case grid.RowActivatedMsg:
		return s, s.activated(msg)
	case tea.KeyPressMsg:
		return s.key(msg)
	}
	return s, nil
}

func (s collections) loaded(msg collectionsLoaded) (nav.Screen, tea.Cmd) {
	if msg.kind != s.kind {
		return s, nil
	}
	if msg.err != nil {
		return s, datatugui.ReportError("load "+strings.ToLower(s.kind.title), msg.err)
	}
	s.items, s.ready = msg.items, true
	rows := make([]grid.Row, len(msg.items))
	for i, item := range msg.items {
		rows[i] = grid.Row{Key: item.Name(), Values: []any{item.Name()}, Ref: item}
	}
	s.list.SetData([]grid.Column{{Name: "Name"}}, rows)
	row, ok := s.list.CurrentRow()
	if !ok {
		row = grid.Row{}
	}
	return s.selected(row)
}

// selected shows the details of the highlighted collection.
func (s collections) selected(row grid.Row) (nav.Screen, tea.Cmd) {
	if !s.hasDetail() {
		return s, nil
	}
	info, ok := row.Ref.(*datatug.CollectionInfo)
	if !ok {
		for k := range s.panes {
			s.panes[k] = s.panes[k].clear()
		}
		return s.relayout(), nil
	}
	s.cur = info.Ref
	cmds := make([]tea.Cmd, 0, len(s.panes))
	for k := range s.panes {
		s.panes[k] = s.panes[k].show(info.Ref.Name())
		cmds = append(cmds, loadPane(paneKind(k), s.schema, info.Ref))
	}
	return s.relayout(), tea.Batch(cmds...)
}

// activated opens the table of the activated row: a collection, or the table a
// foreign key or a referrer points to.
func (s collections) activated(msg grid.RowActivatedMsg) tea.Cmd {
	switch msg.ID {
	case collectionsGridID:
		return openTable(s.db, msg.Row.Ref.(*datatug.CollectionInfo).Ref)
	case paneSpecs[paneReferrers].id, paneSpecs[paneForeignKeys].id:
		return openTable(s.db, dal.NewCollectionRef(msg.Row.Ref.(string), "", s.cur.Parent()))
	}
	return nil
}

// openTable drills into the screen of one table.
func openTable(db dtviewers.DbContext, ref dal.CollectionRef) tea.Cmd {
	return datatugui.Drill(ref.Name(), newTableScreen(dtviewers.CollectionContext{DbContext: db, CollectionRef: ref}))
}

// key moves the keyboard between the panes and otherwise gives keys to the pane
// that holds it.
func (s collections) key(msg tea.KeyPressMsg) (nav.Screen, tea.Cmd) {
	if s.hasDetail() && !s.Editing() {
		if to, ok := s.focusTarget(msg.String()); ok {
			return s.moveFocus(to), nil
		}
	}
	if s.focus == focusList {
		_, cmd := s.list.Update(msg)
		return s, cmd
	}
	var cmd tea.Cmd
	s.panes[s.focus-1], cmd = s.panes[s.focus-1].update(msg)
	return s, cmd
}

// focusTarget is the pane an arrow key moves the keyboard to.
func (s collections) focusTarget(arrow string) (int, bool) {
	switch {
	case arrow == "right" && s.focus < focusReferrers:
		return s.focus + 1, true
	case arrow == "left" && s.focus == focusForeignKeys:
		return focusColumns, true
	case arrow == "left" && s.focus > focusList:
		return s.focus - 1, true
	case arrow == "up" && s.focus == focusForeignKeys && s.panes[paneForeignKeys].atEdge(widgets.Up):
		return focusReferrers, true
	case arrow == "down" && s.focus == focusReferrers && s.panes[paneReferrers].atEdge(widgets.Down):
		return focusForeignKeys, true
	}
	return 0, false
}

func (s collections) moveFocus(to int) collections {
	s.focus = to
	return s.syncFocus()
}

// syncFocus tells the list and the panes whether they hold the keyboard.
func (s collections) syncFocus() collections {
	s.list.SetFocused(s.focused && s.focus == focusList)
	for k := range s.panes {
		s.panes[k] = s.panes[k].focus(s.focused && s.focus == k+1)
	}
	return s
}

// box is the size of one pane including its frame.
type box struct{ w, h int }

// layout divides the screen: the list, the columns and a column with the
// referrers over the foreign keys, in proportions 2, 4 and 3.
func (s collections) layout() (boxes [focusForeignKeys + 1]box) {
	if !s.hasDetail() {
		boxes[focusList] = box{s.w, s.h}
		return boxes
	}
	widths := widgets.Split(s.w, widgets.Fill(2), widgets.Fill(4), widgets.Fill(3))
	heights := widgets.Split(s.h, widgets.Fill(1), widgets.Fill(1))
	boxes[focusList] = box{widths[0], s.h}
	boxes[focusColumns] = box{widths[1], s.h}
	boxes[focusReferrers] = box{widths[2], heights[0]}
	boxes[focusForeignKeys] = box{widths[2], heights[1]}
	return boxes
}

func (s collections) frame(pane int) widgets.Frame {
	title := counted(s.kind.title, len(s.items))
	if pane > focusList {
		title = s.panes[pane-1].title()
	}
	return widgets.NewFrame().WithTitle(title).WithFocus(s.focused && s.focus == pane)
}

// relayout gives the list and the panes the inner size of their frames.
func (s collections) relayout() collections {
	boxes := s.layout()
	w, h := s.frame(focusList).Inner(boxes[focusList].w, boxes[focusList].h)
	s.list.SetSize(w, h)
	for k := range s.panes {
		w, h := s.frame(k+1).Inner(boxes[k+1].w, boxes[k+1].h)
		s.panes[k] = s.panes[k].resize(w, h)
	}
	return s
}

// View implements nav.Screen.
func (s collections) View() string {
	boxes := s.layout()
	frame := s.frame(focusList)
	w, h := frame.Inner(boxes[focusList].w, boxes[focusList].h)
	body := statusView("Loading...", false, w, h)
	if s.ready || !s.hasDetail() {
		body = widgets.Fit(s.list.View(w, s.focused && s.focus == focusList), w, h)
	}
	list := frame.Render(body, boxes[focusList].w, boxes[focusList].h)
	if !s.hasDetail() {
		return list
	}
	render := func(pane int) string {
		return s.frame(pane).Render(s.panes[pane-1].view(), boxes[pane].w, boxes[pane].h)
	}
	right := lipgloss.JoinVertical(lipgloss.Left, render(focusReferrers), render(focusForeignKeys))
	return lipgloss.JoinHorizontal(lipgloss.Top, list, render(focusColumns), right)
}

// Borderless implements nav.Borderless: the panes draw their own frames.
func (collections) Borderless() bool { return true }

// AtEdge implements widgets.Boundary. Up and Left leave the screen from the
// panes at the top-left; moves between the panes are the screen's own.
func (s collections) AtEdge(dir widgets.Direction) bool {
	if s.focus == focusList {
		return !s.Editing() && s.list.AtEdge(dir)
	}
	return dir == widgets.Up && s.focus != focusForeignKeys && s.panes[s.focus-1].atEdge(dir)
}

// Editing implements widgets.Editor: the list's filter is being typed.
func (s collections) Editing() bool { return s.focus == focusList && s.list.Editing() }

// CapturesKey implements nav.KeyCapturer: Esc clears the filter.
func (s collections) CapturesKey(msg tea.KeyPressMsg) bool {
	return msg.String() == "esc" && s.focus == focusList && s.list.CapturesEsc()
}

// ShortHelp implements nav.ShortHelper.
func (collections) ShortHelp() []key.Binding { return collectionsHelp }
