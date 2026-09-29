package dbviewer

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/strongo/strongo-tui/pkg/grid"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const (
	contentGridID = "dbviewer.content"
	previewGridID = "dbviewer.preview"

	// previewHeight is the height of the frame that shows the row a foreign key
	// points to, and minHeightForPreview the pane height below which it is hidden.
	previewHeight       = 6
	minHeightForPreview = 12
)

// contentLoaded is the result of loadContent.
type contentLoaded struct {
	coll string
	rs   recordset.Recordset
	fks  []schemer.ForeignKey
	err  error
}

// loadContent reads all the records of a collection, and its foreign keys, off
// the event loop. Foreign keys only add navigation: when they cannot be read the
// content is still shown, with the "<Table>ID" naming convention as the only
// hint about what a column refers to.
func loadContent(collCtx dtviewers.CollectionContext) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		name := collCtx.CollectionRef.Name()
		msg := contentLoaded{coll: name}
		db, err := collCtx.GetDB(ctx)
		if err != nil {
			msg.err = err
			return msg
		}
		q := dal.From(collCtx.CollectionRef).NewQuery().SelectIntoRecordset(recordset.WithName(name))
		if msg.rs, msg.err = dal.ExecuteQueryAndReadAllToRecordset(ctx, q, db); msg.err != nil {
			return msg
		}
		if schema := collCtx.Schema(); schema != nil {
			msg.fks, _ = schema.GetForeignKeys(ctx, "", name)
		}
		return msg
	}
}

// previewLoaded is the result of loadPreview; seq tells which selection asked.
type previewLoaded struct {
	seq int
	rs  recordset.Recordset
	err error
}

// loadPreview reads the records the current cell's foreign key points to.
func loadPreview(seq int, db dtviewers.DbContext, q dal.Query) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		conn, err := db.GetDB(ctx)
		if err != nil {
			return previewLoaded{seq: seq, err: err}
		}
		rs, err := dal.ExecuteQueryAndReadAllToRecordset(ctx, q, conn)
		return previewLoaded{seq: seq, rs: rs, err: err}
	}
}

// preview is the frame under the content that shows the row the current cell's
// foreign key points to.
type preview struct {
	title  string
	seq    int
	status string
	failed bool
	grid   *grid.Model
}

// contentPane is the Content tab: the records of the collection in a grid, and
// under it the preview of the foreign-key target of the current cell.
type contentPane struct {
	db           dtviewers.DbContext
	status       string
	failed       bool
	src          recordsetSource
	fks          []schemer.ForeignKey
	grid         *grid.Model
	preview      *preview
	seq          int
	previewFocus bool
	w, h         int
	focused      bool
}

func newContentPane(db dtviewers.DbContext) contentPane {
	return contentPane{db: db, status: "Loading..."}
}

// split divides the height between the grid and the preview frame.
func (p contentPane) split() (mainH, previewH int) {
	if p.preview == nil || p.h < minHeightForPreview {
		return p.h, 0
	}
	return p.h - previewHeight, previewHeight
}

// resize gives the pane its size.
func (p contentPane) resize(w, h int) contentPane {
	p.w, p.h = w, h
	mainH, previewH := p.split()
	if p.grid != nil {
		p.grid.SetSize(w, mainH)
	}
	if p.preview != nil && p.preview.grid != nil {
		iw, ih := previewFrame(p.preview.title, false).Inner(w, previewH)
		p.preview.grid.SetSize(iw, ih)
	}
	return p
}

func previewFrame(title string, focused bool) widgets.Frame {
	return widgets.NewFrame().WithTitle(title).WithFocus(focused)
}

// focus sets whether the pane holds the keyboard and which grid gets it.
func (p contentPane) focus(focused bool) contentPane {
	p.focused = focused
	if p.grid != nil {
		p.grid.SetFocused(focused && !p.previewFocus)
	}
	if p.preview != nil && p.preview.grid != nil {
		p.preview.grid.SetFocused(focused && p.previewFocus)
	}
	return p
}

// focusPreview moves the keyboard to the preview when there is one to show.
func (p contentPane) focusPreview(on bool) contentPane {
	if _, previewH := p.split(); on && (previewH == 0 || p.preview.grid == nil) {
		return p
	}
	p.previewFocus = on
	return p.focus(p.focused)
}

// apply takes the loaded records and starts the preview for the first cell.
func (p contentPane) apply(msg contentLoaded) (contentPane, tea.Cmd) {
	if msg.err != nil {
		p.status, p.failed = "Error: "+msg.err.Error(), true
		return p, nil
	}
	p.fks = msg.fks
	p.src = newRecordsetSource(msg.rs, msg.fks)
	p.grid = p.src.newGrid(contentGridID)
	p = p.resize(p.w, p.h).focus(p.focused)
	return p.previewFor(0, 0)
}

// previewFor shows the target of the foreign key that starts at the given cell,
// or nothing when the column has none.
func (p contentPane) previewFor(row, column int) (contentPane, tea.Cmd) {
	p.seq++
	p.preview, p.previewFocus = nil, false
	rs := p.src.rs
	if row < 0 || row >= rs.RowsCount() || column < 0 || column >= rs.ColumnsCount() {
		return p.resize(p.w, p.h).focus(p.focused), nil
	}
	col := rs.GetColumnByIndex(column)
	fk, ok := findForeignKey(p.fks, col.Name())
	if !ok {
		return p.resize(p.w, p.h).focus(p.focused), nil
	}
	value, _ := col.GetValue(row)
	q := dal.From(dal.NewCollectionRef(fk.To.Name, "", nil)).NewQuery().
		WhereField(fk.To.Columns[0], dal.Equal, dal.NewConstant(value)).
		SelectIntoRecordset()
	p.preview = &preview{title: fk.To.Name, seq: p.seq, status: "Loading..."}
	return p.resize(p.w, p.h).focus(p.focused), loadPreview(p.seq, p.db, q)
}

// applyPreview takes the records of a preview, ignoring one that is out of date.
func (p contentPane) applyPreview(msg previewLoaded) contentPane {
	if p.preview == nil || p.preview.seq != msg.seq {
		return p
	}
	next := *p.preview
	if msg.err != nil {
		next.status, next.failed = "Error: "+msg.err.Error(), true
	} else {
		next.grid = newRecordsetSource(msg.rs, nil).newGrid(previewGridID)
	}
	p.preview = &next
	return p.resize(p.w, p.h).focus(p.focused)
}

// update passes a message to the grid that holds the keyboard.
func (p contentPane) update(msg tea.Msg) (contentPane, tea.Cmd) {
	target := p.grid
	if p.previewFocus {
		target = p.preview.grid
	}
	if target == nil {
		return p, nil
	}
	_, cmd := target.Update(msg)
	return p, cmd
}

// atEdge tells whether an arrow key would leave the grid that holds the keyboard.
func (p contentPane) atEdge(dir widgets.Direction) bool {
	target := p.grid
	if p.previewFocus {
		target = p.preview.grid
	}
	return target == nil || target.AtEdge(dir)
}

// current returns the grid that holds the keyboard.
func (p contentPane) current() *grid.Model {
	if p.previewFocus {
		return p.preview.grid
	}
	return p.grid
}

// followTable is the table that Enter on a column opens, or "".
func (p contentPane) followTable(column int) string { return p.src.follow[column] }

// detail returns the title and the text of the dialog that shows a whole row of
// the grid that holds the keyboard, with the selected column marked.
func (p contentPane) detail(row grid.Row, column int) (title, text string) {
	name := p.src.rs.Name()
	if p.previewFocus {
		name = p.preview.title
	}
	index, _ := strconv.Atoi(row.Key)
	columns := p.current().Columns()
	var b strings.Builder
	for c, v := range row.Values {
		marker := "  "
		if c == column {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%s: %v\n", marker, columns[c].Name, v)
	}
	return fmt.Sprintf("%s #%d", name, index+1), strings.TrimRight(b.String(), "\n")
}

// view renders the pane.
func (p contentPane) view() string {
	if p.grid == nil {
		return statusView(p.status, p.failed, p.w, p.h)
	}
	mainH, previewH := p.split()
	main := widgets.Fit(p.grid.View(p.w, p.focused && !p.previewFocus), p.w, mainH)
	if previewH == 0 {
		return main
	}
	frame := previewFrame(p.preview.title, p.focused && p.previewFocus)
	iw, ih := frame.Inner(p.w, previewH)
	body := statusView(p.preview.status, p.preview.failed, iw, ih)
	if p.preview.grid != nil {
		body = widgets.Fit(p.preview.grid.View(iw, p.focused && p.previewFocus), iw, ih)
	}
	return main + "\n" + frame.Render(body, p.w, previewH)
}
