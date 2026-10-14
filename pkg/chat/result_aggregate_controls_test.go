package chat

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestUnifiedWorkspaceAutomaticAggregatesUseAuthoritativeKeysAndExactLoadedValues(t *testing.T) {
	t.Parallel()
	names := []string{"RowKey", "CustomerId", "Quantity", "Amount", "Total", "Category"}
	document := "from: {schema: main, name: Item}\ncolumns: [{field: RowKey}, {field: CustomerId}, {field: Quantity}, {field: Amount}, {field: Total}, {field: Category}]\nlimit: 2\n"
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Items", DTQL: document, Result: secureread.Result{Columns: names, Rows: []secureread.Row{
		{Data: map[string]any{"RowKey": 1, "CustomerId": 10, "Quantity": int64(9007199254740993), "Amount": json.Number("0.1"), "Total": 19.8, "Category": "A"}},
		{Data: map[string]any{"RowKey": 2, "CustomerId": 10, "Quantity": int64(2), "Amount": json.Number("0.2"), "Total": 2.5, "Category": "A"}},
	}}})
	_, record, _ := u.activeGrid()
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", SourceID: record.Database, ObjectID: "main.Item"}, Columns: names, PrimaryKey: []string{"RowKey"}, ColumnTypes: map[string]string{"RowKey": "INTEGER", "CustomerId": "INTEGER", "Quantity": "BIGINT", "Amount": "DECIMAL(10,2)", "Total": "REAL", "Category": "TEXT"}}}}
	u.sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: record.Source, Snapshot: ForeignKeySnapshot{Source: record.Source, Columns: map[string][]string{"main.item": names}, Keys: []ForeignKey{{Schema: "main", FromRelation: "Item", FromFields: []string{"CustomerId"}}}}})
	operation, err := u.newResultOperation(true)
	if err != nil {
		t.Fatal(err)
	}
	if !operation.count || operation.defaultSumEligible(0) || operation.defaultSumEligible(1) || !operation.defaultSumEligible(2) || !operation.defaultSumEligible(3) || !operation.defaultSumEligible(4) {
		t.Fatal("automatic aggregates ignored authoritative key/type metadata")
	}
	u.shell.PushOverlay(operation)
	u.shell.Update(uitest.Key("6"))
	if terms := operation.aggregateTerms(); len(terms) != 3 {
		t.Fatalf("default sums: %#v", terms)
	}
	u.shell.Update(uitest.Key("enter"))
	panel, ok := u.shell.CenterPanel().(*localGroupsPanel)
	if !ok {
		t.Fatalf("loaded group failed: %s", u.shell.View().Content)
	}
	if panel.model.Cell(0, 1) != "2" || panel.model.Cell(0, 2) != "9007199254740995" || panel.model.Cell(0, 3) != "0.3" || panel.model.Cell(0, 4) != "22.3" {
		t.Fatalf("exact defaults: %s", panel.model.View(100, true))
	}
	_, cmd := u.shell.Update(uitest.Key("esc"))
	drainCmd(t, u, cmd)
	operation, err = u.newResultOperation(true)
	if err != nil {
		t.Fatal(err)
	}
	operation.index = 2
	for _, step := range []struct{ key, want string }{{"s", "SUM"}, {"c", "COUNT"}, {"m", "MIN"}, {"m", "MAX"}} {
		operation.Update(uitest.Key(step.key))
		if got := operation.aggregateFunction(2); got != step.want {
			t.Fatalf("row %s: %s", step.key, got)
		}
	}
	operation.toggle(2)
	if operation.aggregateFunction(2) != "" {
		t.Fatal("grouping key retained aggregate")
	}
	operation.toggle(2)
	if operation.aggregateFunction(2) != "MAX" {
		t.Fatal("changing grouping keys lost deliberate valid override")
	}
	operation.index = 5
	operation.Update(uitest.Key("s"))
	if !strings.Contains(operation.err, "numeric type") {
		t.Fatal("text SUM did not explain refusal")
	}
	operation.Update(uitest.Key("c"))
	if operation.aggregateFunction(5) != "COUNT" {
		t.Fatal("text COUNT not available")
	}
	operation.Update(uitest.Key("a"))
	if !strings.Contains(operation.err, "AVG") {
		t.Fatal("AVG did not explain unsupported precision")
	}
	operation.focus = 2
	operation.Update(uitest.Key("s"))
	if !strings.Contains(operation.err, "SUM(*)") {
		t.Fatal("COUNT(*) row accepted SUM silently")
	}
	operation.Update(uitest.Key("shift+tab"))
	operation.Update(uitest.Text("scm"))
	if operation.filter.Value() != "scm" || operation.aggregateFunction(2) != "MAX" {
		t.Fatal("filter literals invoked row commands")
	}
}

func TestUnifiedWorkspaceUnknownForeignKeysDoNotInferIntegerSums(t *testing.T) {
	t.Parallel()
	sessions, _ := derivedFixture(t)
	u, err := NewSessionChatUI(t.Context(), sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	u.shell.FocusEntry(u.lastGridEntryID)
	operation, err := u.newResultOperation(true)
	if err != nil {
		t.Fatal(err)
	}
	if operation.defaultSumEligible(2) {
		t.Fatal("unknown foreign keys treated as known non-FK")
	}
	operation.index = 2
	operation.Update(uitest.Key("s"))
	if operation.aggregateFunction(2) != "SUM" {
		t.Fatal("explicit numeric override unavailable")
	}
}

func TestUnifiedWorkspaceDerivedTypesSurviveReloadWithoutInventingEntityIdentity(t *testing.T) {
	t.Parallel()
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Grouped invoices", DTQL: "from: {schema: main, name: Invoice}\ncolumns: [{field: CustomerId}, {aggregate: {function: sum, args: [{field: Total}]}, as: sum_Total}]\ngroupBy: [{field: CustomerId}]\n", Result: secureread.Result{
		Columns: []string{"CustomerId", "sum_Total"}, ColumnTypes: map[string]string{"CustomerId": "INTEGER", "sum_Total": "DECIMAL"},
		Rows: []secureread.Row{{Data: map[string]any{"CustomerId": int64(29), "sum_Total": json.Number("0.3")}}},
	}})
	g, record, _ := u.activeGrid()
	if record.Result.ColumnTypes["sum_Total"] != "DECIMAL" || u.columnMeta(record, "sum_Total").qualified != "" || u.columnMeta(record, "sum_Total").dbType != "DECIMAL" {
		t.Fatal("durable derived type was lost or invented a physical source")
	}
	if !groupedRecord(record) || !strings.Contains(u.inspectionIdentity(g, record), "identity unavailable") {
		t.Fatal("grouped output pretended to identify an entity")
	}
	for _, hint := range u.workspaceHints(150) {
		if strings.Contains(hint, "F10") || strings.Contains(hint, "JOIN") {
			t.Fatalf("unavailable entity action advertised: %s", hint)
		}
	}
	u.dispatchWorkspaceAction("join")
	if u.shell.ActiveOverlayHints() != nil || !strings.Contains(u.workspaceFeedback, "no entity identity") {
		t.Fatal("grouped JOIN opened a misleading picker")
	}
	_, cmd := u.shell.Update(uitest.Key("f10"))
	if cmd != nil || !strings.Contains(u.workspace.relatedView(30), "Related unavailable") {
		t.Fatal("grouped Related started an entity read")
	}
}

func TestUnifiedWorkspaceGroupedGridDoesNotRenderSourceEntityJoinHints(t *testing.T) {
	t.Parallel()
	query := QueryResult{Title: "Invoices", DTQL: "from: {schema: main, name: Invoice}\ncolumns: [{field: CustomerId}, {field: Total}]\nlimit: 5\n", Result: secureread.Result{
		Columns: []string{"CustomerId", "Total"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": int64(1), "Total": 27.29}}},
	}}
	u := focusedGridStoreBackedTestUI(t, query)
	u.sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}})
	u.loadSession(u.snapshot)
	u.shell.FocusEntry(u.lastGridEntryID)
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "Ctrl+P Actions") || !strings.Contains(view, "Customer") {
		t.Fatalf("fixture does not render source relationships: %s", view)
	}
	query.Title = "Group: Invoices"
	query.DTQL = "from: {schema: main, name: Invoice}\ncolumns: [{field: CustomerId}, {aggregate: {function: sum, args: [{field: Total}]}, as: sum_Total}]\ngroupBy: [{field: CustomerId}]\n"
	query.Result.Columns = []string{"CustomerId", "sum_Total"}
	query.Result.Rows[0].Data = map[string]any{"CustomerId": int64(1), "sum_Total": 27.29}
	_, parent, _ := u.activeGrid()
	query, err := u.sessions.store.AppendQuery(t.Context(), u.sessionID, parent.OriginMessageID, "sqlite:///fixture.db", query)
	if err != nil {
		t.Fatal(err)
	}
	// The live append precedes refreshing the UI's durable snapshot.
	u.appendGridResult(query)
	for _, reload := range []bool{false, true} {
		if reload {
			snapshot, err := u.sessions.store.Load(t.Context(), u.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			u.loadSession(snapshot)
		}
		u.shell.FocusEntry(u.lastGridEntryID)
		u.shell.ExpandSelected()
		view := flattenView(u.shell.View().Content)
		if strings.Contains(view, "You can JOIN") || strings.Contains(view, "Invoice: Customer") {
			t.Fatalf("reload=%v grouped grid advertised source identity: %s", reload, view)
		}
		if !strings.Contains(view, "sum_Total") || !strings.Contains(view, "27.29") {
			t.Fatalf("reload=%v grouped result lost its data: %s", reload, view)
		}
		if _, advertised := u.joinBlocksByRecordSetID[query.RecordSetID]; advertised {
			t.Fatalf("reload=%v grouped result retained an inline relationship owner", reload)
		}
	}
}
