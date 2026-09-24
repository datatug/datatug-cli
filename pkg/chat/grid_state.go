package chat

import (
	"strconv"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/ai/session"
	"github.com/strongo/aichat/tui/grid"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// maxGridHeight bounds a transcript grid's own page size (table rows) and,
// two lines shorter, its secondary-view (chart/current-row/raw/headers) pane
// height.
const maxGridHeight = 12

// gridState is a thin wrapper around strongo/aichat's tui/grid.Model: the
// table itself, per-cell column selection, scrollbar, style presets, sort,
// filter and the card/footer chrome all live in grid.Model (see
// tui/grid/grid.go, render.go, style.go, update.go in that module).
// gridState keeps only what a generic grid cannot know about DataTug: chart
// candidates, an HTTP response's raw body/headers (rendered as ExtraViews),
// a version badge, and the RecordSet's raw (unformatted) values, indexed by
// the RecordSet's own row order (grid.Row.Key round-trips a row's position
// in that order across a Sort).
type gridState struct {
	*grid.Model
	raw          [][]any
	baseTitle    string
	charts       []ChartCandidate
	chartIndex   int
	rawBody      []byte
	httpResponse *HTTPResponse
	versionBadge string
	// entryID is the transcript block ID this grid is currently appended
	// under (set by ChatUI.appendGridResult/loadSession). It lets a caller
	// that only has a RecordSetID — e.g. OnMsg's bridgeTickMsg case,
	// restoring focus across a session reload — recover the id
	// chatshell.Model.FocusEntry needs, without chatshell offering a
	// focus-by-ref lookup.
	entryID string
}

// setVersionBadge prefixes ("unchanged"/"changed") or clears the grid's
// title with a version badge, without losing the underlying title text.
func (g *gridState) setVersionBadge(badge string) {
	g.versionBadge = badge
	if badge == "" {
		g.SetTitle(g.baseTitle)
		return
	}
	g.SetTitle(badge + " · " + g.baseTitle)
}

// view renders the grid at its own last-set width/focus, for a caller
// outside the transcript (a dialog) that doesn't otherwise track those.
func (g *gridState) view() string { return g.View(g.Width(), g.Focused()) }

// gridViewCharts/gridViewCurrentRow/gridViewRaw/gridViewHeaders are the
// (fixed, by registration order — see newGridState/attachHTTPResponse) grid
// view indices for DataTug's own ExtraViews, matching the founder-visible
// "1 Table · 2 Charts · 3 Current row · 4 Raw · 5 Headers" numbering.
const (
	gridViewCharts     grid.View = 1
	gridViewCurrentRow grid.View = 2
	gridViewRaw        grid.View = 3
	gridViewHeaders    grid.View = 4
)

// gridRecordSetRefType tags a grid row's session.EntityRef so ChatUI can
// recover "the currently-focused grid's RecordSetID" generically from
// chatshell.Model.FocusedRef() (see ChatUI.activeGrid in chatui_inspector.go)
// instead of tracking a focused-grid index/field itself.
const gridRecordSetRefType = "datatug.recordset"

// recordSetRef builds the per-row Ref recordSetID rows carry when non-empty
// (transcript-appended grids); dock/bookmark/lookup grids in the SidePanel
// or a dialog pass "" since chatshell's FocusedRef() only ever reports the
// transcript zone's focus.
func recordSetRef(recordSetID string) *session.EntityRef {
	if recordSetID == "" {
		return nil
	}
	return &session.EntityRef{Type: gridRecordSetRefType, Keys: map[string]string{"recordSetID": recordSetID}}
}

// newGridState builds a full-chrome grid (view switcher: Table/Charts/
// Current row, split layout) — a chat transcript entry's own recordset grid.
func newGridState(model GridModel, recordSetID, title string, width int, statistics ...secureread.RecordSetStatistics) *gridState {
	return newProjectedGridState(model, recordSetID, title, width, nil, true, nil, statistics...)
}

// newMinimalGridState builds a grid with NO view switcher (no Charts/
// Current-row views, no split layout) — main's bookmark, dock and
// parameter-lookup grids never had one; they already show a narrow,
// purpose-built row set where "2"/"3" would have nothing to switch to (m9).
func newMinimalGridState(model GridModel, recordSetID, title string, width int) *gridState {
	return newProjectedGridState(model, recordSetID, title, width, nil, false, []grid.Option{grid.WithoutViewSwitcher()})
}

// newProjectedGridState is newGridState for a dock's projected/filtered
// view of a RecordSet (see rebuildDockGrids): sourceRows[i], when given,
// maps model.Rows[i] back to its row index in the FULL RecordSet (used for
// FK-preview lookups, range selection and cross-grid sort sync) — the same
// role GridModel.SourceRows played before grid.Row.Key became the carrier.
// A nil/short sourceRows is the identity mapping (the common, non-projected
// case). fullChrome registers the Charts/Current-row views and split
// layout (see newGridState/newMinimalGridState). extraOpts lets a caller
// (rebuildDockGrids, for a view-backed dock) pass grid.WithInitialSort so a
// grid built from already-externally-sorted rows still knows which
// column/direction that is.
func newProjectedGridState(model GridModel, recordSetID, title string, width int, sourceRows []int, fullChrome bool, extraOpts []grid.Option, statistics ...secureread.RecordSetStatistics) *gridState {
	g := &gridState{raw: model.RawRows, baseTitle: normalizeGridTitle(title)}
	ref := recordSetRef(recordSetID)
	if len(statistics) > 0 {
		g.charts = InferChartCandidates(statistics[0])
	}
	cols := append([]grid.Column(nil), model.Columns...)
	rows := make([]grid.Row, len(model.Rows))
	for i := range model.Rows {
		values := make([]any, len(model.Columns))
		for c := range model.Columns {
			// NewGridModel leaves a cell's display string at its zero
			// value ("") for a column that was never present in the
			// underlying row's data (a sparse cell-range selection) —
			// distinct from an explicit SQL NULL, which formats to the
			// literal text "NULL" (see grid.go's NewGridModel comment).
			// grid.Absent is the sentinel that makes CardView/InspectorView
			// show "—" for it, instead of an ambiguous blank line (m5).
			if c < len(model.Rows[i]) && model.Rows[i][c] != "" {
				values[c] = model.Rows[i][c]
			} else {
				values[c] = grid.Absent
			}
		}
		key := i
		if i < len(sourceRows) {
			key = sourceRows[i]
		}
		rows[i] = grid.Row{Key: strconv.Itoa(key), Values: values, Ref: ref}
	}
	opts := []grid.Option{
		grid.WithTitle(g.baseTitle),
		grid.WithMaxVisibleRows(maxGridHeight - 2), // table page size; secondary-view height (paneHeight) is a cosmetic 2 lines shorter than the pre-adoption recordsetPaneHeight as a result
		grid.WithFilterDisabled(),                  // main deliberately never bound "/" to bubble-table's row filter (M3); restore that rather than adopt a feature main never had
	}
	if fullChrome {
		opts = append(opts,
			grid.WithExtraViews(g.chartsExtraView(), grid.CardView("Current row")),
			grid.WithSplitLayout(chooseGridLayout),
		)
	}
	opts = append(opts, extraOpts...)
	g.Model = grid.New(cols, rows, opts...)
	g.SetWidth(width)
	return g
}

// attachHTTPResponse records an HTTP response on an already-built gridState
// (a query's HTTP body/headers arrive after the grid itself, in
// updateGrid/httpCommand.go) and (re)registers the Raw/Headers ExtraViews —
// absent until now — so "4"/"5" become reachable and the header shows them.
func (g *gridState) attachHTTPResponse(body []byte, response *HTTPResponse) {
	g.rawBody = body
	g.httpResponse = response
	g.SetExtraViews(g.chartsExtraView(), grid.CardView("Current row"), g.rawExtraView(), g.headersExtraView())
}

// chartsExtraView renders DataTug's chart-candidate browser: the currently
// selected ChartCandidate's terminal chart, cycled with ↑↓. Ported from
// recordset_ui.go's secondaryView (recordsetCharts case) and updateSecondary.
func (g *gridState) chartsExtraView() grid.ExtraView {
	return grid.ExtraView{
		Label:      "Charts",
		ShortLabel: "C", // main's own short form (m3)
		Render: func(_ *grid.Model, width, height int) string {
			if len(g.charts) == 0 {
				return "No chart candidates for this recordset."
			}
			g.chartIndex = max(0, min(g.chartIndex, len(g.charts)-1))
			candidate := g.charts[g.chartIndex]
			return "Chart " + strconv.Itoa(g.chartIndex+1) + "/" + strconv.Itoa(len(g.charts)) + ": " + sanitizeTerminalText(candidate.Spec.Title) + "\n" +
				"↑↓ candidates • " + sanitizeTerminalText(candidate.Reason) + "\n" +
				renderChart(candidate.Spec, width, max(4, height-2))
		},
		Update: func(_ *grid.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
			switch msg.String() {
			case "up":
				if g.chartIndex > 0 {
					g.chartIndex--
				}
				return nil, true
			case "down":
				if g.chartIndex+1 < len(g.charts) {
					g.chartIndex++
				}
				return nil, true
			}
			return nil, false
		},
	}
}

// rawExtraView renders the saved HTTP response's raw body in a scrollable
// viewport. Ported from recordset_ui.go's secondaryView (recordsetRaw case).
func (g *gridState) rawExtraView() grid.ExtraView {
	vp := viewport.New(viewport.WithWidth(1), viewport.WithHeight(1))
	vp.SoftWrap = true
	return grid.ExtraView{
		Label: "Raw",
		Render: func(_ *grid.Model, width, height int) string {
			vp.SetWidth(max(1, width))
			vp.SetHeight(max(1, height))
			vp.SetContent(sanitizeMultilineText(boundedText(g.rawBody)))
			return vp.View()
		},
		Update: viewportScrollUpdate(&vp),
	}
}

// headersExtraView renders the saved HTTP response's request/response
// headers in a scrollable viewport. Ported from recordset_ui.go's
// secondaryView (recordsetHeaders case) and headersContent.
func (g *gridState) headersExtraView() grid.ExtraView {
	vp := viewport.New(viewport.WithWidth(1), viewport.WithHeight(1))
	vp.SoftWrap = true
	return grid.ExtraView{
		Label: "Headers",
		Render: func(_ *grid.Model, width, height int) string {
			vp.SetWidth(max(1, width))
			vp.SetHeight(max(1, height))
			content := "No HTTP response metadata."
			if g.httpResponse != nil {
				content = g.httpResponse.headersContent(width)
			}
			vp.SetContent(content)
			return vp.View()
		},
		Update: viewportScrollUpdate(&vp),
	}
}

// viewportScrollUpdate is the up/down/pgup/pgdown/home/end handler shared by
// rawExtraView and headersExtraView.
func viewportScrollUpdate(vp *viewport.Model) func(*grid.Model, tea.KeyPressMsg) (tea.Cmd, bool) {
	return func(_ *grid.Model, msg tea.KeyPressMsg) (tea.Cmd, bool) {
		switch msg.String() {
		case "up", "down", "pgup", "pgdown", "home", "end":
			var cmd tea.Cmd
			*vp, cmd = vp.Update(msg)
			return cmd, true
		}
		return nil, false
	}
}

const (
	minSecondaryCells   = 40
	minUsefulTableCells = 32
	maxUsefulTableCells = 72
	recordsetPaneGap    = 1
)

// chooseGridLayout is DataTug's WithSplitLayout policy: the table and the
// active secondary view share the pane, side by side, only when there's
// enough room for both to be useful; Raw/Headers (already scrollable, often
// wide JSON/text) always take the full pane instead. Ported from
// recordset_views.go's chooseRecordsetLayout.
func chooseGridLayout(totalWidth, naturalWidth int, view grid.View) grid.SplitLayout {
	if view == grid.ViewTable || view == gridViewRaw || view == gridViewHeaders {
		return grid.SplitLayout{}
	}
	useful := max(minUsefulTableCells, min(maxUsefulTableCells, naturalWidth))
	if totalWidth-useful-recordsetPaneGap >= minSecondaryCells {
		return grid.SplitLayout{Split: true, PrimaryWidth: useful, SecondaryWidth: totalWidth - useful - recordsetPaneGap}
	}
	return grid.SplitLayout{}
}

// sourceRowKey/restoreByKey are gridState's stable-selection helpers,
// wrapping grid.Row.Key/Model.IndexForKey (a display row's position in the
// RecordSet's own — never reordered — row order, which is what raw/RawRows
// and cross-grid sort sync key off).
func (g *gridState) sourceRowKey() string {
	if row, ok := g.CurrentRow(); ok {
		return row.Key
	}
	return ""
}

// selectedSourceRow is sourceRowKey as an int (the RecordSet's own row
// order), or -1. Kept for parity with the pre-adoption gridState method of
// the same name/shape.
func (g *gridState) selectedSourceRow() int { return g.sourceIndexAt(g.CurrentIndex()) }

func (g *gridState) restoreByKey(key string) {
	if key == "" {
		return
	}
	if index := g.IndexForKey(key); index >= 0 {
		g.SelectRow(index)
	}
}

// rawValue returns the RecordSet's raw (unformatted) value for the
// currently highlighted row/column, or nil if there is none.
func (g *gridState) rawValue(displayRowIndex, column int) any {
	row := g.rawRow(displayRowIndex)
	if row == nil || column < 0 || column >= len(row) {
		return nil
	}
	return row[column]
}

// rawRow returns the RecordSet's raw (unformatted) values for the row at a
// display index (post-sort position; filter is disabled, M3), resolved via
// grid.Row.Key back to raw's fixed RecordSet-row order.
func (g *gridState) rawRow(displayRowIndex int) []any {
	index := g.sourceIndexAt(displayRowIndex)
	if index < 0 || index >= len(g.raw) {
		return nil
	}
	return g.raw[index]
}

// sourceIndexAt resolves a display row (post-sort position; filter is
// disabled, M3) to its position in the RecordSet's own (never reordered)
// row order, or -1.
func (g *gridState) sourceIndexAt(displayRowIndex int) int {
	rows := g.Rows()
	if displayRowIndex < 0 || displayRowIndex >= len(rows) {
		return -1
	}
	index, err := strconv.Atoi(rows[displayRowIndex].Key)
	if err != nil {
		return -1
	}
	return index
}
