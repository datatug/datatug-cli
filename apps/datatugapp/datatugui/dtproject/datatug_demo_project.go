package dtproject

import (
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/go-git/go-git/v5"
	"github.com/rivo/tview"
)

const datatugDemoProjectRepoID = "datatug-demo-project"
const datatugDemoProjectOrg = "datatug"
const datatugDemoProjectTitle = "Demo Project"
const datatugDemoProjectFullID = "github.com/" + datatugDemoProjectOrg + "/" + datatugDemoProjectRepoID
const datatugDemoProjectGitHubURL = "https://" + datatugDemoProjectFullID
const datatugDemoProjectDir = "~/datatug/" + datatugDemoProjectFullID

func newLocalDemoProjectConfig() *dtconfig.ProjectRef {
	return &dtconfig.ProjectRef{
		ID:    datatugDemoProjectFullID,
		Path:  datatugDemoProjectDir,
		Url:   datatugDemoProjectGitHubURL,
		Title: datatugDemoProjectTitle,
	}
}

func openDatatugDemoProject(tui *sneatnav.TUI) {
	// Expand home in path like ~/...
	projectDir := filestore.ExpandHome(datatugDemoProjectDir)

	projectDirExists, err := filestore.DirExists(projectDir)
	if err != nil {
		panic(err)
	}
	openDemoProject := func() {
		projRef := dtconfig.ProjectRef{
			ID:   datatugDemoProjectFullID,
			Url:  datatugDemoProjectGitHubURL,
			Path: projectDir,
		}
		loader := filestore.NewProjectStore(projRef.ID, projRef.Path)
		projectCtx := NewProjectContext(tui, loader, projRef)
		GoProjectScreen(projectCtx)
	}

	if projectDirExists {
		openDemoProject()
		return
	}

	progressText := tview.NewTextView()
	progressText.SetTitle("Cloning project...")
	progressPanel := sneatnav.NewPanel(tui, sneatnav.WithBox(progressText, progressText.Box))
	tui.SetPanels(tui.Menu, progressPanel, sneatnav.WithFocusTo(sneatnav.FocusToContent))

	go func() {
		// Ensure parent directory exists
		parent := filepath.Dir(projectDir)
		if err = os.MkdirAll(parent, 0o755); err != nil {
			panic(err)
		}
		// Clone public GitHub repository datatugDemoProjectGitHubRepoID into datatugDemoProjectDir using go-git
		if _, err = git.PlainClone(projectDir, false, &git.CloneOptions{
			URL:      datatugDemoProjectGitHubURL,
			Progress: NewTviewProgressWriter(tui, progressText),
			// Depth: 1, // uncomment for shallow clone if desired
		}); err != nil {
			panic("git clone failed: " + err.Error())
		}
		tui.App.QueueUpdateDraw(func() {
			openDemoProject()
		})
	}()
}

// TviewProgressWriter implements io.Writer and appends text to a TextView safely via tview.Application.
type TviewProgressWriter struct {
	tui *sneatnav.TUI
	tv  *tview.TextView
}

func NewTviewProgressWriter(tui *sneatnav.TUI, tv *tview.TextView) *TviewProgressWriter {
	return &TviewProgressWriter{tui: tui, tv: tv}
}

func (w *TviewProgressWriter) Write(p []byte) (n int, err error) {
	// Ensure UI updates happen on the application goroutine
	w.tui.App.QueueUpdateDraw(func() {
		w.tv.SetText(string(p))
	})
	return len(p), nil
}
