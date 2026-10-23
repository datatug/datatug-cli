package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

type fakeExactExecutor struct {
	fakeExecutor
}

func (fakeExactExecutor) RunExactAggregateDTQL(context.Context, string, []byte, []byte, map[string]any, []resultops.Column) (secureread.ExactAggregateResult, error) {
	return secureread.ExactAggregateResult{}, nil
}

// 1. unified_workspace.go:16 and related_open.go:46
// Not parallel: modifies validateBookmarkRefsFn
func TestCoverage100_FinalPush_UnifiedAndRelatedOpen(t *testing.T) {
	// unified_workspace.go:16 apiTick() lambda
	msg := apiTick()()
	if _, ok := msg.(apiTickMsg); !ok {
		t.Fatalf("expected apiTickMsg, got %T", msg)
	}

	// related_open.go:46 ctx.Err() after store.Load
	ctx := context.Background()
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///chinook.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	sessions := u.sessions
	rID := u.activeRecordSetID()
	validSource := sessions.store.info.Sources[sessions.store.info.Database]
	_, _ = sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSource, rID)

	cancelCtx, cancel := context.WithCancel(ctx)
	origValBM := validateBookmarkRefsFn
	validateBookmarkRefsFn = func(s *SessionStore, c context.Context, db bookmarkSQL, state WorkspaceState) error {
		cancel()
		return nil
	}
	defer func() { validateBookmarkRefsFn = origValBM }()

	item := relatedRecord{document: "SELECT 1", key: ForeignKey{Schema: "main", FromRelation: "users", ToSchema: "main", ToRelation: "orders"}}
	if _, _, err := sessions.acceptRelatedPreview(cancelCtx, sessions.activeID, rID, item); err == nil {
		t.Fatal("expected ctx error at line 46")
	}
}

// 2. result_columns_agent.go and result_columns.go
// Not parallel: mutates session store db for source error
func TestCoverage100_FinalPush_ResultColumnsAndAgent(t *testing.T) {
	c, parent, _, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
	ctx := context.Background()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	originID := snap.Messages[0].ID
	colCtx := c.resultColumnsContext(ctx, originID)
	callbacks := colCtx.Value(resultColumnsContextKey{}).(resultColumnsCallbacks)

	// result_columns_agent.go:126 error in resultColumnEvidence (corrupt source in db)
	_, _ = c.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", "sqlite:///nonexistent/does-not-exist.db", parent.ID)
	if _, _, err := callbacks.plan(ctx, parent.ID, nil); err == nil {
		t.Fatal("expected evidence load error at line 126")
	}
	// restore valid source
	validSrc := c.store.info.Sources[c.store.info.Database]
	_, _ = c.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSrc, parent.ID)

	// Get addable columns
	rows, err := c.ResultColumns(ctx, parent.ID)
	if err != nil || len(rows) == 0 {
		t.Fatalf("expected addable columns, err=%v, count=%d", err, len(rows))
	}
	var addableID string
	for _, r := range rows {
		if r.Addable {
			addableID = r.ID
			break
		}
	}
	if addableID == "" {
		t.Fatal("expected at least one addable column")
	}

	_, prev, err := callbacks.plan(ctx, parent.ID, []string{addableID})
	if err != nil || prev.Fingerprint == "" {
		t.Fatalf("expected plan preview, err=%v", err)
	}

	// result_columns_agent.go:147 check error in apply (switched session)
	c.mu.Lock()
	c.activeID = "switched-session-id"
	c.mu.Unlock()
	if _, err := callbacks.apply(ctx, parent.ID, prev.Fingerprint); err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	c.mu.Lock()
	c.activeID = snap.ID
	c.mu.Unlock()

	// first apply succeeds
	if _, err := callbacks.apply(ctx, parent.ID, prev.Fingerprint); err != nil {
		t.Fatalf("expected apply success, got %v", err)
	}

	// result_columns_agent.go:140 second apply fails (already executed)
	if _, err := callbacks.apply(ctx, parent.ID, prev.Fingerprint); err == nil {
		t.Fatal("expected already executed error on second apply")
	}

	// result_columns.go coverage:
	// lines 95, 98, 104, 107, 110, 116
	// 95: non-COUNT aggregate
	qSum := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewFieldRef("i", "CustomerId")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("i", "CustomerId")},
			dal.Column{Expression: dal.NewAggregate("SUM", false, dal.NewFieldRef("i", "CustomerId"))},
		)
	docSum, _ := tugql.Serialize(qSum)
	_, _, err = resultColumnQuery(RecordSet{DTQL: string(docSum)})
	t.Logf("line 143 err: %v, doc: %s", err, string(docSum))
	if err == nil {
		t.Fatal("expected non-COUNT aggregate error")
	}

	// 98: COUNT with 0 args
	qCount0 := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewFieldRef("i", "CustomerId")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("i", "CustomerId")},
			dal.Column{Expression: dal.NewAggregate(dal.COUNT, false)},
		)
	docCount0, _ := tugql.Serialize(qCount0)
	if _, _, err := resultColumnQuery(RecordSet{DTQL: string(docCount0)}); err == nil {
		t.Fatal("expected unsupported COUNT expression error")
	}

	// 104: COUNT with constant operand
	qCountConst := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewFieldRef("i", "CustomerId")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("i", "CustomerId")},
			dal.Column{Expression: dal.NewAggregate(dal.COUNT, false, dal.NewConstant("constant"))},
		)
	docCountConst, _ := tugql.Serialize(qCountConst)
	if _, _, err := resultColumnQuery(RecordSet{DTQL: string(docCountConst)}); err == nil {
		t.Fatal("expected unsupported COUNT operand error")
	}

	// 107: ordered COUNT
	qCountOrdered := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewFieldRef("i", "CustomerId")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("i", "CustomerId")},
			dal.Column{Expression: dal.NewAggregate(dal.COUNT, false, dal.NewFieldRef("i", "CustomerId"), dal.NewFieldRef("i", "InvoiceId"))},
		)
	docCountOrdered, _ := tugql.Serialize(qCountOrdered)
	if _, _, err := resultColumnQuery(RecordSet{DTQL: string(docCountOrdered)}); err == nil {
		t.Fatal("expected ordered COUNT error")
	}

	// 110: default in column expression (constant expression)
	qConstCol := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewFieldRef("i", "CustomerId")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewConstant("computed")},
		)
	docConstCol, _ := tugql.Serialize(qConstCol)
	if _, _, err := resultColumnQuery(RecordSet{DTQL: string(docConstCol)}); err == nil {
		t.Fatal("expected computed projection error")
	}

	// 116: computed or ambiguous GROUP BY (constant in groupBy)
	qConstGroup := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewConstant(1)).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("i", "CustomerId")},
		)
	docConstGroup, _ := tugql.Serialize(qConstGroup)
	if _, _, err := resultColumnQuery(RecordSet{DTQL: string(docConstGroup)}); err == nil {
		t.Fatal("expected ambiguous groupBy error")
	}

	// result_columns.go: lines 254 and 257 in discoverResultColumns
	meta, admitted, err := c.resultColumnEvidence(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	// 254: table.types[fk.FromFields[0]] not containing INT
	metaBadType := meta
	metaBadType.tables = make(map[string]resultColumnTable)
	for k, v := range meta.tables {
		metaBadType.tables[k] = v
	}
	invKey := relationKey("main", "Invoice")
	tbl := metaBadType.tables[invKey]
	typesCopy := make(map[string]string)
	for k, v := range tbl.types {
		typesCopy[k] = v
	}
	typesCopy["CustomerId"] = "TEXT"
	tbl.types = typesCopy
	metaBadType.tables[invKey] = tbl
	rowsBadType, _ := discoverResultColumns(parent, metaBadType, admitted)
	foundSourceKeyReason := false
	for _, r := range rowsBadType {
		if r.Reason == "Source key affinity is not proven INTEGER" {
			foundSourceKeyReason = true
			break
		}
	}
	if !foundSourceKeyReason {
		t.Fatalf("expected source key affinity error at line 254")
	}

	// 257: grouped, but foreign key not projected or grouped
	qNotGrouped := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewFieldRef("i", "InvoiceId")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("i", "InvoiceId")},
			dal.Column{Expression: dal.NewAggregate(dal.COUNT, false, dal.NewFieldRef("i", "InvoiceId")), Alias: "c"},
		)
	docNotGrouped, _ := tugql.Serialize(qNotGrouped)
	parentNotGrouped := parent
	parentNotGrouped.DTQL = string(docNotGrouped)
	rowsNotGrouped, _ := discoverResultColumns(parentNotGrouped, meta, admitted)
	foundNotGroupedReason := false
	for _, r := range rowsNotGrouped {
		if strings.Contains(r.Reason, "projected and grouped") {
			foundNotGroupedReason = true
			break
		}
	}
	if !foundNotGroupedReason {
		t.Fatalf("expected projected and grouped reason at line 257, got rows: %+v", rowsNotGrouped)
	}

	// lines 338: invalid DTQL in parent
	if _, err := planResultColumns(RecordSet{DTQL: "invalid"}, meta, admitted, []string{"id"}); err == nil {
		t.Fatal("expected error at line 338")
	}
	// 400: c.Alias == "" and c.Expression is aggregate (not FieldRef)
	qNoAliasAgg := dal.From(dal.NewRootCollectionRef("Invoice", "i")).NewQuery().
		GroupBy(dal.NewFieldRef("i", "CustomerId")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("i", "CustomerId")},
			dal.Column{Expression: dal.NewAggregate(dal.COUNT, false, dal.NewFieldRef("i", "CustomerId"))},
		)
	docNoAliasAgg, _ := tugql.Serialize(qNoAliasAgg)
	parentNoAlias := parent
	parentNoAlias.DTQL = string(docNoAliasAgg)
	rowsNoAlias, _ := discoverResultColumns(parentNoAlias, meta, admitted)
	var addableNoAliasID string
	for _, r := range rowsNoAlias {
		if r.Addable {
			addableNoAliasID = r.ID
			break
		}
	}
	if addableNoAliasID != "" {
		_, _ = planResultColumns(parentNoAlias, meta, admitted, []string{addableNoAliasID})
	}

	// 423: alias collision with existing target relation (aliases[fieldSource] == true)
	// 436: outputs collision with alias (outputs[alias] == true)
	qCollision := dal.From(dal.NewRootCollectionRef("Invoice", "Customer")).NewQuery().
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("Customer", "CustomerId")},
			dal.Column{Expression: dal.NewFieldRef("Customer", "CustomerId"), Alias: "Customer_FirstName"},
		)
	docCollision, _ := tugql.Serialize(qCollision)
	parentCollision := parent
	parentCollision.DTQL = string(docCollision)
	rowsCollision, _ := discoverResultColumns(parentCollision, meta, admitted)
	for _, r := range rowsCollision {
		if r.field == "FirstName" && r.Addable {
			_, _ = planResultColumns(parentCollision, meta, admitted, []string{r.ID})
			break
		}
	}

	// 488: resultColumnEvidence query error
	if _, _, err := c.resultColumnEvidence(ctx, RecordSet{DTQL: "invalid"}); err == nil {
		t.Fatal("expected error at line 488")
	}
	// 496: loadResultColumnMetadata error
	canceledEvCtx, cancelEv := context.WithCancel(ctx)
	cancelEv()
	if _, _, err := c.resultColumnEvidence(canceledEvCtx, parent); err == nil {
		t.Fatal("expected error at line 496")
	}

	// executeResultColumns: lines 557, 561, 571, 578, 584
	// 557: store.Load error
	if _, _, err := c.executeResultColumns(canceledEvCtx, c.activeID, parent.ID, []string{addableID}, prev.Fingerprint, ""); err == nil {
		t.Fatal("expected store.Load error at line 557")
	}
	// 571: activeID mismatch
	c.mu.Lock()
	c.activeID = "mismatched-session"
	c.mu.Unlock()
	if _, _, err := c.executeResultColumns(ctx, snap.ID, parent.ID, []string{addableID}, prev.Fingerprint, ""); err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged at line 571, got %v", err)
	}
	c.mu.Lock()
	c.activeID = snap.ID
	c.mu.Unlock()

	// 578: store.AppendUser error
	origTxCommit := txCommitFn
	defer func() { txCommitFn = origTxCommit }()
	txCommitFn = func(tx *sql.Tx) error { return errors.New("user commit error") }
	_, _, _ = c.executeResultColumns(ctx, snap.ID, parent.ID, []string{addableID}, prev.Fingerprint, "")

	// 584: store.AppendQuery error
	commitCount := 0
	txCommitFn = func(tx *sql.Tx) error {
		commitCount++
		if commitCount == 2 {
			return errors.New("query commit error")
		}
		return origTxCommit(tx)
	}
	_, _, _ = c.executeResultColumns(ctx, snap.ID, parent.ID, []string{addableID}, prev.Fingerprint, "")
	txCommitFn = origTxCommit
}

// 3. result_operations.go and derived_execution.go
// Not parallel: modifies resultopsCapture
func TestCoverage100_FinalPush_ResultOperationsAndDerived(t *testing.T) {
	ctx := context.Background()
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}, {field: name}]\nlimit: 10\n",
		Source: "sqlite:///chinook.db",
		Result: secureread.Result{Columns: []string{"id", "name"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1), "name": "Alice"}}}},
	})
	g, rec, _ := u.activeGrid()

	// result_operations.go: line 136 quickServerSort column out of range
	g.SelectColumn(-1)
	if cmd := u.quickServerSort(false); cmd != nil {
		t.Fatal("expected nil at line 136")
	}
	g.SelectColumn(0)

	// result_operations.go: line 379 Enter with modifier (ModShift)
	op, _ := u.newResultOperation(false)
	_, _, handled := op.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if handled {
		t.Fatal("expected false at line 379")
	}

	// result_operations.go: line 516 o.reorder error on bad key in sort
	opSort, _ := u.newResultOperation(false)
	opSort.selected = []int{0}
	opSort.capture.rows[0].Key = "bad-key-xyz"
	_ = opSort.applyLoaded()
	if opSort.err == "" {
		t.Fatal("expected reorder error at line 516")
	}

	// result_operations.go: line 540 "Count" column name collision -> "Rows in group"
	recCount := *rec
	recCount.Result.Columns = []string{"Count", "name"}
	recCount.Result.Rows = []secureread.Row{{Data: map[string]any{"Count": int64(1), "name": "Alice"}}}
	uCount := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "counted",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}, {field: name}]\nlimit: 10\n",
		Source: "sqlite:///chinook.db",
		Result: recCount.Result,
	})
	opCount, _ := uCount.newResultOperation(true)
	opCount.selected = []int{0}
	opCount.count = true
	opCount.capture.columns = []grid.Column{{Name: "Count"}}
	_ = opCount.applyLoaded()

	// derived_execution.go:
	// line 37: exactAggregateColumns original query unavailable
	if _, err := exactAggregateColumns(u.catalog, RecordSet{DTQL: "invalid"}); err == nil {
		t.Fatal("expected error at line 37")
	}
	// line 43: ValidateRecursiveQuery error
	if _, err := exactAggregateColumns(u.catalog, RecordSet{DTQL: "from: {name: users}\nlimit: -1\n"}); err == nil {
		t.Fatal("expected error at line 43")
	}
	// line 49: meta.dbType == ""
	u.catalog.Objects = nil
	if _, err := exactAggregateColumns(u.catalog, *rec); err == nil {
		t.Fatal("expected authoritative type error at line 49")
	}

	// line 88 & 103: deterministicOriginal
	qTest, _ := tugql.Deserialize([]byte(rec.DTQL))
	u.catalog.Objects = []ProjectObject{{
		Reference:  ContextReference{SourceID: "other-db", ObjectID: "users"},
		PrimaryKey: []string{"id"},
	}}
	if deterministicOriginal(u.catalog, *rec, qTest) {
		t.Fatal("expected false at line 88/103")
	}

	// executeDerivedResult:
	// line 132: budget exceeded (limit > 5000)
	c := u.sessions
	c.queryExecutor = &fakeExecutor{}
	opExcess := derivedOperation{Sort: []queryops.SortTerm{{Column: "id"}}}
	parentExcess := *rec
	parentExcess.DTQL = "from: {name: users}\ncolumns: [{field: id}]\nlimit: 9999\n"
	if _, _, err := c.executeDerivedResult(ctx, c.activeID, parentExcess.ID, opExcess, ""); err == nil {
		t.Fatal("expected budget exceeded error at line 132")
	}

	// line 153: deriveResultPreview error
	if _, _, err := c.executeDerivedResult(ctx, c.activeID, rec.ID, derivedOperation{Group: true}, ""); err == nil {
		t.Fatal("expected error at line 153")
	}

	// line 160: source unavailable (database not in store info)
	parentBadDb := *rec
	parentBadDb.Database = "nonexistent-db"
	if _, _, err := c.executeDerivedResult(ctx, c.activeID, parentBadDb.ID, opExcess, ""); err == nil {
		t.Fatal("expected source unavailable at line 160")
	}

	// line 163: ctx.Err()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	prevValid, _ := deriveResultPreview(c.catalog, *rec, opExcess)
	if _, _, err := c.executeDerivedResult(canceledCtx, c.activeID, rec.ID, opExcess, prevValid.DTQL); err == nil {
		t.Fatal("expected ctx error at line 163")
	}

	// line 169: executor does not implement exactAggregateDTQLExecutor
	opExact := derivedOperation{Group: true, Aggregates: []resultops.AggregateTerm{{Function: "COUNT", Column: "id"}}}
	prevExact, _ := deriveResultPreview(c.catalog, *rec, opExact)
	if _, _, err := c.executeDerivedResult(ctx, c.activeID, rec.ID, opExact, prevExact.DTQL); err == nil {
		t.Fatal("expected exact aggregate executor error at line 169")
	}

	// line 181: OriginalOutput executor not implementing recursiveDTQLExecutor
	opOrig := derivedOperation{Scope: queryops.OriginalOutput, Sort: []queryops.SortTerm{{Column: "id"}}}
	prevOrig, _ := deriveResultPreview(c.catalog, *rec, opOrig)
	if _, _, err := c.executeDerivedResult(ctx, c.activeID, rec.ID, opOrig, prevOrig.DTQL); err == nil {
		t.Fatal("expected recursive executor error at line 181")
	}

	// line 199: store.AppendUser error
	origTxCommit2 := txCommitFn
	defer func() { txCommitFn = origTxCommit2 }()
	txCommitFn = func(tx *sql.Tx) error { return errors.New("user append error") }
	_, _, _ = c.executeDerivedResult(ctx, c.activeID, rec.ID, opExcess, prevValid.DTQL)

	// line 203: store.AppendQuery error
	commitCount2 := 0
	txCommitFn = func(tx *sql.Tx) error {
		commitCount2++
		if commitCount2 == 2 {
			return errors.New("query append error")
		}
		return origTxCommit2(tx)
	}
	_, _, _ = c.executeDerivedResult(ctx, c.activeID, rec.ID, opExcess, prevValid.DTQL)
	txCommitFn = origTxCommit2

	// line 239: exactAggregateColumns error in serverPreview
	opServer := op
	opServer.group = true
	opServer.aggregates = map[int]string{0: "COUNT"}
	opServer.ui.sessions.queryExecutor = &fakeExactExecutor{}
	opServer.record.DTQL = "from: {name: users}\ncolumns: [{field: id}]\nlimit: 10\n"
	opServer.ui.catalog.Objects = nil // causes exactAggregateColumns to fail
	_, _ = opServer.serverPreview()

	// lines 251 & 253: parseErr and ValidateRecursiveQuery in serverPreview
	opServer.originalOutput = true
	opServer.group = false
	origDeserialize := dtqlDeserialize
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return nil, fmt.Errorf("injected deserialize error")
	}
	_, _ = opServer.serverPreview()
	dtqlDeserialize = origDeserialize

	// lines 325-327: backendBusy in reviewServer
	opServer.selected = []int{0}
	prev, _ := opServer.serverPreview()
	rev := &resultServerPreviewOverlay{operation: opServer, preview: prev}
	opServer.ui.backends = map[uint64]*workspaceOperation{1: {}}
	_, _, handledRev := rev.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if handledRev || rev.err == "" {
		t.Fatal("expected backendBusy error at lines 325-327")
	}
	opServer.ui.backends = nil
}

// 4. workspace_query_editor.go, chatui_refinements.go, and inspector_related_workspace.go
func TestCoverage100_FinalPush_QueryEditorAndRefinements(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)
	// workspace_query_editor.go:
	// line 139: enter with Mod != 0
	ed := &workspaceQueryEditor{ui: u}
	_, _ = ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})

	// lines 204-206: appendCell wrap when column+width > w && column > 0
	sel := &queryTextSelection{text: []rune("123456789012345"), anchor: 0, cursor: 5}
	_ = sel.View(5, 10)

	// line 233: s.copied == true
	sel.copied = true
	_ = sel.View(80, 24)

	// line 269: click on s.controlsY but X outside controls
	sel.controlsY = 3
	sel.controls = []selectionControl{{start: 10, end: 20, key: "enter"}}
	_, _, handled := sel.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 999, Y: 3})
	if handled {
		t.Fatal("expected false at line 269")
	}

	// chatui_refinements.go:
	// lines 34-43: loadInspectResultColumns success and activeGrid ok=false
	u.workspace.tab = 1
	u.workspace.inspectorSubTab = 2
	u.resultColumnsCache.recordID = ""
	cmdLoad := u.loadInspectResultColumns()
	if cmdLoad == nil {
		t.Fatal("expected cmdLoad")
	}
	_ = cmdLoad()

	// activeGrid ok=false
	uEmpty, _ := newTestChatUI(t, nil)
	uEmpty.workspace.tab = 1
	uEmpty.workspace.inspectorSubTab = 2
	if cmd := uEmpty.loadInspectResultColumns(); cmd != nil {
		t.Fatal("expected nil when no active grid")
	}

	// line 70: inspectExplorerSelection when node.issue && node.objectIndex < 0
	u.catalog.Objects = []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "t1", Title: "t1"}, Issue: "corrupted"}}
	nodes := u.explorer.explorerNodes()
	for i, n := range nodes {
		if n.issue && n.objectIndex < 0 {
			u.explorer.explorerIndex = i
			u.explorer.inspectExplorerSelection()
			break
		}
	}

	// line 124: len(p.inspectGroupObjects) == 0 && p.inspectGroup != "No matching objects"
	u.workspace.inspectObject = nil
	u.workspace.inspectGroup = "Custom Group"
	u.workspace.inspectGroupObjects = nil
	view := u.workspace.catalogInspectionView(80, 24)
	if !strings.Contains(view, "No admitted objects.") {
		t.Fatal("expected No admitted objects at line 124")
	}

	// line 157: fitPanelBody when len(lines) > height
	fitted := fitPanelBody("a\nb\nc\nd", 10, 2)
	if strings.Count(fitted, "\n") != 1 {
		t.Fatalf("expected 2 lines, got %s", fitted)
	}

	// line 232: panelContextActions in explorer when selectedExplorerObject == nil
	u.shell.FocusLeftPanel()
	u.explorer.explorerIndex = -1
	_ = u.panelContextActions()

	// lines 271, 278, 280: panelContextAction in explorer
	_ = u.panelContextAction("attach") // line 271 (object == nil)
	u.explorer.explorerIndex = 0
	_ = u.panelContextAction("details") // line 278
	u.shell.PopOverlay()
	u.shell.FocusLeftPanel()
	_ = u.panelContextAction("query") // line 280
	u.shell.FocusLeftPanel()
	_ = u.panelContextAction("attach") // line 275

	// inspector_related_workspace.go:
	// line 90: receiveRelated when relatedOpen == -1
	wp := u.workspace
	wp.relatedOpen = -1
	opID, _ := u.beginOperation("rel")
	normalMsg := inspectorRelatedMsg{
		operationID: opID,
		sequence:    wp.relatedSequence,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		recordID:    wp.relatedRecordID,
		rowKey:      wp.relatedRowKey,
	}
	_ = wp.receiveRelated(normalMsg)

	// line 158: openRelated when activeGrid ok == false
	uEmpty.workspace.relatedRecords = []relatedRecord{{document: "SELECT 1"}}
	if cmd := uEmpty.workspace.openRelated(0); cmd != nil {
		t.Fatal("expected nil at line 158")
	}

	// lines 227, 230, 239, 243, 245, 247, 249: gridActionsOverlay
	act := &gridActionsOverlay{ui: u}
	// line 227: ok == false
	actEmpty := &gridActionsOverlay{ui: uEmpty}
	_, _, _ = actEmpty.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// line 230: index < 3
	act.index = 0
	_, _, _ = act.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// line 239: index >= len(labels)
	act.index = 999
	_, _, _ = act.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// lines 243, 245, 247, 249
	labels := act.labels()
	for i, l := range labels {
		if strings.HasPrefix(l, "Query") || strings.HasPrefix(l, "Group") || strings.HasPrefix(l, "Sort") || strings.HasPrefix(l, "Table style") {
			act.index = i
			_, _, _ = act.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
	}

	// lines 286, 294, 299: openJoinPicker
	// line 286: visibleGridReason != ""
	uEmpty.shell.SetCenterPanel(&localGroupsPanel{}, true)
	if cmd := uEmpty.openJoinPicker(); cmd != nil || uEmpty.workspaceFeedback == "" {
		t.Fatal("expected feedback at line 286")
	}
	// line 294: sessions == nil
	savedS := u.sessions
	u.sessions = nil
	if cmd := u.openJoinPicker(); cmd != nil {
		t.Fatal("expected nil at line 294")
	}
	u.sessions = savedS
	// line 299: JoinCandidatesActive error
	savedRetained := u.retainedRecordSetID
	u.retainedRecordSetID = "not-in-session"
	if cmd := u.openJoinPicker(); cmd != nil {
		t.Fatal("expected nil at line 299")
	}
	u.retainedRecordSetID = savedRetained

	// line 349: joinPickerOverlay Enter with Mod != 0
	joinOverlay := &joinPickerOverlay{ui: u}
	_, _, handledJoin := joinOverlay.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if handledJoin {
		t.Fatal("expected false at line 349")
	}
}

// 5. chatui_sessions.go and chatui_sidepanel.go
func TestCoverage100_FinalPush_SessionsAndSidepanel(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)
	// chatui_sessions.go:
	// lines 36-38: refreshChatTabs sessions == nil
	savedS := u.sessions
	u.sessions = nil
	u.refreshChatTabs()
	u.sessions = savedS

	// lines 44-48: ArchivedSessionIDs error
	cancelCtx, cancel := context.WithCancel(u.ctx)
	cancel()
	u.ctx = cancelCtx
	u.refreshChatTabs()
	u.ctx = context.Background()

	// line 71: newcomers sorting with identical CreatedAt
	now := time.Now()
	_, _ = u.sessions.store.db.ExecContext(u.ctx, "UPDATE sessions SET created_at = ?", now.Format(time.RFC3339Nano))
	u.refreshChatTabs()

	// line 93: captureChatView sessionID == ""
	savedID := u.sessionID
	u.sessionID = ""
	u.captureChatView()
	u.sessionID = savedID

	// line 118: captureChatView with localGroupsPanel
	u.shell.ShowTemporaryCenterPanel(&localGroupsPanel{})
	u.captureChatView()

	// line 210: receiveChatSessionChanged previous != u.sessionID
	_ = u.receiveChatSessionChanged(chatSessionChangedMsg{previous: "other-id"})

	// line 222: receiveChatSessionChanged with chatCloseAfterSwitch == true
	u.chatCloseAfterSwitch = true
	_ = u.receiveChatSessionChanged(chatSessionChangedMsg{previous: u.sessionID, snapshot: u.snapshot})
	u.shell.PopOverlay()

	// lines 313, 315: sessionPrefixKey "d" and "esc"
	for _, k := range []string{"d", "esc"} {
		u.chatTransition = false
		u.chatPrefixUntil = time.Now().Add(5 * time.Second)
		u.chatPrefixSessionID = u.sessionID
		u.chatPrefixGeneration = u.workspaceGeneration
		u.chatPrefixFocus = u.shell.WorkspaceFocus()
		u.chatPrefixInputEpoch = u.shell.InputEpoch() - 1
		_, _ = u.sessionPrefixKey(uitest.Key(k))
	}
	u.chatTransition = false

	// chatui_sidepanel.go:
	// line 108: explorer Title
	if u.explorer.Title() != "Explorer" {
		t.Fatal("expected Explorer title at line 108")
	}

	// line 192: setTab index == 0 on workspace panel
	u.workspace.setTab(0)
	if u.workspace.tab != 1 {
		t.Fatal("expected tab 1 at line 192")
	}

	// line 210: toggleAttachment when already attached (detach)
	ref := ContextReference{Kind: "table", ObjectID: "users"}
	u.snapshot.Workspace.Attachments = []ContextReference{ref}
	u.workspace.toggleAttachment(ref)

	// line 546: explorerNodesView selected == nil
	u.catalog.Objects = []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "users", Title: "users"}}}
	u.explorer.explorerIndex = -1
	_ = u.explorer.explorerNodesView(80, 24)

	// line 628: selectedExplorerObject index < 0
	if obj := u.explorer.selectedExplorerObject(); obj != nil {
		t.Fatal("expected nil at line 628")
	}

	// lines 925 & 934-937: dockedView with dock and dockGrid
	u.snapshot.Workspace.Docks = []Dock{{ID: "d1", Title: "Docked 1", Reference: ContextReference{Kind: "table", ObjectID: "users"}}}
	u.workspace.dockIndex = 0
	u.workspace.dockGrids = map[string]*gridState{"d1": gridStateTestFixture(t)}
	_ = u.workspace.dockedView(80)

	// lines 1081-1082: View tab == 0 on workspace
	u.workspace.tab = 0
	_ = u.workspace.View(80, 24, true)

	// line 1111: View width <= key width on explorer
	_ = u.explorer.View(5, 24, true)

	// lines 1144-1157: explorer mouse clicks
	u.explorer.explorerOffset = 0
	u.explorer.height = 24
	u.explorer.focused = true
	_, _ = u.explorer.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 0})  // line 1144
	_, _ = u.explorer.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 1})  // line 1153
	_, _ = u.explorer.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 99}) // line 1157

	// line 1199: ctrl+n with selectedExplorerObject == nil
	u.explorer.explorerIndex = -1
	_, _ = u.explorer.Update(uitest.Key("ctrl+n"))

	// lines 1205-1211: hasExplorerInspection keys
	u.workspace.inspectGroup = "Inspection"
	for _, k := range []string{"up", "down", "pgup", "pgdown", "home", "end"} {
		_, _ = u.workspace.Update(uitest.Key(k))
	}
	u.workspace.clearExplorerInspection()

	// lines 1222-1225: relatedFocused keys
	u.workspace.relatedFocused = true
	_, _ = u.workspace.Update(uitest.Key("up"))
	_, _ = u.workspace.Update(uitest.Key("esc"))

	// lines 1241-1250: explorerSearchFocused keys
	u.explorer.explorerSearchFocused = true
	u.explorer.explorerSearch = "abc"
	_, _ = u.explorer.Update(uitest.Key("esc")) // clears search (line 1241)
	u.explorer.explorerSearch = ""
	_, _ = u.explorer.Update(uitest.Key("esc")) // blurs search
	u.explorer.explorerSearchFocused = true
	_, _ = u.explorer.Update(uitest.Key("enter")) // blurs search (line 1246)
	u.explorer.explorerSearchFocused = true
	u.explorer.explorerSearch = "ab"
	_, _ = u.explorer.Update(uitest.Key("backspace")) // removes char (lines 1248-1250)

	// lines 1261-1263: "/" key on explorer
	u.explorer.explorerSearchFocused = false
	_, _ = u.explorer.Update(uitest.Key("/"))

	// lines 1301, 1305, 1309, 1349, 1382, 1386:
	u.explorer.tab = 0
	u.explorer.explorerIndex = 0
	_, _ = u.explorer.Update(uitest.Key("pgdown"))
	_, _ = u.explorer.Update(uitest.Key("pgup"))
	// right on expanded branch
	u.explorer.explorerCollapsed = map[string]bool{}
	_, _ = u.explorer.Update(uitest.Key("right"))

	u.workspace.tab = 1
	for _, digit := range []string{"1", "2", "3"} {
		_, _ = u.workspace.Update(uitest.Key(digit))
	}
	_, _ = u.workspace.Update(uitest.Key("home"))
	_, _ = u.workspace.Update(uitest.Key("end"))
}

// 6. chatui.go coverage
func TestCoverage100_FinalPush_ChatUI(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)
	// lines 368-371: Submit while backendBusy
	u.backends = map[uint64]*workspaceOperation{1: {}}
	_ = u.Submit("test query")
	u.backends = nil

	// lines 660-661: blockForGrid while backendBusy
	u.backends = map[uint64]*workspaceOperation{1: {}}
	gFixture := gridStateTestFixture(t)
	_ = u.blockForGrid(gFixture.Model, "rec1")
	u.backends = nil

	// lines 728-732: inspectResultColumnsMsg
	u.resultColumnsCache.recordID = "rec1"
	_ = u.OnMsg(inspectResultColumnsMsg{
		sessionID:  u.sessionID,
		generation: u.workspaceGeneration,
		recordID:   "rec1",
		rows:       []ResultColumnRow{{Name: "c1"}},
	})
	// mismatch
	_ = u.OnMsg(inspectResultColumnsMsg{sessionID: "other"})

	// lines 754-760: header mouse clicks for F3, F8, F1
	header := strings.Split(ansi.Strip(u.shell.View().Content), "\n")[0]
	f3Idx := strings.Index(header, "Project:")
	if f3Idx >= 0 {
		_ = u.OnMsg(tea.MouseClickMsg{Button: tea.MouseLeft, X: f3Idx + 1, Y: 0})
		u.shell.PopOverlay()
	}
	f8Idx := strings.Index(header, "F8 Server")
	if f8Idx >= 0 {
		_ = u.OnMsg(tea.MouseClickMsg{Button: tea.MouseLeft, X: f8Idx + 1, Y: 0})
	}
	f1Idx := strings.Index(header, "F1 Help")
	if f1Idx >= 0 {
		_ = u.OnMsg(tea.MouseClickMsg{Button: tea.MouseLeft, X: f1Idx + 1, Y: 0})
		u.shell.PopOverlay()
	}

	// lines 789, 793-795: workspaceQueryDoneMsg mismatch and err
	opID, _ := u.beginOperation("Query")
	_ = u.OnMsg(workspaceQueryDoneMsg{operationID: opID, sessionID: "other-session"})
	opID, _ = u.beginOperation("Query")
	_ = u.OnMsg(workspaceQueryDoneMsg{operationID: opID, sessionID: u.sessionID, generation: u.workspaceGeneration, err: fmt.Errorf("q err")})

	// lines 832, 847: JoinAppliedMsg mismatch and err
	opID, _ = u.beginOperation("Join")
	_ = u.OnMsg(JoinAppliedMsg{OperationID: opID, SessionID: "other-session"})
	opID, _ = u.beginOperation("Join")
	_ = u.OnMsg(JoinAppliedMsg{OperationID: opID, SessionID: u.sessionID, Generation: u.workspaceGeneration, Err: fmt.Errorf("join err")})

	// lines 898-900: explorePanel Update on unhandled message
	dummy := dummyExplorePanelWithInit{SidePanel: u.explorer}
	u.explorePanel = dummy
	_ = u.OnMsg("arbitrary unhandled message")
	u.explorePanel = u.explorer

	// lines 921-922: reconcilePendingBridge with snapshot.ID != u.sessionID
	u.bridgeReconcilePending = true
	// Create another session in store
	sOther, _ := u.sessions.store.AppendUser(u.ctx, "", "other")
	u.sessions.activeID = sOther.ID
	u.reconcilePendingBridge()
	u.sessions.activeID = u.sessionID

	// lines 978, 991, 997: loadSession view restore
	view := &chatSessionView{
		recordID:      u.activeRecordSetID(),
		center:        &localGroupsPanel{},
		centerFocused: false,
	}
	u.chatViews = map[string]*chatSessionView{u.sessionID: view}
	u.loadSession(u.snapshot)

	// lines 1262, 1264, 1266: runCommand
	_ = u.runCommand("/actions")
	u.shell.PopOverlay()
	_ = u.runCommand("/keys")
	u.shell.PopOverlay()
	_ = u.runCommand("/trail")
	u.shell.PopOverlay()

	// statusHintsRaw coverage:
	// line 1578: !u.shell.MouseEnabled()
	u.shell.SetMouseEnabled(false)
	_ = u.statusHintsRaw(120)
	u.shell.SetMouseEnabled(true)

	// line 1588: composerChipHints
	u.snapshot.Workspace.Attachments = []ContextReference{{Kind: "table", ObjectID: "users"}}
	u.shell.FocusComposer()
	_ = u.statusHintsRaw(120)

	// line 1613: ExportBucket > 0
	u.snapshot.Workspace.ExportBucket = []string{"e1"}
	_ = u.statusHintsRaw(120)

	// lines 1617, 1619, 1621: grid views
	if g, _, ok := u.activeGrid(); ok {
		g.ShowView(gridViewQuery)
		_ = u.statusHintsRaw(120)
		g.ShowView(gridViewCharts)
		_ = u.statusHintsRaw(120)
		g.ShowView(gridViewCurrentRow)
		_ = u.statusHintsRaw(120)
		g.ShowView(grid.ViewTable)
	}

	// line 1632: join.JoinFocused()
	jb := &JoinBlock{
		RecordSetID: u.activeRecordSetID(),
		Candidates:  []JoinCandidate{{Source: RelationInstance{Relation: "users"}}},
		joinFocused: true,
	}
	u.joinBlocksByRecordSetID = map[string]*JoinBlock{
		u.activeRecordSetID(): jb,
	}
	_ = u.statusHintsRaw(120)

	// line 1643: transcriptEntryKindHTTP
	u.transcriptEntryKinds["entry-http"] = transcriptEntryKindHTTP
	u.shell.FocusEntry("entry-http")
	_ = u.statusHintsRaw(120)

	// line 1650, 1652, 1656, 1659: sidebar tabs and bookmark states
	u.shell.FocusRightPanel()
	u.workspace.tab = 1
	_ = u.statusHintsRaw(120)
	u.workspace.tab = 2
	_ = u.statusHintsRaw(120)
	u.workspace.tab = 3
	u.workspace.bookmarkGridFocused = true
	_ = u.statusHintsRaw(120)
	u.workspace.bookmarkGridFocused = false
	u.workspace.bookmarkMode = "rename"
	_ = u.statusHintsRaw(120)
	u.workspace.bookmarkMode = ""

	// line 1666: u.shell.Busy() && u.sessions != nil
	u.shell.SetBusy(true)
	_ = u.statusHintsRaw(120)
	u.shell.SetBusy(false)

	// line 1678: width < 100 && composerChipHints && !Busy()
	u.snapshot.Workspace.Attachments = []ContextReference{{Kind: "table", ObjectID: "users"}}
	u.shell.FocusComposer()
	_ = u.statusHintsRaw(80)

	// line 1693: autoHint with styled string (ansi.Strip(s) != s)
	styled := lipgloss.NewStyle().Bold(true).Render("styled text")
	_, ok := autoHint(styled)
	if ok {
		t.Fatal("expected false for styled string in autoHint")
	}

	// lines 1732, 1734: toolbarHidden
	u.toolbarHidden = true
	u.workspaceFeedback = "feedback message"
	_, _ = u.statusHints(120) // line 1732
	u.workspaceFeedback = ""
	_, _ = u.statusHints(120) // line 1734
	u.toolbarHidden = false

	// line 1776: width < 100 with apiScreen != nil
	u.shell.FocusEntry(u.lastGridEntryID)
	u.apiScreen = &apiPanel{}
	_, _ = u.statusHints(80)
	u.apiScreen = nil
}
