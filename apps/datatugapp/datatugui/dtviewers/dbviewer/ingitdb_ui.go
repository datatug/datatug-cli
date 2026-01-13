package dbviewer

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func goIngitdbBHome(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	breadcrumbs := GetDbViewersBreadcrumbs(tui)
	breadcrumbs.Push(sneatv.NewBreadcrumb("inGitDB", nil))

	menu := getDbViewerMenu(tui, focusTo, "")
	menuPanel := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(menu, menu.Box))

	tree := tview.NewTreeView()
	tree.SetTitle("inGitDB viewer")
	root := tview.NewTreeNode("inGitDB viewer")
	root.SetSelectable(false)

	tree.SetRoot(root)
	tree.SetTopLevel(1)

	openNode := tview.NewTreeNode("Open inGitDB directory")
	root.AddChild(openNode)
	tree.SetCurrentNode(openNode)

	demoNode := tview.NewTreeNode("Demo")
	demoNode.SetSelectable(false)
	root.AddChild(demoNode)

	northwindNode := tview.NewTreeNode(" github.com/ingitdb/demo-ingitdb ")
	northwindNode.SetSelectedFunc(func() {
		//go openSqliteDemoDb(tui, northwindSqliteDbFileName)
	})
	demoNode.AddChild(northwindNode)

	setDbHomeMenuInputCapture(tui, menu, tree)
	setDbHomeTreeInputCapture(tui, tree, openNode)

	content := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(tree, tree.Box))

	tui.SetPanels(menuPanel, content, sneatnav.WithFocusTo(focusTo))
	return nil
}

func setDbHomeTreeInputCapture(tui *sneatnav.TUI, tree *tview.TreeView, openNode *tview.TreeNode) {
	tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			tui.App.SetFocus(tui.Menu)
			return nil
		case tcell.KeyUp:
			if tree.GetCurrentNode() == openNode {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, tree)
				return nil
			}
			return event
		default:
			return event
		}
	})
}
func setDbHomeMenuInputCapture(tui *sneatnav.TUI, menu *tview.List, tree *tview.TreeView) {
	menu.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			tui.App.SetFocus(tree)
			return nil
		case tcell.KeyUp:
			if menu.GetCurrentItem() == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, menu)
				return nil
			}
			return event
		default:
			return event
		}
	})
}
