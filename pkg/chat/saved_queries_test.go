package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

type savedQueryStub struct {
	queries []SavedQuery
	ranID   string
	saved   SavedQuerySaveRequest
	vars    map[string]string
	runErr  error
	saveErr error
}

func (s *savedQueryStub) List(context.Context) ([]SavedQuery, error) { return s.queries, nil }
func (s *savedQueryStub) Run(_ context.Context, id string) (QueryResult, error) {
	s.ranID = id
	if s.runErr != nil {
		return QueryResult{}, s.runErr
	}
	return QueryResult{Title: "Prague customers", Source: "sqlite:///chinook.db", DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}}}, nil
}

func TestSavedQueryErrorDoesNotPersistParameterValue(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	service := &savedQueryStub{queries: []SavedQuery{{ID: "x", Title: "Private query", Type: "DTQL"}}, runErr: errors.New("invalid --var CustomerId=secret123")}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	u.input.SetValue("/query Private")
	_, command := u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, _ = u.Update(command())
	for _, message := range u.snapshot.Messages {
		if strings.Contains(message.Text, "secret123") {
			t.Fatalf("execution error leaked parameter value: %+v", message)
		}
	}
}
func (s *savedQueryStub) Save(_ context.Context, request SavedQuerySaveRequest) (SavedQuery, error) {
	s.saved = request
	if s.saveErr != nil {
		return SavedQuery{}, s.saveErr
	}
	query := SavedQuery{ID: "saved-1", Title: request.Title, Type: request.Type, Tags: request.Tags}
	s.queries = append(s.queries, query)
	return query, nil
}

func TestSaveQueryFailureKeepsDraftAndHidesErrorDetails(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	service := &savedQueryStub{saveErr: errors.New("private-token-123")}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	u.saveQueryDialog = &saveQueryDialog{request: SavedQuerySaveRequest{Type: "DTQL", Text: "from: {name: Customer}"}, name: textinput.New(), tagInput: textinput.New()}
	u.saveQueryDialog.name.SetValue("Customers")
	command := u.saveQueryFromDialog()
	_, _ = u.Update(command())
	if u.saveQueryDialog == nil || u.saveQueryDialog.name.Value() != "Customers" || strings.Contains(u.View().Content, "private-token-123") {
		t.Fatal("save error lost form data or exposed backend details")
	}
}

func TestSaveQueryRejectsParameterizedResultBeforeOpeningDraft(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.savedQueryService = &savedQueryStub{}
	u.gridFocused = true
	u.activeGrid = 0
	u.entries = []historyEntry{{recordSetID: "result"}}
	u.snapshot.RecordSets = map[string]RecordSet{"result": {ID: "result", DTQL: "from: {name: Invoice}\nwhere: {op: '==', left: {field: CustomerId}, right: {param: CustomerId}}", Parameters: map[string]any{"CustomerId": 5}}}
	u.openSaveQueryDialog()
	if u.saveQueryDialog != nil || !strings.Contains(u.entries[len(u.entries)-1].text, "parameter declarations") {
		t.Fatal("parameterized result silently opened a lossy save draft")
	}
}
func (s *savedQueryStub) RunWithVariables(ctx context.Context, id string, variables map[string]string) (QueryResult, error) {
	s.vars = variables
	return s.Run(ctx, id)
}

func TestSavedQueryAutocompleteAliasesFilterAndExecute(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	service := &savedQueryStub{queries: []SavedQuery{{ID: "customers/prague", Title: "Prague customers", Type: "DTQL", Tags: []string{"city"}}, {ID: "orders/recent", Title: "Recent orders", Type: "HTTP"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"/query prague", "/queries prague"} {
		u.input.SetValue(command)
		matches := u.savedQueryMatches()
		if len(matches) != 1 || matches[0].ID != "customers/prague" || !strings.Contains(u.savedQueryMenuView(80), "[DTQL]") {
			t.Fatalf("%q did not filter and label saved queries: %+v", command, matches)
		}
	}
	u.input.SetValue("/queries prague")
	_, command := u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("selecting saved query did not start execution")
	}
	_, _ = u.Update(command())
	if service.ranID != "customers/prague" || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("saved query was not executed and persisted: id=%q records=%d", service.ranID, len(u.snapshot.RecordSets))
	}
	if len(u.snapshot.Messages) < 2 || u.snapshot.Messages[0].Role != "You" || u.snapshot.Messages[0].Text != "Run query: Prague customers" {
		t.Fatalf("query name was not recorded as a user message: %+v", u.snapshot.Messages)
	}
}

func TestSaveQueryDialogCollectsNameAndTagChips(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	service := &savedQueryStub{}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	origin, err := store.AppendUser(ctx, u.sessionID, "Show customers")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendQuery(ctx, u.sessionID, origin.ID, "sqlite:///chinook.db", QueryResult{Title: "Prague customers", SourceID: "chinook", DTQL: "from: {name: Customer}\nlimit: 50\n", Result: secureread.Result{Columns: []string{"CustomerId"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(ctx, u.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	if !u.focusLatestGrid() {
		t.Fatal("could not focus query result")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "q"})
	if u.saveQueryDialog == nil || !strings.Contains(u.View().Content, "Save as project query") {
		t.Fatal("save query dialog did not open")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	for _, r := range "customers" {
		_, _ = u.Update(tea.KeyPressMsg{Text: string(r)})
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(u.saveQueryDialog.tags) != 1 || u.saveQueryDialog.tags[0] != "customers" {
		t.Fatalf("tag was not added as chip: %+v", u.saveQueryDialog.tags)
	}
	_, command := u.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("Ctrl+S did not save the query")
	}
	_, _ = u.Update(command())
	if service.saved.Title != "Prague customers" || service.saved.Type != "DTQL" || service.saved.Database != "chinook" || len(service.saved.Tags) != 1 {
		t.Fatalf("wrong save request: %+v", service.saved)
	}
}

func TestParameterizedSavedQueryPromptsThenRunsWithValues(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	service := &savedQueryStub{queries: []SavedQuery{{ID: "customers/invoices", Title: "Customer invoices", Type: "DTQL", Parameters: []SavedQueryParameter{{ID: "CustomerId", Type: "integer", Required: true}}}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	u.input.SetValue("/query invoices")
	_, command := u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || u.queryParameters == nil || service.ranID != "" {
		t.Fatal("parameterized query should open a dialog before execution")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, command = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || u.queryParameters.err == "" {
		t.Fatal("missing required parameter was accepted")
	}
	u.queryParameters.inputs[0].SetValue("5")
	u.queryParameters.moveFocus(1)
	_, command = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("filled parameter dialog did not run the query")
	}
	_, _ = u.Update(command())
	if service.ranID != "customers/invoices" || service.vars["CustomerId"] != "5" || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("parameterized query not persisted: id=%q vars=%v records=%d", service.ranID, service.vars, len(u.snapshot.RecordSets))
	}
}

func TestHTTPSavedQueryUsesProjectRunner(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	service := &savedQueryStub{queries: []SavedQuery{{ID: "api/people", Title: "People API", Type: "HTTP"}}}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	u.input.SetValue("/queries People")
	_, command := u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("saved HTTP query did not start")
	}
	_, _ = u.Update(command())
	if service.ranID != "api/people" || len(u.snapshot.HTTPResponses) != 0 || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("HTTP query did not use project runner: run=%q responses=%d recordsets=%d", service.ranID, len(u.snapshot.HTTPResponses), len(u.snapshot.RecordSets))
	}
	if u.snapshot.Messages[0].Text != "Run query: People API" {
		t.Fatalf("wrong user message: %+v", u.snapshot.Messages)
	}
}
