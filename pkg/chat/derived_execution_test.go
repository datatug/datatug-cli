package chat

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func derivedFixture(t *testing.T) (*SessionChat, RecordSet) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "six.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec("CREATE TABLE Item (Id INTEGER PRIMARY KEY, Category TEXT, Value INTEGER); INSERT INTO Item VALUES (1,'A',10),(2,'A',20),(3,'B',30),(4,'B',40),(5,'B',50),(6,'B',60)"); err != nil {
		t.Fatal(err)
	}
	source := "sqlite://" + path
	scope := testScope()
	scope.Sources[scope.Database] = source
	store := openTestStore(t, testStorePath(t), scope)
	catalog := ProjectCatalog{ID: scope.ProjectID, Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Item", SourceID: scope.Database, Title: "Item"}, Columns: []string{"Id", "Category", "Value"}, ColumnTypes: map[string]string{"Id": "INTEGER", "Category": "TEXT", "Value": "INTEGER"}, PrimaryKey: []string{"Id"}}}}
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, source, catalog)
	if err != nil {
		t.Fatal(err)
	}
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	sessions.ConfigureQueryExecutor(executor)
	document := "from: {schema: main, name: Item}\ncolumns: [{field: Id}, {field: Category}, {field: Value}]\nwhere: {op: '>', left: {field: Id}, right: {param: minimum}}\norderBy: [{field: Id}]\nlimit: 2\n"
	parameters := map[string]any{"minimum": 0}
	result, err := executor.RunTugQL(ctx, source, []byte(document), parameters)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := store.AppendUser(ctx, sessions.activeID, "Load two")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, sessions.activeID, origin.ID, source, QueryResult{Title: "Item", SourceID: scope.Database, TugQL: document, Parameters: parameters, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return sessions, snapshot.RecordSets[query.RecordSetID]
}

func TestUnifiedWorkspaceDerivedServerScopesExecuteExactPreviewAndLineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sessions, parent := derivedFixture(t)
	group := derivedOperation{Group: true, Columns: []string{"Category"}, Count: true, Scope: queryops.MatchingSource}
	preview, err := deriveResultPreview(sessions.catalog, parent, group)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, group, preview.TugQL+"\n"); err == nil {
		t.Fatal("unreviewed query ran")
	}
	snapshot, query, err := sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, group, preview.TugQL)
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Result.Rows) != 2 || FormatValue(query.Result.Rows[0].Data["count"]) != "2" || FormatValue(query.Result.Rows[1].Data["count"]) != "4" || FormatValue(query.Parameters["minimum"]) != "0" || query.TugQL != preview.TugQL || query.Lineage == nil || query.Lineage.ParentRecordSetID != parent.ID || len(snapshot.RecordSets[parent.ID].Result.Rows) != 2 {
		t.Fatalf("matching scope: %+v", query)
	}
	// Two output groups count all six filtered source rows, while the local
	// original capture contains only the two A rows.
	sortOp := derivedOperation{Sort: []queryops.SortTerm{{Column: "Value", Descending: true}}, Scope: queryops.MatchingSource}
	preview, err = deriveResultPreview(sessions.catalog, parent, sortOp)
	if err != nil {
		t.Fatal(err)
	}
	_, highest, err := sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, sortOp, preview.TugQL)
	if err != nil {
		t.Fatal(err)
	}
	if FormatValue(highest.Result.Rows[0].Data["Value"]) != "60" {
		t.Fatalf("matching source failed to reach outside loaded rows: %+v", highest.Result)
	}
	sortOp.Scope = queryops.OriginalOutput
	preview, err = deriveResultPreview(sessions.catalog, parent, sortOp)
	if err != nil {
		t.Fatal(err)
	}
	_, bounded, err := sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, sortOp, preview.TugQL)
	if err != nil {
		t.Fatal(err)
	}
	if FormatValue(bounded.Result.Rows[0].Data["Value"]) != "20" {
		t.Fatalf("original output escaped inner limit: %+v", bounded.Result)
	}
	group.Scope = queryops.OriginalOutput
	preview, err = deriveResultPreview(sessions.catalog, parent, group)
	if err != nil {
		t.Fatal(err)
	}
	_, bounded, err = sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, group, preview.TugQL)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded.Result.Rows) != 1 || FormatValue(bounded.Result.Rows[0].Data["count"]) != "2" {
		t.Fatalf("original grouping escaped input: %+v", bounded.Result)
	}
	unsafe := parent
	unsafe.TugQL = strings.Replace(parent.TugQL, "orderBy: [{field: Id}]\n", "", 1)
	if _, err = deriveResultPreview(sessions.catalog, unsafe, sortOp); err == nil {
		t.Fatal("nondeterministic limited OriginalOutput was enabled")
	}
	sessions.ConfigureQueryExecutor(secureread.NewExecutor(secureread.Session{}))
	group.Scope = queryops.MatchingSource
	preview, err = deriveResultPreview(sessions.catalog, parent, group)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := sessions.Snapshot(ctx)
	if _, _, err = sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, group, preview.TugQL); err == nil {
		t.Fatal("unconfigured policy executor bypassed")
	}
	after, _ := sessions.Snapshot(ctx)
	if len(before.RecordSets) != len(after.RecordSets) {
		t.Fatal("denied execution persisted a result")
	}
}

func TestUnifiedWorkspaceDerivedDialogReviewCancelAndExecuteThroughShell(t *testing.T) {
	t.Parallel()
	sessions, parent := derivedFixture(t)
	u, err := NewSessionChatUI(context.Background(), sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	u.shell.FocusEntry(u.lastGridEntryID)
	uxSearchAction(t, u, "Sort loaded")
	u.shell.Update(uitest.Text("3"))
	u.shell.Update(uitest.Text("d"))
	// D edits the highlighted column; select Value explicitly and move its cursor.
	u.shell.Update(uitest.Key("down"))
	u.shell.Update(uitest.Key("down"))
	u.shell.Update(uitest.Text("d"))
	_, cmd := u.shell.Update(uitest.Key("shift+enter"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "Exact TugQL submitted") || !strings.Contains(view, "OFFSET") || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("review was skipped: %s", view)
	}
	_, cmd = u.shell.Update(uitest.Key("esc"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Selected:") || len(u.snapshot.RecordSets) != 1 {
		t.Fatal("preview cancel lost chooser or executed")
	}
	_, cmd = u.shell.Update(uitest.Key("shift+enter"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	_, cmd = u.shell.Update(uitest.Key("shift+enter"))
	if cmd == nil {
		t.Fatal("reviewed execution missing")
	}
	drainCmd(t, u, cmd)
	_, record, ok := u.activeGrid()
	if !ok || record.ID == parent.ID || record.Lineage == nil || record.Lineage.ParentRecordSetID != parent.ID || len(u.snapshot.RecordSets) != 2 {
		t.Fatal("server result did not preserve immutable parent lineage")
	}
	workspaceRoutingKey(u, "alt+left")
	if u.activeRecordSetID() != parent.ID {
		t.Fatal("derived result lost return to origin")
	}
}
