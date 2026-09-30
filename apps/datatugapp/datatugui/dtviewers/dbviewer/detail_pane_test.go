package dbviewer

import (
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"github.com/tuigoff/tuigoff/pkg/widgets"
)

func loadPaneMsg(t *testing.T, kind paneKind, schema schemer.SchemaProvider, table string) paneLoaded {
	t.Helper()
	msg := loadPane(kind, schema, dal.NewCollectionRef(table, "", nil))()
	loaded, ok := msg.(paneLoaded)
	require.True(t, ok)
	return loaded
}

func TestLoadPane_Columns(t *testing.T) {
	schema := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t)).Schema()
	msg := loadPaneMsg(t, paneColumns, schema, "items")
	require.NoError(t, msg.err)
	assert.Equal(t, "items", msg.coll)
	require.Len(t, msg.rows, 3)
	assert.Equal(t, []any{"id", "INTEGER", "1", ""}, msg.rows[0].Values)
	assert.NotNil(t, msg.rows[0].Ref)
	assert.Equal(t, []any{"orderID", "INTEGER", "", "orders(id)"}, msg.rows[1].Values)
	assert.Nil(t, msg.rows[1].Ref)
}

func TestLoadPane_ForeignKeysAndReferrers(t *testing.T) {
	schema := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t)).Schema()
	fks := loadPaneMsg(t, paneForeignKeys, schema, "items")
	require.Len(t, fks.rows, 1)
	assert.Equal(t, []any{"orderID", "—>", "orders", "(id)"}, fks.rows[0].Values)
	assert.Equal(t, "orders", fks.rows[0].Ref)

	refs := loadPaneMsg(t, paneReferrers, schema, "orders")
	require.Len(t, refs.rows, 1)
	assert.Equal(t, []any{"<—", "items", "(orderID)"}, refs.rows[0].Values)
	assert.Equal(t, "items", refs.rows[0].Ref)
}

func TestLoadPane_Errors(t *testing.T) {
	real := dtviewers.GetSQLiteDbContext(createTestSqliteDb(t)).Schema()
	boom := errors.New("boom")
	cases := map[string]struct {
		kind   paneKind
		schema *fakeSchema
	}{
		"columns":     {paneColumns, &fakeSchema{SchemaProvider: real, columnsErr: boom}},
		"columns fks": {paneColumns, &fakeSchema{SchemaProvider: real, fksErr: boom}},
		"fks":         {paneForeignKeys, &fakeSchema{SchemaProvider: real, fksErr: boom}},
		"referrers":   {paneReferrers, &fakeSchema{SchemaProvider: real, referrersErr: boom}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, loadPaneMsg(t, c.kind, c.schema, "items").err, boom)
		})
	}
}

func TestFindForeignKey(t *testing.T) {
	fks := []schemer.ForeignKey{
		{From: schemer.FKAnchor{Columns: []string{"a"}}}, // no target column
		{From: schemer.FKAnchor{Columns: []string{"a", "b"}}, To: schemer.FKAnchor{Name: "t", Columns: []string{"x"}}},
	}
	fk, ok := findForeignKey(fks, "b")
	assert.True(t, ok)
	assert.Equal(t, "t", fk.To.Name)
	fk, ok = findForeignKey(fks, "a")
	assert.True(t, ok)
	assert.Equal(t, "t", fk.To.Name)
	_, ok = findForeignKey(fks, "c")
	assert.False(t, ok)
}

func TestDetailPane_Lifecycle(t *testing.T) {
	p := newDetailPane(paneReferrers).resize(30, 6)
	assert.Equal(t, "Referrers", p.title())
	assert.True(t, p.atEdge(widgets.Up)) // nothing to move in
	_, cmd := p.update(uitest.Key("down"))
	assert.Nil(t, cmd)

	p = p.show("orders")
	assert.Equal(t, "orders: Referrers", p.title())
	assert.Contains(t, uitest.Plain(p.view()), "Loading...")

	// A result for another pane or collection is ignored.
	p = p.apply(paneLoaded{kind: paneColumns, coll: "orders"}).apply(paneLoaded{kind: paneReferrers, coll: "users"})
	assert.Contains(t, uitest.Plain(p.view()), "Loading...")

	p = p.apply(paneLoaded{kind: paneReferrers, coll: "orders", err: errors.New("boom")})
	assert.Contains(t, uitest.Plain(p.view()), "Error: boom")
	assert.True(t, p.failed)

	p = p.show("orders").apply(paneLoaded{kind: paneReferrers, coll: "orders"})
	assert.Contains(t, uitest.Plain(p.view()), "No referrers")

	cols := []grid.Column{{Name: "T"}}
	rows := []grid.Row{{Key: "0", Values: []any{"items"}}, {Key: "1", Values: []any{"logs"}}}
	p = p.show("orders").focus(true).apply(paneLoaded{kind: paneReferrers, coll: "orders", cols: cols, rows: rows})
	view := uitest.Plain(p.view())
	assert.Contains(t, view, "items")
	assert.Contains(t, view, "logs")
	assert.True(t, p.atEdge(widgets.Up))
	assert.False(t, p.atEdge(widgets.Down))
	p, cmd = p.update(uitest.Key("down"))
	assert.NotNil(t, cmd) // the selection changed
	p = p.resize(40, 8).focus(false)
	assert.NotEmpty(t, p.view())

	p = p.clear()
	assert.Equal(t, "Referrers", p.title())
	assert.Empty(t, strings.TrimSpace(uitest.Plain(p.view())))
}

func TestColumnCellStyle(t *testing.T) {
	assert.Equal(t, theme.AccentColor(), columnCellStyle(grid.Row{Ref: true}, 0, nil).GetForeground())
	assert.NotEqual(t, theme.AccentColor(), columnCellStyle(grid.Row{Ref: true}, 1, nil).GetForeground())
	assert.NotEqual(t, theme.AccentColor(), columnCellStyle(grid.Row{}, 0, nil).GetForeground())
}
