package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestChooseRecordsetLayoutBoundary(t *testing.T) {
	for _, view := range []recordsetView{recordsetCharts, recordsetCurrentRow} {
		for _, tc := range []struct {
			width int
			split bool
		}{
			{91, false}, // 52 useful table cells + 1 gap leaves only 38
			{92, false},
			{93, true},
			{120, true},
		} {
			layout := chooseRecordsetLayout(tc.width, 52, view)
			if layout.split != tc.split {
				t.Errorf("view %d width %d split = %v, want %v", view, tc.width, layout.split, tc.split)
			}
			if layout.split && layout.tableWidth+layout.secondaryWidth+recordsetPaneGap != tc.width {
				t.Errorf("width %d: pane widths do not fit: %+v", tc.width, layout)
			}
		}
	}
	if layout := chooseRecordsetLayout(93, 52, recordsetTable); layout.split || layout.tableWidth != 93 {
		t.Fatalf("table-only layout = %+v", layout)
	}
}

func TestCurrentRowContentWrapsAndDistinguishesNull(t *testing.T) {
	model := NewGridModel(secureread.Result{
		Columns: []string{"ID", "Comment", "Missing"},
		Rows: []secureread.Row{{Data: map[string]any{
			"ID": 5, "Comment": "a very long description with several words", "Missing": nil,
		}}},
	})
	content := currentRowContent(model, 0, 12)
	for _, want := range []string{"ID", "5", "Comment", "Missing", "NULL"} {
		if !strings.Contains(content, want) {
			t.Errorf("inspector missing %q: %q", want, content)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if ansi.StringWidth(line) > 12 {
			t.Errorf("inspector line exceeds width 12: %q", line)
		}
	}
	if got := currentRowContent(model, 1, 12); got != "No current row." {
		t.Errorf("no-row content = %q", got)
	}
}
