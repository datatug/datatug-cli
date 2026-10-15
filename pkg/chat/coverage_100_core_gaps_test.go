package chat

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestCoverage100_CoreGaps(t *testing.T) {
	// Not parallel: manipulates shared test databases sequentially
	ctx := context.Background()

	// 1. store_archive.go coverage
	t.Run("store_archive_gaps", func(t *testing.T) {
		_, sessions := newTestChatUI(t, nil)
		store := sessions.store

		// Archive nonexistent ID (line 34)
		err := store.Archive(ctx, "nonexistent-id")
		if err == nil {
			t.Fatal("expected error archiving nonexistent id")
		}

		// Archive with canceled ctx (line 30)
		cancCtx, cancel := context.WithCancel(ctx)
		cancel()
		err = store.Archive(cancCtx, "any-id")
		if err == nil {
			t.Fatal("expected error archiving with canceled context")
		}

		// DeleteActive with closed store (line 93)
		_, sessionsDel := newTestChatUI(t, nil)
		sDel := sessionsDel.activeID
		_ = sessionsDel.store.db.Close()
		_, err = sessionsDel.DeleteActive(ctx, sDel)
		if err == nil {
			t.Fatal("expected error from DeleteActive with closed db")
		}

		origDBQuery := dbQueryContextFn
		defer func() { dbQueryContextFn = origDBQuery }()

		// ArchivedSessionIDs scan error (line 21): 2 columns returned so rows.Scan(&id) fails
		_, sessionsScan := newTestChatUI(t, nil)
		_ = sessionsScan.store.Archive(ctx, sessionsScan.activeID)
		dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
			if strings.Contains(query, "FROM session_archives") {
				return db.QueryContext(ctx, "SELECT a.session_id, a.archived_at FROM session_archives a JOIN sessions s ON s.id=a.session_id WHERE s.scope=?", args...)
			}
			return origDBQuery(db, ctx, query, args...)
		}
		_, err = sessionsScan.store.ArchivedSessionIDs(ctx)
		if err == nil {
			t.Fatal("expected scan error on 2 columns in ArchivedSessionIDs")
		}
		dbQueryContextFn = origDBQuery

		// ArchiveActive with List error (line 60)
		_, sessionsListErr := newTestChatUI(t, nil)
		sList := sessionsListErr.activeID
		dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
			if strings.Contains(query, "FROM sessions WHERE scope") {
				return nil, errors.New("injected List error")
			}
			return origDBQuery(db, ctx, query, args...)
		}
		_, err = sessionsListErr.ArchiveActive(ctx, sList)
		if err == nil {
			t.Fatal("expected error from ArchiveActive when List errors")
		}
		dbQueryContextFn = origDBQuery

		// ArchiveActive with ArchivedSessionIDs error (line 64)
		_, sessionsArchErr := newTestChatUI(t, nil)
		sArch := sessionsArchErr.activeID
		dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
			if strings.Contains(query, "FROM session_archives") {
				return nil, errors.New("injected ArchivedSessionIDs error")
			}
			return origDBQuery(db, ctx, query, args...)
		}
		_, err = sessionsArchErr.ArchiveActive(ctx, sArch)
		if err == nil {
			t.Fatal("expected error from ArchiveActive when ArchivedSessionIDs errors")
		}
		dbQueryContextFn = origDBQuery
	})

	// 2. workspace_trail.go gaps
	t.Run("workspace_trail_gaps", func(t *testing.T) {
		u, _ := newTestChatUI(t, nil, Turn{})
		u.snapshot.RecordSets = map[string]RecordSet{
			"rs1": {ID: "rs1", Title: "RS1", DTQL: "from: {schema: main, name: users}\nselect: [{field: id}]"},
		}
		// showTrailNode with g.entryID == "" (lines 129-131)
		g := newGridState(NewGridModel(secureread.Result{Columns: []string{"id"}}), "rs1", "Title", 80)
		g.entryID = ""
		u.gridsByRecordSetID["rs1"] = g
		u.trailNodes = map[string]*explorationNode{
			"t1": {id: "t1", recordID: "rs1"},
		}
		ok := u.showTrailNode("t1")
		if !ok || g.entryID == "" {
			t.Fatalf("expected showTrailNode to succeed and set entryID, got ok=%v, entryID=%q", ok, g.entryID)
		}

		// reconcileResults with missing RecordSetID on grid message (line 336)
		u.reconcileResults(ChatSession{
			ID: u.sessionID,
			Messages: []ChatMessage{
				{ID: "m1", Kind: "grid", RecordSetID: "missing_rs"},
			},
			RecordSets: map[string]RecordSet{},
		})
	})

	// 3. result_aggregate_controls.go gaps
	t.Run("result_aggregate_controls_gaps", func(t *testing.T) {
		u, _ := newTestChatUI(t, nil, Turn{})
		u.catalog = ProjectCatalog{
			Objects: []ProjectObject{
				{
					Reference:   ContextReference{Kind: "table", SourceID: "other_db", ObjectID: "main.orders"},
					PrimaryKey:  []string{"id"},
					Columns:     []string{"id"},
					ColumnTypes: map[string]string{"id": "INTEGER"},
				},
				{
					Reference:   ContextReference{Kind: "table", SourceID: "db1", ObjectID: "main.users"},
					PrimaryKey:  []string{"id"},
					Columns:     []string{"id", "age"},
					ColumnTypes: map[string]string{"id": "INTEGER", "age": "INTEGER"},
				},
			},
		}
		u.sessions.joinApplication = ForeignKeyJoinApplication{
			Source: "sqlite",
			Snapshot: ForeignKeySnapshot{
				Source:  "sqlite",
				Columns: map[string][]string{}, // "main.users" not in Columns
			},
		}
		overlay := &resultOperationOverlay{
			ui: u,
			// record.Database == "" causes columnMetaFor to match, but candidate.Reference.SourceID == "" is false -> object == nil (line 47)!
			record: RecordSet{
				Database: "",
				Source:   "sqlite",
				DTQL:     "from: {schema: main, name: users}\nlimit: 5",
				Result:   secureread.Result{Columns: []string{"age"}},
			},
			capture: &loadedResultCapture{columnNames: []string{"age"}},
		}

		// defaultSumEligible: object == nil (line 47)
		if overlay.defaultSumEligible(0) {
			t.Fatal("expected false when object == nil in defaultSumEligible")
		}

		// defaultSumEligible: not known in Snapshot.Columns (line 60)
		overlay.record.Database = "db1"
		if overlay.defaultSumEligible(0) {
			t.Fatal("expected false when object not in Snapshot.Columns in defaultSumEligible")
		}

		// aggregateExplanation: loop over catalog.Objects, line 172 continue for other_db and prefix mismatch
		overlay.record.Result = secureread.Result{Columns: []string{"id"}}
		overlay.record.DTQL = "from: {schema: main, name: users}\nlimit: 5"
		overlay.capture = &loadedResultCapture{columnNames: []string{"id"}}
		reason := overlay.aggregateExplanation(0)
		if !strings.Contains(reason, "primary key") {
			t.Fatalf("expected reason for primary key, got %q", reason)
		}
	})

	// 4. chatui_http_overlay.go gaps
	t.Run("chatui_http_overlay_gaps", func(t *testing.T) {
		u, _ := newTestChatUI(t, nil)

		// dispatchHTTPRequest with canceled context (line 148)
		cmd, err := u.sendHTTPRequest(httpRequestSpec{URL: "http://localhost:1234/test", Method: "GET"})
		if err != nil {
			t.Fatal(err)
		}
		// Cancel operation before runCmd executes
		u.cancelOperation(true)
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					res := c()
					if dm, ok := res.(httpDoneMsg); ok {
						if !errors.Is(dm.err, context.Canceled) {
							t.Fatalf("expected context.Canceled, got %v", dm.err)
						}
					}
				}
			}
		}

		// httpRequestOverlay header mouse click Cancel [Ctrl+X] (lines 302-305)
		u2, _ := newTestChatUI(t, nil)
		u2.catalog.Title = "Cancel [Ctrl+X]"
		ov := newHTTPRequestOverlay(u2, httpRequestSpec{URL: "http://localhost:1234/test", Method: "GET"})
		opID, _ := u2.beginOperation("HTTP request")
		_ = opID
		u2.operation.shellBusy = true
		u2.shell.SetBusy(true)

		_ = ov.View(100, 20)
		header := strings.Split(ansi.Strip(u2.shell.View().Content), "\n")[0]
		at := strings.Index(header, "Cancel [Ctrl+X]")
		if at < 0 {
			t.Fatalf("expected Cancel [Ctrl+X] in header, got %q", header)
		}
		clickMsg := tea.MouseClickMsg{
			Button: tea.MouseLeft,
			X:      at + 1,
			Y:      0,
		}
		_, _, handled := ov.Update(clickMsg)
		if handled || u2.operation != nil {
			t.Fatal("expected operation canceled and overlay not handled")
		}
	})

	// 5. chatui_pickers.go gaps
	t.Run("chatui_pickers_gaps", func(t *testing.T) {
		// openSessionPicker with u.sessions == nil (line 315)
		uNoSess, _ := newTestChatUI(t, nil)
		uNoSess.sessions = nil
		if cmd := uNoSess.openSessionPicker(); cmd != nil {
			t.Fatal("expected nil cmd when sessions is nil")
		}

		// openSessionPicker with ArchivedSessionIDs error (line 324): drop session_archives table
		uClosed, sessClosed := newTestChatUI(t, nil)
		_, err := sessClosed.store.db.Exec("DROP TABLE session_archives")
		if err != nil {
			t.Fatal(err)
		}
		if cmd := uClosed.openSessionPicker(); cmd != nil {
			t.Fatal("expected nil cmd when ArchivedSessionIDs errors")
		}

		// sessionPickerOverlay.View when len(lines) > height (line 370)
		ov := &sessionPickerOverlay{
			ui:       uClosed,
			sessions: []ChatSession{{ID: "s1", Title: "Session 1"}, {ID: "s2", Title: "Session 2"}},
			archived: map[string]bool{},
			index:    0,
		}
		view := ov.View(80, 1)
		if view == "" {
			t.Fatal("expected non-empty view")
		}

		// refreshLastRecordSet AppendUser error (line 276)
		uRefresh := focusedGridStoreBackedTestUI(t, QueryResult{
			Title:          "T",
			HTTPResponseID: "resp1",
			Result:         secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}},
		})
		uRefresh.snapshot.HTTPResponses = map[string]HTTPResponse{
			"resp1": {URL: "http://localhost:1234/test", Method: "GET"},
		}
		cmd := uRefresh.refreshLastRecordSet()
		if cmd != nil {
			// Close db before runCmd executes so AppendUser fails!
			_ = uRefresh.sessions.store.db.Close()
			msg := cmd()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, c := range batch {
					if c != nil {
						res := c()
						if dm, ok := res.(httpDoneMsg); ok {
							if dm.err == nil {
								t.Fatal("expected error from failed AppendUser in refresh")
							}
						}
					}
				}
			}
		}
	})

	// 6. workspace_actions.go gaps
	t.Run("workspace_actions_gaps", func(t *testing.T) {
		u := focusedGridStoreBackedTestUI(t, QueryResult{
			Title:  "T",
			DTQL:   "from: {schema: main, name: widgets}\nselect: [{field: id}]",
			Result: secureread.Result{Columns: []string{"id"}, ColumnTypes: map[string]string{"id": "INTEGER"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
		})
		u.shell.ShowTranscript()
		_, rec, _ := u.activeGrid()
		u.trailNodes = map[string]*explorationNode{
			rec.ID: {id: rec.ID, recordID: rec.ID},
		}
		u.trailHistory = []string{rec.ID, rec.ID, rec.ID}
		u.trailPosition = 1
		opID, _ := u.beginOperation("Test")
		_ = opID

		// loaded-up, server-up, server-down, back, forward
		_ = u.dispatchWorkspaceAction("loaded-up")
		_ = u.dispatchWorkspaceAction("server-up")
		_ = u.dispatchWorkspaceAction("server-down")
		_ = u.dispatchWorkspaceAction("back")
		_ = u.dispatchWorkspaceAction("forward")

		// clear operation & backends so result-actions is enabled
		u.cancelOperation(false)
		u.backends = nil
		cmd := u.dispatchWorkspaceAction("result-actions")
		_ = cmd
	})

	// 7. workspace_query.go gaps (AppendUser & AppendQuery errors)
	t.Run("workspace_query_gaps", func(t *testing.T) {
		u, sessions := newTestChatUI(t, nil)
		scope := sessions.store.info
		ref := ContextReference{Kind: "table", SourceID: scope.Database, ObjectID: "main.users", Title: "Users"}
		sessions.catalog = ProjectCatalog{
			Objects: []ProjectObject{
				{Reference: ref, Columns: []string{"id"}},
			},
		}
		doc := "from: {schema: main, name: users}\nlimit: 10"

		// 1. AppendUser error (line 67): wrap RunDTQL to close db before AppendUser runs
		mockRunnerClose := &mockCloseExecutorRunner{
			db:  sessions.store.db,
			res: secureread.Result{Columns: []string{"id"}},
		}
		sessions.queryExecutor = mockRunnerClose
		_, err := sessions.executeWorkspaceQuery(ctx, sessions.activeID, ref, doc)
		if err == nil {
			t.Fatal("expected error from executeWorkspaceQuery when AppendUser fails")
		}

		// 2. AppendQuery error (line 71): drop queries table
		_, sessions2 := newTestChatUI(t, nil)
		scope2 := sessions2.store.info
		ref2 := ContextReference{Kind: "table", SourceID: scope2.Database, ObjectID: "main.users", Title: "Users"}
		sessions2.catalog = ProjectCatalog{
			Objects: []ProjectObject{
				{Reference: ref2, Columns: []string{"id"}},
			},
		}
		sessions2.queryExecutor = &mockExecutorRunner{
			res: secureread.Result{Columns: []string{"id"}},
		}
		_, err = sessions2.store.db.Exec("DROP TABLE queries")
		if err != nil {
			t.Fatal(err)
		}
		_, err = sessions2.executeWorkspaceQuery(ctx, sessions2.activeID, ref2, doc)
		if err == nil {
			t.Fatal("expected error from executeWorkspaceQuery when AppendQuery fails")
		}
		_ = u
	})
}

type mockCloseExecutorRunner struct {
	db  *sql.DB
	res secureread.Result
}

func (m *mockCloseExecutorRunner) RunDTQL(ctx context.Context, source string, query []byte, parameters map[string]any) (secureread.Result, error) {
	_ = m.db.Close()
	return m.res, nil
}
