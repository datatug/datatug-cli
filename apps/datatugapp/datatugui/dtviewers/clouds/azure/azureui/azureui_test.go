package azureui

import (
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	sneatv "github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSafeTUI builds a headless TUI without the double-Fini bug:
// only app.Stop() is called in cleanup.
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

func TestViewerID(t *testing.T) {
	assert.Equal(t, dtviewers.ViewerID("azure"), viewerID)
}

func TestGoAzureHome_ReturnsNil(t *testing.T) {
	tui := newSafeTUI(t)
	err := GoAzureHome(&AzureContext{TUI: tui}, sneatnav.FocusToMenu)
	require.NoError(t, err)
}

// TestRegisterAsViewer_ActionClosure intercepts the registerViewer seam to
// capture the Viewer, then invokes its Action to cover the closure body.
func TestRegisterAsViewer_ActionClosure(t *testing.T) {
	orig := registerViewer
	t.Cleanup(func() { registerViewer = orig })

	var captured dtviewers.Viewer
	registerViewer = func(v dtviewers.Viewer) {
		captured = v
	}

	RegisterAsViewer()

	require.NotNil(t, captured.Action, "Action should be set")
	assert.Equal(t, viewerID, captured.ID)

	// Invoke the Action closure — covers the closure body in home_ui.go.
	tui := newSafeTUI(t)
	err := captured.Action(tui, sneatnav.FocusToMenu)
	assert.NoError(t, err)
}
