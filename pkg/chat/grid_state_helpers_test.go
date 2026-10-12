package chat

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func gridStateTestFixture(t *testing.T) *gridState {
	t.Helper()
	return newGridState(NewGridModel(secureread.Result{
		Columns: []string{"First", "Second"},
		Rows: []secureread.Row{
			{Data: map[string]any{"First": "a", "Second": "1"}},
			{Data: map[string]any{"First": "b", "Second": "2"}},
		},
	}), "", "Rows", contentWidth(80))
}

// TestSetVersionBadgeClearsToBaseTitle covers setVersionBadge's own
// badge=="" branch (SetTitle(baseTitle)), distinct from the badge-set path
// other tests already exercise.
func TestSetVersionBadgeClearsToBaseTitle(t *testing.T) {
	g := gridStateTestFixture(t)
	g.setVersionBadge("changed")
	if !strings.Contains(g.Title(), "changed") {
		t.Fatalf("setup: Title() = %q, want the badge", g.Title())
	}
	g.setVersionBadge("")
	if g.Title() != g.baseTitle {
		t.Fatalf("Title() = %q, want the bare baseTitle %q after clearing the badge", g.Title(), g.baseTitle)
	}
}

// TestChartsExtraViewRendersCandidateAndCyclesUpDown covers
// chartsExtraView's Render with candidates present and both the up and down
// Update branches -- TestRecordsetViewsRouteFocusAcrossSplitAndNarrowLayouts
// only exercises "down" (via CurrentView switching, never actually
// rendering the Charts view or pressing "up").
func TestChartsExtraViewRendersCandidateAndCyclesUpDown(t *testing.T) {
	g := gridStateTestFixture(t)
	g.charts = []ChartCandidate{
		{Spec: ChartSpec{Kind: ChartBar, Title: "First chart", Points: []ChartPoint{{Label: "a", Value: 1}}}, Reason: "grouped by First"},
		{Spec: ChartSpec{Kind: ChartBar, Title: "Second chart", Points: []ChartPoint{{Label: "b", Value: 2}}}, Reason: "grouped by Second"},
	}
	view := g.chartsExtraView()
	rendered := view.Render(g.Model, 60, 12)
	if !strings.Contains(rendered, "First chart") || !strings.Contains(rendered, "1/2") {
		t.Fatalf("Render() = %q, want the first candidate's chart", rendered)
	}
	if _, handled := view.Update(g.Model, tea.KeyPressMsg{Code: tea.KeyDown}); !handled || g.chartIndex != 1 {
		t.Fatalf("down did not advance chartIndex: handled=%v index=%d", handled, g.chartIndex)
	}
	rendered = view.Render(g.Model, 60, 12)
	if !strings.Contains(rendered, "Second chart") || !strings.Contains(rendered, "2/2") {
		t.Fatalf("Render() after down = %q, want the second candidate's chart", rendered)
	}
	if _, handled := view.Update(g.Model, tea.KeyPressMsg{Code: tea.KeyDown}); !handled || g.chartIndex != 1 {
		t.Fatalf("down past the last candidate should be a no-op: index=%d", g.chartIndex)
	}
	if _, handled := view.Update(g.Model, tea.KeyPressMsg{Code: tea.KeyUp}); !handled || g.chartIndex != 0 {
		t.Fatalf("up did not retreat chartIndex: handled=%v index=%d", handled, g.chartIndex)
	}
	if _, handled := view.Update(g.Model, tea.KeyPressMsg{Code: tea.KeyUp}); !handled || g.chartIndex != 0 {
		t.Fatalf("up past the first candidate should be a no-op: index=%d", g.chartIndex)
	}
	if _, handled := view.Update(g.Model, tea.KeyPressMsg{Code: tea.KeyLeft}); handled {
		t.Fatal("an unhandled key should return handled=false")
	}
}

// TestChartsExtraViewRenderWithNoCandidates covers the "no chart
// candidates" empty-state Render branch.
func TestChartsExtraViewRenderWithNoCandidates(t *testing.T) {
	g := gridStateTestFixture(t)
	rendered := g.chartsExtraView().Render(g.Model, 60, 12)
	if !strings.Contains(rendered, "No chart candidates") {
		t.Fatalf("Render() = %q, want the empty-candidates message", rendered)
	}
}

// TestViewportScrollUpdateHandlesScrollKeys covers viewportScrollUpdate's
// matched-key branch directly (rawExtraView/headersExtraView both delegate
// to it, but neither test renders and scrolls one far enough to prove the
// Update wiring itself).
func TestViewportScrollUpdateHandlesScrollKeys(t *testing.T) {
	vp := viewport.New(viewport.WithWidth(10), viewport.WithHeight(2))
	vp.SetContent("one\ntwo\nthree\nfour\nfive")
	handler := viewportScrollUpdate(&vp)
	if _, handled := handler(nil, tea.KeyPressMsg{Code: tea.KeyDown}); !handled {
		t.Fatal("expected down to be handled by the viewport")
	}
	if _, handled := handler(nil, tea.KeyPressMsg{Code: 'x', Text: "x"}); handled {
		t.Fatal("expected an unrelated key to be unhandled")
	}
}

// TestGridStateSourceRowHelpers covers sourceRowKey/restoreByKey/rawValue/
// rawRow/sourceIndexAt's guard branches: no current row, an empty key, an
// out-of-range column/row, and a non-numeric row Key.
func TestGridStateSourceRowHelpers(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"A"}}), "", "Empty", contentWidth(80))
	if got := g.sourceRowKey(); got != "" {
		t.Fatalf("sourceRowKey() on an empty grid = %q, want empty", got)
	}
	g.restoreByKey("") // must be a no-op, not a panic
	if got := g.rawValue(0, 0); got != nil {
		t.Fatalf("rawValue() on an empty grid = %v, want nil", got)
	}
	if got := g.rawRow(0); got != nil {
		t.Fatalf("rawRow() on an empty grid = %v, want nil", got)
	}
	if got := g.sourceIndexAt(0); got != -1 {
		t.Fatalf("sourceIndexAt() on an empty grid = %d, want -1", got)
	}

	populated := gridStateTestFixture(t)
	if got := populated.rawValue(0, 5); got != nil {
		t.Fatalf("rawValue() with an out-of-range column = %v, want nil", got)
	}
	if got := populated.sourceIndexAt(99); got != -1 {
		t.Fatalf("sourceIndexAt() with an out-of-range display row = %d, want -1", got)
	}
	// A non-numeric grid.Row.Key (never produced by newGridState/
	// newProjectedGridState itself, but sourceIndexAt must not panic on one)
	// exercises strconv.Atoi's own error branch.
	rows := populated.Rows()
	rows[0].Key = "not-a-number"
	if got := populated.sourceIndexAt(0); got != -1 {
		t.Fatalf("sourceIndexAt() with a non-numeric Key = %d, want -1", got)
	}
}
