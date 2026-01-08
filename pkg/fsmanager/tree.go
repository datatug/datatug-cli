package fsmanager

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

var _ sneatv.PrimitiveWithBox = (*Tree)(nil)

type Tree struct {
	*tview.TreeView
	root        *tview.TreeNode
	currDirRoot *tview.TreeNode
}

func (t *Tree) GetBox() *tview.Box {
	return t.Box
}

func NewTree() *Tree {
	t := &Tree{
		TreeView: tview.NewTreeView(),
	}
	t.SetBorder(true)

	t.root = tview.NewTreeNode("").SetSelectable(false)
	t.TreeView.SetRoot(t.root)
	t.TreeView.SetTopLevel(1)

	favoritesNode := tview.NewTreeNode("Favorites").SetSelectable(false)
	t.root.AddChild(favoritesNode)

	addFavNode := func(text, dir string) {
		favoritesNode.AddChild(tview.NewTreeNode(text).SetReference(dir))
	}

	addFavNode(" ~ [gray]Home[-]", "~")
	addFavNode(" / [gray]Root[-]", "/")

	t.currDirRoot = tview.NewTreeNode("~")
	t.root.AddChild(t.currDirRoot)

	return t
}
