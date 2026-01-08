package fsmanager

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Navigator struct {
	*tview.Flex
	tree  *Tree
	table *tview.Table
}

func NewNavigator() *Navigator {

	tree := NewTree()
	table := tview.NewTable()
	table.SetSelectable(true, false)
	table.SetFixed(1, 1)

	flex := tview.NewFlex()
	flex.AddItem(tree, 0, 1, true)
	flex.AddItem(table, 0, 1, true)

	manager := &Navigator{
		Flex:  flex,
		tree:  tree,
		table: table,
	}
	manager.table.SetBorder(true)

	tree.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			ref := tree.GetCurrentNode().GetReference()
			if ref != nil {
				dir := ref.(string)
				manager.goDir(dir)
				return nil
			}
			return event
		}
		return event
	})

	manager.goDir("~")

	return manager
}

func (m *Navigator) goDir(dir string) {
	t := m.tree
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
			n := tview.NewTreeNode(" " + p).SetReference(nodePath)
			parentNode.AddChild(n)
			parentNode = n
		}
	}

	t.TreeView.SetCurrentNode(parentNode)

	dirPath := filestore.ExpandHome(nodePath)
	children, err := os.ReadDir(dirPath)
	if err != nil {
		parentNode.AddChild(tview.NewTreeNode(fmt.Sprintf("Error for %s: %s", dirPath, err.Error())))
		return
	}
	fileIndex := 0
	m.table.Clear()
	m.table.SetCell(0, 0, tview.NewTableCell("File name"))
	m.table.SetCell(0, 1, tview.NewTableCell("Size").SetAlign(tview.AlignRight))
	fileIndex++
	for _, child := range children {
		name := child.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if child.IsDir() {
			n := tview.NewTreeNode(" " + name).SetReference(path.Join(nodePath, name))
			parentNode.AddChild(n)
		} else {
			m.table.SetCell(fileIndex, 0, tview.NewTableCell(name))
			if fi, err := child.Info(); err == nil {
				m.table.SetCell(fileIndex, 1,
					tview.NewTableCell(strconv.FormatInt(fi.Size(), 10)).SetAlign(tview.AlignRight))
			}
			fileIndex++
		}
	}

	//dirPath := filestore.ExpandHome(dir)
}
