package chat

// Ported from saved_queries_test.go's 6 UI-dependent cases (savedQueryStub
// and its non-test-function methods stay in saved_queries_test.go, shared
// with chatui_query_overlay_test.go).
//
// Porting TestSaveQueryFailureKeepsDraftAndHidesErrorDetails surfaced a
// real, deliberate architecture difference (not a regression specific to
// saved queries): every chatshell.Overlay Submit/Save in this migration
// (httpRequestOverlay.submit, saveQueryOverlayState.save) closes
// optimistically on local-validation success and reports the async
// backend outcome as a transcript message, rather than staying open until
// the backend call resolves the way ui.go's dialogs did. So "the draft
// survives a save failure" no longer holds; what still holds, and is what
// this test now asserts, is the half that matters for the checklist
// ("hides error details"): the backend error text never leaks into the
// transcript.

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestChatUISavedQueryErrorDoesNotPersistParameterValue(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Private query", Type: "DTQL"}}, runErr: errors.New("invalid --var CustomerId=secret123")}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/query Private"))
	for _, message := range u.snapshot.Messages {
		if strings.Contains(message.Text, "secret123") {
			t.Fatalf("execution error leaked parameter value: %+v", message)
		}
	}
}

func TestChatUISaveQueryFailureHidesErrorDetails(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{saveErr: errors.New("private-token-123")}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	overlay := newSaveQueryOverlay(u, SavedQuerySaveRequest{Type: "DTQL", Text: "from: {name: Customer}"})
	overlay.values[0] = "Customers"
	_, cmd, done := overlay.save()
	if !done || cmd == nil {
		t.Fatal("expected save() to close the overlay and return the background save command")
	}
	drainCmd(t, u, cmd)
	if strings.Contains(u.shell.View().Content, "private-token-123") {
		t.Fatal("save error exposed backend details")
	}
}

func TestChatUISaveQueryRejectsParameterizedResultBeforeOpeningDraft(t *testing.T) {
	// See TestChatUISaveQueryDialogCollectsNameAndTagChips's comment: DTQL/
	// Parameters are set on the already-persisted RecordSet after focusing
	// it for real, rather than through the scripted Turn.
	turn := Turn{Queries: []QueryResult{{Title: "Invoices", RecordSetID: "invoices", Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 7}}}}}}}
	u, _ := newTestChatUI(t, nil, turn)
	u.savedQueryService = &savedQueryStub{}
	drainCmd(t, u, u.Submit("Show invoices"))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("could not focus the parameterized result")
	}
	recordSetID := u.activeRecordSetID()
	if recordSetID == "" {
		t.Fatal("grid did not report a focused RecordSet")
	}
	record := u.snapshot.RecordSets[recordSetID]
	record.DTQL = "from: {name: Invoice}\nwhere: {op: '==', left: {field: CustomerId}, right: {param: CustomerId}}"
	record.Parameters = map[string]any{"CustomerId": 5}
	u.snapshot.RecordSets[recordSetID] = record
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("parameterized result silently opened a lossy save draft")
	}
	if !strings.Contains(u.shell.View().Content, "parameter declarations") {
		t.Fatalf("expected a parameter-declarations warning in view:\n%s", u.shell.View().Content)
	}
}

func TestChatUISavedQueryAutocompleteAliasesFilterAndExecute(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{
		{ID: "customers/prague", Title: "Prague customers", Type: "DTQL", Tags: []string{"city"}},
		{ID: "customers/berlin", Title: "Berlin customers", Type: "DTQL", Tags: []string{"city"}},
		{ID: "orders/recent", Title: "Recent orders", Type: "HTTP"},
	}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	// Searching by tag alias ("city") pushes savedQueryPickerOverlay listing
	// both tagged queries, labeled with their type — the ChatUI-era
	// replacement for ui.go's reopened live-filtered slash-command menu
	// (see runQueryCommand's own comment) for the multiple-match case; a
	// single match still runs directly instead (below).
	drainCmd(t, u, u.Submit("/query city"))
	view := u.shell.View().Content
	if !strings.Contains(view, "Prague customers") || !strings.Contains(view, "Berlin customers") || strings.Contains(view, "Recent orders") || !strings.Contains(view, "[DTQL]") {
		t.Fatalf("tag alias did not filter/label saved queries:\n%s", view)
	}
	// Enter on the picker's first (default-selected) match runs it, the
	// same way selecting a single match directly would.
	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainCmd(t, u, cmd)
	if service.ranID != "customers/prague" {
		t.Fatalf("Enter on the saved-query picker did not run the selected query: ranID=%q", service.ranID)
	}
	// A search with exactly one match runs it directly — both /query and
	// /queries route through the same runQueryCommand.
	for _, command := range []string{"/query prague", "/queries prague"} {
		u, _ = newTestChatUI(t, nil, Turn{})
		if err := u.SetSavedQueryService(service); err != nil {
			t.Fatal(err)
		}
		drainCmd(t, u, u.Submit(command))
		if service.ranID != "customers/prague" || len(u.snapshot.RecordSets) != 1 {
			t.Fatalf("%q was not executed and persisted: id=%q records=%d", command, service.ranID, len(u.snapshot.RecordSets))
		}
		if len(u.snapshot.Messages) < 2 || u.snapshot.Messages[0].Role != "You" || u.snapshot.Messages[0].Text != "Run query: Prague customers" {
			t.Fatalf("query name was not recorded as a user message: %+v", u.snapshot.Messages)
		}
	}
}

func TestChatUISaveQueryDialogCollectsNameAndTagChips(t *testing.T) {
	// The Turn deliberately carries no DTQL/Database: those come from a
	// real agent tool call in production, which contextualStub doesn't
	// simulate, and setting them here caused the scripted turn's
	// persistence to silently fail (a real, but out-of-scope-to-chase, gap
	// in this test double — not a ChatUI bug). Once the grid is focused for
	// real (proving the RecordSetID chatshell reports — grid.Model.Current
	// reports the highlighted ROW's ref, so the fixture needs at least one
	// row or FocusedRef is nil), the DTQL/Database fields
	// openSaveQueryDialog needs are set directly on the already-persisted
	// RecordSet.
	turn := Turn{Queries: []QueryResult{{Title: "Prague customers", RecordSetID: "rs1", Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show customers"))
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("could not focus query result")
	}
	recordSetID := u.activeRecordSetID()
	if recordSetID == "" {
		t.Fatal("grid did not report a focused RecordSet")
	}
	record := u.snapshot.RecordSets[recordSetID]
	record.Title = "Prague customers"
	record.DTQL = "from: {name: Customer}\nlimit: 50\n"
	record.Database = "chinook"
	u.snapshot.RecordSets[recordSetID] = record
	service := &savedQueryStub{}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.KeyPressMsg{Text: "q"})
	if !strings.Contains(u.shell.View().Content, "Save as project query") {
		t.Fatal("save query dialog did not open")
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	for _, r := range "customers" {
		u.shell.Update(tea.KeyPressMsg{Text: string(r)})
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(u.shell.View().Content, "customers") {
		t.Fatal("tag was not added as a chip")
	}
	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("Ctrl+S did not save the query")
	}
	drainCmd(t, u, cmd)
	if service.saved.Title != "Prague customers" || service.saved.Type != "DTQL" || service.saved.Database != "chinook" || len(service.saved.Tags) != 1 || service.saved.Tags[0] != "customers" {
		t.Fatalf("wrong save request: %+v", service.saved)
	}
}

func TestChatUIParameterizedSavedQueryPromptsThenRunsWithValues(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "customers/invoices", Title: "Customer invoices", Type: "DTQL", Parameters: []SavedQueryParameter{{ID: "CustomerId", Type: "integer", Required: true}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/query invoices"))
	if service.ranID != "" || !strings.Contains(u.shell.View().Content, "Run saved query") {
		t.Fatal("parameterized query should open a dialog before execution")
	}
	// Missing required parameter blocks Run.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || service.ranID != "" {
		t.Fatal("missing required parameter was accepted")
	}
	// Fill it in and run.
	u.shell.Update(tea.KeyPressMsg{Text: "5"})
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, cmd = u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("filled parameter dialog did not run the query")
	}
	drainCmd(t, u, cmd)
	if service.ranID != "customers/invoices" || service.vars["CustomerId"] != "5" || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("parameterized query not persisted: id=%q vars=%v records=%d", service.ranID, service.vars, len(u.snapshot.RecordSets))
	}
}

func TestChatUIHTTPSavedQueryUsesProjectRunner(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{queries: []SavedQuery{{ID: "api/people", Title: "People API", Type: "HTTP"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	drainCmd(t, u, u.Submit("/queries People"))
	if service.ranID != "api/people" || len(u.snapshot.HTTPResponses) != 0 || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("HTTP query did not use project runner: run=%q responses=%d recordsets=%d", service.ranID, len(u.snapshot.HTTPResponses), len(u.snapshot.RecordSets))
	}
	if u.snapshot.Messages[0].Text != "Run query: People API" {
		t.Fatalf("wrong user message: %+v", u.snapshot.Messages)
	}
}
