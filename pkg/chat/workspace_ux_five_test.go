package chat

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func uxKey(t *testing.T, u *ChatUI, key string) {
	t.Helper()
	_, cmd := u.shell.Update(uitest.Key(key))
	drainCmd(t, u, cmd)
	u.shell.View()
}
func uxUI(t *testing.T) (*ChatUI, RecordSet) {
	t.Helper()
	sessions, parent := derivedFixture(t)
	u, err := NewSessionChatUI(t.Context(), sessions, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	u.shell.FocusEntry(u.lastGridEntryID)
	return u, parent
}
func uxSearchAction(t *testing.T, u *ChatUI, search string) {
	t.Helper()
	uxKey(t, u, "ctrl+p")
	for _, r := range search {
		uxKey(t, u, string(r))
	}
	uxKey(t, u, "enter")
}

func TestWorkspaceUXScopeLocalServerAndRetainedTrail(t *testing.T) {
	u, parent := uxUI(t)
	g := u.gridsByRecordSetID[parent.ID]
	if view := ansi.Strip(g.View(80, true)); !strings.Contains(view, "Server · 2 rows") || !strings.Contains(view, "run "+parent.CreatedAt.Local().Format("15:04:05")) {
		t.Fatalf("server provenance missing: %s", view)
	}
	uxKey(t, u, "right")
	uxKey(t, u, "right")
	uxKey(t, u, "shift+alt+down")
	if !strings.Contains(ansi.Strip(g.View(80, true)), "Loaded · 2 rows") {
		t.Fatal("local sort did not change scope")
	}
	// Local group/member panels are retained separately, never overwriting origin.
	uxKey(t, u, "alt+g")
	uxKey(t, u, "2")
	uxKey(t, u, "enter")
	groups, ok := u.shell.CenterPanel().(*localGroupsPanel)
	if !ok {
		t.Fatal("group panel missing")
	}
	groupID := groups.trailID
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Loaded") {
		t.Fatal("local group scope missing")
	}
	uxKey(t, u, "enter")
	members := u.shell.CenterPanel().(*localGroupsPanel)
	if members == groups || !members.members {
		t.Fatal("members destroyed retained groups")
	}
	uxKey(t, u, "alt+left")
	if u.shell.CenterPanel() != groups {
		t.Fatal("back did not restore same groups")
	}
	uxKey(t, u, "alt+left")
	if u.activeRecordSetID() != parent.ID || u.gridsByRecordSetID[parent.ID] != g {
		t.Fatal("origin instance lost")
	}
	uxKey(t, u, "alt+right")
	if u.shell.CenterPanel() != groups {
		t.Fatal("forward did not restore groups")
	}
	uxKey(t, u, "ctrl+t")
	uxKey(t, u, "enter")
	if u.currentTrailID() != groupID {
		t.Fatal("current trail revisit moved node")
	}
	uxKey(t, u, "esc")
	if u.shell.CenterPanel() == groups {
		t.Fatal("idempotent trail visit corrupted Back closure")
	}
	u.shell.ShowTranscript()
	u.shell.FocusEntry(g.entryID)
	// Reuse exact dialog execution and secure SQLite path for a server child.
	uxKey(t, u, "ctrl+shift+down")
	uxKey(t, u, "shift+enter")
	childID := u.activeRecordSetID()
	if childID == parent.ID || len(u.snapshot.RecordSets) != 2 {
		t.Fatalf("server preview did not execute immutable child: %s", u.workspaceFeedback)
	}
	if view := ansi.Strip(u.shell.View().Content); !strings.Contains(view, "Server · 2 rows") {
		t.Fatalf("server child scope missing: %s", view)
	}
	if u.gridsByRecordSetID[parent.ID] != g || !strings.HasPrefix(g.scopeLabel, "Loaded") {
		t.Fatal("server child altered local origin")
	}
	uxKey(t, u, "alt+left")
	if u.activeRecordSetID() != parent.ID || g.SelectedColumn() != 2 {
		t.Fatal("server origin state lost")
	}
	uxKey(t, u, "alt+right")
	if u.activeRecordSetID() != childID {
		t.Fatal("server forward missing")
	}
}

type uxDelayedExecutor struct {
	started chan context.Context
	release chan struct{}
}

func (e *uxDelayedExecutor) RunDTQL(ctx context.Context, _ string, _ []byte, _ map[string]any) (secureread.Result, error) {
	e.started <- ctx
	<-e.release
	return secureread.Result{Columns: []string{"Id", "Category", "Value"}, Rows: []secureread.Row{{Data: map[string]any{"Id": int64(2), "Category": "A", "Value": int64(20)}}}}, nil
}
func TestWorkspaceUXRunningCancelLateResponseAndContext(t *testing.T) {
	for _, mouse := range []bool{false, true} {
		t.Run(fmt.Sprint(mouse), func(t *testing.T) {
			u, parent := uxUI(t)
			executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
			u.sessions.ConfigureQueryExecutor(executor)
			uxKey(t, u, "ctrl+shift+down")
			_, cmd := u.shell.Update(uitest.Key("shift+enter"))
			if cmd == nil || u.operation == nil {
				t.Fatal("server operation not tracked")
			}
			done := make(chan tea.Msg, 1)
			go func() { done <- cmd() }()
			var ctx context.Context
			select {
			case ctx = <-executor.started:
			case <-time.After(time.Second):
				t.Fatal("executor not started")
			}
			// UI remains responsive despite the session mutex held by the executor.
			u.operation.started = time.Now().Add(-3 * time.Second)
			u.shell.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			header := strings.Split(ansi.Strip(u.shell.View().Content), "\n")[0]
			if !strings.Contains(header, "3s") || !strings.Contains(header, "Cancel [Ctrl+X]") {
				t.Fatalf("running/cancel not visible at80: %s", header)
			}
			uxKey(t, u, "f4")
			u.shell.SetComposerText("newer Ω draft")
			uxKey(t, u, "left")
			caret := u.shell.ComposerCursor()
			if mouse {
				header = strings.Split(ansi.Strip(u.shell.View().Content), "\n")[0]
				at := strings.Index(header, "Cancel [Ctrl+X]")
				_, follow := u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: ansi.StringWidth(header[:at]) + 1, Y: 0})
				drainCmd(t, u, follow)
			} else {
				uxKey(t, u, "ctrl+x")
			}
			if ctx.Err() == nil || u.operation != nil || u.workspaceMutationPending {
				t.Fatal("cancel did not release UI/context")
			}
			close(executor.release)
			select {
			case msg := <-done:
				u.shell.Update(msg)
			case <-time.After(time.Second):
				t.Fatal("late result missing")
			}
			snapshot, err := u.sessions.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.RecordSets) != 1 || snapshot.RecordSets[parent.ID].ID == "" {
				t.Fatal("canceled output persisted")
			}
			if u.shell.WorkspaceFocus() != "input" || u.shell.ComposerText() != "newer Ω draft" || u.shell.ComposerCursor() != caret {
				t.Fatal("late completion clobbered newer composer")
			}
		})
	}
}
func TestWorkspaceUXActionsDiagnosticAndEditorIsolation(t *testing.T) {
	u, parent := uxUI(t)
	uxKey(t, u, "f4")
	u.shell.SetComposerText("p/t Ω unchanged")
	uxKey(t, u, "left")
	caret := u.shell.ComposerCursor()
	uxSearchAction(t, u, "keyboard")
	for _, key := range []string{"ctrl+c", "f4", "ctrl+shift+up", "shift+alt+down", "enter"} {
		uxKey(t, u, key)
	}
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "Keyboard diagnostic") || !strings.Contains(view, "ctrl+c") || !strings.Contains(view, "code=") {
		t.Fatalf("diagnostic missing received keys: %s", view)
	}
	if len(u.snapshot.RecordSets) != 1 || u.operation != nil {
		t.Fatal("diagnostic executed product action")
	}
	uxKey(t, u, "esc")
	if u.shell.ComposerText() != "p/t Ω unchanged" || u.shell.ComposerCursor() != caret || u.shell.WorkspaceFocus() != "input" {
		t.Fatal("diagnostic return lost composer")
	}
	uxKey(t, u, "f1")
	uxKey(t, u, "ctrl+p")
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Actions · type to search") {
		t.Fatal("F1 alternative did not reach actions")
	}
	uxKey(t, u, "esc")
	object := u.catalog.Objects[0]
	drainCmd(t, u, u.openWorkspaceQuery(object, false))
	editor := u.queryEditor
	before := editor.text.Value()
	uxKey(t, u, "p")
	uxKey(t, u, "t")
	uxKey(t, u, "ctrl+t")
	if u.shell.CenterPanel() != editor || u.shell.ActiveOverlayHints() != nil || editor.text.Value() == before {
		t.Fatal("literal/editor key ownership lost")
	}
	uxSearchAction(t, u, "cancel")
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "No running server operation") {
		t.Fatal("disabled action reason missing")
	}
	uxKey(t, u, "esc")
	u.shell.ShowTranscript()
	u.shell.FocusEntry(u.gridsByRecordSetID[parent.ID].entryID)
	uxSearchAction(t, u, "sort loaded column descending")
	if !strings.HasPrefix(u.gridsByRecordSetID[parent.ID].scopeLabel, "Loaded") {
		t.Fatal("searched action did not dispatch local sort")
	}
}
func TestWorkspaceUXTrailBranchPreservesGridSearchScrollAndReconcile(t *testing.T) {
	names := []string{"Id", "Name"}
	for i := range 8 {
		names = append(names, fmt.Sprintf("WideColumn%d", i))
	}
	rows := make([]secureread.Row, 35)
	for i := range rows {
		data := map[string]any{"Id": i, "Name": fmt.Sprintf("row%02d", i)}
		for _, name := range names[2:] {
			data[name] = "long retained cell"
		}
		rows[i] = secureread.Row{Data: data}
	}
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Origin", DTQL: "from: {name: Item}\nlimit: 35", Result: secureread.Result{Columns: names, Rows: rows}})
	origin := u.activeRecordSetID()
	g := u.gridsByRecordSetID[origin]
	for range len(names) - 1 {
		uxKey(t, u, "right")
	}
	uxKey(t, u, "r")
	uxKey(t, u, "o")
	uxKey(t, u, "w")
	uxKey(t, u, "enter")
	for range 24 {
		uxKey(t, u, "down")
	}
	gridWidth := g.Width()
	retainedView := ansi.Strip(g.View(gridWidth, true))
	row, col, offset := g.sourceRowKey(), g.SelectedColumn(), g.ColumnOffset()
	if offset == 0 {
		t.Fatal("fixture never exercised horizontal scrolling")
	}
	// Real durable additions force same-session reconciliation; controls retain identity.
	for _, label := range []string{"Branch one", "Branch two"} {
		m, err := u.sessions.store.AppendUser(t.Context(), u.sessionID, label)
		if err != nil {
			t.Fatal(err)
		}
		_, err = u.sessions.store.AppendQuery(t.Context(), u.sessionID, m.ID, "sqlite:///fixture.db", QueryResult{Title: label, DTQL: "from: {name: Item}\nlimit: 1", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}}}, Lineage: &JoinLineage{ParentRecordSetID: origin}})
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := u.sessions.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	u.shell.FocusEntry(u.gridsByRecordSetID[origin].entryID)
	if u.gridsByRecordSetID[origin] != g || g.sourceRowKey() != row || g.SelectedColumn() != col || g.ColumnOffset() != offset || ansi.Strip(g.View(gridWidth, true)) != retainedView {
		t.Fatalf("reconciliation changed state: row %s/%s col %d/%d offset %d/%d width %d/%d\nbefore=%s\nafter=%s", row, g.sourceRowKey(), col, g.SelectedColumn(), offset, g.ColumnOffset(), gridWidth, g.Width(), retainedView, ansi.Strip(g.View(gridWidth, true)))
	}
	uxKey(t, u, "ctrl+t")
	uxKey(t, u, "down")
	uxKey(t, u, "enter")
	branch1 := u.activeRecordSetID()
	uxKey(t, u, "alt+left")
	if u.activeRecordSetID() != origin {
		t.Fatal("branch back failed")
	}
	uxKey(t, u, "ctrl+t")
	uxKey(t, u, "down")
	uxKey(t, u, "down")
	uxKey(t, u, "enter")
	branch2 := u.activeRecordSetID()
	if branch1 == branch2 || branch2 == origin {
		t.Fatal("branch picker did not visit distinct child")
	}
	uxKey(t, u, "alt+left")
	uxKey(t, u, "alt+right")
	if u.activeRecordSetID() != branch2 {
		t.Fatal("forward followed abandoned branch")
	}
	u.trailNodes[origin].parent = branch2
	if path := u.trailPath(branch2); !strings.Contains(path, "[cycle]") {
		t.Fatal("cyclic lineage unbounded")
	}
	u.trailNodes[origin].parent = "missing"
	if path := u.trailPath(origin); !strings.Contains(path, "unavailable parent") {
		t.Fatal("missing lineage falsely resolved")
	}
}

func TestWorkspaceUXAggregateExplanationsFollowSchemaAndOverrides(t *testing.T) {
	names := []string{"Key", "Customer", "Quantity", "Total", "Unknown"}
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Measures", DTQL: "from: {schema: main, name: Item}\ncolumns: [{field: Key},{field: Customer},{field: Quantity},{field: Total},{field: Unknown}]\nlimit: 1", Result: secureread.Result{Columns: names, Rows: []secureread.Row{{Data: map[string]any{"Key": 1, "Customer": 2, "Quantity": 3, "Total": 1.5, "Unknown": 4}}}}})
	_, record, _ := u.activeGrid()
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", SourceID: record.Database, ObjectID: "main.Item"}, Columns: names, PrimaryKey: []string{"Key"}, ColumnTypes: map[string]string{"Key": "INTEGER", "Customer": "INTEGER", "Quantity": "INTEGER", "Total": "REAL", "Unknown": "INTEGER"}}}}
	u.sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: record.Source, Snapshot: ForeignKeySnapshot{Source: record.Source, Columns: map[string][]string{"main.item": names}, Keys: []ForeignKey{{Schema: "main", FromRelation: "Item", FromFields: []string{"Customer"}}}}})
	uxKey(t, u, "alt+g")
	for _, step := range []struct {
		down int
		want string
	}{{0, "primary key"}, {1, "foreign key"}, {1, "non-key integer"}, {1, "authoritative float type"}} {
		for range step.down {
			uxKey(t, u, "down")
		}
		if view := flattenView(u.shell.View().Content); !strings.Contains(view, step.want) {
			t.Fatalf("schema explanation missing %s: %s", step.want, view)
		}
	}
	uxKey(t, u, "c")
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "COUNT · manual choice") {
		t.Fatalf("manual explanation absent: %s", view)
	}
	uxKey(t, u, "space")
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "Grouping key · excluded") {
		t.Fatal("key exclusion missing")
	}
	uxKey(t, u, "space")
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "COUNT · manual choice") {
		t.Fatal("deliberate override lost after grouping key change")
	}
	uxKey(t, u, "esc")
	u.sessions.joinApplication = nil
	uxKey(t, u, "alt+g")
	for range 2 {
		uxKey(t, u, "down")
	}
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "non-key status is unknown") {
		t.Fatalf("unknown-key evidence overclaimed: %s", view)
	}
	uxKey(t, u, "esc")
}

type uxIgnoringJoin struct {
	started chan struct{}
	release chan struct{}
}

func (*uxIgnoringJoin) Candidates(context.Context, RecordSet) ([]JoinCandidate, error) {
	return []JoinCandidate{{ID: "x", Target: RelationInstance{Relation: "Customer"}}}, nil
}
func (a *uxIgnoringJoin) Apply(_ context.Context, r RecordSet, _ JoinCandidateID) (QueryResult, error) {
	close(a.started)
	<-a.release
	return QueryResult{Title: r.Title + " + Customer", DTQL: r.DTQL, Source: r.Source, Result: r.Result, Lineage: &JoinLineage{ParentRecordSetID: r.ID, CandidateID: "x"}}, nil
}

type uxDelayedSaved struct {
	savedQueryStub
	started chan context.Context
	release chan struct{}
}

func (s *uxDelayedSaved) Run(ctx context.Context, _ string) (QueryResult, error) {
	s.started <- ctx
	<-s.release
	return QueryResult{Title: "Saved", DTQL: "from: {name: Item}\nlimit: 1", Source: "sqlite:///fixture.db", Result: secureread.Result{Columns: []string{"Id"}}}, nil
}
func uxAsyncResult(cmd tea.Cmd) tea.Msg {
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return batch[len(batch)-1]()
	}
	return msg
}
func TestWorkspaceUXExistingQueryPathsCancelBeforePersistence(t *testing.T) {
	for _, path := range []string{"editor", "refresh", "saved", "join"} {
		t.Run(path, func(t *testing.T) {
			u, parent := uxUI(t)
			original := u.gridsByRecordSetID[parent.ID]
			executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
			u.sessions.ConfigureQueryExecutor(executor)
			var cmd tea.Cmd
			var started <-chan struct{}
			var ctxStarted <-chan context.Context
			release := executor.release
			switch path {
			case "editor":
				cmd = u.openWorkspaceQuery(u.catalog.Objects[0], true)
				ctxStarted = executor.started
			case "refresh":
				_, cmd = u.shell.Update(uitest.Key("ctrl+r"))
				ctxStarted = executor.started
			case "saved":
				s := &uxDelayedSaved{savedQueryStub: savedQueryStub{queries: []SavedQuery{{ID: "slow", Title: "Slow"}}}, started: make(chan context.Context, 1), release: release}
				u.savedQueryService = s
				cmd = u.Submit("/query slow")
				ctxStarted = s.started
			case "join":
				app := &uxIgnoringJoin{make(chan struct{}), make(chan struct{})}
				release = app.release
				started = app.started
				u.sessions.ConfigureJoinApplication(app)
				uxKey(t, u, "alt+j")
				_, cmd = u.shell.Update(uitest.Key("enter"))
			}
			if cmd == nil || u.operation == nil {
				t.Fatal("reachable query path untracked")
			}
			done := make(chan tea.Msg, 1)
			go func() { done <- uxAsyncResult(cmd) }()
			if started != nil {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("JOIN not started")
				}
			} else {
				select {
				case <-ctxStarted:
				case <-time.After(time.Second):
					t.Fatal("query not started")
				}
			}
			uxKey(t, u, "ctrl+x")
			close(release)
			select {
			case msg := <-done:
				u.shell.Update(msg)
			case <-time.After(time.Second):
				t.Fatal("canceled completion missing")
			}
			snapshot, err := u.sessions.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.RecordSets) != 1 || u.gridsByRecordSetID[parent.ID] != original || u.operation != nil || u.shell.Busy() {
				t.Fatal("canceled path persisted/selected output or retained Busy")
			}
		})
	}
}

func TestWorkspaceUXCanceledCompletionCannotClearNewOperation(t *testing.T) {
	u, parent := uxUI(t)
	executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
	u.sessions.ConfigureQueryExecutor(executor)
	oldCmd := u.openWorkspaceQuery(u.catalog.Objects[0], true)
	done := make(chan tea.Msg, 1)
	go func() { done <- oldCmd() }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	uxKey(t, u, "ctrl+x")
	close(executor.release)
	var late tea.Msg
	select {
	case late = <-done:
	case <-time.After(time.Second):
		t.Fatal("operation did not return")
	}
	// The canceled backend is fenced until its completion is observed.
	if next := u.openWorkspaceQuery(u.catalog.Objects[0], true); next != nil {
		t.Fatal("new query accepted while canceled backend still unobserved")
	}
	u.shell.Update(late)
	// A real second Run is accepted after completion, and a duplicate old message cannot clear it.
	next := u.openWorkspaceQuery(u.catalog.Objects[0], true)
	if next == nil || u.operation == nil {
		t.Fatal("second query not accepted")
	}
	id := u.operation.id
	u.shell.Update(late)
	if u.operation == nil || u.operation.id != id || !u.workspaceMutationPending || u.activeRecordSetID() != parent.ID {
		t.Fatal("old completion cleared/selected newer operation")
	}
	uxKey(t, u, "ctrl+x")
}

func TestWorkspaceUXRelatedCancelAndCompactHiddenToolbar(t *testing.T) {
	u := reviewJoinUI(t)
	_, record, _ := u.activeGrid()
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", SourceID: record.Database, ObjectID: "main.Invoice"}, Columns: record.Result.Columns, ColumnTypes: map[string]string{"InvoiceId": "INTEGER", "CustomerId": "INTEGER"}, PrimaryKey: []string{"InvoiceId"}}}}
	executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
	u.sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: executor})
	_, cmd := u.shell.Update(uitest.Key("f10"))
	if cmd == nil || u.operation == nil {
		t.Fatal("Related not tracked")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("Related query did not start")
	}
	uxKey(t, u, "alt+f1")
	header := strings.Split(ansi.Strip(u.shell.View().Content), "\n")[0]
	if !strings.Contains(header, "Cancel [Ctrl+X]") || !strings.Contains(header, "F1 Help") || !strings.Contains(header, "API ") {
		t.Fatalf("compact protected controls cropped: %s", header)
	}
	uxKey(t, u, "ctrl+x")
	close(executor.release)
	select {
	case msg := <-done:
		u.shell.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("Related completion missing")
	}
	if u.operation != nil || u.workspace.relatedLoading || len(u.workspace.relatedRecords) > 0 || len(u.snapshot.RecordSets) != 1 {
		t.Fatal("canceled Related response changed current result")
	}
	if !strings.Contains(u.workspaceFeedback, "canceled") {
		t.Fatal("cancellation feedback lost")
	}
	uxKey(t, u, "f4") // dismiss the transient feedback before measuring the hotkey toolbar
	for _, width := range []int{80, 100, 150} {
		u.toolbarHidden = false
		u.shell.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		u.shell.ShowTranscript()
		u.shell.FocusEntry(u.lastGridEntryID)
		if rows := strings.Count(u.statusBar(width), "\n") + 1; rows > 2 {
			t.Fatalf("toolbar grew at%d: %d rows", width, rows)
		}
	}
}

func TestWorkspaceUXLocalPanelToolbarAndActionsOwnVisibleContext(t *testing.T) {
	u, parent := uxUI(t)
	uxKey(t, u, "alt+g")
	uxKey(t, u, "2")
	uxKey(t, u, "enter")
	local := u.shell.CenterPanel().(*localGroupsPanel)
	for _, width := range []int{80, 150} {
		u.shell.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		status := u.statusBar(width)
		if strings.Contains(status, "JOIN") || strings.Contains(status, "Alt+G") || strings.Contains(status, "Alt+S") || !strings.Contains(status, "Trail") || !strings.Contains(status, "Esc back") {
			t.Fatalf("local panel advertised origin controls at%d: %s", width, status)
		}
	}
	uxSearchAction(t, u, "sort loaded column descending")
	if !strings.Contains(flattenView(u.shell.View().Content), "Return to the source grid") {
		t.Fatal("hidden-origin operation advertised enabled")
	}
	if u.shell.CenterPanel() != local || u.operation != nil || !strings.HasPrefix(u.gridsByRecordSetID[parent.ID].scopeLabel, "Server") {
		t.Fatal("palette operated on hidden origin")
	}
	uxKey(t, u, "esc")
	uxKey(t, u, "alt+left")
	if u.activeRecordSetID() != parent.ID || u.shell.WorkspaceFocus() != "transcript" {
		t.Fatal("local return was disabled")
	}
}

func TestWorkspaceUXUnprovenImportedSnapshotStaysLoaded(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Imported snapshot", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}}}})
	_, record, _ := u.activeGrid()
	if record.CreatedAt.IsZero() {
		t.Fatal("fixture lacks stored timestamp")
	}
	snapshot := u.snapshot
	snapshot.Queries = nil
	unknown := NewChatUI(t.Context(), nil, "fixture")
	unknown.sessions = u.sessions
	unknown.loadSession(snapshot)
	unknown.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	unknown.shell.FocusEntry(unknown.lastGridEntryID)
	view := ansi.Strip(unknown.shell.View().Content)
	if !strings.Contains(view, "Loaded · 1 rows") || strings.Contains(view, "Server ·") || strings.Contains(view, " · run ") {
		t.Fatalf("stored timestamp invented execution provenance: %s", view)
	}
}

func uxResponsive(t *testing.T, label string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("UI blocked in %s while backend held session mutex", label)
	}
}
func TestWorkspaceUXReviewBackendFenceBeforeAndAfterCancel(t *testing.T) {
	u, parent := uxUI(t)
	executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
	u.sessions.ConfigureQueryExecutor(executor)
	cmd := u.openWorkspaceQuery(u.catalog.Objects[0], true)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not acquire session mutex")
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(executor.release)
		}
	})
	for _, phase := range []string{"running", "cancel requested"} {
		uxResponsive(t, phase+" direct JOIN", func() { uxKey(t, u, "alt+j") })
		uxResponsive(t, phase+" duplicate action JOIN", func() { u.shell.PushOverlay(&gridActionsOverlay{ui: u, index: 14}); uxKey(t, u, "enter") })
		uxResponsive(t, phase+" bridge", func() { u.shell.Update(bridgeTickMsg{}); u.shell.View() })
		uxResponsive(t, phase+" sessions", func() { u.runCommand("/sessions"); u.openSessionPicker() })
		uxResponsive(t, phase+" context mutation", func() {
			_ = u.applyWorkspaceAction(WorkspaceAction{Kind: "bucket_clear"})
			_ = u.workspace.refreshBookmarks()
		})
		uxResponsive(t, phase+" browsing", func() {
			u.shell.FocusEntry(u.gridsByRecordSetID[parent.ID].entryID)
			uxKey(t, u, "down")
			uxKey(t, u, "f4")
			u.shell.SetComposerText("retained draft")
			uxKey(t, u, "left")
		})
		if phase == "running" {
			uxKey(t, u, "ctrl+x")
			if !u.backendBusy() {
				t.Fatal("Cancel falsely marked held backend idle")
			}
		}
	}
	if !strings.Contains(strings.Split(ansi.Strip(u.shell.View().Content), "\n")[0], "cancel requested") {
		t.Fatal("waiting backend not visibly identified")
	}
	close(executor.release)
	released = true
	select {
	case msg := <-done:
		u.shell.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("backend did not complete")
	}
	if u.backendBusy() || u.operation != nil || u.shell.ComposerText() != "retained draft" {
		t.Fatal("completion fence leaked or draft lost")
	}
}

func TestWorkspaceUXReviewComposerEditingDuringBackendAndCancel(t *testing.T) {
	u, parent := uxUI(t)
	executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
	u.sessions.ConfigureQueryExecutor(executor)
	cmd := u.openWorkspaceQuery(u.catalog.Objects[0], true)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	t.Cleanup(func() {
		select {
		case <-executor.release:
		default:
			close(executor.release)
		}
	})
	for _, phase := range []string{"running", "cancellation requested"} {
		uxKey(t, u, "f4")
		u.shell.SetComposerText("draft Ω")
		uxKey(t, u, "shift+enter")
		if u.shell.ComposerText() != "draft Ω\n" {
			t.Fatalf("%s: newline editing was fenced", phase)
		}
		caret := u.shell.ComposerCursor()
		uxKey(t, u, "tab")
		if !u.shell.ComposerActionFocused() {
			t.Fatalf("%s: Select text action not focused", phase)
		}
		uxResponsive(t, phase+" Select text", func() { uxKey(t, u, "enter") })
		if !strings.Contains(ansi.Strip(u.shell.View().Content), "arrows extend") {
			t.Fatalf("%s: Select text did not open", phase)
		}
		uxKey(t, u, "esc")
		if u.shell.ComposerText() != "draft Ω\n" || u.shell.ComposerCursor() != caret {
			t.Fatalf("%s: selection cancel changed draft or caret", phase)
		}
		uxKey(t, u, "enter")
		if u.shell.ComposerText() != "draft Ω\n" || !u.backendBusy() || len(u.snapshot.RecordSets) != 1 {
			t.Fatalf("%s: attempted Submit replaced draft/backend", phase)
		}
		u.shell.SetComposerText("/sess")
		uxKey(t, u, "enter")
		if u.shell.ComposerText() != "/sessions " {
			t.Fatalf("%s: command completion was fenced: %q", phase, u.shell.ComposerText())
		}
		u.shell.SetComposerText("retained draft")
		if phase == "running" {
			uxKey(t, u, "ctrl+x")
		}
	}
	close(executor.release)
	select {
	case msg := <-done:
		u.shell.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("backend completion missing")
	}
	if u.activeRecordSetID() != parent.ID || u.shell.ComposerText() != "retained draft" || u.backendBusy() {
		t.Fatal("canceled backend disturbed the editing context")
	}
}

func TestWorkspaceUXReviewBridgeRetriesContendedNotification(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		t.Run(fmt.Sprintf("tracked=%v", tracked), func(t *testing.T) {
			u, parent := uxUI(t)
			origin := u.gridsByRecordSetID[parent.ID]
			uxKey(t, u, "down")
			uxKey(t, u, "f4")
			u.shell.SetComposerText("browser retained Ω")
			uxKey(t, u, "left")
			caret, row := u.shell.ComposerCursor(), origin.CurrentIndex()
			var operationID uint64
			if tracked {
				operationID, _ = u.beginOperation("Server fixture")
			}
			// BrowserSessionAction notifies before releasing this same mutex.
			u.sessions.mu.Lock()
			locked := true
			t.Cleanup(func() {
				if locked {
					u.sessions.mu.Unlock()
				}
			})
			message, err := u.sessions.store.AppendUser(t.Context(), u.sessionID, "external browser request")
			if err != nil {
				t.Fatal(err)
			}
			query, err := u.sessions.store.AppendQuery(t.Context(), u.sessionID, message.ID, parent.Source, QueryResult{Title: "External child", DTQL: parent.DTQL, Result: parent.Result})
			if err != nil {
				t.Fatal(err)
			}
			u.sessions.notifyChanged()
			uxResponsive(t, "contended bridge notification", func() { u.shell.Update(bridgeTickMsg{}) })
			if !u.bridgeReconcilePending || u.gridsByRecordSetID[query.RecordSetID] != nil {
				t.Fatal("contended notification was dropped or blocked fence bypassed")
			}
			u.sessions.mu.Unlock()
			locked = false
			if tracked {
				u.cancelOperation(false)
				u.shell.Update(apiTickMsg{})
				if !u.bridgeReconcilePending || u.gridsByRecordSetID[query.RecordSetID] != nil {
					t.Fatal("Cancel consumed pending bridge before backend completion")
				}
				u.finishOperation(operationID)
			}
			// No second bridge notification: the existing bounded timer retries it.
			u.shell.Update(apiTickMsg{})
			if u.bridgeReconcilePending || u.gridsByRecordSetID[query.RecordSetID] == nil || u.trailNodes[query.RecordSetID] == nil {
				t.Fatal("pending browser child did not reconcile into results/trail")
			}
			if u.gridsByRecordSetID[parent.ID] != origin || origin.CurrentIndex() != row || u.shell.WorkspaceFocus() != "input" || u.shell.ComposerText() != "browser retained Ω" || u.shell.ComposerCursor() != caret {
				t.Fatal("bridge retry rebuilt or moved current input/result")
			}
			entries := len(u.trailNodes)
			u.shell.Update(apiTickMsg{})
			if len(u.trailNodes) != entries {
				t.Fatal("retry duplicated immutable child")
			}
		})
	}
}
func TestWorkspaceUXReviewMouseOnlyBackgroundSuccessEntersTrail(t *testing.T) {
	for _, path := range []string{"query", "derived", "saved", "refresh"} {
		t.Run(path, func(t *testing.T) {
			u, parent := uxUI(t)
			g := u.gridsByRecordSetID[parent.ID]
			u.shell.SetComposerText("mouse Ω retained")
			executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
			u.sessions.ConfigureQueryExecutor(executor)
			var cmd tea.Cmd
			started := executor.started
			switch path {
			case "query":
				cmd = u.openWorkspaceQuery(u.catalog.Objects[0], true)
			case "derived":
				uxKey(t, u, "ctrl+shift+down")
				_, cmd = u.shell.Update(uitest.Key("shift+enter"))
			case "refresh":
				_, cmd = u.shell.Update(uitest.Key("ctrl+r"))
			case "saved":
				service := &uxDelayedSaved{started: make(chan context.Context, 1), release: executor.release}
				u.savedQueryService = service
				cmd = u.runSavedQuery(SavedQuery{ID: "mouse", Title: "Mouse saved"}, nil)
				started = service.started
			}
			if cmd == nil {
				t.Fatal("command missing")
			}
			done := make(chan tea.Msg, 1)
			go func() { done <- uxAsyncResult(cmd) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("command not started")
			}
			// No keyboard interaction after start: click the actual rendered composer row.
			view := ansi.Strip(u.shell.View().Content)
			y := -1
			for i, line := range strings.Split(view, "\n") {
				if strings.Contains(line, "mouse Ω retained") {
					y = i
					break
				}
			}
			if y < 0 {
				t.Fatalf("composer draft not rendered: %s", view)
			}
			interaction := u.workspaceInteraction
			u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 30, Y: y})
			if u.shell.WorkspaceFocus() != "input" || u.workspaceInteraction != interaction {
				t.Fatal("fixture did not use mouse-only composer focus")
			}
			caret := u.shell.ComposerCursor()
			generation := u.workspaceGeneration
			close(executor.release)
			select {
			case msg := <-done:
				u.shell.Update(msg)
			case <-time.After(time.Second):
				t.Fatal("background success missing")
			}
			if u.workspaceGeneration != generation || u.shell.WorkspaceFocus() != "input" || u.shell.ComposerText() != "mouse Ω retained" || u.shell.ComposerCursor() != caret || u.gridsByRecordSetID[parent.ID] != g {
				t.Fatal("mouse-only newer context clobbered")
			}
			if len(u.snapshot.RecordSets) != 2 || len(u.trailNodes) < 2 {
				t.Fatal("successful background child missing from retained trail")
			}
			uxKey(t, u, "ctrl+p")
			for _, r := range "exploration trail" {
				uxKey(t, u, string(r))
			}
			uxKey(t, u, "enter")
			uxKey(t, u, "down")
			uxKey(t, u, "enter")
			if u.activeRecordSetID() == parent.ID || u.shell.WorkspaceFocus() != "transcript" {
				t.Fatal("background output not reachable without reload")
			}
			uxKey(t, u, "alt+left")
			if u.activeRecordSetID() != parent.ID || u.gridsByRecordSetID[parent.ID] != g {
				t.Fatal("background trail lost origin")
			}
		})
	}
}
func TestWorkspaceUXReviewLocalDirectActionsAndRepeatedEsc(t *testing.T) {
	u, parent := uxUI(t)
	executor := &fakeExecutor{}
	u.sessions.ConfigureQueryExecutor(executor)
	uxKey(t, u, "alt+g")
	uxKey(t, u, "2")
	uxKey(t, u, "enter")
	group := u.shell.CenterPanel().(*localGroupsPanel)
	for _, members := range []bool{false, true} {
		if members {
			uxKey(t, u, "enter")
		}
		for _, key := range []string{"alt+g", "alt+s", "ctrl+r"} {
			uxKey(t, u, key)
			if u.shell.ActiveOverlayHints() != nil || u.operation != nil || executor.calls != 0 {
				t.Fatalf("%s targeted hidden original", key)
			}
		}
		if members {
			uxKey(t, u, "esc")
			if u.shell.CenterPanel() != group {
				t.Fatal("member Esc did not return group")
			}
		}
	}
	uxKey(t, u, "esc")
	for range 3 {
		uxKey(t, u, "ctrl+t")
		uxKey(t, u, "down")
		uxKey(t, u, "enter")
		if u.shell.CenterPanel() != group {
			t.Fatal("trail did not retain same group")
		}
		uxKey(t, u, "esc")
		if u.shell.WorkspaceFocus() != "transcript" || u.activeRecordSetID() != parent.ID {
			t.Fatal("revisited Esc callback was consumed")
		}
	}
	u.loadSession(u.snapshot)
	u.shell.FocusEntry(u.gridsByRecordSetID[parent.ID].entryID)
	uxKey(t, u, "ctrl+t")
	uxKey(t, u, "down")
	uxKey(t, u, "enter")
	uxKey(t, u, "esc")
	if u.shell.WorkspaceFocus() != "transcript" || u.activeRecordSetID() != parent.ID {
		t.Fatal("reconciliation invalidated local Esc ancestry")
	}
	uxSearchAction(t, u, "sort column")
	if !strings.HasPrefix(u.gridsByRecordSetID[parent.ID].scopeLabel, "Loaded") || executor.calls != 0 {
		t.Fatal("searched legacy local sort retained Server scope or executed")
	}
}

func TestWorkspaceUXReviewHTTPMouseSuccessAndJoinBackgroundTrail(t *testing.T) {
	t.Run("HTTP mouse", func(t *testing.T) {
		u, parent := uxUI(t)
		u.shell.SetComposerText("http mouse retained")
		started, release := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":9}]`))
		}))
		t.Cleanup(server.Close)
		released := false
		t.Cleanup(func() {
			if !released {
				close(release)
			}
		})
		cmd, err := u.sendHTTPRequest(httpRequestSpec{Method: "GET", URL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- uxAsyncResult(cmd) }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("HTTP did not start")
		}
		y := -1
		for i, line := range strings.Split(ansi.Strip(u.shell.View().Content), "\n") {
			if strings.Contains(line, "http mouse retained") {
				y = i
				break
			}
		}
		if y < 0 {
			t.Fatal("draft missing")
		}
		u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 30, Y: y})
		caret, generation := u.shell.ComposerCursor(), u.workspaceGeneration
		close(release)
		released = true
		select {
		case msg := <-done:
			u.shell.Update(msg)
		case <-time.After(2 * time.Second):
			t.Fatal("HTTP missing completion")
		}
		if u.workspaceGeneration != generation || u.shell.WorkspaceFocus() != "input" || u.shell.ComposerCursor() != caret || u.shell.ComposerText() != "http mouse retained" {
			t.Fatal("HTTP mouse context was reset")
		}
		if len(u.trailNodes) != 2 || len(u.snapshot.RecordSets) != 2 || u.gridsByRecordSetID[parent.ID] == nil {
			t.Fatal("HTTP background grid/trail missing")
		}
	})
	t.Run("JOIN background", func(t *testing.T) {
		u := reviewJoinUI(t)
		parent := u.activeRecordSetID()
		app := &uxIgnoringJoin{make(chan struct{}), make(chan struct{})}
		u.sessions.ConfigureJoinApplication(app)
		uxKey(t, u, "alt+j")
		_, cmd := u.shell.Update(uitest.Key("enter"))
		done := make(chan tea.Msg, 1)
		go func() { done <- cmd() }()
		select {
		case <-app.started:
		case <-time.After(time.Second):
			t.Fatal("JOIN did not start")
		}
		uxKey(t, u, "f4")
		u.shell.SetComposerText("JOIN retained draft")
		close(app.release)
		select {
		case msg := <-done:
			u.shell.Update(msg)
		case <-time.After(time.Second):
			t.Fatal("JOIN did not finish")
		}
		if len(u.trailNodes) != 2 || u.shell.WorkspaceFocus() != "input" || u.shell.ComposerText() != "JOIN retained draft" {
			t.Fatal("JOIN background result missing or clobbered input")
		}
		uxSearchAction(t, u, "exploration trail")
		uxKey(t, u, "down")
		uxKey(t, u, "enter")
		if u.activeRecordSetID() == parent || u.activeRecordSetID() == "" {
			t.Fatal("background JOIN not reachable")
		}
		uxKey(t, u, "alt+left")
		if u.activeRecordSetID() != parent {
			t.Fatal("JOIN lineage lost origin")
		}
	})
}

func TestWorkspaceUXNativeSQLExecutionAndEmptyResultScope(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Native SQL", Source: "sqlite:///fixture.db", Result: secureread.Result{Columns: []string{"Id"}}})
	id := u.activeRecordSetID()
	record := u.snapshot.RecordSets[id]
	if id == "" || record.DTQL != "" || record.QueryID == "" {
		t.Fatal("native SQL fixture lacks empty-result execution provenance")
	}
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "Server · 0 rows") || !strings.Contains(view, " · run ") {
		t.Fatalf("native SQL execution mislabeled: %s", view)
	}
	uxKey(t, u, "f4")
	if u.activeRecordSetID() != id {
		t.Fatal("empty result identity lost on composer transfer")
	}
	uxSearchAction(t, u, "exploration trail")
	uxKey(t, u, "enter")
	if u.activeRecordSetID() != id {
		t.Fatal("empty result unreachable from trail")
	}
}

func TestWorkspaceUXReviewLoadedRelatedCannotReplaceRefresh(t *testing.T) {
	u, parent := uxUI(t)
	g := u.gridsByRecordSetID[parent.ID]
	p := u.workspace
	p.relatedRecordID, p.relatedRowKey, p.relatedSessionID, p.relatedGeneration = parent.ID, g.sourceRowKey(), u.sessionID, u.workspaceGeneration
	p.relatedRecords = []relatedRecord{{key: ForeignKey{ToRelation: "Child"}, document: "from: {name: Child}\nlimit: 1", result: secureread.Result{Columns: []string{"Id"}}}}
	p.relatedFocused = true
	u.shell.FocusRightPanel()
	executor := &uxDelayedExecutor{make(chan context.Context, 1), make(chan struct{})}
	u.sessions.ConfigureQueryExecutor(executor)
	cmd := u.refreshLastRecordSet()
	done := make(chan tea.Msg, 1)
	go func() { done <- uxAsyncResult(cmd) }()
	var ctx context.Context
	select {
	case ctx = <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	id := u.operation.id
	uxResponsive(t, "loaded Related during refresh", func() { uxKey(t, u, "enter") })
	if u.operation == nil || u.operation.id != id || ctx.Err() != nil || u.workspaceMutationPending {
		t.Fatal("loaded Related implicitly replaced/canceled refresh")
	}
	uxKey(t, u, "ctrl+x")
	close(executor.release)
	select {
	case msg := <-done:
		u.shell.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("refresh did not finish")
	}
	if u.backendBusy() {
		t.Fatal("refresh fence leaked")
	}
}
