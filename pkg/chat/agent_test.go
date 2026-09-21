package chat

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
	_ "modernc.org/sqlite"
)

type fakeExecutor struct {
	result secureread.Result
	err    error
	calls  int
	doc    string
	source string
	params map[string]any
}

func (f *fakeExecutor) RunDTQL(_ context.Context, source string, doc []byte, params map[string]any) (secureread.Result, error) {
	f.calls++
	f.doc = string(doc)
	f.source = source
	f.params = params
	return f.result, f.err
}

func TestRunDTQLBindsAttachedSelectionLocally(t *testing.T) {
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId"}}}
	conversation := &ADKConversation{sources: map[string]string{"chinook-local": "sqlite:///chinook.db"}}
	ctx := withSelectionParameters(context.Background(), func() map[string]any {
		return map[string]any{"selection_1_c1": []any{int64(5), int64(6)}, "selection_2_c1": []any{"unrelated private value"}}
	})
	doc := "from: {name: Invoice}\nwhere:\n  op: In\n  left: {field: CustomerId}\n  right: {param: selection_1_c1}\nlimit: 20"
	response, err := conversation.runDTQL(ctx, executor, "sqlite:///other.db", runDTQLArgs{SourceID: "chinook-local", DTQL: doc})
	if err != nil || !response.OK {
		t.Fatalf("runDTQL = %+v, %v", response, err)
	}
	if executor.source != "sqlite:///chinook.db" || !reflect.DeepEqual(executor.params["selection_1_c1"], []any{int64(5), int64(6)}) {
		t.Fatalf("local binding = source %q params %#v", executor.source, executor.params)
	}
	if _, ok := executor.params["selection_2_c1"]; ok {
		t.Fatal("unreferenced selection was passed to DALgo")
	}
	if !reflect.DeepEqual(conversation.takePending()[0].Parameters, executor.params) {
		t.Fatal("query lineage lost locally bound parameters")
	}
	response, err = conversation.runDTQL(context.Background(), executor, "sqlite:///other.db", runDTQLArgs{SourceID: "unregistered", DTQL: doc})
	if err != nil || response.Error == "" || executor.calls != 1 {
		t.Fatalf("unknown source was executed: %+v, %v, calls=%d", response, err, executor.calls)
	}
}

func TestRunDTQLReturnsPersistedRecordSetReference(t *testing.T) {
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}}}
	conversation := &ADKConversation{}
	ctx := withQueryObserver(context.Background(), func(query QueryResult) (QueryResult, error) {
		query.RecordSetID = "saved-recordset"
		return query, nil
	})
	response, err := conversation.runDTQL(ctx, executor, "sqlite:///chinook.db", runDTQLArgs{DTQL: "from: {name: Customer}\nlimit: 5"})
	if err != nil || !response.OK || response.RecordSetID != "saved-recordset" {
		t.Fatalf("query tool did not return its saved RecordSet reference: %+v, %v", response, err)
	}
}

func TestRunDTQLRefusesModelAuthoredJoin(t *testing.T) {
	executor := &fakeExecutor{}
	conversation := &ADKConversation{}
	doc := `from:
  name: Invoice
  alias: i
  joins:
    - from: {name: Customer, alias: c}
      on: [{left: {field: CustomerId, source: i}, op: '==', right: {field: CustomerId, source: c}}]
limit: 5
`
	response, err := conversation.runDTQL(context.Background(), executor, "sqlite:///chinook.db", runDTQLArgs{DTQL: doc})
	if err != nil || response.OK || !strings.Contains(response.Error, "foreign-key candidate") || executor.calls != 0 {
		t.Fatalf("model-authored JOIN was not refused before execution: %+v, %v, calls=%d", response, err, executor.calls)
	}
}

func TestAgentJoinToolUsesSameApplicationOperation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	llm := &scriptedLLM{}
	queryExecutor := &fakeExecutor{}
	conversation, err := NewADKConversation(llm, queryExecutor, "sqlite:///fixture.db", "- Invoice: InvoiceId, CustomerId")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, conversation, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, chat.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": "private-row-value"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	joinExecutor := &joinExecutorStub{}
	chat.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: joinExecutor})
	candidates, err := chat.JoinCandidates(ctx, base.RecordSetID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %#v, %v", candidates, err)
	}
	llm.responses = []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("apply_join_candidate", map[string]any{"recordSetId": base.RecordSetID, "candidateId": string(candidates[0].ID)}, genai.RoleModel)},
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"title": "Placeholder", "dtql": "from: {name: Invoice}\nlimit: 1"}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Joined.", genai.RoleModel)},
	}
	turn, err := chat.Ask(ctx, "Join customers")
	if err != nil || len(turn.Actions) != 1 || turn.Actions[0].Err != nil {
		t.Fatalf("agent JOIN turn = %+v, %v", turn, err)
	}
	if len(turn.Queries) != 0 || queryExecutor.calls != 0 {
		t.Fatalf("model's follow-up query should not execute or create a second grid: queries=%+v calls=%d", turn.Queries, queryExecutor.calls)
	}
	agentResultID := turn.Actions[0].Reference.ObjectID
	if agentResultID == "" {
		t.Fatal("agent did not return a persisted RecordSet ID")
	}
	uiResult, err := chat.ApplyJoinCandidate(ctx, base.RecordSetID, candidates[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agentResult := snapshot.RecordSets[agentResultID]
	if agentResult.DTQL != uiResult.DTQL || agentResult.Lineage == nil || uiResult.Lineage == nil {
		t.Fatalf("agent/UI JOIN paths diverged: %#v / %#v", agentResult, uiResult)
	}
	if len(llm.requests) == 0 || len(llm.requests[0].Contents) == 0 {
		t.Fatal("no model request captured")
	}
	prompt := llm.requests[0].Contents[0].Parts[0].Text
	if !strings.Contains(prompt, string(candidates[0].ID)) || strings.Contains(prompt, "private-row-value") {
		t.Fatalf("candidate context missing or row value leaked: %q", prompt)
	}
}

func TestAgentJoinTargetAmbiguityCannotBeGuessed(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	llm := &scriptedLLM{}
	conversation, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Order: BillingAddressId, ShippingAddressId")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, conversation, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, chat.activeID, "Show orders")
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.AppendQuery(ctx, chat.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Orders", DTQL: "from: {name: Order}\ncolumns: [{field: OrderId}]\nlimit: 5\n",
		Result: secureread.Result{Columns: []string{"OrderId"}, Rows: []secureread.Row{{Data: map[string]any{"OrderId": 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ForeignKeySnapshot{Source: "sqlite:///fixture.db", Columns: map[string][]string{
		"main.order":   {"OrderId", "BillingAddressId", "ShippingAddressId"},
		"main.address": {"AddressId", "City"},
	}, Keys: []ForeignKey{
		{ConstraintID: "billing", Schema: "main", FromRelation: "Order", FromFields: []string{"BillingAddressId"}, ToSchema: "main", ToRelation: "Address", ToFields: []string{"AddressId"}},
		{ConstraintID: "shipping", Schema: "main", FromRelation: "Order", FromFields: []string{"ShippingAddressId"}, ToSchema: "main", ToRelation: "Address", ToFields: []string{"AddressId"}},
	}}
	joinExecutor := &fakeExecutor{result: secureread.Result{Columns: []string{"OrderId"}}}
	chat.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: snapshot, Executor: joinExecutor})
	candidates, err := chat.JoinCandidates(ctx, base.RecordSetID)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates = %#v, %v", candidates, err)
	}
	var shipping JoinCandidateID
	for _, candidate := range candidates {
		if candidate.ConstraintID == "shipping" {
			shipping = candidate.ID
		}
	}
	if shipping == "" {
		t.Fatal("shipping edge missing")
	}
	call := func() *model.LLMResponse {
		return &model.LLMResponse{Content: genai.NewContentFromFunctionCall("apply_join_candidate", map[string]any{"recordSetId": base.RecordSetID, "candidateId": string(shipping)}, genai.RoleModel)}
	}
	llm.responses = []*model.LLMResponse{call(), {Content: genai.NewContentFromText("Done.", genai.RoleModel)}}
	turn, err := chat.Ask(ctx, "Join address")
	if err != nil || len(turn.Actions) != 1 || turn.Actions[0].Err == nil || joinExecutor.calls != 0 {
		t.Fatalf("ambiguous target was guessed: turn=%+v err=%v calls=%d", turn, err, joinExecutor.calls)
	}
	llm.responses = []*model.LLMResponse{call(), {Content: genai.NewContentFromText("Done.", genai.RoleModel)}}
	llm.calls = 0
	turn, err = chat.Ask(ctx, "Join shipping address")
	if err != nil || len(turn.Actions) != 1 || turn.Actions[0].Err != nil || joinExecutor.calls != 1 {
		t.Fatalf("explicit shipping edge was refused: turn=%+v err=%v calls=%d", turn, err, joinExecutor.calls)
	}
}

func TestQueryToolDoesNotReturnLocallyBoundValuesInErrors(t *testing.T) {
	const secret = "Paris-private-selected-value"
	conversation := &ADKConversation{}
	executor := &fakeExecutor{err: errors.New("driver rejected parameter " + secret)}
	ctx := withSelectionParameters(context.Background(), func() map[string]any {
		return map[string]any{"selection_1_c1": []any{secret}}
	})
	doc := "from: {name: Customer}\nwhere:\n  op: In\n  left: {field: City}\n  right: {param: selection_1_c1}\nlimit: 5"
	response, err := conversation.runDTQL(ctx, executor, "sqlite:///chinook.db", runDTQLArgs{DTQL: doc})
	if err != nil || response.OK || strings.Contains(response.Error, secret) {
		t.Fatalf("private parameter leaked through query tool: %+v, %v", response, err)
	}
}

func TestAgentWorkspaceToolUsesSameApplicationAction(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chat.Snapshot(ctx)
	recordID := workspaceTestRecord(t, store, session.ID)
	action := WorkspaceAction{Kind: "select", RecordSetID: recordID, Column: "City", Equals: "Prague", Limit: 1}
	agent := &ADKConversation{}
	toolContext := withWorkspaceObserver(ctx, func(a WorkspaceAction) (ContextReference, error) {
		return chat.ApplyWorkspaceAction(ctx, a)
	})
	response := agent.runWorkspaceAction(toolContext, action)
	if !response.OK || response.Kind != "selection" {
		t.Fatalf("agent action = %+v", response)
	}
	viaUI, err := chat.ApplyWorkspaceAction(ctx, action)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := chat.Snapshot(ctx)
	if err != nil || len(saved.Workspace.Selections) != 2 {
		t.Fatalf("shared application state = %+v, %v", saved.Workspace, err)
	}
	agentSelection := saved.Workspace.Selections[response.ID]
	uiSelection := saved.Workspace.Selections[viaUI.ObjectID]
	if !reflect.DeepEqual(agentSelection.Rows, uiSelection.Rows) || !reflect.DeepEqual(agentSelection.Columns, uiSelection.Columns) {
		t.Fatalf("agent/UI diverged: %+v / %+v", agentSelection, uiSelection)
	}
}

type scriptedLLM struct {
	mu        sync.Mutex
	responses []*model.LLMResponse
	errs      []error
	requests  []*model.LLMRequest
	calls     int
}

func (m *scriptedLLM) Name() string { return "scripted" }

func (m *scriptedLLM) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests = append(m.requests, request)
		if m.calls >= len(m.responses) {
			yield(nil, errors.New("script exhausted"))
			return
		}
		if m.calls < len(m.errs) && m.errs[m.calls] != nil {
			err := m.errs[m.calls]
			m.calls++
			yield(nil, err)
			return
		}
		response := m.responses[m.calls]
		m.calls++
		yield(response, nil)
	}
}

func TestADKConversation_PreservesToolResultWhenFinalModelCallFails(t *testing.T) {
	doc := "from: {name: Customer}\nlimit: 1\n"
	llm := &scriptedLLM{
		responses: []*model.LLMResponse{
			{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)},
			nil,
		},
		errs: []error{nil, errors.New("provider unavailable")},
	}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}}}
	conversation, err := NewADKConversation(llm, executor, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "show one customer")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if executor.calls != 1 || len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("turn = %+v, executor calls = %d", turn, executor.calls)
	}
}

func TestADKConversation_BoundsRepeatedToolCalls(t *testing.T) {
	doc := "from: {name: Customer}\nlimit: 1\n"
	call := func() *model.LLMResponse {
		return &model.LLMResponse{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)}
	}
	llm := &scriptedLLM{responses: []*model.LLMResponse{call(), call(), call(), call()}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}}}
	conversation, err := NewADKConversation(llm, executor, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "keep querying")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if executor.calls != maxToolCallsPerTurn {
		t.Fatalf("executor calls = %d, want %d", executor.calls, maxToolCallsPerTurn)
	}
	if llm.calls != maxModelCallsPerTurn {
		t.Fatalf("model calls = %d, want %d", llm.calls, maxModelCallsPerTurn)
	}
	if len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("bounded turn = %+v", turn)
	}
}

func TestADKConversation_AppliesThinkingLevelToADKRequests(t *testing.T) {
	llm := &scriptedLLM{responses: []*model.LLMResponse{{Content: genai.NewContentFromText("Unsupported.", genai.RoleModel)}}}
	conversation, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer", WithThinkingLevel("low"))
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	if _, err := conversation.Ask(context.Background(), "unsupported request"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(llm.requests) != 1 || llm.requests[0].Config == nil || llm.requests[0].Config.ThinkingConfig == nil || llm.requests[0].Config.ThinkingConfig.ThinkingBudget == nil {
		t.Fatalf("request config = %+v", llm.requests)
	}
	if got := *llm.requests[0].Config.ThinkingConfig.ThinkingBudget; got != 500 {
		t.Fatalf("thinking budget = %d, want 500", got)
	}
	if _, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer", WithThinkingLevel("extreme")); err == nil {
		t.Fatal("expected invalid thinking level error")
	}
}

func TestADKConversationRebuildsEachTurnFromExplicitContext(t *testing.T) {
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromText("First answer", genai.RoleModel)},
		{Content: genai.NewContentFromText("Second answer", genai.RoleModel)},
	}}
	conversation, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.AskWithContext(context.Background(), "first", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.AskWithContext(context.Background(), "second", "RecordSet rs-1: CustomerId 5"); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 2 {
		t.Fatalf("model requests = %d", len(llm.requests))
	}
	if len(llm.requests[1].Contents) != 1 {
		t.Fatalf("second request inherited opaque ADK history: %+v", llm.requests[1].Contents)
	}
	secondPrompt := llm.requests[1].Contents[0].Parts[0].Text
	if !strings.Contains(secondPrompt, "RecordSet rs-1") || !strings.Contains(secondPrompt, "Current user request:\nsecond") {
		t.Fatalf("rebuilt prompt = %q", secondPrompt)
	}
}

func TestADKConversation_ModelToolResponseRunsDTQL(t *testing.T) {
	doc := "from:\n  name: Customer\nlimit: 2\n"
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"title": "Customers", "dtql": doc}, genai.RoleModel)},
		{Content: genai.NewContentFromText("Here are the customers.", genai.RoleModel)},
	}}
	executor := &fakeExecutor{result: secureread.Result{
		Columns: []string{"CustomerId"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1}}},
	}}
	conversation, err := NewADKConversation(llm, executor, "sqlite:///fixture.db", "- Customer (schema: main; BASE TABLE): CustomerId [INTEGER]")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "show customers")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if executor.calls != 1 || executor.doc != strings.TrimSpace(doc) || executor.source != "sqlite:///fixture.db" {
		t.Fatalf("executor call = count %d, source %q, doc %q", executor.calls, executor.source, executor.doc)
	}
	if turn.Text != "" {
		t.Errorf("successful query Text = %q, want empty because the grid is the answer", turn.Text)
	}
	if len(turn.Queries) != 1 || len(turn.Queries[0].Result.Rows) != 1 {
		t.Fatalf("Queries = %+v", turn.Queries)
	}
	if turn.Queries[0].Title != "Customers" {
		t.Fatalf("query title = %q, want Customers", turn.Queries[0].Title)
	}
}

func TestADKConversation_TextResponseFiltersThoughts(t *testing.T) {
	llm := &scriptedLLM{
		responses: []*model.LLMResponse{
			{Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{
					{Text: "internal reasoning", Thought: true},
					{Text: "That request needs a join, which this PoC does not support."},
				},
			}},
		},
	}
	conversation, err := NewADKConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Track")
	if err != nil {
		t.Fatalf("NewADKConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "show tracks by artist")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if turn.Text != "That request needs a join, which this PoC does not support." {
		t.Fatalf("Text = %q", turn.Text)
	}
	if len(turn.Queries) != 0 {
		t.Fatalf("Queries = %+v", turn.Queries)
	}
}

func TestRunDTQLTool_EmptyAndExecutionErrorsStayStructured(t *testing.T) {
	conversation := &ADKConversation{}
	executor := &fakeExecutor{err: errors.New("column foo does not exist")}

	empty, err := conversation.runDTQL(context.Background(), executor, "sqlite:///fixture.db", runDTQLArgs{})
	if err != nil {
		t.Fatalf("empty tool call: %v", err)
	}
	if empty.OK || !strings.Contains(empty.Error, "must not be empty") || executor.calls != 0 {
		t.Fatalf("empty response = %+v, calls = %d", empty, executor.calls)
	}

	failed, err := conversation.runDTQL(context.Background(), executor, "sqlite:///fixture.db", runDTQLArgs{DTQL: "from: {name: customers}\nlimit: 10"})
	if err != nil {
		t.Fatalf("failed tool call: %v", err)
	}
	if failed.OK || !strings.Contains(failed.Error, "column foo does not exist") {
		t.Fatalf("failed response = %+v", failed)
	}
	queries := conversation.takePending()
	if len(queries) != 2 || queries[0].Err == nil || queries[1].Err == nil {
		t.Fatalf("captured queries = %+v", queries)
	}
}

func TestRunDTQLTool_RealValidationExecutionAndEmptyResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, City TEXT); INSERT INTO Customer VALUES (1, 'Prague'), (2, 'Dublin'); CREATE TABLE Invoice (InvoiceId INTEGER PRIMARY KEY, CustomerId INTEGER, Total NUMERIC); INSERT INTO Invoice VALUES (10, 1, 5.00), (11, 1, 7.00), (12, 2, 4.00)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	conversation := &ADKConversation{}
	source := "sqlite://" + path

	valid := `from:
  name: Customer
columns:
  - field: CustomerId
  - field: City
where:
  op: ==
  left: {field: City}
  right: {value: Prague}
limit: 50`
	response, err := conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: valid})
	if err != nil || !response.OK || response.Rows != 1 {
		t.Fatalf("real DTQL response = %+v, err = %v", response, err)
	}

	empty := strings.Replace(valid, "Prague", "Nowhere", 1)
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: empty})
	if err != nil || !response.OK || response.Rows != 0 {
		t.Fatalf("empty DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	withLocalIDs := withSelectionParameters(context.Background(), func() map[string]any {
		return map[string]any{"selection_1_c1": []any{int64(1), int64(2)}}
	})
	bound := "from: {name: Customer}\nwhere:\n  op: In\n  left: {field: CustomerId}\n  right: {param: selection_1_c1}\nlimit: 50"
	response, err = conversation.runDTQL(withLocalIDs, executor, source, runDTQLArgs{DTQL: bound})
	if err != nil || !response.OK || response.Rows != 2 {
		t.Fatalf("locally bound DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	aggregated := `from: {name: Invoice}
groupBy:
  - field: CustomerId
orderBy:
  - field: TotalAmount
    desc: true
limit: 20
columns:
  - field: CustomerId
  - aggregate: {function: COUNT, args: [{star: true}]}
    as: OrderCount
  - aggregate: {function: SUM, args: [{field: Total}]}
    as: TotalAmount`
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: aggregated})
	if err != nil || !response.OK || response.Rows != 2 {
		t.Fatalf("aggregated DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: "limit: broken"})
	if err != nil || response.OK || !strings.Contains(response.Error, "invalid DTQL") {
		t.Fatalf("malformed DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: "from: {name: Customer}"})
	if err != nil || response.OK || !strings.Contains(response.Error, "limit must be") {
		t.Fatalf("unbounded DTQL response = %+v, err = %v", response, err)
	}

	conversation.resetTurn()
	badColumn := strings.Replace(valid, "CustomerId", "MissingColumn", 1)
	response, err = conversation.runDTQL(context.Background(), executor, source, runDTQLArgs{DTQL: badColumn})
	if err != nil || response.OK || !strings.Contains(response.Error, "MissingColumn") {
		t.Fatalf("execution error response = %+v, err = %v", response, err)
	}
}

func TestNewADKConversationRejectsMissingDependencies(t *testing.T) {
	executor := &fakeExecutor{}
	if _, err := NewADKConversation(nil, executor, "sqlite:///x", "schema"); err == nil {
		t.Fatal("expected missing model error")
	}
	if _, err := NewADKConversation(&scriptedLLM{}, nil, "sqlite:///x", "schema"); err == nil {
		t.Fatal("expected missing executor error")
	}
	if _, err := NewADKConversation(&scriptedLLM{}, executor, "", "schema"); err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestFinalQueriesHidesCorrectedFailures(t *testing.T) {
	failure := QueryResult{Err: errors.New("bad table")}
	success := QueryResult{Result: secureread.Result{Columns: []string{"id"}}}
	got := finalQueries([]QueryResult{failure, success})
	if len(got) != 1 || got[0].Err != nil {
		t.Fatalf("final queries = %+v", got)
	}
	got = finalQueries([]QueryResult{success, success})
	if len(got) != 1 {
		t.Fatalf("duplicate successful queries = %+v", got)
	}
	got = finalQueries([]QueryResult{failure, {Err: errors.New("still bad")}})
	if len(got) != 1 || got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "still bad") {
		t.Fatalf("final failed queries = %+v", got)
	}
}
