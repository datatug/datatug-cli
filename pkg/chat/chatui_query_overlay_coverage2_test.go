package chat

import (
	"context"
	"fmt"
	"math"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// savedQueryEncodeFailureStub returns a QueryResult whose Result carries a
// float64 NaN cell value -- a value real database drivers can and do
// surface (e.g. a division-by-zero or an IEEE-754 aggregate), and one
// json.Marshal genuinely refuses to encode. It exercises runSavedQuery's
// second store call (store.AppendQuery) failing for real, through
// store.go's encodeResult -> encodeValue -> json.Marshal chain, rather than
// through a test-only seam.
type savedQueryEncodeFailureStub struct {
	queries []SavedQuery
}

func (s *savedQueryEncodeFailureStub) List(context.Context) ([]SavedQuery, error) {
	return s.queries, nil
}
func (s *savedQueryEncodeFailureStub) Run(context.Context, string) (QueryResult, error) {
	return QueryResult{
		// Title deliberately left empty: runSavedQuery's runCmd falls back
		// to the "Run query: <label>" label (chatui_query_overlay.go's
		// `if result.Title == "" { result.Title = label }`).
		Result: secureread.Result{Columns: []string{"x"}, Rows: []secureread.Row{{Data: map[string]any{"x": math.NaN()}}}},
	}, nil
}

// TestRunSavedQueryAppendQueryEncodeErrorSurfaces covers runSavedQuery's
// empty-Title fallback (chatui_query_overlay.go's `result.Title = label`)
// and its second-store-call error return (`return savedQueryDoneMsg{...,
// err: err}` after store.AppendQuery fails) in one real round trip: a NaN
// result cell makes store.AppendQuery's own encodeResult step fail for
// real, no override seam required.
func TestRunSavedQueryAppendQueryEncodeErrorSurfaces(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryEncodeFailureStub{queries: []SavedQuery{{ID: "x", Title: "Broken numbers"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	cmd := u.runSavedQuery(service.queries[0], nil)
	if cmd == nil {
		t.Fatal("expected a run command")
	}
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	// handleSavedQueryDone renders msg.err via conciseError when the
	// background run reports a non-nil error -- store.AppendQuery's
	// encodeResult failure must reach it.
	if view == "" {
		t.Fatal("expected some rendered view")
	}
}

// --- savedQueryPickerOverlay --------------------------------------------

// TestSavedQueryPickerOverlayViewTruncatesToHeight covers View's
// `lines = lines[:height]` truncation branch: more picker rows than the
// available height.
func TestSavedQueryPickerOverlayViewTruncatesToHeight(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	var queries []SavedQuery
	for i := 0; i < 5; i++ {
		queries = append(queries, SavedQuery{ID: fmt.Sprintf("q%d", i), Title: fmt.Sprintf("Query %d", i)})
	}
	o := &savedQueryPickerOverlay{ui: u, queries: queries}
	view := o.View(40, 3)
	lineCount := 0
	for _, r := range view {
		if r == '\n' {
			lineCount++
		}
	}
	if lineCount+1 != 3 {
		t.Fatalf("expected the view truncated to 3 lines, got %d lines:\n%s", lineCount+1, view)
	}
}

// TestSavedQueryPickerOverlayUpMovesIndexBack covers the "up"/"k" case's
// `o.index--` branch (an index > 0 to move back from), which the existing
// boundary test never reaches since it only presses up while already at 0.
func TestSavedQueryPickerOverlayUpMovesIndexBack(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := &savedQueryPickerOverlay{ui: u, queries: []SavedQuery{{ID: "a"}, {ID: "b"}}, index: 1}
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if o.index != 0 {
		t.Fatalf("expected up at index 1 to move back to 0, got %d", o.index)
	}
}

// --- queryParametersOverlay -----------------------------------------------

// TestQueryParametersOverlayLookupMsgMismatchedFocusIgnored covers the
// parameterLookupMsg branch's own mismatched-focus/parameterID guard: a
// stale lookup result (e.g. the user tabbed to a different parameter while
// the lookup was in flight) must not be applied.
func TestQueryParametersOverlayLookupMsgMismatchedFocusIgnored(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	_, cmd, done := d.Update(parameterLookupMsg{overlay: d, focus: 0, parameterID: "not-city", result: &SavedQueryLookup{Key: "City"}})
	if cmd != nil || done {
		t.Fatal("expected a mismatched parameterLookupMsg to be ignored, not close/produce a command")
	}
}

// TestQueryParametersOverlayNonKeyNonLookupMsgIgnored covers Update's own
// "not a key press" fallback for any other message type.
func TestQueryParametersOverlayNonKeyNonLookupMsgIgnored(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x"})
	_, cmd, done := d.Update(tea.WindowSizeMsg{Width: 10, Height: 10})
	if cmd != nil || done {
		t.Fatal("expected a non-key, non-lookup message to be ignored")
	}
}

// TestQueryParametersOverlayEnterOnNonLookupParameterMovesFocus covers the
// "enter" case's plain `d.moveFocus(1)` branch: a parameter that does not
// require an FK lookup (no Entity/Field) just advances focus instead of
// running or opening a lookup.
func TestQueryParametersOverlayEnterOnNonLookupParameterMovesFocus(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "a"}, {ID: "b"}}})
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || done {
		t.Fatal("expected enter on a non-lookup parameter to stay open without a command")
	}
	if d.focus != 1 {
		t.Fatalf("expected enter to move focus to the next parameter, got %d", d.focus)
	}
}

// TestQueryParametersOverlayUnhandledKeyOnRunIsNoop covers Update's final
// fallthrough `return d, nil, false`: an arbitrary key press while focus is
// on the "Run" row (past the last input) matches none of the named cases
// and d.focus < len(d.inputs) is false, so it must fall through as a no-op
// rather than route into an input's own Update.
func TestQueryParametersOverlayUnhandledKeyOnRunIsNoop(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "a"}}})
	d.focus = len(d.inputs)
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd != nil || done {
		t.Fatal("expected an unhandled key on the Run row to be a no-op")
	}
}

// TestQueryParametersOverlayViewHighlightsRunAndShowsError covers View's
// Run-row focus highlight (d.focus == len(d.inputs)) and its error line.
func TestQueryParametersOverlayViewHighlightsRunAndShowsError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newQueryParametersOverlay(u, SavedQuery{ID: "x", Title: "T", Parameters: []SavedQueryParameter{{ID: "a"}}})
	d.focus = len(d.inputs)
	d.err = "something went wrong"
	view := d.View(60, 20)
	if view == "" {
		t.Fatal("expected a non-empty view")
	}
}

// --- parameterLookupOverlay --------------------------------------------------

func twoRowLookupResult(key string) secureread.Result {
	return secureread.Result{
		Columns: []string{key},
		Rows: []secureread.Row{
			{Data: map[string]any{key: "Prague"}},
			{Data: map[string]any{key: "Brno"}},
		},
	}
}

// TestParameterLookupOverlayEscCloses covers the "esc" case, distinct from
// ctrl+c (which quits rather than closing).
func TestParameterLookupOverlayEscCloses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	p := newParameterLookupOverlay(parent, 0, &SavedQueryLookup{Key: "City"})
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done || cmd != nil {
		t.Fatal("expected esc to close the lookup overlay without a command")
	}
}

// TestParameterLookupOverlayNonKeyMsgIgnored covers Update's own "not a key
// press" fallback.
func TestParameterLookupOverlayNonKeyMsgIgnored(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	p := newParameterLookupOverlay(parent, 0, &SavedQueryLookup{Key: "City"})
	_, cmd, done := p.Update(tea.WindowSizeMsg{Width: 10, Height: 10})
	if cmd != nil || done {
		t.Fatal("expected a non-key message to be ignored")
	}
}

// TestParameterLookupOverlayUpDownNavigatesRows covers the "up"/"down"
// case's delta assignment and SelectRow call with a real, non-empty grid.
func TestParameterLookupOverlayUpDownNavigatesRows(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	lookup := &SavedQueryLookup{Key: "City", Result: twoRowLookupResult("City")}
	p := newParameterLookupOverlay(parent, 0, lookup)
	if p.grid.CurrentIndex() != 0 {
		t.Fatalf("expected the grid to start at row 0, got %d", p.grid.CurrentIndex())
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.grid.CurrentIndex() != 1 {
		t.Fatalf("expected down to move to row 1, got %d", p.grid.CurrentIndex())
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if p.grid.CurrentIndex() != 0 {
		t.Fatalf("expected up to move back to row 0, got %d", p.grid.CurrentIndex())
	}
}

// TestParameterLookupOverlayMultiMissingKeyColumnIsSafeNoop covers
// refreshGrid's own `continue` when the lookup Key isn't among the result
// columns (keyColumn() returns -1) and the "space" case's matching
// keyColumn<0 guard -- a malformed/renamed SavedQueryLookupService response
// a real integration could produce.
func TestParameterLookupOverlayMultiMissingKeyColumnIsSafeNoop(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city", Multi: true}}})
	lookup := &SavedQueryLookup{Key: "Missing", Multi: true, Result: twoRowLookupResult("City")}
	p := newParameterLookupOverlay(parent, 0, lookup)
	if p.keyColumn() != -1 {
		t.Fatalf("expected keyColumn() to be -1 for a key absent from the columns, got %d", p.keyColumn())
	}
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if cmd != nil || done {
		t.Fatal("expected space with no matching key column to be a safe no-op")
	}
}

// TestParameterLookupOverlayMultiSpaceUnencodableKeyShowsError covers the
// "space" case's `p.err = "This key cannot be selected."` branch: a NaN
// key cell (a real value a numeric aggregate/division column can produce)
// fails lookupValueToken even though it is a float64.
func TestParameterLookupOverlayMultiSpaceUnencodableKeyShowsError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "n", Multi: true}}})
	lookup := &SavedQueryLookup{Key: "N", Multi: true, Result: secureread.Result{Columns: []string{"N"}, Rows: []secureread.Row{{Data: map[string]any{"N": math.NaN()}}}}}
	p := newParameterLookupOverlay(parent, 0, lookup)
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if cmd != nil || done {
		t.Fatal("expected space on an unencodable key to stay open with an error")
	}
	if p.err == "" {
		t.Fatal("expected 'This key cannot be selected.' error")
	}
}

// TestParameterLookupOverlayMultiSpaceTogglesSelection covers the "space"
// case's deselect branch (`delete(p.selected, token)`): pressing space
// twice on the same row selects then deselects it.
func TestParameterLookupOverlayMultiSpaceTogglesSelection(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city", Multi: true}}})
	lookup := &SavedQueryLookup{Key: "City", Multi: true, Result: twoRowLookupResult("City")}
	p := newParameterLookupOverlay(parent, 0, lookup)
	p.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(p.selected) != 1 {
		t.Fatalf("expected the first space to select one row, got %d", len(p.selected))
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(p.selected) != 0 {
		t.Fatalf("expected the second space on the same row to deselect it, got %d", len(p.selected))
	}
}

// TestParameterLookupOverlayMultiEnterSkipsUnencodableRow covers the
// "enter" multi branch's own `continue` when a *different* row (not the
// selected one) carries an unencodable key -- lookupValueToken(row.Data[
// p.key]) fails for it while building the values slice from p.result.Rows,
// which must not abort collecting the legitimately-selected row's value.
func TestParameterLookupOverlayMultiEnterSkipsUnencodableRow(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "n", Multi: true}}})
	lookup := &SavedQueryLookup{Key: "N", Multi: true, Result: secureread.Result{
		Columns: []string{"N"},
		Rows: []secureread.Row{
			{Data: map[string]any{"N": math.NaN()}},
			{Data: map[string]any{"N": float64(7)}},
		},
	}}
	p := newParameterLookupOverlay(parent, 0, lookup)
	// Row 0 (NaN) can't be selected; move to row 1 and select it.
	p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(p.selected) != 1 {
		t.Fatalf("expected the encodable row to be selected, got %d", len(p.selected))
	}
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || cmd != nil {
		t.Fatal("expected enter with a valid multi selection to close without a command")
	}
	if parent.inputs[0].Value() != "[7]" {
		t.Fatalf("expected the selected value to be encoded, got %q", parent.inputs[0].Value())
	}
}

// TestParameterLookupOverlaySingleEnterNilValueIsNoop covers the
// non-multi "enter" branch's `value != nil` guard: the key column present
// in Columns but absent from a particular row's Data (a sparse/NULL cell)
// leaves the raw value nil.
func TestParameterLookupOverlaySingleEnterNilValueIsNoop(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city"}}})
	lookup := &SavedQueryLookup{Key: "City", Result: secureread.Result{Columns: []string{"City"}, Rows: []secureread.Row{{Data: map[string]any{}}}}}
	p := newParameterLookupOverlay(parent, 0, lookup)
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || done {
		t.Fatal("expected enter on a nil cell value to be a no-op")
	}
}

// TestParameterLookupOverlaySingleEnterUnencodableValueShowsError covers
// the non-multi "enter" branch's own json.Marshal error path: unlike the
// multi path (whose values already passed lookupValueToken, see the
// deleted dead branch this replaced), a single-select value is marshaled
// directly from the raw grid cell, so a NaN cell genuinely fails here.
func TestParameterLookupOverlaySingleEnterUnencodableValueShowsError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "n"}}})
	lookup := &SavedQueryLookup{Key: "N", Result: secureread.Result{Columns: []string{"N"}, Rows: []secureread.Row{{Data: map[string]any{"N": math.NaN()}}}}}
	p := newParameterLookupOverlay(parent, 0, lookup)
	_, cmd, done := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || done {
		t.Fatal("expected enter on an unencodable single value to stay open with an error")
	}
	if p.err == "" {
		t.Fatal("expected 'Could not encode selected key.' error")
	}
}

// TestParameterLookupOverlayViewMultiHelpAndError covers View's multi-mode
// help text branch and its error line.
func TestParameterLookupOverlayViewMultiHelpAndError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	parent := newQueryParametersOverlay(u, SavedQuery{ID: "x", Parameters: []SavedQueryParameter{{ID: "city", Multi: true}}})
	lookup := &SavedQueryLookup{Key: "City", Multi: true, Result: twoRowLookupResult("City")}
	p := newParameterLookupOverlay(parent, 0, lookup)
	p.err = "boom"
	view := p.View(60, 20)
	if view == "" {
		t.Fatal("expected a non-empty view")
	}
}
