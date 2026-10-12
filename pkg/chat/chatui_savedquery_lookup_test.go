package chat

// Ported from saved_query_lookup_test.go's 8 cases that built a legacy &UI{}
// directly (the other 5 — pure DiscoverSavedQueryLookup/
// DiscoverSavedQueryParameterLookup(WithMode)/direct DTQL execution tests —
// never touched UI/ChatUI and stay in saved_query_lookup_test.go unchanged).
//
// Ported onto queryParametersOverlay/parameterLookupOverlay
// (chatui_query_overlay.go) directly, same white-box-overlay-construction
// style as TestQueryParametersOverlayRequiredFieldBlocksRun in
// chatui_query_overlay_test.go: the lookup overlay chatshell pushes
// internally (queryParametersOverlay.Update's parameterLookupMsg branch)
// isn't reachable through the exported shell API, so each test resolves
// loadLookup's Cmd itself and constructs the parameterLookupOverlay
// directly with the resulting *SavedQueryLookup — exactly what
// queryParametersOverlay.Update does internally, just without going
// through chatshell's private overlay stack to get a handle back.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// openLookup drives d through Enter on its focused (required, FK-bound)
// parameter, resolves loadLookup's background Cmd, and returns the pushed
// parameterLookupOverlay — or nil if the lookup failed/was unavailable (the
// caller checks d.err in that case).
func openLookup(t *testing.T, d *queryParametersOverlay) *parameterLookupOverlay {
	t.Helper()
	_, cmd, _ := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("FK field did not request a lookup")
	}
	msg := cmd()
	lookupMsg, ok := msg.(parameterLookupMsg)
	if !ok {
		t.Fatalf("expected a parameterLookupMsg, got %T", msg)
	}
	d.Update(lookupMsg)
	if lookupMsg.err != nil || lookupMsg.result == nil || lookupMsg.result.Multi != d.query.Parameters[lookupMsg.focus].Multi {
		return nil
	}
	return newParameterLookupOverlay(d, lookupMsg.focus, lookupMsg.result)
}

func TestChatUISavedQueryLookupFirstHundredBoundaryAndManualKey(t *testing.T) {
	result := secureread.Result{Columns: []string{"CustomerId"}}
	for i := 1; i <= savedQueryLookupLimit; i++ {
		result.Rows = append(result.Rows, secureread.Row{Data: map[string]any{"CustomerId": i}})
	}
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{result: &SavedQueryLookup{Key: "CustomerId", Result: result}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	lookup := openLookup(t, d)
	if lookup == nil {
		t.Fatal("bounded lookup did not open")
	}
	if !strings.Contains(lookup.View(100, 30), "First 100") {
		t.Fatal("bounded lookup did not explain the limit")
	}
	d.inputs[0].SetValue("101")
	parsed, err := accesspolicies.ParseVariables([]string{"CustomerId=" + d.inputs[0].Value()})
	if err != nil || parsed["CustomerId"] != 101 {
		t.Fatalf("unlisted key was not enterable: %#v, %v", parsed, err)
	}
}

func TestChatUISavedQueryMultiFKPickerPreservesSelectionAcrossFilter(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{result: &SavedQueryLookup{Key: "CustomerId", Multi: true, Result: secureread.Result{Columns: []string{"CustomerId", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": int64(5), "Name": "Alice"}}, {Data: map[string]any{"CustomerId": int64(6), "Name": "Bob"}}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerIds", Required: true, Multi: true, Entity: "Customer", Field: "ID"}}})
	lookup := openLookup(t, d)
	if lookup == nil {
		t.Fatal("lookup did not open")
	}
	if _, _, done := lookup.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); done || !strings.Contains(lookup.err, "at least one") {
		t.Fatal("zero selection was accepted")
	}
	lookup.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if !strings.Contains(lookup.grid.Cell(0, 0), "☑") {
		t.Fatal("selected marker missing")
	}
	for _, r := range "Bob" {
		lookup.Update(tea.KeyPressMsg{Text: string(r)})
	}
	if len(lookup.selected) != 1 || len(lookup.grid.Rows()) != 1 {
		t.Fatal("filter lost selection")
	}
	lookup.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(lookup.selected) != 2 {
		t.Fatal("second row not selected")
	}
	if _, _, done := lookup.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); !done {
		t.Fatal("confirm did not close the picker")
	}
	parsed, err := accesspolicies.ParseVariables([]string{"CustomerIds=" + d.inputs[0].Value()})
	if err != nil || !reflect.DeepEqual(parsed["CustomerIds"], []any{5, 6}) {
		t.Fatalf("typed selection = %v, %v", parsed, err)
	}
}

func TestChatUISavedQueryMultiFKPickerQuotesStringKeys(t *testing.T) {
	value := "x,] \\\"quoted\\\""
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{result: &SavedQueryLookup{Key: "Code", Multi: true, Result: secureread.Result{Columns: []string{"Code"}, Rows: []secureread.Row{{Data: map[string]any{"Code": value}}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "Codes", Required: true, Multi: true, Entity: "Item", Field: "ID"}}})
	lookup := openLookup(t, d)
	if lookup == nil {
		t.Fatal("lookup did not open")
	}
	lookup.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	lookup.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	parsed, err := accesspolicies.ParseVariables([]string{"Codes=" + d.inputs[0].Value()})
	if err != nil || !reflect.DeepEqual(parsed["Codes"], []any{value}) {
		t.Fatalf("string selection = %v, %v", parsed, err)
	}
}

func TestChatUISavedQueryScalarFKPickerPreservesStringKey(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{result: &SavedQueryLookup{Key: "Code", Result: secureread.Result{Columns: []string{"Code"}, Rows: []secureread.Row{{Data: map[string]any{"Code": "001"}}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "Code", Required: true, Entity: "Item", Field: "Code"}}})
	lookup := openLookup(t, d)
	if lookup == nil {
		t.Fatal("lookup did not open")
	}
	lookup.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	parsed, err := accesspolicies.ParseVariables([]string{"Code=" + d.inputs[0].Value()})
	if err != nil || parsed["Code"] != "001" {
		t.Fatalf("selected string key lost its type: %#v, %v", parsed, err)
	}
}

func TestChatUISavedQueryMultiFKPolicyErrorKeepsManualInput(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{err: errors.New("policy denied secret")}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerIds", Required: true, Multi: true, Entity: "Customer", Field: "ID"}}})
	if lookup := openLookup(t, d); lookup != nil {
		t.Fatal("policy-denied lookup unexpectedly opened")
	}
	if !strings.Contains(d.err, "access denied") || strings.Contains(d.err, "secret") {
		t.Fatalf("policy error state = %q", d.err)
	}
	d.inputs[0].SetValue("[1, 2]")
	if got := d.inputs[0].Value(); got != "[1, 2]" {
		t.Fatalf("manual array lost: %q", got)
	}
}

func TestChatUISavedQueryFKLookupFilterAndSelect(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{result: &SavedQueryLookup{Key: "CustomerId", Result: secureread.Result{Columns: []string{"CustomerId", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5, "Name": "Alice"}}, {Data: map[string]any{"CustomerId": 6, "Name": "Bob"}}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	lookup := openLookup(t, d)
	if lookup == nil {
		t.Fatal("lookup did not open")
	}
	for _, r := range "Bob" {
		lookup.Update(tea.KeyPressMsg{Text: string(r)})
	}
	if len(lookup.grid.Rows()) != 1 {
		t.Fatal("filter did not narrow rows")
	}
	lookup.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if value := d.inputs[0].Value(); value != "6" {
		t.Fatalf("selected key = %q", value)
	}
}

func TestChatUISavedQueryFKLookupPolicyErrorKeepsManualInput(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{err: errors.New("policy denied private field")}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	if lookup := openLookup(t, d); lookup != nil {
		t.Fatal("policy-denied lookup unexpectedly opened")
	}
	if !strings.Contains(d.err, "access denied") || strings.Contains(d.err, "private field") {
		t.Fatalf("policy error state = %q", d.err)
	}
}

func TestChatUISavedQueryFKLookupNoFKFallsBackToManualInput(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := parameterLookupStub{}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	d := newQueryParametersOverlay(u, SavedQuery{ID: "q", Parameters: []SavedQueryParameter{{ID: "CustomerId", Required: true, Entity: "Invoice", Field: "CustomerId"}}})
	if lookup := openLookup(t, d); lookup != nil {
		t.Fatal("missing FK unexpectedly opened a lookup")
	}
	if !strings.Contains(d.err, "No single-column") {
		t.Fatal("missing FK did not retain manual entry")
	}
	d.inputs[0].SetValue("42")
	if d.inputs[0].Value() != "42" {
		t.Fatal("manual input was lost")
	}
}
