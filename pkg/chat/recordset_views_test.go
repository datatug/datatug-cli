package chat

import (
	"testing"

	"github.com/strongo/aichat/tui/grid"
)

// TestChooseGridLayoutBoundary is TestChooseRecordsetLayoutBoundary, ported
// to chooseGridLayout/grid.SplitLayout: the split-pane policy DataTug
// registers with the shared grid via grid.WithSplitLayout (chooseRecordsetLayout
// moved into tui/grid.Model itself; this is DataTug's own policy function).
// (currentRowContent's own coverage moved with the code to
// strongo/aichat/tui/grid's test suite — TestRowValuesAbsentVsNull,
// TestCurrentRowContentNoRows — since DataTug no longer has that function;
// it renders via grid.CardView.)
func TestChooseGridLayoutBoundary(t *testing.T) {
	for _, view := range []grid.View{gridViewCharts, gridViewCurrentRow} {
		for _, tc := range []struct {
			width int
			split bool
		}{
			{91, false}, // 52 useful table cells + 1 gap leaves only 38
			{92, false},
			{93, true},
			{120, true},
		} {
			layout := chooseGridLayout(tc.width, 52, view)
			if layout.Split != tc.split {
				t.Errorf("view %d width %d split = %v, want %v", view, tc.width, layout.Split, tc.split)
			}
			if layout.Split && layout.PrimaryWidth+layout.SecondaryWidth+recordsetPaneGap != tc.width {
				t.Errorf("width %d: pane widths do not fit: %+v", tc.width, layout)
			}
		}
	}
	if layout := chooseGridLayout(93, 52, grid.ViewTable); layout.Split {
		t.Fatalf("table-only layout = %+v, want no split", layout)
	}
	if layout := chooseGridLayout(200, 52, gridViewRaw); layout.Split {
		t.Fatalf("Raw view layout = %+v, want no split (always full width)", layout)
	}
	if layout := chooseGridLayout(200, 52, gridViewHeaders); layout.Split {
		t.Fatalf("Headers view layout = %+v, want no split (always full width)", layout)
	}
}
