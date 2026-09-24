package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/strongo/aichat/tui/grid"
)

func TestPadAnsiLineZeroOrNegativeWidthIsEmpty(t *testing.T) {
	if got := padAnsiLine("hello", 0); got != "" {
		t.Fatalf("width 0 = %q, want empty", got)
	}
	if got := padAnsiLine("hello", -1); got != "" {
		t.Fatalf("negative width = %q, want empty", got)
	}
}

func TestNextTableStyleZeroValueStartsAtSoft(t *testing.T) {
	next := nextTableStyle(grid.Style{})
	if next.Name != grid.StyleSoft.Name {
		t.Fatalf("zero-value style advanced to %q, want %q", next.Name, grid.StyleSoft.Name)
	}
}

func TestNextTableStyleUnknownNameFallsBackToFirst(t *testing.T) {
	next := nextTableStyle(grid.Style{Name: "not-a-real-style"})
	if next.Name != grid.Styles[0].Name {
		t.Fatalf("unknown style fell back to %q, want %q", next.Name, grid.Styles[0].Name)
	}
}

func TestConciseErrorNilIsEmpty(t *testing.T) {
	if got := conciseError(nil); got != "" {
		t.Fatalf("conciseError(nil) = %q, want empty", got)
	}
}

func TestConciseErrorTruncatesLongMessages(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := conciseError(errors.New(long))
	if len(got) != 240 || !strings.HasSuffix(got, "...") {
		t.Fatalf("conciseError length = %d, suffix ok = %v", len(got), strings.HasSuffix(got, "..."))
	}
}
