package chat

import (
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// workspaceTabs/workspaceTabIndex name the SidePanel's fixed tab order,
// shared by ChatUI's workspacePanel (chatui_sidepanel.go).
var workspaceTabs = []string{"Project", "Selected", "Docked", "Bookmarks"}

func workspaceTabIndex(name string) int {
	for i, tab := range workspaceTabs {
		if tab == name {
			return i
		}
	}
	return 0
}

// columnIndexOf returns the index of a named column, or -1.
func columnIndexOf(columns []string, name string) int {
	for i, column := range columns {
		if column == name {
			return i
		}
	}
	return -1
}

// explorerNode is one row of the project explorer tree (source/table/view/
// query, plus grouping and issue rows), shared by ChatUI's own
// workspacePanel.explorerNodes (chatui_sidepanel.go).
type explorerNode struct {
	id          string
	label       string
	depth       int
	objectIndex int // -1 for a grouping node
	branch      bool
	issue       bool
	issueFor    int // owner of an unattachable issue row
}

type referenceGridData struct {
	Result      secureread.Result
	SourceRows  []int
	RecordSetID string
	ViewID      string
}

// gridDataForReference resolves a workspace ContextReference (bookmark,
// recordset, view or selection) to the RecordSet-projected data a grid needs:
// the (possibly filtered/reordered) Result plus SourceRows mapping each
// projected row back to its position in the underlying RecordSet.
func gridDataForReference(session ChatSession, ref ContextReference) (referenceGridData, bool) {
	switch ref.Kind {
	case "bookmark":
		bookmark, ok := session.Bookmarks[ref.ObjectID]
		if !ok {
			return referenceGridData{}, false
		}
		result, sourceRows := bookmarkResult(bookmark)
		return referenceGridData{Result: result, SourceRows: sourceRows}, true
	case "recordset":
		record, ok := session.RecordSets[ref.ObjectID]
		if !ok {
			return referenceGridData{}, false
		}
		rows := make([]int, len(record.Result.Rows))
		for i := range rows {
			rows[i] = i
		}
		return referenceGridData{Result: record.Result, SourceRows: rows, RecordSetID: record.ID}, true
	case "view", "selection":
		var view RecordSetView
		var rows []int
		var columns []string
		if ref.Kind == "selection" {
			selection, ok := session.Workspace.Selections[ref.ObjectID]
			if !ok {
				return referenceGridData{}, false
			}
			view, ok = session.Workspace.Views[selection.ViewID]
			if !ok {
				return referenceGridData{}, false
			}
			rows = selection.Rows
			columns = selection.Columns
		} else {
			var ok bool
			view, ok = session.Workspace.Views[ref.ObjectID]
			if !ok {
				return referenceGridData{}, false
			}
			rows = view.RowIndices
			columns = view.Columns
		}
		record, ok := session.RecordSets[view.RecordSetID]
		if !ok {
			return referenceGridData{}, false
		}
		if len(columns) == 0 {
			columns = record.Result.Columns
		}
		var selection *Selection
		if ref.Kind == "selection" {
			selected := session.Workspace.Selections[ref.ObjectID]
			selection = &selected
		}
		projectedView := view
		projectedView.RowIndices = rows
		projectedView.Columns = columns
		result, sourceRows := projectSnapshot(record, &projectedView, selection)
		return referenceGridData{Result: result, SourceRows: sourceRows, RecordSetID: record.ID, ViewID: view.ID}, true
	}
	return referenceGridData{}, false
}
