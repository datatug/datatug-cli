package clouds

import (
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	sneatv "github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSafeTUI builds a headless TUI without the double-Fini bug in
// sneatnav.NewTestTUI: only app.Stop() is called in cleanup (tview's
// app.Stop already finalises the screen).
func newSafeTUI(t *testing.T) *sneatnav.TUI {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("simulation screen Init: %v", err)
	}
	app := tview.NewApplication().SetScreen(screen)
	root := sneatv.NewBreadcrumb("test", func() error { return nil })
	tui := sneatnav.NewTUI(app, root)
	t.Cleanup(func() { app.Stop() })
	return tui
}

// callAndCaptureTextView calls GoCloudPlaceholderHome while intercepting the
// newTextViewFunc seam so we get back the exact *tview.TextView that received
// SetInputCapture — letting us drive every branch of the closure directly.
func callAndCaptureTextView(t *testing.T) (*tview.TextView, func(*tcell.EventKey) *tcell.EventKey) {
	t.Helper()

	tui := newSafeTUI(t)
	cCtx := &CloudContext{TUI: tui}

	var captured *tview.TextView
	orig := newTextViewFunc
	t.Cleanup(func() { newTextViewFunc = orig })
	newTextViewFunc = func() *tview.TextView {
		tv := orig()
		captured = tv
		return tv
	}

	err := GoCloudPlaceholderHome(cCtx, dtviewers.ViewerID("test"), "T", "M", sneatnav.FocusToMenu)
	require.NoError(t, err)
	require.NotNil(t, captured)

	return captured, captured.GetInputCapture()
}

func TestGoCloudPlaceholderHome_ReturnsNil(t *testing.T) {
	tui := newSafeTUI(t)
	cCtx := &CloudContext{TUI: tui}
	err := GoCloudPlaceholderHome(cCtx, dtviewers.ViewerID("test"), "Test Title", "Test message", sneatnav.FocusToMenu)
	assert.NoError(t, err)
}

// TestGoCloudPlaceholderHome_InputCapture_KeyUp_AtScrollZero covers the
// "KeyUp when scroll row == 0" branch → focuses header, returns nil.
func TestGoCloudPlaceholderHome_InputCapture_KeyUp_AtScrollZero(t *testing.T) {
	tv, capture := callAndCaptureTextView(t)
	require.NotNil(t, capture)
	// Default lineOffset is -1; ScrollTo(0,0) sets it to 0 so the branch fires.
	tv.ScrollTo(0, 0)
	row, _ := tv.GetScrollOffset()
	require.Equal(t, 0, row)
	result := capture(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	assert.Nil(t, result)
}

// TestGoCloudPlaceholderHome_InputCapture_KeyUp_NotAtZero covers the
// "KeyUp when scroll row != 0" branch → returns event unchanged.
func TestGoCloudPlaceholderHome_InputCapture_KeyUp_NotAtZero(t *testing.T) {
	tv, capture := callAndCaptureTextView(t)
	require.NotNil(t, capture)
	tv.ScrollTo(5, 0)
	result := capture(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	assert.NotNil(t, result)
}

// TestGoCloudPlaceholderHome_InputCapture_KeyLeft covers the KeyLeft branch
// → focuses menu, returns nil.
func TestGoCloudPlaceholderHome_InputCapture_KeyLeft(t *testing.T) {
	_, capture := callAndCaptureTextView(t)
	require.NotNil(t, capture)
	result := capture(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	assert.Nil(t, result)
}

// TestGoCloudPlaceholderHome_InputCapture_Default covers the default branch
// → returns the event unchanged.
func TestGoCloudPlaceholderHome_InputCapture_Default(t *testing.T) {
	_, capture := callAndCaptureTextView(t)
	require.NotNil(t, capture)
	ev := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	result := capture(ev)
	assert.Equal(t, ev, result)
}
