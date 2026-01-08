package filetug

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/fsutils"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Navigator struct {
	app *tview.Application
	o   navigatorOptions
	*tview.Flex
	tree      *Tree
	favorites *favorites
	left      *tview.Flex
	table     *tview.Table
}

func (n *Navigator) SetFocus() {
	n.app.SetFocus(n.tree.TreeView)
}

type navigatorOptions struct {
	moveFocusUp func(source tview.Primitive)
}

type NavigatorOption func(o *navigatorOptions)

func OnMoveFocusUp(f func(source tview.Primitive)) NavigatorOption {
	return func(o *navigatorOptions) {
		o.moveFocusUp = f
	}
}

func NewNavigator(app *tview.Application, options ...NavigatorOption) *Navigator {

	nav := new(Navigator)

	for _, option := range options {
		option(&nav.o)
	}

	nav.app = app

	nav.tree = NewTree()

	nav.favorites = newFavorites()

	nav.table = tview.NewTable()
	nav.table.SetSelectable(true, false)
	nav.table.SetFixed(1, 1)
	nav.table.SetBorderColor(Style.BlurBorderColor)
	nav.table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			app.SetFocus(nav.tree)
			return nil
		default:
			return event
		}
	})
	nav.table.SetFocusFunc(func() {
		nav.table.SetBorderColor(Style.FocusedBorderColor)
	})
	nav.table.SetBlurFunc(func() {
		nav.table.SetBorderColor(Style.BlurBorderColor)
	})

	nav.left = tview.NewFlex().SetDirection(tview.FlexRow)
	nav.left.AddItem(nav.favorites, 3, 0, false)
	nav.left.AddItem(nav.tree, 0, 1, true)
	nav.left.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			nav.app.SetFocus(nav.table)
			return nil
		default:
			return event
		}
	})

	flex := tview.NewFlex()
	nav.Flex = flex
	flex.AddItem(nav.left, 0, 4, true)
	flex.AddItem(nav.table, 0, 8, true)

	flex.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Modifiers()&tcell.ModAlt != 0 && event.Key() == tcell.KeyRune {
			switch r := event.Rune(); r {
			case '/', 'r', 'R':
				nav.goDir("/")
				return nil
			case '~', 'h', 'H':
				nav.goDir("~")
				return nil
			default:
				return event
			}
		}
		return event
	})

	nav.left.SetBorder(true)
	nav.table.SetBorder(true)

	treeViewInputCapture := func(t *tview.TreeView, event *tcell.EventKey, f func(*tcell.EventKey) *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			ref := t.GetCurrentNode().GetReference()
			if ref != nil {
				dir := ref.(string)
				nav.goDir(dir)
				return nil
			}
		}
		if f != nil {
			return f(event)
		}
		return event
	}
	nav.favorites.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return treeViewInputCapture(nav.favorites.TreeView, event, func(key *tcell.EventKey) *tcell.EventKey {
			switch event.Key() {
			case tcell.KeyUp:
				rootNode := nav.favorites.GetRoot()
				current := nav.favorites.GetCurrentNode()
				if current == rootNode || current == rootNode.GetChildren()[0] {
					nav.o.moveFocusUp(nav.favorites.TreeView)
					nav.favorites.SetCurrentNode(nil)
					return nil
				}
				return event
			case tcell.KeyDown:
				favNodes := nav.favorites.GetRoot().GetChildren()
				if nav.favorites.GetCurrentNode() == favNodes[len(favNodes)-1] {
					nav.favorites.SetCurrentNode(nil)
					nav.tree.SetCurrentNode(nav.tree.GetRoot())
					nav.app.SetFocus(nav.tree.TreeView)
					return nil
				}
				return event
			default:
				return event
			}
		})
	})
	nav.tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return treeViewInputCapture(nav.tree.TreeView, event, func(key *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyUp && nav.tree.GetCurrentNode() == nav.tree.GetRoot() {
				children := nav.favorites.GetRoot().GetChildren()
				nav.favorites.SetCurrentNode(children[len(children)-1])
				nav.tree.SetCurrentNode(nil)
				nav.app.SetFocus(nav.favorites.TreeView)
				return nil
			}
			return event
		})
	})

	nav.left.SetFocusFunc(func() {
		nav.left.SetBorderColor(Style.FocusedBorderColor)
		nav.app.SetFocus(nav.favorites.TreeView)
	})

	nav.left.SetBlurFunc(func() {
		nav.left.SetBorderColor(Style.BlurBorderColor)
	})

	onLeftTreeViewFocus := func(t *tview.TreeView) {
		t.SetGraphicsColor(tcell.ColorWhite)
		nav.left.SetBorderColor(Style.FocusedBorderColor)
		if t.GetCurrentNode() == nil {
			children := t.GetRoot().GetChildren()
			if len(children) > 0 {
				t.SetCurrentNode(children[0])
			}
		}
	}

	onLeftTreeViewBlur := func(t *tview.TreeView) {
		t.SetGraphicsColor(Style.BlurGraphicsColor)
		nav.left.SetBorderColor(Style.BlurBorderColor)
	}

	nav.favorites.SetFocusFunc(func() {
		if nav.favorites.GetCurrentNode() == nil {
			nav.favorites.SetCurrentNode(nav.tree.GetRoot().GetChildren()[0])
		}
		onLeftTreeViewFocus(nav.favorites.TreeView)
	})
	nav.tree.SetFocusFunc(func() {
		onLeftTreeViewFocus(nav.tree.TreeView)
	})
	nav.favorites.SetBlurFunc(func() {
		onLeftTreeViewBlur(nav.favorites.TreeView)
	})
	nav.tree.SetBlurFunc(func() {
		onLeftTreeViewBlur(nav.tree.TreeView)
	})

	nav.goDir("~")

	return nav
}

func (nav *Navigator) goDir(dir string) {

	nav.favorites.SetCurrentNode(nil)

	t := nav.tree
	t.currDirRoot.ClearChildren()

	parentNode := t.currDirRoot

	var nodePath string

	if strings.HasPrefix(dir, "~") || strings.HasPrefix(dir, "/") {
		nodePath = dir[:1]
		t.currDirRoot.SetText(nodePath).SetReference(nodePath)
	}

	dirRelPath := strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/")

	if dirRelPath != "" {
		parents := strings.Split(dirRelPath, "/")
		for _, p := range parents {
			if nodePath == "/" {
				nodePath += p
			} else {
				nodePath = nodePath + "/" + p
			}
			n := tview.NewTreeNode("📁" + p).SetReference(nodePath)
			parentNode.AddChild(n)
			parentNode = n
		}
	}

	dirPath := fsutils.ExpandHome(nodePath)
	children, err := os.ReadDir(dirPath)
	if err != nil {
		parentNode.AddChild(tview.NewTreeNode(fmt.Sprintf("Error for %s: %s", dirPath, err.Error())))
		return
	}
	fileIndex := 0

	nav.table.SetTitle(fmt.Sprintf(" Files: %s ", dir))
	nav.table.Clear()
	nav.table.SetCell(0, 0, tview.NewTableCell("File name"))
	nav.table.SetCell(0, 1, tview.NewTableCell("Size").SetAlign(tview.AlignRight))

	fileIndex++
	for _, child := range children {
		name := child.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if child.IsDir() {
			n := tview.NewTreeNode("📁" + name).SetReference(path.Join(nodePath, name))
			parentNode.AddChild(n)
		} else {
			nav.table.SetCell(fileIndex, 0, tview.NewTableCell(name))
			if fi, err := child.Info(); err == nil {
				nav.table.SetCell(fileIndex, 1,
					tview.NewTableCell(strconv.FormatInt(fi.Size(), 10)).SetAlign(tview.AlignRight))
			}
			fileIndex++
		}
	}
	t.SetCurrentNode(parentNode)
	nav.app.SetFocus(t)
}
