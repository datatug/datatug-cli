package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// savedQueryNoRunnerStub implements SavedQueryService but deliberately not
// SavedQueryParameterizedRunner, to reach runSavedQuery's "this query runner
// does not accept parameters" branch.
type savedQueryNoRunnerStub struct {
	queries []SavedQuery
	ranID   string
}

func (s *savedQueryNoRunnerStub) List(context.Context) ([]SavedQuery, error) { return s.queries, nil }
func (s *savedQueryNoRunnerStub) Run(_ context.Context, id string) (QueryResult, error) {
	s.ranID = id
	return QueryResult{Title: "t"}, nil
}

// TestReloadSavedQueriesNoServiceClearsList covers reloadSavedQueries' own
// nil-service branch (chatui_query_overlay.go:26-29), reached only by
// calling it directly before SetSavedQueryService has ever run.
func TestReloadSavedQueriesNoServiceClearsList(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueries = []SavedQuery{{ID: "stale"}}
	if err := u.reloadSavedQueries(); err != nil {
		t.Fatal(err)
	}
	if u.savedQueries != nil {
		t.Fatalf("expected reloadSavedQueries with no service to clear the list, got %+v", u.savedQueries)
	}
}

// TestReloadSavedQueriesPropagatesListError covers reloadSavedQueries'
// service.List error branch (chatui_query_overlay.go:30-33), reached via
// SetSavedQueryService.
func TestReloadSavedQueriesPropagatesListError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{listErr: errors.New("boom")}
	if err := u.SetSavedQueryService(service); err == nil {
		t.Fatal("expected List's error to propagate from SetSavedQueryService")
	}
}

// TestRunQueryCommandPropagatesReloadError covers runQueryCommand's own
// reloadSavedQueries error branch (chatui_query_overlay.go:124-126).
func TestRunQueryCommandPropagatesReloadError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Prague"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	service.listErr = errors.New("boom")
	if _, err := u.runQueryCommand("Prague"); err == nil {
		t.Fatal("expected runQueryCommand to propagate reloadSavedQueries' error")
	}
}

// TestRunQueryCommandSingleMatchWithEmptyArgumentOpensPicker covers the
// len(matches)==1 && argument=="" false branch: even a single match, when
// reached with an empty argument (e.g. bare "/query"), goes to the picker
// overlay instead of running directly.
func TestRunQueryCommandSingleMatchWithEmptyArgumentOpensPicker(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Prague"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	cmd, err := u.runQueryCommand("")
	if err != nil {
		t.Fatal(err)
	}
	// PushOverlay always returns nil (it mutates the shell's overlay stack
	// directly) -- the only observable signal is the rendered view.
	drainCmd(t, u, cmd)
	if service.ranID != "" {
		t.Fatalf("bare /query with a single match should open the picker, not run directly; ranID=%q", service.ranID)
	}
	if !strings.Contains(u.shell.View().Content, "Saved project queries") {
		t.Fatalf("expected the saved query picker overlay in view:\n%s", u.shell.View().Content)
	}
}

// TestRunSavedQueryGuardsMissingService covers runSavedQuery's own
// unavailable guard (chatui_query_overlay.go:59-62) when called without
// SetSavedQueryService ever having been called.
func TestRunSavedQueryGuardsMissingService(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if cmd := u.runSavedQuery(SavedQuery{ID: "x"}, nil); cmd != nil {
		t.Fatal("expected a nil command when no saved-query service is configured")
	}
	if !strings.Contains(u.shell.View().Content, "unavailable") {
		t.Fatalf("expected an unavailable message in view:\n%s", u.shell.View().Content)
	}
}

// TestRunSavedQueryAppendUserErrorSurfaces covers runCmd's own AppendUser
// error branch (chatui_query_overlay.go:70-72) by closing the store before
// running.
func TestRunSavedQueryAppendUserErrorSurfaces(t *testing.T) {
	u, chat := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Prague"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	if err := chat.store.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := u.runSavedQuery(service.queries[0], nil)
	if cmd == nil {
		t.Fatal("expected a command even though the store is closed")
	}
	drainCmd(t, u, cmd)
}

// TestRunSavedQueryUnsupportedParameterizedRunner covers runCmd's
// "this query runner does not accept parameters" branch
// (chatui_query_overlay.go:78-80) when variables are supplied but the
// service doesn't implement SavedQueryParameterizedRunner.
func TestRunSavedQueryUnsupportedParameterizedRunner(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryNoRunnerStub{queries: []SavedQuery{{ID: "x", Title: "Prague"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	cmd := u.runSavedQuery(service.queries[0], map[string]string{"city": "Prague"})
	if cmd == nil {
		t.Fatal("expected a run command")
	}
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "Query failed") {
		t.Fatalf("expected the unsupported-runner error to surface as a query failure:\n%s", view)
	}
}

// TestHandleSavedQueryDoneIgnoresStaleSession covers handleSavedQueryDone's
// session-mismatch branch (chatui_query_overlay.go:111): a background
// saved-query run that completes after the user has since switched sessions
// must not clobber the now-current session's view.
func TestHandleSavedQueryDoneIgnoresStaleSession(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	before := u.sessionID
	u.handleSavedQueryDone(savedQueryDoneMsg{sessionID: "some-other-session", snapshot: ChatSession{ID: "some-other-session"}})
	if u.sessionID != before {
		t.Fatalf("expected the stale session's snapshot to be ignored; sessionID changed from %q to %q", before, u.sessionID)
	}
}

// --- savedQueryPickerOverlay ------------------------------------------------

func TestSavedQueryPickerOverlayNonKeyMsgIgnored(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &savedQueryPickerOverlay{ui: u, queries: []SavedQuery{{ID: "a"}}}
	_, cmd, done := o.Update(tea.WindowSizeMsg{Width: 10, Height: 10})
	if cmd != nil || done {
		t.Fatal("expected a non-key message to be ignored")
	}
}

func TestSavedQueryPickerOverlayUpDownBoundaries(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &savedQueryPickerOverlay{ui: u, queries: []SavedQuery{{ID: "a"}, {ID: "b"}}}
	o.Update(tea.KeyPressMsg{Code: 'k'}) // up at index 0: no-op
	if o.index != 0 {
		t.Fatalf("expected up at index 0 to stay put, got %d", o.index)
	}
	o.Update(tea.KeyPressMsg{Code: 'j'}) // down
	if o.index != 1 {
		t.Fatalf("expected down to move to index 1, got %d", o.index)
	}
	o.Update(tea.KeyPressMsg{Code: 'j'}) // down at last index: no-op
	if o.index != 1 {
		t.Fatalf("expected down at the last index to stay put, got %d", o.index)
	}
}

func TestSavedQueryPickerOverlayEnterAndEscClose(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "a", Title: "A"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	o := &savedQueryPickerOverlay{ui: u, queries: service.queries}
	_, cmd, done := o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || cmd == nil {
		t.Fatal("expected Enter on a valid index to close and return a run command")
	}
	drainCmd(t, u, cmd)
	if service.ranID != "a" {
		t.Fatalf("expected the picker's Enter to run the selected query, ranID=%q", service.ranID)
	}

	o2 := &savedQueryPickerOverlay{ui: u, queries: nil, index: 0}
	_, cmd2, done2 := o2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done2 || cmd2 != nil {
		t.Fatal("expected Enter with an out-of-range index to close without a command")
	}

	o3 := &savedQueryPickerOverlay{ui: u, queries: service.queries}
	_, cmd3, done3 := o3.Update(tea.KeyPressMsg{Code: 'q'})
	if !done3 || cmd3 != nil {
		t.Fatal("expected q to close the picker without a command")
	}
}

// --- queryParametersOverlay --------------------------------------------------

func TestQueryParametersOverlayCtrlCQuits(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x"})
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if done || cmd == nil {
		t.Fatal("expected ctrl+c to return tea.Quit without closing the overlay")
	}
}

func TestQueryParametersOverlayTabWrapsAroundToRun(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "a"}}})
	d.Update(tea.KeyPressMsg{Code: tea.KeyTab}) // focus -> "Run" (index 1)
	if d.focus != len(d.inputs) {
		t.Fatalf("expected Tab to move focus onto Run, got %d", d.focus)
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyTab}) // wraps back to 0
	if d.focus != 0 {
		t.Fatalf("expected Tab from Run to wrap back to the first parameter, got %d", d.focus)
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // shift+tab wraps backward onto Run
	if d.focus != len(d.inputs) {
		t.Fatalf("expected shift+tab from the first parameter to wrap onto Run, got %d", d.focus)
	}
}

func TestQueryParametersOverlayLoadLookupUnavailable(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city", Required: true, Entity: "City", Field: "Name"}}})
	cmd := d.loadLookup()
	if cmd != nil {
		t.Fatal("expected loadLookup to return nil when the service isn't a SavedQueryLookupService")
	}
	if d.err == "" {
		t.Fatal("expected an 'unavailable' error message")
	}
}

// --- parameterLookupOverlay --------------------------------------------------

func singleRowLookupResult() secureread.Result {
	return secureread.Result{Columns: []string{"City"}, Rows: []secureread.Row{{Data: map[string]any{"City": "Prague"}}}}
}

func TestParameterLookupOverlayCtrlCQuits(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	p := newParameterLookupOverlay(parent, 0, &SavedQueryLookup{Key: "City"})
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if done || cmd == nil {
		t.Fatal("expected ctrl+c to return tea.Quit without closing the overlay")
	}
}

func TestParameterLookupOverlayUpDownAndSpaceWithEmptyGrid(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	p := newParameterLookupOverlay(parent, 0, &SavedQueryLookup{Key: "City"})
	// No rows: up/down/space/enter must all be safe no-ops.
	if _, _, done := p.Update(tea.KeyPressMsg{Code: tea.KeyUp}); done {
		t.Fatal("up with an empty grid should not close the overlay")
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if _, _, done := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("enter with an empty grid should not close the overlay")
	}
}

func TestParameterLookupOverlaySpaceOnSingleValueIsNoop(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	lookup := &SavedQueryLookup{Key: "City", Multi: false, Result: singleRowLookupResult()}
	p := newParameterLookupOverlay(parent, 0, lookup)
	_, _, done := p.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if done {
		t.Fatal("space on a non-multi lookup should be a no-op, not close the overlay")
	}
}

func TestParameterLookupOverlayEnterMultiWithNoSelectionShowsError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	lookup := &SavedQueryLookup{Key: "City", Multi: true, Result: singleRowLookupResult()}
	p := newParameterLookupOverlay(parent, 0, lookup)
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || cmd != nil {
		t.Fatal("expected Enter with nothing selected to stay open with an error, not close")
	}
	if p.err == "" {
		t.Fatal("expected a 'select at least one row' error")
	}
}
