package chat

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

// Not parallel: tests JoinAppliedMsg retain workspace error branch.
func TestCoverage100_ChatUI_JoinFinishedError(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	opID, _ := u.beginOperation("join")
	_ = u.OnMsg(JoinAppliedMsg{
		OperationID: opID,
		SessionID:   u.sessionID,
		Generation:  u.workspaceGeneration,
		Captured:    true,
		Interaction: u.workspaceInteraction + 999, // Mismatched interaction so current == false
		Err:         errors.New("custom join failure"),
	})
	require.Contains(t, u.workspaceFeedback, "I couldn't apply that JOIN")
}

// Not parallel: mutates u.sessionID and u.chatViews.
func TestCoverage100_ChatUI_ReconcilePendingBridge_LoadSession(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	// Setup a query editor in u.queryEditors and u.chatViews to cover lines 978-979, 991-992, 997
	editor := &workspaceQueryEditor{
		ui: u,
	}
	if u.queryEditors == nil {
		u.queryEditors = map[string]*workspaceQueryEditor{}
	}
	u.queryEditors[u.sessions.activeID+"\x00query1"] = editor

	localPanel := &localGroupsPanel{}

	view := &chatSessionView{
		recordID:      u.lastGridRecordSetID,
		center:        editor,
		centerFocused: false,
	}
	if u.chatViews == nil {
		u.chatViews = map[string]*chatSessionView{}
	}
	u.chatViews[u.sessions.activeID] = view

	// Simulate different session active in snapshot
	u.sessionID = "other-session-id"
	u.bridgeReconcilePending = true
	u.reconcilePendingBridge()

	// Now test localGroupsPanel center panel branch (line 997)
	u.sessionID = "other-session-id"
	view.center = localPanel
	view.centerFocused = true
	u.bridgeReconcilePending = true
	u.reconcilePendingBridge()
}

// Not parallel: tests statusHintsRaw branches.
func TestCoverage100_ChatUI_StatusHintsRaw(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	// Lines 1591-1592: webLinkVisible = true
	u.webLinkVisible = true
	u.browserURL = "http://example.com"
	hints := u.statusHintsRaw(120)
	require.NotEmpty(t, hints)

	// In transcript grid focus:
	g, _, ok := u.activeGrid()
	require.True(t, ok)
	require.NotNil(t, g)

	// Line 1613-1614: ExportBucket > 0
	u.snapshot.Workspace.ExportBucket = []string{"rs1"}
	hints = u.statusHintsRaw(120)
	require.NotEmpty(t, hints)

	// Line 1617: gridViewQuery
	g.ShowView(gridViewQuery)
	hints = u.statusHintsRaw(120)
	require.NotEmpty(t, hints)

	// Line 1619: gridViewCharts
	g.ShowView(gridViewCharts)
	hints = u.statusHintsRaw(120)
	require.NotEmpty(t, hints)

	// Line 1621: gridViewCurrentRow
	g.ShowView(gridViewCurrentRow)
	hints = u.statusHintsRaw(120)
	require.NotEmpty(t, hints)

	// Lines 1632-1633: join.JoinFocused() == true
	recID := u.activeRecordSetID()
	join := &JoinBlock{joinFocused: true}
	u.joinBlocksByRecordSetID[recID] = join
	hints = u.statusHintsRaw(120)
	require.NotEmpty(t, hints)
}

// Not parallel: tests panelContextAction explorer details and query.
func TestCoverage100_ChatUI_PanelContextAction(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	u.catalog = workspaceTestCatalog()
	u.shell.SetLeftPanel(u.explorer)
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 22})
	u.shell.FocusLeftPanel()

	// Uncollapse all branches so Customer is reachable
	for _, n := range u.explorer.allExplorerNodes() {
		u.explorer.explorerCollapsed[n.id] = false
	}
	for index := range u.explorer.explorerNodes() {
		u.explorer.explorerIndex = index
		if obj := u.explorer.selectedExplorerObject(); obj != nil && obj.Reference.Kind == "table" {
			break
		}
	}
	require.NotNil(t, u.explorer.selectedExplorerObject())

	_ = u.panelContextAction("details")

	cmd2 := u.panelContextAction("query")
	require.NotNil(t, cmd2)
}

// Not parallel: mutates dbQueryContextFn seam.
func TestCoverage100_ChatUI_RefreshChatTabs(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	// Lines 46-48: ArchivedSessionIDs error
	origQuery := dbQueryContextFn
	defer func() { dbQueryContextFn = origQuery }()

	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if query != "" && len(query) > 20 && (query[7:21] == "a.session_id F" || query[7:19] == "a.session_id") {
			return nil, errors.New("archive db fail")
		}
		return origQuery(db, ctx, query, args...)
	}

	u.refreshChatTabs()
	require.Contains(t, u.workspaceFeedback, "archive db fail")
	dbQueryContextFn = origQuery

	// Lines 70-72: newcomers with identical CreatedAt
	now := time.Now()
	u.chatSessions = nil
	ctx := context.Background()
	_, _ = u.sessions.store.Create(ctx, "session-a")
	_, _ = u.sessions.store.Create(ctx, "session-b")
	// Override created_at in DB to be identical
	_, _ = u.sessions.store.db.Exec(`UPDATE sessions SET created_at = ?`, now.Format(time.RFC3339Nano))

	u.refreshChatTabs()
	require.NotEmpty(t, u.chatSessions)
}

// Not parallel: tests explorer sidepanel keys and methods.
func TestCoverage100_Sidepanel_Keys(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	u.catalog = workspaceTestCatalog()
	exp := u.explorer

	// Line 546: projectExplorer when selected == nil
	exp.explorerIndex = -1
	res := exp.projectExplorer(60, 20)
	require.NotEmpty(t, res)

	u.workspace.explorerIndex = -1
	resNonExp := u.workspace.projectExplorer(60, 20)
	require.NotEmpty(t, resNonExp)

	exp.explorerIndex = 0

	// Lines 1241-1242: esc when explorerSearch != ""
	exp.explorerSearchFocused = true
	exp.explorerSearch = "hello"
	exp.updateKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	require.Empty(t, exp.explorerSearch)

	// Lines 1296-1297: ctrl+f
	exp.tab = 0
	exp.updateKey(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	exp.explorerSearchFocused = false

	u.workspace.clearExplorerInspection()
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})

	// Lines 1300-1302: pgdown
	exp.height = 20
	for _, n := range exp.allExplorerNodes() {
		exp.explorerCollapsed[n.id] = false
	}
	for index := range exp.explorerNodes() {
		exp.explorerIndex = index
		if exp.selectedExplorerObject() != nil {
			break
		}
	}
	require.NotNil(t, exp.selectedExplorerObject())
	exp.updateKey(uitest.Key("pgdown"))
	require.True(t, exp.explorerDetailOffset > 0)

	// Lines 1304-1306: pgup
	exp.updateKey(uitest.Key("pgup"))

	// Lines 1308-1311: "1", "2", "3" when tab == 1
	exp.tab = 1
	exp.updateKey(tea.KeyPressMsg{Code: '2'})
	require.Equal(t, 1, exp.inspectorSubTab)

	// Lines 1349-1351: right key steps into child
	exp.tab = 0
	nodes := exp.explorerNodes()
	if len(nodes) >= 2 {
		exp.explorerIndex = 0
		exp.explorerCollapsed[nodes[0].id] = false
		exp.updateKey(tea.KeyPressMsg{Code: tea.KeyRight})
	}
}

// Not parallel: tests inspector related and join picker overlay branches.
func TestCoverage100_InspectorRelated_JoinPicker(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	wp := u.workspace
	wp.relatedRecordID = u.lastGridRecordSetID
	wp.relatedSessionID = u.sessionID
	wp.relatedGeneration = u.workspaceGeneration
	wp.relatedOpen = -1

	// Line 90: receiveRelated when current is true and relatedOpen < 0
	opID, _ := u.beginOperation("related")
	g, _, ok := u.activeGrid()
	require.True(t, ok)

	cmd := wp.receiveRelated(inspectorRelatedMsg{
		operationID: opID,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		recordID:    u.lastGridRecordSetID,
		rowKey:      g.sourceRowKey(),
	})
	require.Nil(t, cmd)

	// Lines 299-301: openJoinPicker error when session mismatch
	origSessionID := u.sessionID
	u.sessionID = "mismatched-session"
	cmd = u.openJoinPicker()
	require.Nil(t, cmd)
	u.sessionID = origSessionID

	// Lines 349-350: joinPickerOverlay Enter with Mod != 0
	overlay := &joinPickerOverlay{
		ui:         u,
		recordID:   u.lastGridRecordSetID,
		sessionID:  u.sessionID,
		candidates: []JoinCandidate{{Cardinality: "one", Target: RelationInstance{Relation: "orders"}}},
	}
	_, _, active := overlay.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	require.False(t, active)
}

// Not parallel: tests workspaceQueryEditor enter with Mod != 0.
func TestCoverage100_WorkspaceQueryEditor_ModEnter(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	obj := ProjectObject{
		Reference: ContextReference{Kind: "table", SourceID: "s1", ObjectID: "users"},
	}
	_ = u.openWorkspaceQuery(obj, false)
	require.NotNil(t, u.queryEditor)
	_, _ = u.queryEditor.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
}

// Not parallel: tests quickServerSort column out of range and Enter with Mod in resultOperationOverlay.
func TestCoverage100_ResultOperations_Gaps(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	g, _, ok := u.activeGrid()
	require.True(t, ok)

	// Lines 136-137: SelectedColumn out of range in quickServerSort
	g.SelectColumn(-1)
	cmd := u.quickServerSort(false)
	require.Nil(t, cmd)

	// Lines 379-380: Enter with Mod != 0 in resultOperationOverlay
	op, err := u.newResultOperation(false)
	require.NoError(t, err)
	_, _, active := op.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	require.False(t, active)

	// Lines 516-518: applyLoaded reorder error
	op.selected = []int{0}
	op.capture.rows = []grid.Row{{Key: "non-existent-key"}}
	_ = op.applyLoaded()
	require.NotEmpty(t, op.err)

	// Lines 540-541: count label when a column is already named "Count"
	opGroup, err := u.newResultOperation(true)
	require.NoError(t, err)
	opGroup.capture.columns = []grid.Column{{Name: "count", Numeric: false}}
	opGroup.selected = []int{0}
	opGroup.count = true
	_ = opGroup.applyLoaded()
}

type mockOrderedAggregate struct {
	dal.AggregateFunc
	str string
}

func (m mockOrderedAggregate) String() string { return m.str }

// Not parallel: tests result_columns.go gap branches.
func TestCoverage100_ResultColumns_Gaps(t *testing.T) {
	recInvalidDTQL := RecordSet{
		DTQL: "invalid: dtql: {{{",
	}

	// Line 338: planResultColumns with invalid DTQL
	_, err := planResultColumns(recInvalidDTQL, resultColumnMetadata{}, map[string]bool{}, []string{"col1"})
	require.Error(t, err)

	// Lines 95, 98, 107, 110, 116 in resultColumnQuery
	// Non-COUNT aggregate (MAX)
	qMax := dal.From(dal.NewRootCollectionRef("users", "")).NewQuery().
		GroupBy(dal.NewFieldRef("", "id")).
		Limit(10).
		SelectColumns(
			dal.Column{Expression: dal.NewFieldRef("", "id")},
			dal.Column{Expression: dal.NewAggregate("MAX", false, dal.NewFieldRef("", "id"))},
		)
	docMax, _ := dtql.Serialize(qMax)
	_, _, err = resultColumnQuery(RecordSet{DTQL: string(docMax)})
	require.ErrorContains(t, err, "preserves COUNT only")

	// Lines 98, 107, 110, 116 using dtqlDeserialize seam:
	origDes := dtqlDeserialize
	defer func() { dtqlDeserialize = origDes }()

	// Line 98: COUNT with 0 args
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return dal.From(dal.NewRootCollectionRef("users", "")).NewQuery().
			GroupBy(dal.NewFieldRef("", "id")).
			Limit(10).
			SelectColumns(
				dal.Column{Expression: dal.NewFieldRef("", "id")},
				dal.Column{Expression: dal.NewAggregate(dal.COUNT, false)},
			), nil
	}
	_, _, err = resultColumnQuery(RecordSet{DTQL: "mock"})
	require.ErrorContains(t, err, "unsupported COUNT expression")

	// Line 107: ordered COUNT
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return dal.From(dal.NewRootCollectionRef("users", "")).NewQuery().
			GroupBy(dal.NewFieldRef("", "id")).
			Limit(10).
			SelectColumns(
				dal.Column{Expression: dal.NewFieldRef("", "id")},
				dal.Column{Expression: mockOrderedAggregate{
					AggregateFunc: dal.NewAggregate(dal.COUNT, false, dal.NewFieldRef("", "id")),
					str:           "count(id order by id)",
				}},
			), nil
	}
	_, _, err = resultColumnQuery(RecordSet{DTQL: "mock"})
	require.ErrorContains(t, err, "ordered COUNT enrichment is not supported")

	// Line 110: computed constant projection
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return dal.From(dal.NewRootCollectionRef("users", "")).NewQuery().
			GroupBy(dal.NewFieldRef("", "id")).
			Limit(10).
			SelectColumns(
				dal.Column{Expression: dal.NewConstant("computed")},
			), nil
	}
	_, _, err = resultColumnQuery(RecordSet{DTQL: "mock"})
	require.ErrorContains(t, err, "computed or nondeterministic projections")

	// Line 116: GROUP BY with computed expression
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return dal.From(dal.NewRootCollectionRef("users", "")).NewQuery().
			GroupBy(dal.NewConstant(1)).
			Limit(10).
			SelectColumns(
				dal.Column{Expression: dal.NewFieldRef("", "id")},
			), nil
	}
	_, _, err = resultColumnQuery(RecordSet{DTQL: "mock"})
	require.ErrorContains(t, err, "computed or ambiguous GROUP BY")
	dtqlDeserialize = origDes

	// Line 488: resultColumnEvidence with invalid DTQL
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	_, _, err = u.sessions.resultColumnEvidence(context.Background(), recInvalidDTQL)
	require.Error(t, err)

	// Lines 557, 561, 571 in executeResultColumns
	// Canceled context (line 561)
	cancCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = u.sessions.executeResultColumns(cancCtx, u.sessions.activeID, u.lastGridRecordSetID, []string{"id"}, "fp", "")
	require.Error(t, err)

	// Invalid sessionID (line 527 / 557)
	_, _, err = u.sessions.executeResultColumns(context.Background(), "invalid-sess", u.lastGridRecordSetID, []string{"id"}, "fp", "")
	require.Error(t, err)
}

type stubDTQLExecutor struct{}

func (stubDTQLExecutor) RunDTQL(context.Context, string, []byte, map[string]any) (secureread.Result, error) {
	return secureread.Result{}, nil
}

type mockExactExec struct{}

func (mockExactExec) RunDTQL(context.Context, string, []byte, map[string]any) (secureread.Result, error) {
	return secureread.Result{}, nil
}

func (mockExactExec) RunExactAggregateDTQL(context.Context, string, []byte, []byte, map[string]any, []resultops.Column) (secureread.ExactAggregateResult, error) {
	return secureread.ExactAggregateResult{}, nil
}

func (mockExactExec) RunRecursiveDTQL(context.Context, string, []byte, map[string]any) (secureread.Result, error) {
	return secureread.Result{}, nil
}

func (mockExactExec) CanReadWholeCollection(context.Context, string) error {
	return nil
}

type badAgg struct {
	dal.AggregateFunc
}

func (badAgg) FuncName() string           { return "count" }
func (badAgg) FuncArgs() []dal.Expression { return []dal.Expression{dal.NewFieldRef("", "id")} }
func (badAgg) String() string             { return "COUNT_DIFFERENT" }

// Not parallel: tests derived_execution.go gap branches.
func TestCoverage100_DerivedExecution_Gaps(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	// Lines 43-44: exactAggregateColumns recursive query validation failure
	recWhereBinary := RecordSet{
		DTQL: "from: {name: users}\ncolumns: [{field: id}]\nwhere: {comparison: {left: {binary: {left: {field: id}, op: '+', right: 1}}, op: '=', right: 2}}\n",
	}
	_, err := exactAggregateColumns(u.catalog, recWhereBinary)
	require.Error(t, err)

	// Lines 132-133: deriveResultPreview bounded execution budget error
	origDes := dtqlDeserialize
	defer func() { dtqlDeserialize = origDes }()
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return nil, errors.New("parse error")
	}
	recValid := RecordSet{
		DTQL: "from: {name: users}\ncolumns: [{field: id}]\n",
	}
	_, err = deriveResultPreview(u.catalog, recValid, derivedOperation{
		Columns: []string{"id"},
		Sort:    []queryops.SortTerm{{Column: "id"}},
		Scope:   queryops.MatchingSource,
	})
	require.ErrorContains(t, err, "bounded execution budget")
	dtqlDeserialize = origDes

	// Lines 160, 163, 169, 173, 181 in executeDerivedResult
	sess := u.sessions
	sess.ConfigureQueryExecutor(stubDTQLExecutor{})
	rec := u.snapshot.RecordSets[u.lastGridRecordSetID]

	// Line 160: source not available
	validOp := derivedOperation{
		Columns: []string{"id"},
		Sort:    []queryops.SortTerm{{Column: "id"}},
		Scope:   queryops.MatchingSource,
	}
	prevForSource, err := deriveResultPreview(u.catalog, rec, validOp)
	require.NoError(t, err)

	origSources := sess.store.info.Sources
	defer func() { sess.store.info.Sources = origSources }()
	sess.store.info.Sources = map[string]string{} // Empty sources so lookup fails
	_, _, err = sess.executeDerivedResult(context.Background(), sess.activeID, u.lastGridRecordSetID, validOp, prevForSource.DTQL)
	require.ErrorContains(t, err, "origin source is no longer available")
	sess.store.info.Sources = map[string]string{rec.Database: rec.Source}

	// Line 163: context canceled with valid preview
	cancCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = sess.executeDerivedResult(cancCtx, sess.activeID, u.lastGridRecordSetID, validOp, prevForSource.DTQL)
	require.Error(t, err)

	// Line 169: executor does not implement exactAggregateDTQLExecutor
	groupOp := derivedOperation{
		Group:      true,
		Columns:    []string{"id"},
		Aggregates: []resultops.AggregateTerm{{Column: "id", Function: "count"}},
		Scope:      queryops.MatchingSource,
	}
	prev, err := deriveResultPreview(u.catalog, rec, groupOp)
	require.NoError(t, err)
	_, _, err = sess.executeDerivedResult(context.Background(), sess.activeID, u.lastGridRecordSetID, groupOp, prev.DTQL)
	require.ErrorContains(t, err, "cannot run exact server aggregates")

	// Line 181: OriginalOutput but executor does not implement recursiveDTQLExecutor
	origOp := derivedOperation{
		Columns: []string{"id"},
		Sort:    []queryops.SortTerm{{Column: "id"}},
		Scope:   queryops.OriginalOutput,
	}
	prevSort, err := deriveResultPreview(u.catalog, rec, origOp)
	require.NoError(t, err)
	_, _, err = sess.executeDerivedResult(context.Background(), sess.activeID, u.lastGridRecordSetID, origOp, prevSort.DTQL)
	require.ErrorContains(t, err, "cannot safely run Original query output")

	// Lines 43-44: exactAggregateColumns recursive validation error
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		col := dal.NewQualifiedRootCollectionRef("main", "users", "")
		q := dal.From(col).NewQuery().SelectColumns(dal.Column{
			Expression: dal.Binary(dal.NewFieldRef("", "id"), dal.Add, dal.NewFieldRef("", "id")),
		})
		return q, nil
	}
	_, err = exactAggregateColumns(u.catalog, rec)
	require.Error(t, err)
	dtqlDeserialize = origDes

	// Lines 163-164: context error in executeDerivedResult
	stepCtxDerived := &stepCancelCtx{Context: context.Background(), cancelAt: 1}
	_, _, err = sess.executeDerivedResult(stepCtxDerived, sess.activeID, u.lastGridRecordSetID, origOp, prevSort.DTQL)
	require.Error(t, err)

	// Line 173: exactAggregateColumns error in executeDerivedResult
	sess.ConfigureQueryExecutor(mockExactExec{})
	callExact := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		callExact++
		if callExact > 1 {
			return nil, errors.New("parse error in exactAggregateColumns")
		}
		return origDes(data)
	}
	_, _, err = sess.executeDerivedResult(context.Background(), sess.activeID, u.lastGridRecordSetID, groupOp, prev.DTQL)
	require.Error(t, err)
	dtqlDeserialize = origDes

	// Line 199: AppendUser error
	origInsert := insertMessageFn
	defer func() { insertMessageFn = origInsert }()
	insertMessageFn = func(ctx context.Context, tx *sql.Tx, sessionID string, message ChatMessage) error {
		return errors.New("insert failed")
	}
	_, _, err = sess.executeDerivedResult(context.Background(), sess.activeID, u.lastGridRecordSetID, origOp, prevSort.DTQL)
	require.Error(t, err)
	insertMessageFn = origInsert

	// Line 203: AppendQuery error
	origAppendQuery := appendQueryTxFn
	defer func() { appendQueryTxFn = origAppendQuery }()
	appendQueryTxFn = func(s *SessionStore, ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
		return errors.New("append query failed")
	}
	_, _, err = sess.executeDerivedResult(context.Background(), sess.activeID, u.lastGridRecordSetID, origOp, prevSort.DTQL)
	require.Error(t, err)
	appendQueryTxFn = origAppendQuery

	// Lines 239-240: serverPreview with exactAggregateColumns failure
	u2 := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users2",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}, {field: name}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id", "name"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1), "name": "Alice"}}}},
	})
	u2.sessions.queryExecutor = mockExactExec{}
	opGroup2, err := u2.newResultOperation(false)
	require.NoError(t, err)
	opGroup2.group = true
	opGroup2.selected = []int{0}
	opGroup2.aggregates = map[int]string{1: "sum"}
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return nil, errors.New("cannot parse")
	}
	_, err = opGroup2.serverPreview()
	require.Error(t, err)
	dtqlDeserialize = origDes

	// Lines 251-252: serverPreview originalOutput
	opOrig, _ := u.newResultOperation(false)
	opOrig.originalOutput = true
	opOrig.group = false
	opOrig.selected = []int{0}
	callPrev := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		callPrev++
		if callPrev > 1 {
			return nil, errors.New("cannot parse preview DTQL")
		}
		return origDes(data)
	}
	_, err = opOrig.serverPreview()
	require.Error(t, err)
	dtqlDeserialize = origDes
	_, err = opOrig.serverPreview()
	require.NoError(t, err)

	// Lines 325-327: shift+enter when workspaceMutationPending
	op, err := u.newResultOperation(false)
	require.NoError(t, err)
	op.selected = []int{0}
	prev, prevErr := op.serverPreview()
	require.NoError(t, prevErr)
	rev := &resultServerPreviewOverlay{operation: op, preview: prev}
	u.workspaceMutationPending = true
	_, _, _ = rev.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	require.Contains(t, rev.err, "Another result operation is still running")
	u.workspaceMutationPending = false
}

// Not parallel: tests operation and inspector gap branches.
func TestCoverage100_FinalStretch_OperationsAndInspector(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: count}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{
			Columns:     []string{"count"},
			ColumnTypes: map[string]string{"count": "INTEGER"},
			Rows:        []secureread.Row{{Data: map[string]any{"count": int64(1)}}},
		},
	})

	// inspector_related_workspace.go:90 (receiveRelated when relatedOpen < 0)
	p := u.workspace
	p.relatedSequence = 42
	p.relatedOpen = -1
	p.ui.sessionID = "sess1"
	p.ui.workspaceGeneration = 1
	g, record, _ := u.activeGrid()
	msg := inspectorRelatedMsg{
		operationID: 0,
		sequence:    42,
		sessionID:   "sess1",
		generation:  1,
		recordID:    record.ID,
		rowKey:      g.sourceRowKey(),
	}
	cmd := p.receiveRelated(msg)
	require.Nil(t, cmd)

	// inspector_related_workspace.go:349-350 (joinPickerOverlay.Update with ModShift)
	j := &joinPickerOverlay{}
	_, _, handled := j.Update(tea.KeyPressMsg{Text: "enter", Mod: tea.ModShift})
	require.False(t, handled)

	// result_operations.go:136-137 (quickServerSort bounds on empty columns)
	uEmpty := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "empty",
		DTQL:   "from: {name: empty}\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{}},
	})
	require.Nil(t, uEmpty.quickServerSort(false))

	// result_operations.go:379-380 (resultOperationOverlay.Update with ModShift enter)
	op, err := u.newResultOperation(false)
	require.NoError(t, err)
	_, _, handled = op.Update(tea.KeyPressMsg{Text: "enter", Mod: tea.ModShift})
	require.False(t, handled)

	// result_operations.go:516-518 (applyLoaded reorder error)
	opBad, err := u.newResultOperation(false)
	require.NoError(t, err)
	opBad.capture = &loadedResultCapture{
		snapshot:    opBad.capture.snapshot,
		columns:     opBad.capture.columns,
		columnNames: opBad.capture.columnNames,
		rows:        []grid.Row{{Key: "999"}},
	}
	opBad.selected = []int{0}
	opBad.applyLoaded()
	require.NotEmpty(t, opBad.err)

	// result_operations.go:540-541 (applyLoaded grouping with column named "count")
	opCount, err := u.newResultOperation(true)
	require.NoError(t, err)
	opCount.count = true
	opCount.selected = []int{0}
	opCount.applyLoaded()
	pGroup, ok := u.shell.CenterPanel().(*localGroupsPanel)
	require.True(t, ok)
	require.Equal(t, "Rows in group", pGroup.model.Columns()[1].Name)

	// workspace_query_editor.go:139 (workspaceQueryEditor.Update with ModShift enter)
	wqe := &workspaceQueryEditor{ui: u}
	_, _ = wqe.Update(tea.KeyPressMsg{Text: "enter", Mod: tea.ModShift})
}

type mockExecActiveSwitch struct {
	mockExactExec
	sess *SessionChat
}

func (m *mockExecActiveSwitch) RunDTQL(ctx context.Context, source string, doc []byte, params map[string]any) (secureread.Result, error) {
	if m.sess != nil {
		m.sess.activeID = "switched-to-other-session"
	}
	return secureread.Result{}, nil
}

type stepCancelCtx struct {
	context.Context
	mu       sync.Mutex
	calls    int
	cancelAt int
}

func (s *stepCancelCtx) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls >= s.cancelAt {
		return context.Canceled
	}
	return nil
}

type fakeCondition struct{}

func (fakeCondition) Operator() dal.Operator { return "" }
func (fakeCondition) String() string         { return "" }

type mockCloseOnEvidence struct {
	mockExactExec
	store *SessionStore
}

func (m *mockCloseOnEvidence) CanReadWholeCollection(ctx context.Context, col string) error {
	if m.store != nil && m.store.db != nil {
		_ = m.store.db.Close()
	}
	return nil
}

// Not parallel: tests result_columns.go gap branches.
func TestCoverage100_FinalStretch_ResultColumns(t *testing.T) {
	recValid := RecordSet{
		DTQL: "from: {name: users}\ncolumns: [{field: id}]\n",
	}
	metaValid := resultColumnMetadata{
		tables: map[string]resultColumnTable{
			"main.users": {columns: []string{"id", "name"}},
		},
	}
	admitted := map[string]bool{"main.users": true}
	candID := columnCandidateID(recValid, "main.users", "name", "")
	ids := []string{candID}

	origDes := dtqlDeserialize
	defer func() { dtqlDeserialize = origDes }()

	// Line 337: resultColumnQuery error in planResultColumns (call 2 fails)
	callCount := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		callCount++
		if callCount > 1 {
			return nil, errors.New("second parse failed")
		}
		return origDes(data)
	}
	_, err := planResultColumns(recValid, metaValid, admitted, ids)
	require.Error(t, err)
	dtqlDeserialize = origDes

	// Line 362: qualifyJoinCondition where error
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		col := dal.NewQualifiedRootCollectionRef("main", "users", "")
		q := dal.From(col).NewQuery().Where(dal.NewComparison(badAgg{}, dal.Equal, dal.NewConstant(1))).SelectColumns(dal.Column{Expression: dal.NewFieldRef("", "id")})
		return q, nil
	}
	_, err = planResultColumns(recValid, metaValid, admitted, ids)
	require.Error(t, err)
	dtqlDeserialize = origDes

	// Line 366: qualifyColumnHaving having error
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		col := dal.NewQualifiedRootCollectionRef("main", "users", "")
		q := dal.From(col).NewQuery().GroupBy(dal.NewFieldRef("", "id")).Having(fakeCondition{}).SelectColumns(dal.Column{Expression: dal.NewFieldRef("", "id")})
		return q, nil
	}
	_, err = planResultColumns(recValid, metaValid, admitted, ids)
	require.Error(t, err)
	dtqlDeserialize = origDes

	// Line 447: dtqlDeserialize(doc) error at end of planResultColumns
	callIdx := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		callIdx++
		if callIdx >= 3 {
			return nil, errors.New("final parse failed")
		}
		return origDes(data)
	}
	_, err = planResultColumns(recValid, metaValid, admitted, ids)
	require.Error(t, err)
	dtqlDeserialize = origDes

	// Setup real SQLite database for executeResultColumns tests
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "res_col.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users VALUES (1, 'Alice');`)
	require.NoError(t, err)
	_ = db.Close()

	origCheck := resultColCheckSourceFile
	origOpen := resultColOpenDB
	defer func() {
		resultColCheckSourceFile = origCheck
		resultColOpenDB = origOpen
	}()
	resultColCheckSourceFile = func(path string) error { return nil }
	resultColOpenDB = func(driver, dsn string) (*sql.DB, error) {
		return sql.Open("sqlite", dbPath)
	}

	uReal := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	sessReal := uReal.sessions
	recReal := uReal.snapshot.RecordSets[uReal.lastGridRecordSetID]
	sessReal.store.info.Sources[recReal.Database] = recReal.Source
	sessReal.ConfigureQueryExecutor(mockExactExec{})

	// Line 481: resultColumnQuery error in resultColumnEvidence
	recBadDTQL := RecordSet{
		Database: recReal.Database,
		Source:   recReal.Source,
		DTQL:     "invalid: dtql: {{{",
	}
	_, _, err = sessReal.resultColumnEvidence(context.Background(), recBadDTQL)
	require.Error(t, err)

	// Lines 550, 554, 564 in executeResultColumns
	rows, err := sessReal.ResultColumns(context.Background(), recReal.ID)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	var colIDs []string
	for _, r := range rows {
		if r.Addable {
			colIDs = []string{r.ID}
			break
		}
	}
	require.NotEmpty(t, colIDs)

	// Line 564: Active session changed during RunDTQL
	mockExecWithCallback := &mockExecActiveSwitch{sess: sessReal}
	sessReal.ConfigureQueryExecutor(mockExecWithCallback)
	prevCol, err := sessReal.resultColumnsPreview(context.Background(), sessReal.activeID, recReal.ID, colIDs)
	require.NoError(t, err)
	_, _, err = sessReal.executeResultColumns(context.Background(), sessReal.activeID, recReal.ID, colIDs, prevCol.Fingerprint, "")
	require.Equal(t, ErrActiveSessionChanged, err)

	// Line 554: Context canceled after preview
	sessReal.activeID = uReal.sessionID
	sessReal.ConfigureQueryExecutor(mockExactExec{})
	stepCtx := &stepCancelCtx{Context: context.Background(), cancelAt: 2}
	_, _, err = sessReal.executeResultColumns(stepCtx, sessReal.activeID, recReal.ID, colIDs, prevCol.Fingerprint, "")
	require.Error(t, err)

	// Line 550: store.Load error when store DB is closed during preview
	uClosed := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	sessClosed := uClosed.sessions
	recClosed := uClosed.snapshot.RecordSets[uClosed.lastGridRecordSetID]
	sessClosed.store.info.Sources[recClosed.Database] = recClosed.Source
	sessClosed.ConfigureQueryExecutor(mockExactExec{})
	rowsClosed, err := sessClosed.ResultColumns(context.Background(), recClosed.ID)
	require.NoError(t, err)
	var colIDsClosed []string
	for _, r := range rowsClosed {
		if r.Addable {
			colIDsClosed = []string{r.ID}
			break
		}
	}
	require.NotEmpty(t, colIDsClosed)
	prevColClosed, err := sessClosed.resultColumnsPreview(context.Background(), sessClosed.activeID, recClosed.ID, colIDsClosed)
	require.NoError(t, err)
	closer := &mockCloseOnEvidence{store: sessClosed.store}
	sessClosed.ConfigureQueryExecutor(closer)
	_, _, err = sessClosed.executeResultColumns(context.Background(), sessClosed.activeID, recClosed.ID, colIDsClosed, prevColClosed.Fingerprint, "")
	require.Error(t, err)
}
