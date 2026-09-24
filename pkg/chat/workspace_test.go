package chat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func workspaceTestCatalog() ProjectCatalog {
	return ProjectCatalog{ID: "chinook", Title: "Chinook", Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "project", ProjectID: "chinook", ObjectID: "chinook", Title: "Chinook"}},
		{Reference: ContextReference{Kind: "source", ProjectID: "chinook", SourceID: "chinook-local", ObjectID: "chinook-local", Title: "Chinook local"}},
		{Reference: ContextReference{Kind: "table", ProjectID: "chinook", SourceID: "chinook-local", ObjectID: "main.Customer", Title: "Customer"}, Columns: []string{"CustomerId", "City"}},
	}}
}

func TestAttachedTableContextSurvivesHistoryAndIncludesColumnDefinitions(t *testing.T) {
	catalog := workspaceTestCatalog()
	catalog.Objects[2].ColumnTypes = map[string]string{"CustomerId": "INTEGER", "City": "NVARCHAR(40)"}
	attached := catalog.Objects[2].Reference
	session := ChatSession{Workspace: WorkspaceState{Attachments: []ContextReference{attached}}}
	for range 20 {
		session.Messages = append(session.Messages, ChatMessage{Role: "You", Kind: "text", Text: strings.Repeat("Invoice ", 250)})
	}
	contextText := buildSessionContext(session, catalog)
	if !strings.Contains(contextText, "Attached table Customer") || !strings.Contains(contextText, "CustomerId INTEGER") || !strings.Contains(contextText, "City NVARCHAR(40)") {
		t.Fatalf("attached table definition was lost: %s", contextText)
	}
	if len(contextText) > maxContextChars {
		t.Fatalf("context exceeded limit: %d", len(contextText))
	}
}

func TestComposerAttachmentChipsCanBeFocusedClearedAndRestored(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	ref := workspaceTestCatalog().Objects[2].Reference
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, chat, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.input.SetValue("Top 5 rows")
	if !strings.Contains(u.composerView(80), "Customer") {
		t.Fatal("attached Customer chip is not inside composer card")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if u.attachmentFocus != 0 {
		t.Fatalf("Tab did not focus attachment chip: %d", u.attachmentFocus)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if u.input.Value() != "" || len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("first Esc should clear text only: %q, %+v", u.input.Value(), u.snapshot.Workspace.Attachments)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("second Esc should clear attachments: %+v", u.snapshot.Workspace.Attachments)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if u.input.Value() != "Top 5 rows" || len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("Shift+Esc did not restore draft and attachments: %q, %+v", u.input.Value(), u.snapshot.Workspace.Attachments)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatal("Backspace did not remove focused chip")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if len(u.snapshot.Workspace.Attachments) != 1 || u.input.Value() != "Top 5 rows" {
		t.Fatal("Shift+Esc did not restore chip removed with Backspace")
	}
	_, _ = u.Update(tea.MouseClickMsg{X: responsiveGutter(u.width) + 2 + len("Customer") + 2, Y: u.historyHeight() + 3, Button: tea.MouseLeft})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatal("clicking the visible × did not remove the chip")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatal("Shift+Esc did not restore chip removed with mouse")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatal("Ctrl+D did not remove last attachment")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatal("Shift+Esc did not restore chip removed with Ctrl+D")
	}
}

func TestProjectExplorerShowsSourceIssueInPlace(t *testing.T) {
	u := NewUI(context.Background(), nil, "test-model")
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects[1].Issue = "Schema unavailable: relation main.Customer has no columns file"
	nodes := u.explorerNodes()
	found := false
	for i, node := range nodes {
		if node.id == "source:chinook-local" {
			u.explorerIndex = i
		}
		if node.issue && node.id == "source:chinook-local:issue" && node.objectIndex == -1 && strings.Contains(node.label, "main.Customer") {
			found = true
		}
	}
	if !found {
		t.Fatalf("source-local error node missing: %+v", nodes)
	}
	view := u.workspaceView(100, 20)
	if !strings.Contains(view, "Status: Schema unavailable") || !strings.Contains(view, "Customer") {
		t.Fatalf("source details did not show the schema error: %q", view)
	}
	u.explorerCollapsed["source:chinook-local"] = true
	found = false
	for _, node := range u.explorerNodes() {
		if node.id == "source:chinook-local" && node.issue && strings.Contains(node.label, "⚠") {
			found = true
		}
		if node.id == "source:chinook-local:issue" {
			t.Fatal("collapsed source still shows its error child")
		}
	}
	if !found {
		t.Fatal("collapsed source lost its warning indicator")
	}
}

func TestProjectExplorerIssueDetailsRemainVisibleInLongTree(t *testing.T) {
	u := NewUI(context.Background(), nil, "test-model")
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects[1].Issue = "Schema unavailable: relation main.Customer has no columns file"
	for i := range 30 {
		object := ProjectObject{Reference: ContextReference{
			Kind: "source", SourceID: fmt.Sprintf("extra-%02d", i), ObjectID: fmt.Sprintf("extra-%02d", i), Title: "Extra source",
		}}
		if i == 20 {
			object.Issue = "Schema unavailable: deep source failed"
		}
		u.catalog.Objects = append(u.catalog.Objects, object)
	}
	for i, node := range u.explorerNodes() {
		if node.id == "source:chinook-local:issue" {
			u.explorerIndex = i
			break
		}
	}
	view := u.workspaceView(80, 12)
	if !strings.Contains(view, "Status: Schema unavailable") {
		t.Fatalf("selected issue details hidden below long explorer: %q", view)
	}
	for i, node := range u.explorerNodes() {
		if node.id == "source:extra-20:issue" {
			u.explorerIndex = i
			break
		}
	}
	view = u.workspaceView(80, 12)
	if !strings.Contains(view, "Status: Schema unavailable: deep source failed") {
		t.Fatalf("deep selected issue details hidden below viewport: %q", view)
	}
}

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
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("workspace_action", map[string]any{"kind": "select", "recordSetId": recordID, "column": "City", "equals": "Prague", "limit": 2, "columns": []string{"CustomerId"}}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Selected.", genai.RoleModel)},
		{Content: genai.NewContentFromFunctionCall("workspace_action", map[string]any{"kind": "dock"}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Docked.", genai.RoleModel)},
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc, "title": "Largest selected orders"}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Here are their invoices.", genai.RoleModel)},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId", "CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 404, "CustomerId": 6}}}}}
	agent, err := NewADKConversation(llm, executor, "sqlite:///chinook.db", "- Customer: CustomerId, City\n- Invoice: InvoiceId, CustomerId")
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
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)},
		{Content: genai.NewContentFromText("The query could not be completed.", genai.RoleModel)},
		{Content: genai.NewContentFromText("Ready to try again.", genai.RoleModel)},
	}}
	agent, err := NewADKConversation(llm, &fakeExecutor{err: errors.New("driver echoed selected value " + secret)}, "sqlite:///chinook.db", "- Customer: CustomerId, City")
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
		if text := fmt.Sprint(request.Contents); strings.Contains(text, secret) {
			t.Fatalf("selected value leaked into model request %d: %s", i+1, text)
		}
	}
}

func TestSelectedCellValueStaysOutOfModelRequest(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	llm := &scriptedLLM{responses: []*model.LLMResponse{{Content: genai.NewContentFromText("Ready.", genai.RoleModel)}}}
	agent, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///chinook.db", "- Customer: CustomerId, City")
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
	prompt := llm.requests[0].Contents[0].Parts[0].Text
	if strings.Contains(prompt, "Paris") {
		t.Fatalf("selected cell value leaked to model: %s", prompt)
	}
	if !strings.Contains(prompt, "selection_1_c1") {
		t.Fatalf("opaque local binding missing from model prompt: %s", prompt)
	}
}

func TestWorkspaceSplitAndKeyboardSelection(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chat.Snapshot(ctx)
	workspaceTestRecord(t, store, session.ID)
	u, err := NewSessionUI(ctx, chat, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = u.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	view := u.View().Content
	for _, want := range []string{"Project: Chinook", "● Project", "Inspect", "Docked", "Customer"} {
		if !strings.Contains(view, want) {
			t.Errorf("split view missing %q", want)
		}
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	for index, node := range u.explorerNodes() {
		if node.objectIndex >= 0 && u.catalog.Objects[node.objectIndex].Reference.Kind == "table" {
			u.explorerIndex = index
			break
		}
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(u.snapshot.Workspace.Attachments) != 1 || u.snapshot.Workspace.Attachments[0].Kind != "table" {
		t.Fatalf("project attachment = %+v", u.snapshot.Workspace.Attachments)
	}
	_, _ = u.Update(tea.MouseClickMsg{X: responsiveGutter(u.width) + 2 + len("Customer") + 2, Y: u.historyHeight() + 3, Button: tea.MouseLeft})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("clicking attachment close did not detach: %+v", u.snapshot.Workspace.Attachments)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeySpace}) // attach again
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !u.focusLatestGrid() {
		t.Fatal("grid missing")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(u.snapshot.Workspace.Selections) != 1 || u.snapshot.Workspace.CurrentSelectionID == "" {
		t.Fatalf("row selection = %+v", u.snapshot.Workspace)
	}
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("row selection changed attachments: %+v", u.snapshot.Workspace.Attachments)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeySpace}) // explicitly attach selected rows
	if len(u.snapshot.Workspace.Attachments) != 2 {
		t.Fatalf("selected rows were not attached: %+v", u.snapshot.Workspace.Attachments)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: 'd'})
	if len(u.snapshot.Workspace.Docks) != 1 {
		t.Fatalf("dock state = %+v", u.snapshot.Workspace.Docks)
	}
	if !strings.Contains(u.View().Content, "Inspect") || !strings.Contains(u.View().Content, "Docked") {
		t.Fatal("workspace disappeared after selection/dock")
	}
}

func TestExplorerGroupsObjectsByDeclaredSourceAndCollapses(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects = append(u.catalog.Objects,
		ProjectObject{Reference: ContextReference{Kind: "query", ProjectID: "chinook", SourceID: "chinook-local", ObjectID: "by-city", Title: "By city"}},
		ProjectObject{Reference: ContextReference{Kind: "query", ProjectID: "chinook", ObjectID: "unbound", Title: "Unbound"}},
	)
	nodes := u.explorerNodes()
	var sourceDepth, boundDepth, unboundDepth int
	for _, node := range nodes {
		switch node.label {
		case "Chinook local":
			sourceDepth = node.depth
		case "By city":
			boundDepth = node.depth
		case "Unbound":
			unboundDepth = node.depth
		}
	}
	if sourceDepth != 2 || boundDepth != 2 || unboundDepth != 2 {
		t.Fatalf("unexpected explorer hierarchy: %+v", nodes)
	}
	var databases, queryGroups int
	for _, node := range nodes {
		if node.label == "Databases (1)" {
			databases++
		}
		if strings.HasPrefix(node.label, "Queries (") {
			queryGroups++
		}
	}
	if databases != 1 || queryGroups != 1 {
		t.Fatalf("want one Databases and one Queries group: %+v", nodes)
	}
	u.explorerCollapsed["source:chinook-local"] = true
	for _, node := range u.explorerNodes() {
		if node.label == "Customer" {
			t.Fatalf("collapsed source still exposes child %q", node.label)
		}
	}
}

func TestComposerShrinksAsWrappedAttachmentsAreRemoved(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width, u.height = 62, 30
	u.catalog = workspaceTestCatalog()
	u.snapshot.Workspace.Attachments = []ContextReference{
		{Title: "First customer table"},
		{Title: "Second customer table"},
		{Title: "Third customer table"},
	}
	width := u.chatPaneWidth() - 1
	if got := len(u.attachmentRows(width)); got != 2 {
		t.Fatalf("attachment rows = %d, want 2", got)
	}
	initialHeight := u.historyHeight()
	if !strings.Contains(u.composerView(u.chatPaneWidth()), "Third customer table") {
		t.Fatal("wrapped attachment is not visible")
	}
	lastChip := u.attachmentRows(width)[1][0]
	if ref, ok := u.attachmentCloseAt(lastChip.x, 1); !ok || ref.Title != "Third customer table" {
		t.Fatalf("second-row close target = %+v, %v", ref, ok)
	}
	u.snapshot.Workspace.Attachments = u.snapshot.Workspace.Attachments[:2]
	if got := len(u.attachmentRows(width)); got != 1 {
		t.Fatalf("attachment rows after removing third chip = %d, want 1", got)
	}
	if got := u.historyHeight(); got != initialHeight+1 {
		t.Fatalf("history height after removing second-row chip = %d, want %d", got, initialHeight+1)
	}
	u.snapshot.Workspace.Attachments = nil
	if got := len(u.attachmentRows(width)); got != 0 {
		t.Fatalf("attachment rows after clearing chips = %d, want 0", got)
	}
	if got := u.historyHeight(); got != initialHeight+2 {
		t.Fatalf("history height after clearing chips = %d, want %d", got, initialHeight+2)
	}
}

func TestProjectCardUsesProjectTitle(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.catalog = workspaceTestCatalog()
	view := ansi.Strip(u.projectWorkspaceCards(50, 15))
	if !strings.Contains(view, "Project: "+u.catalog.Title) || strings.Contains(view, "Project explorer") {
		t.Fatalf("project card title is incorrect:\n%s", view)
	}
}

func TestExplorerSelectionShowsTableAndQueryCards(t *testing.T) {
	u := NewUI(context.Background(), nil, "test-model")
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects[2].ColumnTypes = map[string]string{"CustomerId": "INTEGER", "City": "TEXT"}
	queryText := "SELECT GenreName\n" + strings.Repeat("-- detail line\n", 12) + "FROM purchases"
	u.catalog.Objects = append(u.catalog.Objects, ProjectObject{Reference: ContextReference{
		Kind: "query", SourceID: "chinook-local", ObjectID: "purchases", Title: "Customer purchases by genre",
	}, QueryType: "SQL", QueryText: queryText})
	for index, node := range u.explorerNodes() {
		if node.label == "Customer" {
			u.explorerIndex = index
			break
		}
	}
	view := ansi.Strip(u.workspaceView(48, 24))
	if !strings.Contains(view, "Table: Customer") || !strings.Contains(view, "CustomerId") || !strings.Contains(view, "INTEGER") {
		t.Fatalf("table selection did not show columns card: %q", view)
	}
	for index, node := range u.explorerNodes() {
		if node.label == "Customer purchases by genre" {
			u.explorerIndex = index
			break
		}
	}
	view = ansi.Strip(u.workspaceView(48, 24))
	if !strings.Contains(view, "Query: Customer purchases by genre") || !strings.Contains(view, "SQL") || !strings.Contains(view, "SELECT GenreName") {
		t.Fatalf("query selection did not show query text: %q", view)
	}
	for range 3 {
		u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	view = ansi.Strip(u.workspaceView(48, 24))
	if !strings.Contains(view, "FROM purchases") {
		t.Fatalf("query detail cannot scroll to end of text: %q", view)
	}
}

func TestExplorerArrowsFoldTreeAndTabSwitchesWorkspace(t *testing.T) {
	u := NewUI(context.Background(), nil, "test-model")
	u.catalog = workspaceTestCatalog()
	u.workspaceFocused = true
	for index, node := range u.explorerNodes() {
		if node.id == "source:chinook-local" {
			u.explorerIndex = index
			break
		}
	}
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !u.explorerCollapsed["source:chinook-local"] || u.workspaceTab != 0 {
		t.Fatalf("Left should collapse source, not switch tab: collapsed=%v tab=%d", u.explorerCollapsed["source:chinook-local"], u.workspaceTab)
	}
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if u.explorerCollapsed["source:chinook-local"] || u.workspaceTab != 0 {
		t.Fatalf("Right should expand source, not switch tab: collapsed=%v tab=%d", u.explorerCollapsed["source:chinook-local"], u.workspaceTab)
	}
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if u.workspaceTab != 1 {
		t.Fatalf("Tab should switch workspace tab: %d", u.workspaceTab)
	}
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if u.workspaceTab != 0 {
		t.Fatalf("Shift+Tab should switch back: %d", u.workspaceTab)
	}
}

func TestProjectPickerSelectsConfiguredProject(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.catalog = workspaceTestCatalog()
	u.SetProjectChoices([]ProjectChoice{{Key: "/projects/chinook", Title: "Chinook"}, {Key: "sales", Title: "Sales"}})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if !u.projectPicker || !strings.Contains(u.View().Content, "Sales") {
		t.Fatal("project picker did not open")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := u.SelectedProject(); got != "sales" || cmd == nil {
		t.Fatalf("project switch = %q, quit command = %v", got, cmd)
	}
}

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

func TestDockedGridSortAndCellSelectionUseSourceCoordinates(t *testing.T) {
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
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0, 2}, Title: "Subset"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, chat, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = u.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	u.workspaceTab, u.workspaceFocused, u.dockGridFocused = 2, true, true
	dock := u.snapshot.Workspace.Docks[0]
	dockGrid := u.dockGrids[dock.ID]
	dockGrid.SelectColumn(0)
	_, _ = u.Update(tea.KeyPressMsg{Code: 's'}) // ascending
	_, _ = u.Update(tea.KeyPressMsg{Code: 's'}) // descending
	dockGrid = u.dockGrids[dock.ID]
	if got := []int{dockGrid.sourceIndexAt(0), dockGrid.sourceIndexAt(1)}; !reflect.DeepEqual(got, []int{2, 0}) {
		t.Fatalf("docked sorted source rows = %v", got)
	}
	dockGrid.SelectColumn(1)
	dockGrid.SelectRow(0) // City in source row 2
	_, _ = u.Update(tea.KeyPressMsg{Code: 'c'})
	saved, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selection := saved.Workspace.Selections[saved.Workspace.CurrentSelectionID]
	if !reflect.DeepEqual(selection.Rows, []int{2}) || !reflect.DeepEqual(selection.Columns, []string{"City"}) {
		t.Fatalf("docked cell selection = %+v", selection)
	}
	if !reflect.DeepEqual(selection.Ranges, []CellRange{{FirstRow: 2, LastRow: 2, FirstCol: 1, LastCol: 1}}) {
		t.Fatalf("docked cell coordinates = %+v", selection.Ranges)
	}
}

// TestDockGridHasNoViewSwitcher is the regression test for m9: a dock grid
// never had a view switcher in main (no Charts/Current-row views to jump
// to with "2"/"3") — it already shows a narrow, purpose-built row set.
func TestDockGridHasNoViewSwitcher(t *testing.T) {
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
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0, 2}, Title: "Subset"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, chat, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = u.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	dock := u.snapshot.Workspace.Docks[0]
	dockGrid := u.dockGrids[dock.ID]
	if got := dockGrid.ExtraViews(); len(got) != 0 {
		t.Fatalf("dock grid ExtraViews() = %+v, want none", got)
	}
	if header := ansi.Strip(dockGrid.HeaderLine(60)); strings.Contains(header, "2 Charts") || strings.Contains(header, "3 Current row") {
		t.Fatalf("dock grid header still advertises a view switcher: %q", header)
	}
}
