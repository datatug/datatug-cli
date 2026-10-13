package apimonitor

import (
	"strings"
	"testing"
)

func TestEmptyChartReportsObservedZeroPeak(t *testing.T) {
	if view := Render(Snapshot{}, 80, 24, 0); !strings.Contains(view, "Peak 0 requests") {
		t.Fatalf("empty chart invents an observed request: %s", view)
	}
}
