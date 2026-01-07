package dtproject

import (
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

func NewProjectPanel(tui *sneatnav.TUI, projectConfig *dtconfig.ProjectRef) sneatnav.Panel {
	content := tview.NewTextView().SetTextAlign(tview.AlignCenter)
	projectTitle := GetProjectTitle(projectConfig)
	sneatv.SetPanelTitle(content.Box, fmt.Sprintf("Project: %s", projectTitle))
	return sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(content, content.Box))
}

func GoDatatugProjectScreen(projectCtx *ProjectContext) {
	tui := projectCtx.TUI()
	pConfig := projectCtx.Config()
	breadcrumbs := projectsBreadcrumbs(tui)
	title := GetProjectTitle(pConfig)
	projectBreadcrumb := sneatv.NewBreadcrumb(title, nil)
	breadcrumbs.Push(projectBreadcrumb)
	menu := getOrCreateProjectMenuPanel(projectCtx, "project")
	content := NewProjectPanel(tui, pConfig)
	tui.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToMenu))

	dtstate.BumpRecentProject(pConfig.ID)

	go func() {
		err := <-projectCtx.WatchProject()
		if err != nil {
			tui.App.Stop()
			panic(fmt.Errorf("watch project error: %w", err))
		}
		project := projectCtx.Project()
		projectBreadcrumb.SetTitle(project.Title)
		sneatv.SetPanelTitle(content.GetBox(), fmt.Sprintf("Project: %s", project.Title))
		menu.SetProject(project)
	}()

}

type ProjectTitle struct {
	Host   string // github.com
	Owner  string
	RepoID string
}

func GetProjectTitle(p *dtconfig.ProjectRef) (projectTitle string) {
	projectTitle = p.Title
	if projectTitle == "" {
		projectTitle = p.ID
	}
	if projectTitle == "" {
		projectTitle = p.Origin
	}
	return projectTitle
}

func GetProjectShortTitle(p *dtconfig.ProjectRef) (projectTitle string) {
	projectTitle = GetProjectTitle(p)
	if parts := strings.Split(projectTitle, "/"); len(parts) > 1 {
		projectTitle = parts[len(parts)-1]
	}
	return projectTitle
}
