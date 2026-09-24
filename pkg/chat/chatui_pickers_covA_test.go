package chat

// Coverage lane A (datatug-cli#289): direct, in-package unit tests for
// chatui_pickers.go's globalKeys, refreshLastRecordSet, openSessionPicker,
// and the session/project picker Overlays -- most of these are plain
// methods callable directly, without driving a full tea.Program Update
// loop.

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// secureReadResult is a minimal, valid secureread.Result for tests that
// only need a QueryResult to persist as a real RecordSet.
func secureReadResult() secureread.Result {
	return secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}}
}

func TestGlobalKeysF4WithoutSessionsIsANoop(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "fake-model")
	cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF4, Text: "f4"})
	if cmd != nil || !handled {
		t.Fatalf("F4 without sessions: cmd=%v handled=%v, want nil/true", cmd, handled)
	}
}

func TestGlobalKeysF3WithoutChoicesIsANoop(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3, Text: "f3"})
	if cmd != nil || !handled {
		t.Fatalf("F3 without project choices: cmd=%v handled=%v, want nil/true", cmd, handled)
	}
}

func TestGlobalKeysF3PushesProjectPicker(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.SetProjectChoices([]ProjectChoice{{Key: "a", Title: "My Project"}})
	// PushOverlay's own tea.Cmd is legitimately nil (no init command needed
	// for this Overlay); what matters is that it was actually pushed and is
	// now rendered.
	if _, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3, Text: "f3"}); !handled {
		t.Fatal("F3 with project choices was not handled")
	}
	if !strings.Contains(u.shell.View().Content, "My Project") {
		t.Fatalf("expected the project picker overlay to be visible:\n%s", u.shell.View().Content)
	}
}

func TestGlobalKeysCtrlGWithAndWithoutLastGrid(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// No lastGridEntryID yet: still handled, just a no-op FocusEntry call.
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}); cmd != nil || !handled {
		t.Fatalf("Ctrl+G with no grid: cmd=%v handled=%v", cmd, handled)
	}
	u.lastGridEntryID = "turn-1"
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}); cmd != nil || !handled {
		t.Fatalf("Ctrl+G with a grid: cmd=%v handled=%v", cmd, handled)
	}
}

func TestGlobalKeysCtrlRDelegatesToRefresh(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// sessions is set (newTestChatUI uses NewSessionChatUI), but there's no
	// active/last grid, so refreshLastRecordSet's own recordSetID=="" guard
	// returns nil -- this only proves Ctrl+R reaches it (handled=true).
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}); cmd != nil || !handled {
		t.Fatalf("Ctrl+R: cmd=%v handled=%v", cmd, handled)
	}
}

func TestGlobalKeysAltSCyclesTableStyle(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	before := u.tableStyle.Name
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: 's', Mod: tea.ModAlt}); cmd == nil && !handled {
		t.Fatalf("Alt+S: cmd=%v handled=%v", cmd, handled)
	} else if !handled {
		t.Fatal("Alt+S was not handled")
	}
	if u.tableStyle.Name == before {
		t.Fatal("Alt+S did not cycle the table style")
	}
	// The macOS Option+S alias (ß) does the same thing.
	u2, _ := newTestChatUI(t, nil, Turn{})
	before2 := u2.tableStyle.Name
	u2.globalKeys(tea.KeyPressMsg{Code: 'ß', Text: "ß"})
	if u2.tableStyle.Name == before2 {
		t.Fatal("ß alias did not cycle the table style")
	}
}

func TestGlobalKeysF2TogglesMouse(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	before := u.shell.MouseEnabled()
	cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF2, Text: "f2"})
	if cmd != nil || !handled {
		t.Fatalf("F2: cmd=%v handled=%v", cmd, handled)
	}
	if u.shell.MouseEnabled() == before {
		t.Fatal("F2 did not toggle mouse reporting")
	}
}

func TestGlobalKeysF5Branches(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// No browserURL yet: a no-op.
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5, Text: "f5"}); cmd != nil || !handled {
		t.Fatalf("F5 with no browserURL: cmd=%v handled=%v", cmd, handled)
	}
	u.browserURL = "https://example.test/chat"
	// openBrowser nil: falls back to showing the hyperlink.
	u.openBrowser = nil
	cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5, Text: "f5"})
	if cmd != nil || !handled || !u.webLinkVisible {
		t.Fatalf("F5 with nil openBrowser: cmd=%v handled=%v webLinkVisible=%v", cmd, handled, u.webLinkVisible)
	}
	// F5 again while the fallback link is visible hides it.
	cmd, handled = u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5, Text: "f5"})
	if cmd != nil || !handled || u.webLinkVisible {
		t.Fatalf("second F5 should hide the link: cmd=%v handled=%v webLinkVisible=%v", cmd, handled, u.webLinkVisible)
	}
	// openBrowser set: returns a cmd that reports the outcome.
	var openedURL string
	u.openBrowser = func(url string) error { openedURL = url; return errors.New("no browser here") }
	cmd, handled = u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF5, Text: "f5"})
	if cmd == nil || !handled {
		t.Fatalf("F5 with an opener: cmd=%v handled=%v", cmd, handled)
	}
	msg := cmd()
	result, ok := msg.(browserOpenResultMsg)
	if !ok || result.url != u.browserURL || result.err == nil {
		t.Fatalf("F5 opener result = %+v, want a failed browserOpenResultMsg for %q", msg, u.browserURL)
	}
	if openedURL != u.browserURL {
		t.Fatalf("opener called with %q, want %q", openedURL, u.browserURL)
	}
}

func TestGlobalKeysEscBranches(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// Esc while the web link is NOT visible: unhandled, falls through to
	// chatshell's own Esc handling.
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil || handled {
		t.Fatalf("Esc with no web link: cmd=%v handled=%v, want nil/false", cmd, handled)
	}
	u.webLinkVisible = true
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil || !handled || u.webLinkVisible {
		t.Fatalf("Esc with the web link visible: cmd=%v handled=%v webLinkVisible=%v", cmd, handled, u.webLinkVisible)
	}
}

func TestGlobalKeysUnmatchedKeyIsUnhandled(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: 'z', Text: "z"}); cmd != nil || handled {
		t.Fatalf("an unmatched key: cmd=%v handled=%v, want nil/false", cmd, handled)
	}
}

// --- refreshLastRecordSet ---------------------------------------------

func TestRefreshLastRecordSetGuardBranches(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// sessions.store is non-nil normally; force the sessions==nil guard.
	u.sessions = nil
	if cmd := u.refreshLastRecordSet(); cmd != nil {
		t.Fatal("refreshLastRecordSet without sessions should be a no-op")
	}

	u2, _ := newTestChatUI(t, nil, Turn{})
	// No active/last grid at all: recordSetID stays empty.
	if cmd := u2.refreshLastRecordSet(); cmd != nil {
		t.Fatal("refreshLastRecordSet with no recordset should be a no-op")
	}

	// lastGridRecordSetID names a RecordSet that isn't in the snapshot.
	u2.lastGridRecordSetID = "does-not-exist"
	if cmd := u2.refreshLastRecordSet(); cmd != nil {
		t.Fatal("refreshLastRecordSet for an unknown recordset should be a no-op")
	}
}

func TestRefreshLastRecordSetPlainResultCannotBeRefreshed(t *testing.T) {
	// A RecordSet with neither an HTTPResponseID nor a DTQL (e.g. a saved
	// project query result) reports the "cannot be refreshed" message.
	turn := Turn{Text: "Found one.", Queries: []QueryResult{{Title: "Customers", RecordSetID: "rs1", Result: secureReadResult()}}}
	u, sessions := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show a customer"))
	snapshot, err := sessions.Snapshot(u.ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	if len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("expected exactly one persisted RecordSet, got %d", len(u.snapshot.RecordSets))
	}
	var recordSetID string
	for id := range u.snapshot.RecordSets {
		recordSetID = id
	}
	u.lastGridRecordSetID = recordSetID
	if cmd := u.refreshLastRecordSet(); cmd != nil {
		t.Fatal("a plain RecordSet with no DTQL/HTTPResponseID should not be refreshable")
	}
	if !strings.Contains(u.shell.View().Content, "cannot be refreshed here") {
		t.Fatalf("expected the 'cannot be refreshed' message to appear:\n%s", u.shell.View().Content)
	}
}

func TestRefreshLastRecordSetQueuesDTQLRefresh(t *testing.T) {
	turn := Turn{Text: "Found one.", Queries: []QueryResult{{Title: "Customers", RecordSetID: "rs1", DTQL: "from: {name: Customer}\nlimit: 1", Result: secureReadResult()}}}
	u, sessions := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show a customer"))
	snapshot, err := sessions.Snapshot(u.ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	var recordSetID string
	for id, rs := range u.snapshot.RecordSets {
		if rs.DTQL != "" {
			recordSetID = id
		}
	}
	if recordSetID == "" {
		t.Fatal("expected a persisted RecordSet with a DTQL")
	}
	u.lastGridRecordSetID = recordSetID
	cmd := u.refreshLastRecordSet()
	if cmd == nil {
		t.Fatal("expected a refresh command for a DTQL RecordSet")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			if _, ok := sub().(httpDoneMsg); ok {
				return
			}
		}
		t.Fatalf("batched refresh cmd never produced an httpDoneMsg: %+v", batch)
	}
	if _, ok := msg.(httpDoneMsg); !ok {
		t.Fatalf("refresh cmd produced %T, want httpDoneMsg (or a batch resolving to one)", msg)
	}
}

// --- openSessionPicker / sessionPickerOverlay --------------------------

func TestOpenSessionPickerReportsListError(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	if err := sessions.store.Close(); err != nil {
		t.Fatal(err)
	}
	if cmd := u.openSessionPicker(); cmd != nil {
		t.Fatal("openSessionPicker should report the List error, not push an overlay")
	}
	if !strings.Contains(u.shell.View().Content, "database is closed") {
		t.Fatalf("expected the List error in the transcript:\n%s", u.shell.View().Content)
	}
}

func TestSessionPickerOverlayViewNoSessions(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &sessionPickerOverlay{ui: u}
	view := o.View(40, 5)
	if !strings.Contains(view, "(no sessions)") {
		t.Fatalf("expected the empty-sessions placeholder:\n%s", view)
	}
}

func TestSessionPickerOverlayViewTruncatesToHeight(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	sessions := make([]ChatSession, 10)
	for i := range sessions {
		sessions[i] = ChatSession{ID: "session-id-number", Title: "Session"}
	}
	o := &sessionPickerOverlay{ui: u, sessions: sessions}
	view := o.View(20, 3)
	if got := strings.Count(view, "\n") + 1; got != 3 {
		t.Fatalf("View(20, 3) produced %d lines, want exactly 3", got)
	}
}

func TestSessionPickerOverlayUpdateBranches(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	// A single fresh session isn't enough to exercise multi-step up/down
	// navigation meaningfully -- create two more first.
	if _, err := sessions.Create(u.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Create(u.ctx); err != nil {
		t.Fatal(err)
	}
	list, err := sessions.List(u.ctx)
	if err != nil || len(list) < 3 {
		t.Fatalf("expected at least 3 existing sessions: %v, %v", list, err)
	}
	o := &sessionPickerOverlay{ui: u, sessions: list, index: 0}

	// A non-key message is unhandled.
	if _, _, handled := o.Update(tea.WindowSizeMsg{}); handled {
		t.Fatal("a non-key message should not be handled")
	}
	// "up" at index 0 stays clamped.
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if o.index != 0 {
		t.Fatalf("up at index 0 should stay clamped, got %d", o.index)
	}
	// "down"/j moves forward, clamped at the end.
	for range len(list) + 2 {
		o.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	if o.index != len(list)-1 {
		t.Fatalf("index after over-shooting down = %d, want %d", o.index, len(list)-1)
	}
	// "k" moves back up.
	o.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if o.index != len(list)-2 {
		t.Fatalf("index after k = %d, want %d", o.index, len(list)-2)
	}
	// "esc"/"q" close without acting.
	if _, cmd, handled := o.Update(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil || !handled {
		t.Fatalf("esc: cmd=%v handled=%v", cmd, handled)
	}
	if _, cmd, handled := o.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil || !handled {
		t.Fatalf("q: cmd=%v handled=%v", cmd, handled)
	}
	// "n" creates a new session and loads it.
	firstID := u.sessionID
	if _, cmd, handled := o.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil || !handled {
		t.Fatalf("n: cmd=%v handled=%v", cmd, handled)
	}
	if u.sessionID == firstID {
		t.Fatal("n should have switched to a newly created session")
	}
	// "enter" switches to the session at the current index.
	o2 := &sessionPickerOverlay{ui: u, sessions: list, index: 0}
	if _, cmd, handled := o2.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || !handled {
		t.Fatalf("enter: cmd=%v handled=%v", cmd, handled)
	}
	if u.sessionID != list[0].ID {
		t.Fatalf("enter did not switch to the chosen session: got %q, want %q", u.sessionID, list[0].ID)
	}
	// enter with an out-of-range index is a no-op (still handled=true, no panic).
	o3 := &sessionPickerOverlay{ui: u, sessions: list, index: len(list)}
	if _, cmd, handled := o3.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || !handled {
		t.Fatalf("out-of-range enter: cmd=%v handled=%v", cmd, handled)
	}
}

func TestSessionPickerOverlayEnterAndNAppendErrorReports(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	list, err := sessions.List(u.ctx)
	if err != nil || len(list) == 0 {
		t.Fatal("expected an existing session")
	}
	if err := sessions.store.Close(); err != nil {
		t.Fatal(err)
	}
	o := &sessionPickerOverlay{ui: u, sessions: list, index: 0}
	o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(u.shell.View().Content, "database is closed") {
		t.Fatalf("expected Switch's error to appear after the store closed:\n%s", u.shell.View().Content)
	}
	o.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if strings.Count(u.shell.View().Content, "database is closed") < 2 {
		t.Fatalf("expected Create's error to also appear after the store closed:\n%s", u.shell.View().Content)
	}
}

// --- projectPickerOverlay ------------------------------------------------

func TestProjectPickerOverlayViewShowsDetailAndTruncates(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	choices := []ProjectChoice{{Key: "a", Title: "Alpha", Detail: "/path/a"}, {Key: "b", Title: "Beta"}}
	o := newProjectPickerOverlay(u, choices)
	view := o.View(40, 20)
	if !strings.Contains(view, "Alpha") || !strings.Contains(view, "/path/a") || !strings.Contains(view, "Beta") {
		t.Fatalf("View missing expected content:\n%s", view)
	}
	truncated := o.View(40, 2)
	if got := strings.Count(truncated, "\n") + 1; got != 2 {
		t.Fatalf("View(40, 2) produced %d lines, want 2", got)
	}
}

func TestProjectPickerOverlayUpdateBranches(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	choices := []ProjectChoice{{Key: "a", Title: "Alpha"}, {Key: "b", Title: "Beta"}}
	o := newProjectPickerOverlay(u, choices)

	if _, _, handled := o.Update(tea.WindowSizeMsg{}); handled {
		t.Fatal("a non-key message should not be handled")
	}
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if o.index != 0 {
		t.Fatalf("up at index 0 should stay clamped, got %d", o.index)
	}
	o.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if o.index != 1 {
		t.Fatalf("j should move to index 1, got %d", o.index)
	}
	o.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // clamped at the end
	if o.index != 1 {
		t.Fatalf("j past the end should stay clamped at 1, got %d", o.index)
	}
	o.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if o.index != 0 {
		t.Fatalf("k should move back to index 0, got %d", o.index)
	}
	if _, cmd, handled := o.Update(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil || !handled {
		t.Fatalf("esc: cmd=%v handled=%v", cmd, handled)
	}
	if _, cmd, handled := o.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil || !handled {
		t.Fatalf("q: cmd=%v handled=%v", cmd, handled)
	}
	if _, cmd, handled := o.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || !handled {
		t.Fatalf("enter: cmd=%v handled=%v, want tea.Quit/true", cmd, handled)
	}
	if u.selectedProject != "a" {
		t.Fatalf("selectedProject = %q, want %q", u.selectedProject, "a")
	}
	// enter with an out-of-range index falls through unhandled.
	empty := newProjectPickerOverlay(u, nil)
	if _, cmd, handled := empty.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || handled {
		t.Fatalf("enter with no choices: cmd=%v handled=%v, want nil/false", cmd, handled)
	}
}
