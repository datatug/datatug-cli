package dtproject

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type ProjectScreenID string

const (
	ProjectScreenDashboards   = "dashboards"
	ProjectScreenEnvironments = "environments"
)

type projectMenuPanel struct {
	sneatnav.Panel
	app     *tview.Application
	project *datatug.Project
	//
	environments *tview.TreeNode
}

func (p *projectMenuPanel) SetProject(project *datatug.Project) {
	p.project = project
	if project == nil {
		return
	}
	project.Environments = nil // force reload
	environments, err := project.GetEnvironments(context.Background())

	p.app.QueueUpdateDraw(func() {
		if err != nil {
			p.environments.ClearChildren()
			p.environments.SetText("Environments (error)")
		}
		p.environments.SetText(fmt.Sprintf("Environments (%d)", len(environments)))
		p.environments.ClearChildren()
		for _, environment := range environments {
			p.environments.AddChild(tview.NewTreeNode(environment.ID))
		}
	})
}

func getOrCreateProjectMenuPanel(ctx ProjectContext, currentScreen ProjectScreenID) *projectMenuPanel {
	if existing := ctx.TUI().Menu; existing != nil {
		if m, ok := existing.(*projectMenuPanel); ok {
			return m
		}
	}
	return newProjectMenuPanel(ctx, currentScreen)
}

func newProjectMenuPanel(ctx ProjectContext, currentScreen ProjectScreenID) *projectMenuPanel {
	tree := tview.NewTreeView()

	//datatugNode := tview.NewTreeNode("DataTug").SetSelectable(true)
	//tree.SetRoot(datatugNode)
	//
	//projectsNode := tview.NewTreeNode("Projects").SetSelectable(true)
	//datatugNode.AddChild(projectsNode)

	prjConfig := ctx.Config()
	//projectTitle := " 📁 " + GetProjectShortTitle(project) // TODO: emoji breaks borders
	projectTitle := GetProjectShortTitle(prjConfig)
	projectNode := tview.NewTreeNode(projectTitle).SetSelectable(true)
	tree.SetRoot(projectNode)
	projectNode.SetSelectedFunc(func() {
		GoProjectScreen(ctx)
	})

	tree.SetCurrentNode(projectNode)

	tui := ctx.TUI()

	menu := projectMenuPanel{
		app:   tui.App,
		Panel: sneatnav.NewPanel(tui, sneatnav.WithBox(tree, tree.Box)),
	}

	tree.SetChangedFunc(func(node *tview.TreeNode) {
		switch node {
		case menu.environments:
			goEnvironmentsScreen(ctx, sneatnav.FocusToMenu)
		}
	})

	tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		key := event.Key()
		switch key {
		case tcell.KeyUp:
			if tree.GetCurrentNode() == tree.GetRoot() {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, tree)
				return nil
			}
			return event
		case tcell.KeyRight, tcell.KeyLeft:
			node := tree.GetCurrentNode()
			if strings.HasPrefix(node.GetText(), "Environments") {
				node.SetExpanded(key == tcell.KeyRight)
				return nil
			}
			return event
		default:
			return event
		}
	})

	dashboardsNode := tview.NewTreeNode("Dashboards").SetSelectable(true)
	projectNode.AddChild(dashboardsNode)
	dashboardsNode.SetSelectedFunc(func() {
		goProjectDashboards(ctx)
	})
	if currentScreen == ProjectScreenDashboards {
		tree.SetCurrentNode(dashboardsNode)
	}

	dbsNode := tview.NewTreeNode("Databases").SetSelectable(true)
	projectNode.AddChild(dbsNode)
	dbsNode.SetSelectedFunc(func() {
		goDatabasesScreen(ctx, sneatnav.FocusToContent)
	})

	menu.environments = tview.NewTreeNode("Environments").SetSelectable(true)
	projectNode.AddChild(menu.environments)
	menu.environments.SetSelectedFunc(func() {
		goEnvironmentsScreen(ctx, sneatnav.FocusToContent)
	})

	menu.environments.SetExpanded(false)

	entitiesNode := tview.NewTreeNode("Entities").SetSelectable(true)
	projectNode.AddChild(entitiesNode)

	logsNode := tview.NewTreeNode("Logs").SetSelectable(true)
	projectNode.AddChild(logsNode)

	if currentScreen == ProjectScreenEnvironments {
		tree.SetCurrentNode(menu.environments)
	}

	/*
		list := tview.NewList().
			//AddItem("Databases", "", 'D', nil).
			AddItem("Dashboards", "", 'B', func() {
				goProjectDashboards(tui, project)
			}).
			AddItem("Environments", "", 'E', func() {
				goEnvironmentsScreen(tui, project)
			})

		AddItem("Queries", "", 'Q', nil).
		AddItem("Web UI", "", 'W', nil)

		currentItem := -1
		switch currentScreen {
		case ProjectScreenDashboards:
			currentItem = 0
		case ProjectScreenEnvironments:
			currentItem = 1
		}
		if currentItem >= 0 {
			list.SetCurrentItem(currentItem)
		}

		sneatv.DefaultListStyle(list)
	*/

	sneatv.DefaultBorderWithPadding(tree.Box)

	return &menu
}
