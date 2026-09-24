package chat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
)

func workspaceTestCatalog() ProjectCatalog {
	return ProjectCatalog{ID: "chinook", Title: "Chinook", Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "project", ProjectID: "chinook", ObjectID: "chinook", Title: "Chinook"}},
		{Reference: ContextReference{Kind: "source", ProjectID: "chinook", SourceID: "chinook-local", ObjectID: "chinook-local", Title: "Chinook local"}},
		{Reference: ContextReference{Kind: "table", ProjectID: "chinook", SourceID: "chinook-local", ObjectID: "main.Customer", Title: "Customer"}, Columns: []string{"CustomerId", "City"}},
	}}
}

// TestProjectExplorerShowsSourceIssueInPlace and
// TestProjectExplorerIssueDetailsRemainVisibleInLongTree were ported onto
// ChatUI's workspacePanel in chatui_sidepanel_test.go.

func workspaceTestRecord(t *testing.T, store *SessionStore, sessionID string) string {
	t.Helper()
	ctx := context.Background()
	user, err := store.AppendUser(ctx, sessionID, "show customers")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AppendTurn(ctx, sessionID, user.ID, "sqlite:///chinook.db", Turn{Queries: []QueryResult{{
		Title: "Customers", DTQL: "from: {name: Customer}\nlimit: 3",
		Result: secureread.Result{Columns: []string{"CustomerId", "City"}, Rows: []secureread.Row{
			{Data: map[string]any{"CustomerId": int64(5), "City": "Prague"}},
			{Data: map[string]any{"CustomerId": int64(6), "City": "Prague"}},
			{Data: map[string]any{"CustomerId": int64(7), "City": "Paris"}},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return turn.Queries[0].RecordSetID
}

func TestWorkspacePersistenceSelectionAttachmentsDocksAndSessionIsolation(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	a, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, a.ID)
	pragueRef, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Column: "City", Equals: "Prague", Limit: 2, Title: "Prague customers"})
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Column: "City", Equals: "Paris", Title: "Paris customers"})
	if err != nil {
		t.Fatal(err)
	}
	if sameReference(pragueRef, secondRef) {
		t.Fatal("second selection reused first identity")
	}
	beforeAttach, err := chat.Snapshot(ctx)
	if err != nil || len(beforeAttach.Workspace.Attachments) != 0 {
		t.Fatalf("selection attached without explicit action: %+v, %v", beforeAttach.Workspace.Attachments, err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: pragueRef}); err != nil {
		t.Fatal(err)
	}
	table := workspaceTestCatalog().Objects[2].Reference
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: table}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: pragueRef}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workspace.Views) != 2 || len(snapshot.Workspace.Selections) != 2 || len(snapshot.Workspace.Attachments) != 2 || len(snapshot.Workspace.Docks) != 1 {
		t.Fatalf("workspace state = %+v", snapshot.Workspace)
	}
	if got := snapshot.Workspace.Selections[pragueRef.ObjectID].Rows; !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("Prague rows = %v", got)
	}
	if got := selectionParameters(snapshot)["selection_1_c1"]; !reflect.DeepEqual(got, []any{int64(5), int64(6)}) {
		t.Fatalf("locally bound IDs = %#v", got)
	}
	prompt := buildSessionContext(snapshot, workspaceTestCatalog())
	if !strings.Contains(prompt, "selection_1_c1") || !strings.Contains(prompt, "main.Customer") {
		t.Fatalf("structured context missing identity/parameter: %s", prompt)
	}
	if strings.Contains(prompt, "CustomerId=5") || strings.Contains(prompt, "CustomerId distinct values") {
		t.Fatalf("selected values leaked into model context: %s", prompt)
	}
	_ = store.Close()
	reloaded := openTestStore(t, path, testScope())
	chat, err = NewSessionChat(ctx, reloaded, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Workspace.Docks) != 1 || len(restored.Workspace.Selections) != 2 || len(restored.RecordSets) != 1 {
		t.Fatalf("workspace did not restore: %+v", restored.Workspace)
	}
	if result, ok := resultForReference(restored, pragueRef); !ok || len(result.Rows) != 2 {
		t.Fatalf("docked view did not resolve original rows: %+v, %v", result, ok)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "undock", DockID: restored.Workspace.Docks[0].ID}); err != nil {
		t.Fatal(err)
	}
	undocked, err := chat.Snapshot(ctx)
	if err != nil || len(undocked.Workspace.Docks) != 0 || len(undocked.RecordSets) != 1 {
		t.Fatalf("undock lost underlying data: %+v, %v", undocked, err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "detach", Reference: table}); err != nil {
		t.Fatal(err)
	}
	detached, err := chat.Snapshot(ctx)
	if err != nil || len(detached.Workspace.Attachments) != 1 {
		t.Fatalf("detach did not persist: %+v, %v", detached.Workspace, err)
	}
	b, err := chat.Create(ctx)
	if err != nil || len(b.Workspace.Attachments) != 0 {
		t.Fatalf("new session leaked context: %+v, %v", b.Workspace, err)
	}
	if _, err := chat.Switch(ctx, a.ID[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	cleared, err := chat.Snapshot(ctx)
	if err != nil || len(cleared.Workspace.Selections) != 0 || len(cleared.Workspace.Attachments) != 0 || len(cleared.RecordSets) != 0 {
		t.Fatalf("clear kept session state: %+v, %v", cleared, err)
	}
	if _, err := chat.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Load(ctx, a.ID); err == nil {
		t.Fatal("deleted session reopened")
	}
}

func TestWorkspaceRejectsUnavailableReferencesAndMissingColumns(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chat.Snapshot(ctx)
	recordID := workspaceTestRecord(t, store, session.ID)
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Column: "NoSuchColumn", Equals: "Prague"}); err == nil || !strings.Contains(err.Error(), "no NoSuchColumn column") {
		t.Fatalf("missing column error = %v", err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Ranges: []CellRange{{FirstRow: 0, LastRow: 3, FirstCol: 0, LastCol: 1}}}); err == nil {
		t.Fatal("out-of-bounds cell range was accepted")
	}
	viewRef, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0}, Title: "Only first row"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	viewID := selected.Workspace.Selections[viewRef.ObjectID].ViewID
	if docked, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock"}); err != nil || !sameReference(docked, viewRef) {
		t.Fatalf("dock current selection = %+v, %v", docked, err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", ViewID: viewID, Rows: []int{2}}); err == nil {
		t.Fatal("selection escaped its parent View")
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", ViewID: viewID, Ranges: []CellRange{{FirstRow: 0, LastRow: 2, FirstCol: 0, LastCol: 0}}}); err == nil {
		t.Fatal("cell range escaped its parent View")
	}
	columnRef, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0, 1}, Columns: []string{"CustomerId"}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err = chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	columnViewID := selected.Workspace.Selections[columnRef.ObjectID].ViewID
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", ViewID: columnViewID, Ranges: []CellRange{{FirstRow: 0, LastRow: 0, FirstCol: 1, LastCol: 1}}}); err == nil {
		t.Fatal("cell range escaped its parent View columns")
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", ViewID: columnViewID, Columns: []string{"City"}}); err == nil {
		t.Fatal("selection escaped its parent View columns")
	}
	rangeRef, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Ranges: []CellRange{{FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	selected, err = chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := selected.Workspace.Selections[rangeRef.ObjectID]; !reflect.DeepEqual(got.Rows, []int{1}) || !reflect.DeepEqual(got.Columns, []string{"City"}) {
		t.Fatalf("range-only selection = %+v", got)
	}
	bad := ContextReference{Kind: "table", ProjectID: "chinook", SourceID: "elsewhere", ObjectID: "main.Customer", Title: "Customer"}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: bad}); err == nil {
		t.Fatal("ambiguous/wrong-source table attached")
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ContextReference{Kind: "recordset", ObjectID: "missing", Title: "Missing"}}); err == nil {
		t.Fatal("missing RecordSet docked")
	}
}

func TestWorkspaceLoadRejectsInvalidPersistedCoordinates(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	view := RecordSetView{ID: "view-1", RecordSetID: recordID, RowIndices: []int{0}, Columns: []string{"CustomerId"}}
	selection := Selection{ID: "selection-1", ViewID: view.ID, Rows: []int{0}, Columns: []string{"CustomerId"}}
	base := WorkspaceState{Views: map[string]RecordSetView{view.ID: view}, Selections: map[string]Selection{selection.ID: selection}}
	tests := []struct {
		name   string
		mutate func(*WorkspaceState)
	}{
		{"view column", func(w *WorkspaceState) { v := w.Views[view.ID]; v.Columns = []string{"Missing"}; w.Views[view.ID] = v }},
		{"selection row", func(w *WorkspaceState) {
			s := w.Selections[selection.ID]
			s.Rows = []int{1}
			w.Selections[selection.ID] = s
		}},
		{"selection column", func(w *WorkspaceState) {
			s := w.Selections[selection.ID]
			s.Columns = []string{"City"}
			w.Selections[selection.ID] = s
		}},
		{"cell range", func(w *WorkspaceState) {
			s := w.Selections[selection.ID]
			s.Ranges = []CellRange{{FirstRow: 0, LastRow: 3, FirstCol: 0, LastCol: 0}}
			w.Selections[selection.ID] = s
		}},
		{"current selection", func(w *WorkspaceState) { w.CurrentSelectionID = "missing" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := WorkspaceState{Views: map[string]RecordSetView{view.ID: view}, Selections: map[string]Selection{selection.ID: selection}}
			test.mutate(&state)
			if err := store.SaveWorkspace(ctx, session.ID, state); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(ctx, session.ID); err == nil {
				t.Fatal("invalid persisted workspace was accepted")
			}
		})
	}
	if err := store.SaveWorkspace(ctx, session.ID, base); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, session.ID); err != nil {
		t.Fatalf("valid workspace was rejected: %v", err)
	}
}

func TestDockedSelectionIsContextWithoutImplicitAttachment(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0, 1}, Columns: []string{"CustomerId"}, Title: "Prague customers"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := chat.Snapshot(ctx)
	if err != nil || len(selected.Workspace.Attachments) != 0 || len(selectionParameters(selected)) != 0 {
		t.Fatalf("selection became query context without attachment: %+v, %v", selected.Workspace, err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	docked, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(docked.Workspace.Attachments) != 0 || !reflect.DeepEqual(selectionParameters(docked)["selection_1_c1"], []any{int64(5), int64(6)}) {
		t.Fatalf("docked context bindings = %+v", selectionParameters(docked))
	}
	prompt := buildSessionContext(docked)
	if !strings.Contains(prompt, "Docked selection") || !strings.Contains(prompt, "selection_1_c1") || strings.Contains(prompt, "CustomerId=5") {
		t.Fatalf("docked context = %s", prompt)
	}
}

func TestAgentSelectDockAndFollowUpBindsSelectionLocally(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	seed, err := store.LatestOrCreate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, seed.ID)
	doc := "from: {name: Invoice}\nwhere:\n  op: In\n  left: {field: CustomerId}\n  right: {param: selection_1_c1}\nlimit: 20"
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolWorkspaceAction, map[string]any{"kind": "select", "recordSetId": recordID, "column": "City", "equals": "Prague", "limit": 2, "columns": []string{"CustomerId"}})}},
		{text: "Selected."},
		{toolCalls: []ai.ToolCall{toolCall("2", toolWorkspaceAction, map[string]any{"kind": "dock"})}},
		{text: "Docked."},
		{toolCalls: []ai.ToolCall{toolCall("3", toolRunDTQL, map[string]any{"dtql": doc, "title": "Largest selected orders"})}},
		{text: "Here are their invoices."},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId", "CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 404, "CustomerId": 6}}}}}
	agent, err := NewAIConversation(llm, executor, "sqlite:///chinook.db", "- Customer: CustomerId, City\n- Invoice: InvoiceId, CustomerId")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Select top 5 from Prague"); err != nil {
		t.Fatal(err)
	}
	selected, err := chat.Snapshot(ctx)
	if err != nil || len(selected.Workspace.Selections) != 1 || len(selected.Workspace.Attachments) != 0 {
		t.Fatalf("selection was not isolated from attachments: %+v, %v", selected.Workspace, err)
	}
	if _, err := chat.Ask(ctx, "Dock them"); err != nil {
		t.Fatal(err)
	}
	docked, err := chat.Snapshot(ctx)
	if err != nil || len(docked.Workspace.Docks) != 1 {
		t.Fatalf("agent dock action did not persist: %+v, %v", docked.Workspace, err)
	}
	if _, err := chat.Ask(ctx, "Show their largest orders"); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || !reflect.DeepEqual(executor.params["selection_1_c1"], []any{int64(5), int64(6)}) {
		t.Fatalf("follow-up binding = calls %d, params %#v", executor.calls, executor.params)
	}
	saved, err := chat.Snapshot(ctx)
	if err != nil || len(saved.RecordSets) != 2 || len(saved.Workspace.Docks) != 1 {
		t.Fatalf("follow-up result or dock not durable: %+v, %v", saved, err)
	}
}

func TestSortingViewKeepsSelectionAndDockOrderDurable(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Title: "All customers"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	viewID := current.Workspace.Selections[ref.ObjectID].ViewID
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "sort_view", ViewID: viewID, OrderBy: "CustomerId", Descending: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path, testScope())
	saved, err := reopened.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{2, 1, 0}
	if got := saved.Workspace.Views[viewID].RowIndices; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored view order = %v, want %v", got, want)
	}
	if got := saved.Workspace.Selections[ref.ObjectID].Rows; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored selection order = %v, want %v", got, want)
	}
	if len(saved.Workspace.Docks) != 1 || saved.Workspace.Docks[0].Reference.ObjectID != ref.ObjectID {
		t.Fatalf("dock lost shared selection reference: %+v", saved.Workspace.Docks)
	}
}

func TestPrivateSelectionErrorStaysOutOfSavedModelContext(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.LatestOrCreate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "Use selected rows")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "Paris-private-selected-value"
	_, err = store.AppendQuery(ctx, session.ID, user.ID, "sqlite:///chinook.db", QueryResult{
		DTQL: "from: {name: Customer}\nlimit: 5", Err: errors.New("driver echoed " + secret),
		Parameters: map[string]any{"selection_1_c1": []any{secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prompt := buildSessionContext(saved); strings.Contains(prompt, secret) {
		t.Fatalf("private value leaked from saved error into model context: %s", prompt)
	}
}

func TestFailedBoundQueryNeverSendsSelectedValueToModelAcrossTurns(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	const secret = "Paris"
	doc := "from: {name: Customer}\nwhere:\n  op: In\n  left: {field: City}\n  right: {param: selection_1_c1}\nlimit: 5"
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"dtql": doc})}},
		{text: "The query could not be completed."},
		{text: "Ready to try again."},
	}}
	agent, err := NewAIConversation(llm, &fakeExecutor{err: errors.New("driver echoed selected value " + secret)}, "sqlite:///chinook.db", "- Customer: CustomerId, City")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{2}, Columns: []string{"City"}, Title: "Chosen cell"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Use the selected cell"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Try a different approach"); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 3 {
		t.Fatalf("model requests = %d, want 3", len(llm.requests))
	}
	for i, request := range llm.requests {
		if text := fmt.Sprint(request.Messages); strings.Contains(text, secret) {
			t.Fatalf("selected value leaked into model request %d: %s", i+1, text)
		}
	}
}

func TestSelectedCellValueStaysOutOfModelRequest(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	llm := &scriptedProvider{steps: []scriptedStep{{text: "Ready."}}}
	agent, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", "- Customer: CustomerId, City")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{2}, Columns: []string{"City"}, Title: "Chosen cell"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Use the selected cell later"); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 1 {
		t.Fatalf("model requests = %d", len(llm.requests))
	}
	prompt := llm.requests[0].Messages[0].Text
	if strings.Contains(prompt, "Paris") {
		t.Fatalf("selected cell value leaked to model: %s", prompt)
	}
	if !strings.Contains(prompt, "selection_1_c1") {
		t.Fatalf("opaque local binding missing from model prompt: %s", prompt)
	}
}

// TestWorkspaceSplitAndKeyboardSelection was ported onto ChatUI's
// workspacePanel in chatui_sidepanel_test.go
// (TestChatUIWorkspaceSplitAndKeyboardSelection) — its mouse-click
// attachment-close sub-case is a documented, real gap there (ChatUI's
// topBar renders no attachment chips yet); the detach behaviour itself is
// covered via the explorer's keyboard "space" toggle instead.
//
// TestExplorerGroupsObjectsByDeclaredSourceAndCollapses was ported onto
// ChatUI's workspacePanel in chatui_sidepanel_test.go.
//
// TestProjectPickerSelectsConfiguredProject's Down-navigation case was
// ported onto ChatUI's project picker overlay in chatui_pickers_test.go
// (TestChatUIProjectPickerDownThenEnterSelectsSecondChoice); its
// Enter-selects-the-first-choice case is already covered there by
// TestChatUIF3OpensProjectPickerAndSelects.

// TestSplitDividerResizesWithinUsefulBounds is NOT ported: Ctrl+←/→ split
// resize is checklist item #46, NATIVE to tui/chatshell (no DataTug product
// code — chatshell.Model owns chatPanePercent/growPanelChat itself now,
// unexported, with its own test coverage in strongo/aichat). Kept here,
// unchanged, against the legacy UI's own chatPaneWidth/chatPanePercent,
// since that's still real, exercised code as long as ui.go exists.
func TestSplitDividerResizesWithinUsefulBounds(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	_, _ = u.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	initial := u.chatPaneWidth()
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl})
	if u.chatPaneWidth() <= initial || u.workspacePaneWidth() < 23 {
		t.Fatalf("right resize: chat=%d workspace=%d", u.chatPaneWidth(), u.workspacePaneWidth())
	}
	for range 20 {
		_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl})
	}
	if u.chatPanePercent != 40 || u.chatPaneWidth() < 42 {
		t.Fatalf("left resize exceeded minimum: percent=%d width=%d", u.chatPanePercent, u.chatPaneWidth())
	}
}

// TestDockedGridSortAndCellSelectionUseSourceCoordinates and
// TestDockGridHasNoViewSwitcher were ported onto ChatUI's workspacePanel in
// chatui_sidepanel_test.go.
