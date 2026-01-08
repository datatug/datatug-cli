package fsmanager

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

var _ sneatv.PrimitiveWithBox = (*Tree)(nil)

type Tree struct {
	*tview.TreeView
	currDirRoot *tview.TreeNode
}

func (t *Tree) GetBox() *tview.Box {
	return t.Box
}

func NewTree() *Tree {
	t := &Tree{
		TreeView: tview.NewTreeView(),
	}

	t.currDirRoot = tview.NewTreeNode("~")
	t.SetRoot(t.currDirRoot)

	return t
}
