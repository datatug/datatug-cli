package dtproject

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatcolors"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/strongo/logus"
)

var _ tview.Primitive = (*projectsPanel)(nil)
var _ sneatnav.Cell = (*projectsPanel)(nil)

func GoDataTugProjectsScreen(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	_ = projectsBreadcrumbs(tui)
	content, err := newDataTugProjectsPanel(tui)
	if err != nil {
		return err
	}
	menu := datatugui.NewDataTugMainMenu(tui, datatugui.RootScreenProjects)
	tui.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
	dtstate.SaveCurrentScreePath("projects")
	dtlog.ScreenOpened("projects", "Projects")
	return nil
}

type projectsPanel struct {
	sneatnav.PanelBase
	tui             *sneatnav.TUI
	projects        []*dtconfig.ProjectRef
	selectProjectID string
	layout          *tview.Flex
	tree            *tview.TreeView
	details         *tview.Flex
}

func (*projectsPanel) Close() {
}

func projectsBreadcrumbs(tui *sneatnav.TUI) sneatnav.Breadcrumbs {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	breadcrumbs.Push(sneatv.NewBreadcrumb("Projects", func() error {
		return GoDataTugProjectsScreen(tui, sneatnav.FocusToContent)
	}))
	return breadcrumbs
}

func newDataTugProjectsPanel(tui *sneatnav.TUI) (*projectsPanel, error) {
	ctx := context.Background()

	// Create 3 separate trees
	tree := tview.NewTreeView().SetTopLevel(1)
	tree.SetBorder(true).SetTitle("Projects")
	tree.SetBorderPadding(1, 1, 2, 2)

	layout := tview.NewFlex().SetDirection(tview.FlexColumn)

	// Create a layout to hold both trees horizontally

	panel := &projectsPanel{
		PanelBase: sneatnav.NewPanelBase(tui, sneatv.WithBoxWithoutBorder(layout, layout.Box)),
		tui:       tui,
		layout:    layout,
		tree:      tree,
		details:   tview.NewFlex(),
	}

	layout.SetFocusFunc(func() {
		panel.ensureTreeHasCurrentNode(tree)
		tui.App.SetFocus(tree)
	})

	projRefText := tview.NewTextView()
	panel.details.AddItem(projRefText, 0, 1, false)

	layout.AddItem(panel.tree, 0, 1, true)
	layout.AddItem(panel.details, 0, 1, false)

	//box := layout.Box
	//box.SetBorder(false)
	//box.SetBorderPadding(0, 0, 5, 0)
	//box.SetTitle("Projects")

	//sneatv.SetPanelTitle(panel.GetBox(), "Projects")
	//sneatv.DefaultBorderWithoutPadding(panel.GetBox())

	settings, err := dtconfig.GetSettings()
	if err != nil {
		logus.Errorf(ctx, "Failed to get app settings: %v", err)
		//return nil, err
	}

	openProjectByRef := func(projectConfig dtconfig.ProjectRef) {
		if projectConfig.ID == datatugDemoProjectFullID {
			openDatatugDemoProject(tui)
		} else {
			projectPath := filestore.ExpandHome(projectConfig.Path)
			store := filestore.NewProjectStore(projectConfig.ID, projectPath)
			_, err = store.LoadProjectFile(ctx)
			if errors.Is(err, datatug.ErrProjectDoesNotExist) {
				tui.ShowAlert("Not able to open DataTug project", err.Error(), 0, tree)
				return
			}
			projectCtx := NewProjectContext(tui, store, projectConfig)
			GoDataTugProjectScreen(projectCtx)
		}
	}

	panel.projects = settings.Projects

	sort.Slice(panel.projects, func(i, j int) bool {
		return panel.projects[i].ID < panel.projects[j].ID
	})

	// === DATATUG CLOUD PROJECTS TREE ===
	rootNode := tview.NewTreeNode("root_node").
		SetColor(tcell.ColorLightBlue).
		SetSelectable(false)
	tree.SetRoot(rootNode)

	githubNode := tview.NewTreeNode("🐙 GitHub.com").SetColor(tcell.ColorLightYellow)
	githubNode.SetSelectable(false)

	//datatugCloud := tview.NewTreeNode("DataTug Cloud")
	//datatugCloud.SetColor(tcell.ColorLightBlue).SetSelectable(false)
	//rootNode.AddChild(datatugCloud)

	// === LOCAL PROJECTS TREE ===
	localProjectsNode := tview.NewTreeNode("🖥️ Local projects").
		SetColor(tcell.ColorLightYellow).
		SetSelectable(false)
	//tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
	//	switch event.Key() {
	//	case tcell.KeyEnter:
	//		tree.GetSelectedFunc()(tree.GetCurrentNode())
	//		return nil
	//	}
	//	return event
	//})

	const folderEmoji = "📁 "
	const repoEmoji = "📦 "

	// Add existing projects under Local projects
	for _, p := range panel.projects {
		if strings.HasPrefix(p.ID, "github.com/") {
			ids := strings.Split(strings.TrimPrefix(p.ID, "github.com/"), "/")
			if len(ids) < 2 {
				continue
			}
			owner, repo := ids[0], ids[1]
			var ownerNode *tview.TreeNode
			for _, node := range githubNode.GetChildren() {
				if node.GetText() == folderEmoji+owner {
					ownerNode = node
					break
				}
			}
			if ownerNode == nil {
				ownerNode = tview.NewTreeNode(folderEmoji + owner).SetColor(tcell.ColorLightBlue).SetSelectable(false)
				githubNode.AddChild(ownerNode)
			}
			repoNode := tview.NewTreeNode(repoEmoji + repo + " ")
			ownerNode.AddChild(repoNode)
			repoNode.SetReference(p)
			continue
		}
		//title := " 📁 " + GetProjectTitle(p) + " "
		title := GetProjectTitle(p)

		projectNode := tview.NewTreeNode(repoEmoji + title + " ").SetReference(p)
		localProjectsNode.AddChild(projectNode)
	}

	addToGithubRepoNode := tview.NewTreeNode(" Add DataTug project to existing GitHub Repo ").
		SetReference("local-create").
		SetColor(sneatcolors.TreeNodeLink).
		SetSelectedFunc(func() {
			ShowAddToGitHubRepo(tui)
		})
	githubNode.AddChild(addToGithubRepoNode)

	selectGithubRepoNode := tview.NewTreeNode(" Add GitHub repo with DataTug project ").
		SetReference("local-create").
		SetColor(sneatcolors.TreeNodeLink).
		SetSelectedFunc(func() {
			ShowAddToGitHubRepo(tui)
		})
	githubNode.AddChild(selectGithubRepoNode)

	// Add a demo project first
	localDemoProjectConfig := newLocalDemoProjectConfig()

	localProjectsNode.AddChild(tview.NewTreeNode(
		repoEmoji + fmt.Sprintf("%s [gray]@ %s[i]", localDemoProjectConfig.Title, datatugDemoProjectFullID),
	).SetReference(localDemoProjectConfig))

	// Add actions to Local projects
	localAddNode := tview.NewTreeNode(" Add exising ").
		SetReference("local-add").
		SetColor(sneatcolors.TreeNodeLink)
	localProjectsNode.AddChild(localAddNode)

	createNewLocalProjectNode := tview.NewTreeNode(" Create new local project ").
		SetReference("local-create").
		SetColor(sneatcolors.TreeNodeLink).
		SetSelectedFunc(func() {
			goCreateProjectScreen(tui)
			//panic("suxx")
		})
	localProjectsNode.AddChild(createNewLocalProjectNode)

	localProjectsNode.SetExpanded(true)
	tree.SetCurrentNode(localProjectsNode.GetChildren()[0])

	//// DataTug demo project
	//datatugDemoProject := &dtconfig.ProjectRef{
	//	ID:  datatugDemoProjectRepoID,
	//	Origin: "cloud",
	//}
	//cloudDemoProjectNode := tview.NewTreeNode(" DataTug demo project ").
	//	SetReference(datatugDemoProject) //.
	////SetColor(tcell.ColorWhite)
	//datatugCloud.AddChild(cloudDemoProjectNode)
	//
	//// Login to view action (moved to end)
	//loginNode := tview.NewTreeNode(" Login to view personal or work projects ").
	//	SetReference("login").
	//	SetColor(sneatcolors.TreeNodeLink)
	//datatugCloud.AddChild(loginNode)
	//
	//datatugCloud.SetExpanded(true)

	rootNode.SetExpanded(true)

	// Create a selection handler function
	selectionHandler := func(node *tview.TreeNode) {
		reference := node.GetReference()
		if reference != nil {
			switch ref := reference.(type) {
			case *dtconfig.ProjectRef:
				panel.selectProjectID = ref.ID
				if ref.ID == datatugDemoProjectFullID {
					openDatatugDemoProject(tui)
					return
				}
				openProjectByRef(*ref)
			case string:
				switch ref {
				case "login":
					// Handle login action
					logus.Infof(ctx, "Login action triggered")
				case "local-add":
					// Handle local add action
					logus.Infof(ctx, "Local add action triggered")
				case "local-create":
					// Handle local create action
					logus.Infof(ctx, "Local create action triggered")
				case "add":
					// Handle GitHub add action
					logus.Infof(ctx, "GitHub add action triggered")
				case "create":
					// Handle GitHub create action
					logus.Infof(ctx, "GitHub create action triggered")
				}
			}
		}
	}

	tree.SetSelectedFunc(selectionHandler)

	// Set up focus and blur handlers for each tree to manage selected item styling
	{
		tree.SetFocusFunc(func() {
			tree.SetGraphicsColor(tcell.ColorWhite) // tree lines
			// Apply active styling to current node
			panel.applyNodeStyling(tree, true)
		})

		tree.SetBlurFunc(func() {
			tree.SetGraphicsColor(tcell.ColorGrey) // tree lines
			// When tree loses focus, apply dimmed styling to current node
			panel.applyNodeStyling(tree, false)
		})
	}

	// Main input capture function for the layout
	layout.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if !tree.HasFocus() { // Workaround for a bug
			panel.tui.SetFocus(tree)
		}

		switch event.Key() {
		case tcell.KeyESC:
			tui.SetFocus(tui.Menu)
			return nil
		case tcell.KeyLeft:
			panel.tui.SetFocus(tui.Menu)
			return nil
		case tcell.KeyRight:
			panel.tui.SetFocus(panel.details)
			return event
		case tcell.KeyUp:
			// Check if we're on the first non-root item
			currentNode := tree.GetCurrentNode()
			if currentNode != nil && currentNode == tree.GetRoot().GetChildren()[0] {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, tree)
				return nil
			}
			// Normal UP navigation within a tree
			return event
		case tcell.KeyDown:
			return event // Normal DOWN navigation within a tree
		//case tcell.KeyEnter:
		//	//Handle ENTER key press on project nodes
		//	currentNode := tree.GetCurrentNode()
		//	if currentNode != nil {
		//		reference := currentNode.GetReference()
		//		if reference != nil {
		//			switch ref := reference.(type) {
		//			case *dtconfig.ProjectRef:
		//				// Call goProjectDashboards when ENTER is pressed on a project node
		//				GoDataTugProjectScreen(tui, ref)
		//				return nil
		//			}
		//		}
		//	}
		//	return event
		default:
			return event
		}
	})

	addRecent(rootNode)
	rootNode.AddChild(tview.NewTreeNode("").SetSelectable(false))
	rootNode.AddChild(localProjectsNode)
	rootNode.AddChild(tview.NewTreeNode("").SetSelectable(false))
	rootNode.AddChild(githubNode)

	return panel, nil
}

func addRecent(rootNode *tview.TreeNode) {
	recentNode := tview.NewTreeNode("🕘 Recent projects")
	recentNode.SetSelectable(false)
	recentNode.AddChild(tview.NewTreeNode(" No recent projects").SetSelectable(false).SetColor(tcell.ColorGray))
	rootNode.AddChild(recentNode)
}

func (p *projectsPanel) Draw(screen tcell.Screen) {
	p.layout.Draw(screen)
}

func (p *projectsPanel) ensureTreeHasCurrentNode(tree *tview.TreeView) {
	if tree.GetCurrentNode() == nil {
		root := tree.GetRoot()
		if root != nil && len(root.GetChildren()) > 0 {
			tree.SetCurrentNode(root.GetChildren()[0])
		}
	}
}

const dimGray = tcell.ColorDarkSlateGray // 255 * 50 / 100

func (p *projectsPanel) applyNodeStyling(tree *tview.TreeView, isActive bool) {
	currentNode := tree.GetCurrentNode()
	if currentNode == nil {
		return
	}

	reference := currentNode.GetReference()
	if reference == nil {
		return
	}

	// Check node reference for *dtconfig.ProjectRef to determine node type
	switch reference.(type) {
	case *dtconfig.ProjectRef:
		// Project link node - has *dtconfig.ProjectRef reference
		if isActive {
			currentNode.SetColor(tcell.ColorWhite)
			currentNode.SetSelectedTextStyle(currentNode.GetSelectedTextStyle().Foreground(tcell.ColorBlack))
		} else {
			// Inactive project link nodes have different color than action nodes
			currentNode.SetColor(dimGray)
			currentNode.SetSelectedTextStyle(currentNode.GetSelectedTextStyle().Foreground(tcell.ColorWhite))

		}
	default:
		// Action node - all other nodes (string references, etc.)
		if isActive {
			currentNode.SetColor(sneatcolors.TreeNodeLink)
		} else {
			// Inactive action nodes have different color than project link nodes
			currentNode.SetColor(dimGray)
			currentNode.SetSelectedTextStyle(currentNode.GetSelectedTextStyle().Foreground(tcell.ColorWhite))
		}
	}
}

func (p *projectsPanel) TakeFocus() {
	p.tui.SetFocus(p.tree)
}
