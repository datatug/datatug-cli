package chat

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func gridDataTestSession() ChatSession {
	record := RecordSet{ID: "rs1", Result: secureread.Result{
		Columns: []string{"CustomerId", "City"},
		Rows: []secureread.Row{
			{Data: map[string]any{"CustomerId": 1, "City": "Prague"}},
			{Data: map[string]any{"CustomerId": 2, "City": "Brno"}},
		},
	}}
	return ChatSession{
		RecordSets: map[string]RecordSet{"rs1": record},
		Workspace: WorkspaceState{
			Views: map[string]RecordSetView{
				"view1": {ID: "view1", RecordSetID: "rs1", RowIndices: []int{0, 1}},
			},
			Selections: map[string]Selection{
				"sel1":            {ID: "sel1", ViewID: "view1", Rows: []int{0}, Columns: []string{"CustomerId"}},
				"sel-orphan-view": {ID: "sel-orphan-view", ViewID: "missing-view"},
			},
		},
	}
}

func TestGridDataForReferenceBookmarkNotFound(t *testing.T) {
	session := gridDataTestSession()
	if _, ok := gridDataForReference(session, ContextReference{Kind: "bookmark", ObjectID: "missing"}); ok {
		t.Fatal("expected a missing bookmark to report not-found")
	}
}

func TestGridDataForReferenceRecordSet(t *testing.T) {
	session := gridDataTestSession()
	data, ok := gridDataForReference(session, ContextReference{Kind: "recordset", ObjectID: "rs1"})
	if !ok || data.RecordSetID != "rs1" || len(data.Result.Rows) != 2 || len(data.SourceRows) != 2 {
		t.Fatalf("gridDataForReference(recordset) = %+v, %v", data, ok)
	}
	if data.SourceRows[0] != 0 || data.SourceRows[1] != 1 {
		t.Fatalf("expected identity SourceRows, got %v", data.SourceRows)
	}
}

func TestGridDataForReferenceRecordSetNotFound(t *testing.T) {
	session := gridDataTestSession()
	if _, ok := gridDataForReference(session, ContextReference{Kind: "recordset", ObjectID: "missing"}); ok {
		t.Fatal("expected a missing RecordSet to report not-found")
	}
}

func TestGridDataForReferenceView(t *testing.T) {
	session := gridDataTestSession()
	data, ok := gridDataForReference(session, ContextReference{Kind: "view", ObjectID: "view1"})
	if !ok || data.RecordSetID != "rs1" || data.ViewID != "view1" || len(data.Result.Rows) != 2 {
		t.Fatalf("gridDataForReference(view) = %+v, %v", data, ok)
	}
}

func TestGridDataForReferenceViewNotFound(t *testing.T) {
	session := gridDataTestSession()
	if _, ok := gridDataForReference(session, ContextReference{Kind: "view", ObjectID: "missing"}); ok {
		t.Fatal("expected a missing view to report not-found")
	}
}

func TestGridDataForReferenceSelection(t *testing.T) {
	session := gridDataTestSession()
	data, ok := gridDataForReference(session, ContextReference{Kind: "selection", ObjectID: "sel1"})
	if !ok || data.RecordSetID != "rs1" || len(data.Result.Rows) != 1 {
		t.Fatalf("gridDataForReference(selection) = %+v, %v", data, ok)
	}
}

func TestGridDataForReferenceSelectionNotFound(t *testing.T) {
	session := gridDataTestSession()
	if _, ok := gridDataForReference(session, ContextReference{Kind: "selection", ObjectID: "missing"}); ok {
		t.Fatal("expected a missing selection to report not-found")
	}
}

func TestGridDataForReferenceSelectionWithOrphanedView(t *testing.T) {
	session := gridDataTestSession()
	if _, ok := gridDataForReference(session, ContextReference{Kind: "selection", ObjectID: "sel-orphan-view"}); ok {
		t.Fatal("expected a selection referencing a missing view to report not-found")
	}
}

func TestGridDataForReferenceRecordSetMissingUnderViewFallsThrough(t *testing.T) {
	session := gridDataTestSession()
	session.Workspace.Views["orphan"] = RecordSetView{ID: "orphan", RecordSetID: "missing-recordset"}
	if _, ok := gridDataForReference(session, ContextReference{Kind: "view", ObjectID: "orphan"}); ok {
		t.Fatal("expected a view whose RecordSet is missing to report not-found")
	}
}

func TestGridDataForReferenceUnknownKind(t *testing.T) {
	session := gridDataTestSession()
	if _, ok := gridDataForReference(session, ContextReference{Kind: "unknown", ObjectID: "x"}); ok {
		t.Fatal("expected an unrecognized reference kind to report not-found")
	}
}
