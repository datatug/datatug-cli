package chat

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
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

// TestBookmarkWorkspaceTabOpensStructuredGrid and
// TestEmptyBookmarkedGridNavigation were ported onto ChatUI's workspacePanel
// in chatui_sidepanel_test.go.

func TestAgentBookmarkActionsAndDiscoveryUseDataTug(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.LatestOrCreate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolWorkspaceAction, map[string]any{"kind": "bookmark_create", "reference": map[string]any{"kind": "recordset", "objectId": recordID, "title": "Customers"}, "title": "Saved customers"})}},
		{text: "Saved."},
		{toolCalls: []ai.ToolCall{toolCall("2", toolWorkspaceAction, map[string]any{"kind": "bookmark_add_tag", "tag": "incident"})}},
		{text: "Tagged."},
		{toolCalls: []ai.ToolCall{toolCall("3", toolFindBookmarks, map[string]any{"tags": []string{"incident"}})}},
		{text: "Found the saved customers bookmark."},
	}}
	agent, err := NewAIConversation(llm, &fakeExecutor{result: secureread.Result{}}, "sqlite:///chinook.db", "- Customer: CustomerId, City")
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
		if strings.Contains(fmt.Sprint(request.Messages), "CustomerId=5") {
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
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolFindBookmarks, map[string]any{})}},
		{toolCalls: []ai.ToolCall{toolCall("2", toolRunDTQL, map[string]any{
			"sourceId": "private", "title": "Invoices", "dtql": "from: {name: Invoice}\nwhere:\n  op: In\n  left: {field: CustomerId}\n  right: {param: selection_1_c1}\nlimit: 20",
		})}},
		{text: "Done."},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId"}}}
	agent, err := NewAIConversation(llm, executor, scope.Sources[scope.Database], "- Invoice: InvoiceId, CustomerId", WithSources(scope.Sources))
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
		payload := fmt.Sprint(request.Messages)
		if strings.Contains(payload, "private-cell-value") || strings.Contains(payload, "token=secret") || strings.Contains(payload, "sqlite:///") || strings.Contains(payload, "CustomerId=5") {
			t.Fatalf("private bookmark data leaked into model request: %s", payload)
		}
	}
}
