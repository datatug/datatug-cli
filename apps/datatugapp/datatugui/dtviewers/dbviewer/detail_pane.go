package dbviewer

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

// paneKind is the kind of schema information a detailPane shows.
type paneKind int

const (
	paneColumns paneKind = iota
	paneReferrers
	paneForeignKeys
	paneKinds
)

// paneSpec describes how one kind of pane is captioned.
var paneSpecs = [paneKinds]struct{ id, title, empty string }{
	paneColumns:     {"dbviewer.columns", "Columns", "No columns"},
	paneReferrers:   {"dbviewer.referrers", "Referrers", "No referrers"},
	paneForeignKeys: {"dbviewer.fks", "Foreign Keys", "No foreign keys"},
}

// paneLoaded is the result of loadPane: the rows of one pane for one collection.
type paneLoaded struct {
	kind paneKind
	coll string
	cols []grid.Column
	rows []grid.Row
	err  error
}

// loadPane reads the columns, referrers or foreign keys of a collection off the
// event loop. A row's Ref is what Enter opens (the name of a table) or, in the
// columns pane, whether the column is part of the primary key.
func loadPane(kind paneKind, schema schemer.SchemaProvider, ref dal.CollectionRef) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		msg := paneLoaded{kind: kind, coll: ref.Name()}
		switch kind {
		case paneColumns:
			msg.cols, msg.rows, msg.err = columnRows(ctx, schema, ref)
		case paneReferrers:
			msg.cols, msg.rows, msg.err = referrerRows(ctx, schema, ref.Name())
		default:
			msg.cols, msg.rows, msg.err = foreignKeyRows(ctx, schema, ref.Name())
		}
		return msg
	}
}

func columnRows(ctx context.Context, schema schemer.SchemaProvider, ref dal.CollectionRef) ([]grid.Column, []grid.Row, error) {
	columns, err := schema.GetColumns(ctx, "", schemer.ColumnsFilter{CollectionRef: &ref})
	if err != nil {
		return nil, nil, err
	}
	fks, err := schema.GetForeignKeys(ctx, "", ref.Name())
	if err != nil {
		return nil, nil, err
	}
	rows := make([]grid.Row, len(columns))
	for i, col := range columns {
		pk := ""
		if col.PrimaryKeyPosition > 0 {
			pk = strconv.Itoa(col.PrimaryKeyPosition)
		}
		fkText := ""
		if fk, ok := findForeignKey(fks, col.Name); ok {
			fkText = fk.To.Name
			if len(fk.To.Columns) > 1 || fk.To.Columns[0] != col.Name {
				fkText += "(" + strings.Join(fk.To.Columns, ",") + ")"
			}
		}
		var pkMark any
		if col.PrimaryKeyPosition > 0 {
			pkMark = true
		}
		rows[i] = grid.Row{Key: col.Name, Values: []any{col.Name, col.DbType, pk, fkText}, Ref: pkMark}
	}
	return []grid.Column{{Name: "Name"}, {Name: "Type"}, {Name: "PK", Numeric: true}, {Name: "FKs"}}, rows, nil
}

func referrerRows(ctx context.Context, schema schemer.SchemaProvider, name string) ([]grid.Column, []grid.Row, error) {
	referrers, err := schema.GetReferrers(ctx, "", name)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]grid.Row, len(referrers))
	for i, r := range referrers {
		rows[i] = grid.Row{Key: strconv.Itoa(i), Ref: r.From.Name,
			Values: []any{"<—", r.From.Name, "(" + strings.Join(r.From.Columns, ",") + ")"}}
	}
	return []grid.Column{{Name: ""}, {Name: "Table"}, {Name: "Columns"}}, rows, nil
}

func foreignKeyRows(ctx context.Context, schema schemer.SchemaProvider, name string) ([]grid.Column, []grid.Row, error) {
	fks, err := schema.GetForeignKeys(ctx, "", name)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]grid.Row, len(fks))
	for i, fk := range fks {
		key := ""
		if !slices.Equal(fk.To.Columns, fk.From.Columns) {
			key = "(" + strings.Join(fk.To.Columns, ",") + ")"
		}
		rows[i] = grid.Row{Key: strconv.Itoa(i), Ref: fk.To.Name,
			Values: []any{strings.Join(fk.From.Columns, ","), "—>", fk.To.Name, key}}
	}
	return []grid.Column{{Name: "Column"}, {Name: ""}, {Name: "Table"}, {Name: "Key"}}, rows, nil
}

// findForeignKey returns the first foreign key that starts at column name and
// has a target column, which is what a viewer can follow.
func findForeignKey(fks []schemer.ForeignKey, name string) (schemer.ForeignKey, bool) {
	for _, fk := range fks {
		if slices.Contains(fk.From.Columns, name) && len(fk.To.Columns) > 0 {
			return fk, true
		}
	}
	return schemer.ForeignKey{}, false
}

// detailPane shows one kind of schema information about the current collection:
// a status line while loading or when there is nothing to show, a grid otherwise.
type detailPane struct {
	kind    paneKind
	coll    string
	status  string
	failed  bool
	grid    *grid.Model
	w, h    int
	focused bool
}

func newDetailPane(kind paneKind) detailPane { return detailPane{kind: kind} }

// show starts showing a collection: the previous rows are dropped until the
// load reports.
func (p detailPane) show(coll string) detailPane {
	p.coll, p.status, p.failed, p.grid = coll, "Loading...", false, nil
	return p
}

// clear shows nothing.
func (p detailPane) clear() detailPane {
	p.coll, p.status, p.failed, p.grid = "", "", false, nil
	return p
}

// resize gives the pane the size of its inner area.
func (p detailPane) resize(w, h int) detailPane {
	p.w, p.h = w, h
	if p.grid != nil {
		p.grid.SetSize(w, h)
	}
	return p
}

// focus sets whether the pane holds the keyboard.
func (p detailPane) focus(focused bool) detailPane {
	p.focused = focused
	if p.grid != nil {
		p.grid.SetFocused(focused)
	}
	return p
}

// apply takes a load result, ignoring one for another pane or collection.
func (p detailPane) apply(msg paneLoaded) detailPane {
	if msg.kind != p.kind || msg.coll != p.coll {
		return p
	}
	switch {
	case msg.err != nil:
		p.status, p.failed = "Error: "+msg.err.Error(), true
	case len(msg.rows) == 0:
		p.status = paneSpecs[p.kind].empty
	default:
		opts := []grid.Option{grid.WithID(paneSpecs[p.kind].id), grid.WithoutFrame(), grid.WithRowSelection(), grid.WithFilterDisabled()}
		if p.kind == paneColumns {
			opts = append(opts, grid.WithCellStyle(columnCellStyle))
		}
		p.grid = grid.New(msg.cols, msg.rows, opts...)
		p = p.resize(p.w, p.h).focus(p.focused)
	}
	return p
}

// update passes a message to the grid.
func (p detailPane) update(msg tea.Msg) (detailPane, tea.Cmd) {
	if p.grid == nil {
		return p, nil
	}
	_, cmd := p.grid.Update(msg)
	return p, cmd
}

// atEdge tells whether an arrow key would leave the pane's content.
func (p detailPane) atEdge(dir widgets.Direction) bool {
	return p.grid == nil || p.grid.AtEdge(dir)
}

// view renders the pane in its inner area.
func (p detailPane) view() string {
	if p.grid != nil {
		return widgets.Fit(p.grid.View(p.w, p.focused), p.w, p.h)
	}
	return statusView(p.status, p.failed, p.w, p.h)
}

// title is the caption of the pane's frame.
func (p detailPane) title() string {
	if p.coll == "" {
		return paneSpecs[p.kind].title
	}
	return p.coll + ": " + paneSpecs[p.kind].title
}

// statusView renders a one-line status, muted or red, in a w x h area.
func statusView(status string, failed bool, w, h int) string {
	colour := theme.MutedColor()
	if failed {
		colour = theme.ErrorColor()
	}
	return widgets.Fit(lipgloss.NewStyle().Foreground(colour).Render(status), w, h)
}

// columnCellStyle highlights the names of primary key columns.
func columnCellStyle(row grid.Row, column int, _ any) lipgloss.Style {
	if column == 0 && row.Ref != nil {
		return lipgloss.NewStyle().Foreground(theme.AccentColor())
	}
	return lipgloss.NewStyle()
}

// counted is the frame title of a list with n entries.
func counted(title string, n int) string { return fmt.Sprintf("%s (%d)", title, n) }
