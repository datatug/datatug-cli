package dtproject

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/datatug/datatug-cli/pkg/sneatcolors"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/strongo/logus"
)

var _ tview.Primitive = (*projectsPanel)(nil)
var _ sneatnav.Cell = (*projectsPanel)(nil)

type projectsPanel struct {
	sneatnav.PanelBase
	tui              *sneatnav.TUI
	projects         []*dtconfig.ProjectRef
	selectProjectID  string
	localTree        *tview.TreeView
	cloudTree        *tview.TreeView
	layout           *tview.Flex
	currentTreeIndex int               // 0=local, 1=cloud, 2=github
	trees            []*tview.TreeView // slice for easy access
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

func GoDataTugProjectsScreen(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	_ = projectsBreadcrumbs(tui)
	content, err := newDataTugProjectsPanel(tui)
	if err != nil {
		return err
	}
	menu := datatugui.NewDataTugMainMenu(tui, datatugui.RootScreenProjects)
	tui.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
	dtlog.ScreenOpened("projects", "Projects")
	return nil
}

//type nodeType int
//
//const (
//	nodeTypeAction nodeType = iota
//	nodeTypeLink
//)
//
//type nodeRef struct {
//	nodeType nodeType
//}

func newDataTugProjectsPanel(tui *sneatnav.TUI) (*projectsPanel, error) {
	ctx := context.Background()

	// Create 3 separate trees
	localTree := tview.NewTreeView()
	cloudTree := tview.NewTreeView()

	// Create layout to hold all 3 trees horizontally
	layout := tview.NewFlex().SetDirection(tview.FlexColumn)

	panel := &projectsPanel{
		PanelBase: sneatnav.NewPanelBase(tui, sneatnav.WithBox(layout, layout.Box)),
		tui:       tui,
		localTree: localTree,
		cloudTree: cloudTree,
		layout:    layout,
		trees:     []*tview.TreeView{localTree, cloudTree},
	}

	for _, tree := range panel.trees {
		layout.AddItem(tree, 0, 1, false)
	}

	sneatv.SetPanelTitle(panel.GetBox(), "Projects")

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
				tui.ShowAlert("Not able to open DataTug project", err.Error(), 0, localTree)
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

	// === LOCAL PROJECTS TREE ===
	localRoot := tview.NewTreeNode("Local projects").
		SetColor(tcell.ColorLightBlue).
		SetSelectable(false)
	localTree.SetRoot(localRoot)
	localTree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			localTree.GetSelectedFunc()(localTree.GetCurrentNode())
			return nil
		}
		return event
	})

	// Add existing projects under Local projects
	for _, p := range panel.projects {
		//title := " 📁 " + GetProjectTitle(p) + " "
		title := GetProjectTitle(p)
		projectNode := tview.NewTreeNode(" " + title + " ").
			SetReference(p).
			SetColor(tcell.ColorWhite)
		localRoot.AddChild(projectNode)
	}

	// Add a demo project first
	localDemoProjectConfig := newLocalDemoProjectConfig()

	localRoot.AddChild(tview.NewTreeNode(
		fmt.Sprintf(" %s @ %s", localDemoProjectConfig.Title, datatugDemoProjectFullID),
	).SetReference(localDemoProjectConfig))

	// Add actions to Local projects
	localAddNode := tview.NewTreeNode(" Add exising ").
		SetReference("local-add").
		SetColor(sneatcolors.TreeNodeLink)
	localRoot.AddChild(localAddNode)

	localCreateNode := tview.NewTreeNode(" Create new ").
		SetReference("local-create").
		SetColor(sneatcolors.TreeNodeLink).
		SetSelectedFunc(func() {
			/* TODO: Open modal and ask for project name
			The modal should be defined in separate file
			The modal initially consist of 2 fields:
			Name: string (max 50 chars)
			Create at: (radio group: local, GitHub)
			If GitHub is choosen an addiional "Repository name" field shown
			If Local is choose an additional "Location" text field shown with default value of "~/datatug"
			At bottom of the modal 2 buttons: "Create" and "Cancel"
			Cancel closes dialog and nothing happens
			If "Create" button selected:
			   1) creates a `datatug.Project` with provided name
			   2) If local chosen safes to files store,
			      otherwise create repo using GitHub API `client.Repositories.Create`,
				  clones it to the "~/datatug/github.com/{owner}/{repo}" directory.
			      See example at `openDatatugDemoProject` and refactor code to reuse logic.
			   3) Once project created and if a Github one has local copy open the project (see how in `openDatatugDemoProject`)
			*/
			showCreateProjectScreen(tui)
		})
	localRoot.AddChild(localCreateNode)

	localRoot.SetExpanded(true)
	localTree.SetCurrentNode(localRoot.GetChildren()[0])

	// === DATATUG CLOUD PROJECTS TREE ===
	cloudsRoot := tview.NewTreeNode("Cloud projects").
		SetColor(tcell.ColorLightBlue).
		SetSelectable(false)
	cloudTree.SetRoot(cloudsRoot)

	githubNode := tview.NewTreeNode("GitHub")
	githubNode.SetSelectable(false)
	cloudsRoot.AddChild(githubNode)

	addToGithubRepoNode := tview.NewTreeNode(" Add to existing GitHub Repo ").
		SetReference("local-create").
		SetColor(sneatcolors.TreeNodeLink).
		SetSelectedFunc(func() {
			ShowAddToGitHubRepo(tui)
		})

	githubNode.AddChild(addToGithubRepoNode)

	datatugCloud := tview.NewTreeNode("Datatug Cloud")
	datatugCloud.SetColor(tcell.ColorLightBlue).SetSelectable(false)
	cloudsRoot.AddChild(datatugCloud)

	// DataTug demo project
	datatugDemoProject := &dtconfig.ProjectRef{
		ID:  datatugDemoProjectRepoID,
		Url: "cloud",
	}
	cloudDemoProjectNode := tview.NewTreeNode(" DataTug demo project ").
		SetReference(datatugDemoProject) //.
	//SetColor(tcell.ColorWhite)
	datatugCloud.AddChild(cloudDemoProjectNode)

	// Login to view action (moved to end)
	loginNode := tview.NewTreeNode(" Login to view personal or work projects ").
		SetReference("login").
		SetColor(sneatcolors.TreeNodeLink)
	datatugCloud.AddChild(loginNode)

	datatugCloud.SetExpanded(true)
	cloudsRoot.SetExpanded(true)

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

	// Function to update visual styling based on active tree
	updateTreeStyling := func() {
		for i, tree := range panel.trees {
			tree.SetSelectedFunc(selectionHandler)
			// Remove titles as requested - no SetTitle calls

			// Use available TreeView styling methods for highlighting

			if i == panel.currentTreeIndex {
				// Active tree: use bright colors for selected item highlighting
				tree.SetGraphicsColor(tcell.ColorWhite) // tree lines
			} else {
				// Inactive tree: use dim gray for selected item highlighting
				tree.SetGraphicsColor(tcell.ColorGrey) // tree lines
			}
		}
	}

	// Set up focus and blur handlers for each tree to manage selected item styling
	for i, tree := range panel.trees {
		treeIndex := i // Enqueue loop variable

		tree.SetFocusFunc(func() {
			// When tree gains focus, update styling for active state
			panel.currentTreeIndex = treeIndex
			updateTreeStyling()
			// Apply active styling to current node
			panel.applyNodeStyling(tree, true)
		})

		tree.SetBlurFunc(func() {
			// Update overall tree styling for inactive state
			updateTreeStyling()
			// When tree loses focus, apply dimmed styling to current node
			panel.applyNodeStyling(tree, false)
		})
	}

	// Main input capture function for the layout
	layout.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		currentTree := panel.trees[panel.currentTreeIndex]
		if !currentTree.HasFocus() { // Workaround for a bug
			panel.tui.SetFocus(currentTree)
		}

		switch event.Key() {
		case tcell.KeyESC:
			tui.SetFocus(tui.Menu)
			return nil
		case tcell.KeyLeft:
			// Move to previous tree
			if panel.currentTreeIndex == 0 {
				panel.tui.SetFocus(tui.Menu)
				return nil
			}
			// Apply dimmed styling to current tree before switching
			panel.applyNodeStyling(currentTree, false)
			panel.currentTreeIndex--
			currentTree = panel.trees[panel.currentTreeIndex]
			updateTreeStyling()
			// Set focus to the newly activated tree
			panel.ensureTreeHasCurrentNode(currentTree)
			tui.SetFocus(currentTree)
			return nil
		case tcell.KeyRight:
			// Move to next tree
			if panel.currentTreeIndex < len(panel.trees)-1 {
				// Apply dimmed styling to current tree before switching
				panel.applyNodeStyling(currentTree, false)
				panel.currentTreeIndex++
				currentTree = panel.trees[panel.currentTreeIndex]
				updateTreeStyling()
				// Set focus to the newly activated tree
				panel.ensureTreeHasCurrentNode(currentTree)
				tui.SetFocus(currentTree)
				return nil
			}
			return event
		case tcell.KeyUp:
			// Check if we're on the first non-root item
			currentNode := currentTree.GetCurrentNode()
			if currentNode != nil && currentNode == currentTree.GetRoot().GetChildren()[0] {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, currentTree)
				return nil
			}
			// Normal UP navigation within a tree
			return event
		case tcell.KeyDown:
			return event // Normal DOWN navigation within a tree
		case tcell.KeyEnter:
			// Handle ENTER key press on project nodes
			//currentNode := currentTree.GetCurrentNode()
			//if currentNode != nil {
			//	reference := currentNode.GetReference()
			//	if reference != nil {
			//		switch ref := reference.(type) {
			//		case *dtconfig.ProjectRef:
			//			// Call goProjectDashboards when ENTER is pressed on a project node
			//			GoDataTugProjectScreen(tui, ref)
			//			return nil
			//		}
			//	}
			//}
			return event
		default:
			return event
		}
	})

	// Set up all trees with basic styling
	updateTreeStyling()

	return panel, nil
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
	if len(p.trees) == 0 {
		return
	}
	// Ensure the tree has a current node before setting focus
	p.ensureTreeHasCurrentNode(p.trees[p.currentTreeIndex])

	// When the projectsPanel gets focus, delegate it to the current tree
	// Default to the first tree (local projects) if currentTreeIndex is not set
	if p.currentTreeIndex >= 0 && p.currentTreeIndex < len(p.trees) {
		p.tui.SetFocus(p.trees[p.currentTreeIndex])
	} else {
		p.tui.SetFocus(p.trees[0])
	}
}
