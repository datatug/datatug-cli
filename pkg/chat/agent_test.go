package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"iter"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
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
	conversation := &AIConversation{sources: map[string]string{"chinook-local": "sqlite:///chinook.db"}}
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
	conversation := &AIConversation{}
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
	conversation := &AIConversation{}
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
	llm := &scriptedProvider{}
	queryExecutor := &fakeExecutor{}
	conversation, err := NewAIConversation(llm, queryExecutor, "sqlite:///fixture.db", "- Invoice: InvoiceId, CustomerId")
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
	llm.steps = []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolApplyJoinCandidate, map[string]any{"recordSetId": base.RecordSetID, "candidateId": string(candidates[0].ID)})}},
		{toolCalls: []ai.ToolCall{toolCall("2", toolRunDTQL, map[string]any{"title": "Placeholder", "dtql": "from: {name: Invoice}\nlimit: 1"})}},
		{text: "Joined."},
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
	if len(llm.requests) == 0 || len(llm.requests[0].Messages) == 0 {
		t.Fatal("no model request captured")
	}
	prompt := llm.requests[0].Messages[0].Text
	if !strings.Contains(prompt, string(candidates[0].ID)) || strings.Contains(prompt, "private-row-value") {
		t.Fatalf("candidate context missing or row value leaked: %q", prompt)
	}
}

func TestAgentJoinTargetAmbiguityCannotBeGuessed(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	llm := &scriptedProvider{}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Order: BillingAddressId, ShippingAddressId")
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
	step := func() scriptedStep {
		return scriptedStep{toolCalls: []ai.ToolCall{toolCall("1", toolApplyJoinCandidate, map[string]any{"recordSetId": base.RecordSetID, "candidateId": string(shipping)})}}
	}
	llm.steps = []scriptedStep{step(), {text: "Done."}}
	turn, err := chat.Ask(ctx, "Join address")
	if err != nil || len(turn.Actions) != 1 || turn.Actions[0].Err == nil || joinExecutor.calls != 0 {
		t.Fatalf("ambiguous target was guessed: turn=%+v err=%v calls=%d", turn, err, joinExecutor.calls)
	}
	llm.steps = []scriptedStep{step(), {text: "Done."}}
	llm.calls = 0
	turn, err = chat.Ask(ctx, "Join shipping address")
	if err != nil || len(turn.Actions) != 1 || turn.Actions[0].Err != nil || joinExecutor.calls != 1 {
		t.Fatalf("explicit shipping edge was refused: turn=%+v err=%v calls=%d", turn, err, joinExecutor.calls)
	}
}

func TestQueryToolDoesNotReturnLocallyBoundValuesInErrors(t *testing.T) {
	const secret = "Paris-private-selected-value"
	conversation := &AIConversation{}
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
	agent := &AIConversation{}
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

// scriptedStep is one queued agent.Loop model step: either a text answer, one
// or more tool calls (mutually exclusive here, as every real adapter step
// is), optional usage, or a fatal error -- mirroring how the old ADK fake
// scripted one *model.LLMResponse per model call.
type scriptedStep struct {
	text      string
	toolCalls []ai.ToolCall
	usage     *ai.Usage
	err       *ai.Error
}

// scriptedProvider is a fake ai.LLMProvider (see ai/agent's Provider field)
// that replays one scriptedStep per Stream call, in order -- one call per
// agent.Loop step, replacing the old fake ADK model.LLM.
type scriptedProvider struct {
	mu       sync.Mutex
	steps    []scriptedStep
	requests []ai.ChatRequest
	calls    int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Stream(_ context.Context, req ai.ChatRequest) iter.Seq2[ai.Event, error] {
	return func(yield func(ai.Event, error) bool) {
		p.mu.Lock()
		p.requests = append(p.requests, req)
		if p.calls >= len(p.steps) {
			p.mu.Unlock()
			err := &ai.Error{Code: ai.ErrCodeUpstream, Message: "scriptedProvider: script exhausted"}
			yield(ai.Event{Type: ai.EventError, Error: err}, err)
			return
		}
		step := p.steps[p.calls]
		p.calls++
		p.mu.Unlock()

		if !yield(ai.Event{Type: ai.EventStarted}, nil) {
			return
		}
		if step.err != nil {
			yield(ai.Event{Type: ai.EventError, Error: step.err}, step.err)
			return
		}
		if step.text != "" {
			if !yield(ai.Event{Type: ai.EventTextDelta, Text: step.text}, nil) {
				return
			}
		}
		for i := range step.toolCalls {
			call := step.toolCalls[i]
			if !yield(ai.Event{Type: ai.EventToolCall, ToolCall: &call}, nil) {
				return
			}
		}
		if step.usage != nil {
			// Real adapters report usage as its own progressive event (see
			// ai.LLMProvider's documented Stream contract), not only as a
			// field tacked onto the terminal EventCompleted -- and
			// agent.Loop swallows each step's own EventCompleted, so a fake
			// that only set Completed.Usage would make per-step usage
			// unobservable to a caller ranging over Loop.Run.
			if !yield(ai.Event{Type: ai.EventUsage, Usage: step.usage}, nil) {
				return
			}
		}
		stop := ai.StopReasonEnd
		if len(step.toolCalls) > 0 {
			stop = ai.StopReasonToolCalls
		}
		yield(ai.Event{Type: ai.EventCompleted, StopReason: stop}, nil)
	}
}

func toolCall(id, name string, args any) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: name, Arguments: mustJSON(args)}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestAIConversation_PreservesToolResultWhenFinalModelCallFails(t *testing.T) {
	doc := "from: {name: Customer}\nlimit: 1\n"
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"dtql": doc})}, usage: &ai.Usage{InputTokens: 10, OutputTokens: 5}},
		{err: &ai.Error{Code: ai.ErrCodeUpstream, Message: "provider unavailable"}},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}}}
	conversation, err := NewAIConversation(llm, executor, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatalf("NewAIConversation: %v", err)
	}
	turn, err := conversation.Ask(context.Background(), "show one customer")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if executor.calls != 1 || len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("turn = %+v, executor calls = %d", turn, executor.calls)
	}
	// The first step's ai.EventUsage was observed live while ranging over
	// loop.Run, before the second step's fatal error arrived, so it survives
	// even though agent.Loop itself never gets to yield a final summed
	// EventCompleted for this aborted run.
	if turn.Usage == nil || turn.Usage.InputTokens != 10 || turn.Usage.OutputTokens != 5 || turn.Usage.TotalTokens != 15 {
		t.Fatalf("early-return usage = %+v", turn.Usage)
	}
}

func TestAIConversation_AggregatesMixedUsageTotals(t *testing.T) {
	doc := "from: {name: Customer}\nlimit: 1\n"
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"dtql": doc})}, usage: &ai.Usage{InputTokens: 10, OutputTokens: 5}},
		{text: "Done", usage: &ai.Usage{InputTokens: 7, OutputTokens: 3}},
	}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.Ask(context.Background(), "show one customer")
	if err != nil {
		t.Fatal(err)
	}
	if turn.Usage == nil || turn.Usage.InputTokens != 17 || turn.Usage.OutputTokens != 8 || turn.Usage.TotalTokens != 25 {
		t.Fatalf("mixed usage = %+v", turn.Usage)
	}
}

func TestAIConversation_BoundsRepeatedToolCalls(t *testing.T) {
	doc := "from: {name: Customer}\nlimit: 1\n"
	step := func(id string) scriptedStep {
		return scriptedStep{toolCalls: []ai.ToolCall{toolCall(id, toolRunDTQL, map[string]any{"dtql": doc})}}
	}
	llm := &scriptedProvider{steps: []scriptedStep{step("1"), step("2"), step("3"), step("4")}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}}}
	conversation, err := NewAIConversation(llm, executor, "sqlite:///fixture.db", "- Customer")
	if err != nil {
		t.Fatalf("NewAIConversation: %v", err)
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

func TestAIConversation_AppliesThinkingLevelToRequests(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{{text: "Unsupported."}}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer", WithThinkingLevel("low"))
	if err != nil {
		t.Fatalf("NewAIConversation: %v", err)
	}
	if _, err := conversation.Ask(context.Background(), "unsupported request"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(llm.requests) != 1 || llm.requests[0].Reasoning != ai.ReasoningLow {
		t.Fatalf("request reasoning = %+v", llm.requests)
	}
	if _, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer", WithThinkingLevel("extreme")); err == nil {
		t.Fatal("expected invalid thinking level error")
	}
}

func TestAIConversationRebuildsEachTurnFromExplicitContext(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{{text: "First answer"}, {text: "Second answer"}}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Customer")
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
	if len(llm.requests[1].Messages) != 1 {
		t.Fatalf("second request inherited opaque provider history: %+v", llm.requests[1].Messages)
	}
	secondPrompt := llm.requests[1].Messages[0].Text
	if !strings.Contains(secondPrompt, "RecordSet rs-1") || !strings.Contains(secondPrompt, "Current user request:\nsecond") {
		t.Fatalf("rebuilt prompt = %q", secondPrompt)
	}
}

func TestAIConversation_ModelToolResponseRunsDTQL(t *testing.T) {
	doc := "from:\n  name: Customer\nlimit: 2\n"
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"title": "Customers", "dtql": doc})}},
		{text: "Here are the customers."},
	}}
	executor := &fakeExecutor{result: secureread.Result{
		Columns: []string{"CustomerId"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1}}},
	}}
	conversation, err := NewAIConversation(llm, executor, "sqlite:///fixture.db", "- Customer (schema: main; BASE TABLE): CustomerId [INTEGER]")
	if err != nil {
		t.Fatalf("NewAIConversation: %v", err)
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

func TestAIConversation_ReturnsPlainText(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{
		{text: "That request needs a join, which this PoC does not support."},
	}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Track")
	if err != nil {
		t.Fatalf("NewAIConversation: %v", err)
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

// TestAIConversation_TextResponseFiltersThinkTags is the aichat-migration
// equivalent of the pre-migration ADK-era TestADKConversation_TextResponseFiltersThoughts:
// there genai.Part.Thought gave the ADK path a structured field to filter on;
// here, a local/open model streamed over ai/openaicompat has no such field --
// its hidden reasoning arrives inline in ordinary EventTextDelta text,
// wrapped in a <think>...</think> block (see stripThinkTags in agent.go) --
// so the turn's assembled text must still come out with the reasoning
// removed and only the real answer left.
func TestAIConversation_TextResponseFiltersThinkTags(t *testing.T) {
	llm := &scriptedProvider{steps: []scriptedStep{
		{text: "<think>internal reasoning about the join</think>That request needs a join, which this PoC does not support."},
	}}
	conversation, err := NewAIConversation(llm, &fakeExecutor{}, "sqlite:///fixture.db", "- Track")
	if err != nil {
		t.Fatalf("NewAIConversation: %v", err)
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
	conversation := &AIConversation{}
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
	conversation := &AIConversation{}
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

func TestNewAIConversationRejectsMissingDependencies(t *testing.T) {
	executor := &fakeExecutor{}
	if _, err := NewAIConversation(nil, executor, "sqlite:///x", "schema"); err == nil {
		t.Fatal("expected missing model error")
	}
	if _, err := NewAIConversation(&scriptedProvider{}, nil, "sqlite:///x", "schema"); err == nil {
		t.Fatal("expected missing executor error")
	}
	if _, err := NewAIConversation(&scriptedProvider{}, executor, "", "schema"); err == nil {
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
