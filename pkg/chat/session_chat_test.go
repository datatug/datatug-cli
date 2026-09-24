package chat

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
)

func TestFriendlyAgentErrorDoesNotExposeProviderPayload(t *testing.T) {
	if got := friendlyAgentError(fmt.Errorf("chat: agent turn: %w", context.DeadlineExceeded)); got != "The AI request timed out. Please try again." {
		t.Fatalf("timeout message = %q", got)
	}
	if got := friendlyAgentError(errors.New("provider response contains private payload")); strings.Contains(got, "private payload") {
		t.Fatalf("provider payload leaked to UI: %q", got)
	}
}

type contextualStub struct {
	contexts []string
	turns    []Turn
	calls    int
}

func (s *contextualStub) AskWithContext(_ context.Context, _, prior string) (Turn, error) {
	s.contexts = append(s.contexts, prior)
	turn := s.turns[s.calls]
	s.calls++
	return turn, nil
}

func TestSessionChatRebuildsContextAfterRestartAndSwitch(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	firstAgent := &contextualStub{turns: []Turn{{Queries: []QueryResult{{Title: "Orders", DTQL: "from: {name: Invoice}\nlimit: 2", Result: secureread.Result{
		Columns: []string{"InvoiceId", "CustomerId"},
		Rows: []secureread.Row{
			{Data: map[string]any{"InvoiceId": 412, "CustomerId": 58}},
			{Data: map[string]any{"InvoiceId": 411, "CustomerId": 44}},
		},
	}}}}}}
	chat, err := NewSessionChat(ctx, store, firstAgent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	first, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Show last 2 orders"); err != nil {
		t.Fatal(err)
	}
	if firstAgent.contexts[0] != "" {
		t.Fatalf("first context = %q", firstAgent.contexts[0])
	}
	second, err := chat.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("session IDs reused")
	}
	_ = store.Close()
	reloaded := openTestStore(t, path, testScope())
	continuing := &contextualStub{turns: []Turn{{Text: "I can use those identifiers."}}}
	chat, err = NewSessionChat(ctx, reloaded, continuing, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Switch(ctx, first.ID[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Show customers associated with those orders"); err != nil {
		t.Fatal(err)
	}
	prior := continuing.contexts[0]
	for _, want := range []string{"Show last 2 orders", "RecordSet", "columns: InvoiceId, CustomerId", "from: {name: Invoice}"} {
		if !strings.Contains(prior, want) {
			t.Errorf("restored context missing %q: %s", want, prior)
		}
	}
	if strings.Contains(prior, "CustomerId distinct values") {
		t.Fatalf("raw row identifiers leaked into model context: %s", prior)
	}
	snapshot, err := chat.Snapshot(ctx)
	if err != nil || len(snapshot.Messages) != 4 || len(snapshot.RecordSets) != 1 {
		t.Fatalf("continuation snapshot = %+v, %v", snapshot, err)
	}
	if _, err := chat.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	cleared, err := chat.Snapshot(ctx)
	if err != nil || len(cleared.RecordSets) != 0 || len(cleared.Messages) != 0 {
		t.Fatalf("cleared = %+v, %v", cleared, err)
	}
	if _, err := chat.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	remaining, err := chat.Snapshot(ctx)
	if err != nil || remaining.ID != second.ID {
		t.Fatalf("remaining = %+v, %v", remaining, err)
	}
}

func TestSwitchIsRestoredAsMostRecentlyActiveSession(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	first, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Switch(ctx, first.ID[:8]); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	reloaded := openTestStore(t, path, testScope())
	chat, err = NewSessionChat(ctx, reloaded, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	active, err := chat.Snapshot(ctx)
	if err != nil || active.ID != first.ID {
		t.Fatalf("reopened session = %+v, %v", active, err)
	}
}

type blockingFinalLLM struct {
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (m *blockingFinalLLM) Name() string { return "blocking-final" }

func (m *blockingFinalLLM) Stream(_ context.Context, _ ai.ChatRequest) iter.Seq2[ai.Event, error] {
	return func(yield func(ai.Event, error) bool) {
		m.calls++
		if !yield(ai.Event{Type: ai.EventStarted}, nil) {
			return
		}
		if m.calls == 1 {
			call := ai.ToolCall{ID: "1", Name: toolRunDTQL, Arguments: mustJSON(map[string]any{"title": "One customer", "dtql": "from: {name: Customer}\nlimit: 1"})}
			if !yield(ai.Event{Type: ai.EventToolCall, ToolCall: &call}, nil) {
				return
			}
			yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonToolCalls}, nil)
			return
		}
		close(m.entered)
		<-m.release
		if !yield(ai.Event{Type: ai.EventTextDelta, Text: "Done"}, nil) {
			return
		}
		yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonEnd}, nil)
	}
}

func TestSuccessfulQueryIsDurableBeforeModelFinishes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	llm := &blockingFinalLLM{entered: make(chan struct{}), release: make(chan struct{})}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}}}
	agent, err := NewAIConversation(llm, executor, "sqlite:///chinook.db", "- Customer: CustomerId")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, askErr := chat.Ask(ctx, "Show one customer"); done <- askErr }()
	select {
	case <-llm.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("model never reached final response")
	}
	during, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(during.RecordSets) != 1 || len(during.Queries) != 1 || len(during.Messages) != 2 {
		t.Fatalf("successful query was not saved before model completion: %+v", during)
	}
	close(llm.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, err := store.Load(ctx, session.ID)
	if err != nil || len(after.RecordSets) != 1 {
		t.Fatalf("duplicate or missing snapshot after turn: %+v, %v", after, err)
	}
}

func TestRepeatedSuccessfulDTQLExecutionsHaveDistinctSnapshots(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	doc := "from: {name: Customer}\nlimit: 1"
	step := func(id string) scriptedStep {
		return scriptedStep{toolCalls: []ai.ToolCall{toolCall(id, toolRunDTQL, map[string]any{"dtql": doc})}}
	}
	llm := &scriptedProvider{steps: []scriptedStep{step("1"), step("2"), {text: "Done"}}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}}}
	agent, err := NewAIConversation(llm, executor, "sqlite:///chinook.db", "- Customer: CustomerId")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Run the same query twice"); err != nil {
		t.Fatal(err)
	}
	saved, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 2 || len(saved.Queries) != 2 || len(saved.RecordSets) != 2 {
		t.Fatalf("repeated executions were collapsed: calls=%d queries=%d snapshots=%d", executor.calls, len(saved.Queries), len(saved.RecordSets))
	}
}

func TestSessionContextHasHardSizeLimit(t *testing.T) {
	large := strings.Repeat("x", 40000)
	record := RecordSet{ID: "rs", DTQL: large, Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": large}}}}}
	session := ChatSession{RecordSets: map[string]RecordSet{"rs": record}, Messages: []ChatMessage{{Role: "You", Kind: "text", Text: large}, {Kind: "grid", RecordSetID: "rs"}}}
	contextText := buildSessionContext(session)
	if len(contextText) > maxContextChars || !strings.Contains(contextText, "truncated") {
		t.Fatalf("unbounded context: %d bytes", len(contextText))
	}
}

// TestSessionChatStreamAskPersistsSameAsAsk drives a real AIConversation
// (StreamingConversation) through SessionChat.StreamAsk and checks the
// persisted session -- the user message, the executed query/RecordSet, and
// the final Turn returned by the drain func -- matches what SessionChat.Ask
// would have committed for the same scripted turn.
func TestSessionChatStreamAskPersistsSameAsAsk(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	doc := "from: {name: Customer}\nlimit: 1\n"
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"title": "Customers", "dtql": doc})}},
		{text: "Here you go."},
	}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}}}
	agent, err := NewAIConversation(llm, executor, "sqlite:///chinook.db", "- Customer: CustomerId")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	seq, drain := chat.StreamAsk(ctx, "show one customer")
	var gotToolResult, gotCompleted bool
	for event, streamErr := range seq {
		if streamErr != nil {
			t.Fatalf("unexpected stream error: %v", streamErr)
		}
		switch event.Type {
		case ai.EventToolResult:
			gotToolResult = true
		case ai.EventCompleted:
			gotCompleted = true
		}
	}
	if !gotToolResult || !gotCompleted {
		t.Fatalf("stream did not forward tool-result/completed events: toolResult=%v completed=%v", gotToolResult, gotCompleted)
	}
	turn, drainErr := drain()
	if drainErr != nil {
		t.Fatalf("drain: %v", drainErr)
	}
	if executor.calls != 1 || len(turn.Queries) != 1 || turn.Queries[0].Err != nil {
		t.Fatalf("streamed turn = %+v, executor calls = %d", turn, executor.calls)
	}
	snapshot, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 2 || len(snapshot.RecordSets) != 1 {
		t.Fatalf("StreamAsk did not persist through the same path as Ask: %+v", snapshot)
	}
	if snapshot.Messages[0].Role != "You" || snapshot.Messages[0].Text != "show one customer" {
		t.Fatalf("user message not persisted: %+v", snapshot.Messages[0])
	}
}

// TestSessionChatStreamAskFallsBackForNonStreamingAgent checks that a
// ContextualConversation which doesn't implement StreamingConversation still
// works through StreamAsk, replaying its buffered Turn as one synthetic
// EventTextDelta + EventCompleted pair.
func TestSessionChatStreamAskFallsBackForNonStreamingAgent(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	stub := &contextualStub{turns: []Turn{{Text: "buffered answer"}}}
	chat, err := NewSessionChat(ctx, store, stub, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	seq, drain := chat.StreamAsk(ctx, "hello")
	var texts []string
	var sawCompleted bool
	for event, streamErr := range seq {
		if streamErr != nil {
			t.Fatalf("unexpected stream error: %v", streamErr)
		}
		if event.Type == ai.EventTextDelta {
			texts = append(texts, event.Text)
		}
		if event.Type == ai.EventCompleted {
			sawCompleted = true
		}
	}
	if !sawCompleted || len(texts) != 1 || texts[0] != "buffered answer" {
		t.Fatalf("fallback stream = texts=%v completed=%v", texts, sawCompleted)
	}
	turn, drainErr := drain()
	if drainErr != nil || turn.Text != "buffered answer" {
		t.Fatalf("drain = %+v, %v", turn, drainErr)
	}
}
