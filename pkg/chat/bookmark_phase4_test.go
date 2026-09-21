package chat

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestBookmarkCrossSessionContextRestartAndDeletion(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	catalog := ProjectCatalog{ID: testScope().ProjectID, Title: "Demo"}
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, origin.ID)
	selected, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Column: "City", Equals: "Prague", Limit: 2, Title: "Prague customers"})
	if err != nil {
		t.Fatal(err)
	}
	bookmark, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: selected, Title: "Top Prague customers"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_add_tag", BookmarkID: bookmark.ObjectID, Tag: " Incident "}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_add_tag", BookmarkID: bookmark.ObjectID, Tag: "Prague"}); err != nil {
		t.Fatal(err)
	}
	items, err := chat.FindBookmarks(ctx, "top prague", []string{"incident", "PRAGUE"})
	if err != nil || len(items) != 1 || items[0].ID != bookmark.ObjectID {
		t.Fatalf("AND tag search = %+v, %v", items, err)
	}
	other, err := chat.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: bookmark}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: bookmark}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, origin.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := chat.Snapshot(ctx)
	if err != nil || snapshot.ID != other.ID || len(snapshot.RecordSets) != 0 || len(snapshot.Bookmarks) != 1 {
		t.Fatalf("bookmark did not survive original session deletion: %+v, %v", snapshot, err)
	}
	if got := selectionParameters(snapshot)["selection_1_c1"]; !reflect.DeepEqual(got, []any{int64(5), int64(6)}) {
		t.Fatalf("bound bookmark IDs = %#v", got)
	}
	modelContext := buildSessionContext(snapshot, catalog)
	if !strings.Contains(modelContext, "selection_1_c1") || strings.Contains(modelContext, "sqlite:///") || strings.Contains(modelContext, "Top Prague customers") {
		t.Fatalf("unsafe/missing model context: %s", modelContext)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path, testScope())
	chat, err = NewSessionChat(ctx, reopened, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := chat.Snapshot(ctx)
	if err != nil || len(restored.Bookmarks) != 1 || len(restored.Workspace.Docks) != 1 {
		t.Fatalf("bookmark/dock restart = %+v, %v", restored, err)
	}
	grid, ok := gridDataForReference(restored, bookmark)
	if !ok || len(grid.Result.Rows) != 2 || grid.Result.Rows[0].Data["CustomerId"] != int64(5) {
		t.Fatalf("restored grid = %+v, %v", grid, ok)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_delete", BookmarkID: bookmark.ObjectID}); err == nil {
		t.Fatal("deleted an attached/docked bookmark")
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "detach", Reference: bookmark}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "undock", Reference: bookmark}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_delete", BookmarkID: bookmark.ObjectID}); err != nil {
		t.Fatal(err)
	}
	items, err = chat.FindBookmarks(ctx, "", nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("deleted bookmark remains: %+v, %v", items, err)
	}
}

func TestBookmarkWorkspaceTabOpensStructuredGrid(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	catalog := ProjectCatalog{ID: testScope().ProjectID, Title: "Demo"}
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chat.Snapshot(ctx)
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}, Title: "Saved customers"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, chat, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.focusWorkspace()
	u.setWorkspaceTab(3)
	view := u.workspaceView(80, 30)
	if len(u.bookmarkItems) != 1 || !strings.Contains(view, "Saved customers") || !strings.Contains(view, "Source: chinook") || !strings.Contains(view, "snapshot:") || !strings.Contains(view, "DTQL:") {
		t.Fatalf("bookmark tab did not render: %+v", u.bookmarkItems)
	}
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !u.bookmarkGridFocused || u.bookmarkGrid == nil || len(u.bookmarkGrid.model.Rows) != 3 {
		t.Fatalf("bookmark grid did not open: %+v", u.bookmarkGrid)
	}
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	snapshot, err := chat.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Attachments) != 1 || !sameReference(snapshot.Workspace.Attachments[0], ref) {
		t.Fatalf("UI attachment did not use shared action: %+v, %v", snapshot.Workspace.Attachments, err)
	}
}

func TestEmptyBookmarkedGridNavigation(t *testing.T) {
	ctx := context.Background()
	scope := testScope()
	store := openTestStore(t, testStorePath(t), scope)
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, scope.Sources[scope.Database], ProjectCatalog{ID: scope.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show missing rows")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, scope.Sources[scope.Database], Turn{Queries: []QueryResult{{
		Title: "Empty", DTQL: "from: {name: Customer}\nlimit: 1", SourceID: scope.Database,
		Result: secureread.Result{Columns: []string{"CustomerId"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: turn.Queries[0].RecordSetID}}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, chat, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.focusWorkspace()
	u.setWorkspaceTab(3)
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyDown})
	u.updateWorkspaceKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if u.bookmarkGrid == nil || u.bookmarkGrid.rowIndex < 0 {
		t.Fatalf("empty bookmark grid navigation = %+v", u.bookmarkGrid)
	}
}

func TestAgentBookmarkActionsAndDiscoveryUseDataTug(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.LatestOrCreate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("workspace_action", map[string]any{"kind": "bookmark_create", "reference": map[string]any{"kind": "recordset", "objectId": recordID, "title": "Customers"}, "title": "Saved customers"}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Saved.", genai.RoleModel)},
		{Content: genai.NewContentFromFunctionCall("workspace_action", map[string]any{"kind": "bookmark_add_tag", "tag": "incident"}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Tagged.", genai.RoleModel)},
		{Content: genai.NewContentFromFunctionCall("find_bookmarks", map[string]any{"tags": []string{"incident"}}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Found the saved customers bookmark.", genai.RoleModel)},
	}}
	agent, err := NewADKConversation(llm, &fakeExecutor{result: secureread.Result{}}, "sqlite:///chinook.db", "- Customer: CustomerId, City")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db", ProjectCatalog{ID: testScope().ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if turn, err := chat.Ask(ctx, "Bookmark these customers"); err != nil || len(turn.Actions) != 1 || turn.Actions[0].Err != nil {
		t.Fatalf("agent bookmark create = %+v, %v", turn, err)
	}
	if turn, err := chat.Ask(ctx, "Add tag incident"); err != nil || len(turn.Actions) != 1 || turn.Actions[0].Err != nil {
		t.Fatalf("agent bookmark tag = %+v, %v", turn, err)
	}
	turn, err := chat.Ask(ctx, "Show my bookmarks tagged incident")
	if err != nil || !strings.Contains(turn.Text, "saved customers") {
		t.Fatalf("agent bookmark discovery = %+v, %v", turn, err)
	}
	items, err := chat.FindBookmarks(ctx, "", []string{"incident"})
	if err != nil || len(items) != 1 || items[0].ID == "" {
		t.Fatalf("DataTug bookmark state = %+v, %v", items, err)
	}
	if len(llm.requests) < 6 {
		t.Fatalf("model did not receive structured tool results: %d requests", len(llm.requests))
	}
	for _, request := range llm.requests {
		if strings.Contains(fmt.Sprint(request.Contents), "CustomerId=5") {
			t.Fatal("raw row values leaked to the model")
		}
	}
}

func TestImplicitBookmarkTargetExpiresAfterInterveningTurn(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{turns: []Turn{{Text: "Hello."}}}, "sqlite:///chinook.db", ProjectCatalog{ID: testScope().ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chat.Snapshot(ctx)
	recordID := workspaceTestRecord(t, store, session.ID)
	created, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_delete"}); err == nil {
		t.Fatal("stale implicit bookmark target was deleted")
	}
	items, err := chat.FindBookmarks(ctx, "", nil)
	if err != nil || len(items) != 1 || items[0].ID != created.ObjectID {
		t.Fatalf("bookmark changed after stale action: %+v, %v", items, err)
	}
}

func TestBookmarkUsesActualSafeSourceIDAcrossSessions(t *testing.T) {
	ctx := context.Background()
	scope := testScope()
	scope.Sources["alias"] = scope.Sources["private"] // same URL must not change the source identity
	store := openTestStore(t, testStorePath(t), scope)
	origin, target := bookmarkableSelection(t, store)
	bookmark, err := store.CreateBookmark(ctx, origin.ID, target, "private-cell-value")
	if err != nil {
		t.Fatal(err)
	}
	if bookmark.SourceID != "private" {
		t.Fatalf("source ID became ambiguous: %q", bookmark.SourceID)
	}
	if _, err := store.AddBookmarkTag(ctx, bookmark.ID, "private-cell-value"); err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", ProjectCatalog{ID: scope.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Create(ctx); err != nil {
		t.Fatal(err)
	}
	wrongProject := bookmarkReference(bookmark)
	wrongProject.ProjectID = "other-project"
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: wrongProject}); err == nil {
		t.Fatal("cross-project bookmark reference was accepted")
	}
	wrongSource := bookmarkReference(bookmark)
	wrongSource.SourceID = "chinook"
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: wrongSource}); err == nil {
		t.Fatal("wrong-source bookmark reference was accepted")
	}
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: bookmarkReference(bookmark)}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	modelContext := buildSessionContext(snapshot, ProjectCatalog{ID: scope.ProjectID})
	if !strings.Contains(modelContext, "source=private") || strings.Contains(modelContext, "private-cell-value") || strings.Contains(modelContext, "sqlite:///") || strings.Contains(modelContext, "token=secret") {
		t.Fatalf("source or private metadata leaked: %s", modelContext)
	}
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("find_bookmarks", map[string]any{}, genai.RoleModel)},
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{
			"sourceId": "private", "title": "Invoices", "dtql": "from: {name: Invoice}\nwhere:\n  op: In\n  left: {field: CustomerId}\n  right: {param: selection_1_c1}\nlimit: 20",
		}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Done.", genai.RoleModel)},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId"}}}
	agent, err := NewADKConversation(llm, executor, scope.Sources[scope.Database], "- Invoice: InvoiceId, CustomerId", WithSources(scope.Sources))
	if err != nil {
		t.Fatal(err)
	}
	chat, err = NewSessionChat(ctx, store, agent, scope.Sources[scope.Database], ProjectCatalog{ID: scope.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if turn, err := chat.Ask(ctx, "Show invoices for the attached customers"); err != nil || len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("bookmark follow-up = %+v, %v", turn, err)
	}
	if executor.source != scope.Sources["private"] || !reflect.DeepEqual(executor.params["selection_1_c1"], []any{int64(1)}) {
		t.Fatalf("wrong local source/binding: source=%q params=%#v", executor.source, executor.params)
	}
	for _, request := range llm.requests {
		payload := fmt.Sprint(request.Contents)
		if strings.Contains(payload, "private-cell-value") || strings.Contains(payload, "token=secret") || strings.Contains(payload, "sqlite:///") || strings.Contains(payload, "CustomerId=5") {
			t.Fatalf("private bookmark data leaked into model request: %s", payload)
		}
	}
}
