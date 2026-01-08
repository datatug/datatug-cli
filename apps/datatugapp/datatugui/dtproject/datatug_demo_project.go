package dtproject

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/fsutils"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/go-git/go-git/v5"
	"github.com/rivo/tview"
)

const demoProjectsRepoID = "datatug-demo-projects"
const datatugOrg = "datatug"
const demoProjectOrigin = "github.com/" + datatugOrg + "/" + demoProjectsRepoID

const demoProject1DirName = "demo-project-1"
const demoProject1LocalID = "github.com~" + datatugOrg + "~" + demoProjectsRepoID + "~" + demoProject1DirName
const demoProject1Title = "Demo Project 1"

const demoProject1FullID = demoProjectOrigin + "/" + demoProject1LocalID

const datatugDemoProjectsGitURL = "https://" + demoProjectOrigin

const datatugDemoProjectsDir = "~/datatug/" + demoProjectOrigin
const demoProjectDir = datatugDemoProjectsDir + "/" + demoProject1DirName

// const ghContentUrlPrefix = "/tree/main/"
// const demoProject1WebURL = "https://" + demoProjectOrigin + ghContentUrlPrefix + demoProject1LocalID

func newDemoProject1Ref() *dtconfig.ProjectRef {
	return &dtconfig.ProjectRef{
		ID:     demoProject1LocalID,
		Path:   demoProjectDir,
		Origin: demoProjectOrigin,
		Title:  demoProject1Title,
	}
}

func openDatatugDemoProject(tui *sneatnav.TUI, projectRef dtconfig.ProjectRef) {
	// Expand home in path like ~/...
	projectDir := fsutils.ExpandHome(projectRef.Path)

	projectDirExists, err := fsutils.DirExists(projectDir)
	if err != nil {
		panic(err)
	}
	openDemoProject := func() {
		projRef := dtconfig.ProjectRef{
			ID:     demoProject1LocalID,
			Origin: demoProjectOrigin,
			Path:   projectDir,
		}
		loader := filestore.NewProjectStore(projRef.ID, projRef.Path)
		projectCtx := NewProjectContext(tui, loader, projRef)
		GoDatatugProjectScreen(projectCtx)
	}

	if projectDirExists {
		openDemoProject()
		return
	}

	progressText := tview.NewTextView()
	progressText.SetTitle("Cloning project...")
	progressPanel := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(progressText, progressText.Box))
	tui.SetPanels(tui.Menu, progressPanel, sneatnav.WithFocusTo(sneatnav.FocusToContent))

	go func() {
		// Ensure parent directory exists
		parent := filepath.Dir(datatugDemoProjectsDir)
		parent = fsutils.ExpandHome(parent)
		if err = os.MkdirAll(parent, 0o755); err != nil {
			panic(err)
		}
		// Clone public GitHub repository datatugDemoProjectGitHubRepoID into demoProjectDir using go-git
		cloneOptions := git.CloneOptions{
			URL:      datatugDemoProjectsGitURL,
			Progress: NewTviewProgressWriter(tui, progressText),
			// Depth: 1, // uncomment for shallow clone if desired
		}
		targetDir := fsutils.ExpandHome(datatugDemoProjectsDir)
		if _, err = git.PlainClone(targetDir, false, &cloneOptions); err != nil {
			tui.App.Stop()
			panic(fmt.Sprintf("failed to git clone %s into %s: %v", cloneOptions.URL, projectDir, err))
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
