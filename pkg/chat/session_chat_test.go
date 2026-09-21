package chat

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

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
	for _, want := range []string{"Show last 2 orders", "RecordSet", "CustomerId distinct values: 58, 44", "from: {name: Invoice}"} {
		if !strings.Contains(prior, want) {
			t.Errorf("restored context missing %q: %s", want, prior)
		}
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

func TestContextBoundsIdentifierValues(t *testing.T) {
	record := RecordSet{ID: "rs", Result: secureread.Result{Columns: []string{"CustomerId"}}}
	for i := 0; i < 120; i++ {
		record.Result.Rows = append(record.Result.Rows, secureread.Row{Data: map[string]any{"CustomerId": i}})
	}
	contextText := recordIdentifierContext(record)
	if !strings.Contains(contextText, "truncated; not a complete set") || strings.Contains(contextText, ", 119") {
		t.Fatalf("unbounded identifier context: %s", contextText)
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

func (m *blockingFinalLLM) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"title": "One customer", "dtql": "from: {name: Customer}\nlimit: 1"}, genai.RoleModel)}, nil)
			return
		}
		close(m.entered)
		<-m.release
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Done", genai.RoleModel)}, nil)
	}
}

func TestSuccessfulQueryIsDurableBeforeModelFinishes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	llm := &blockingFinalLLM{entered: make(chan struct{}), release: make(chan struct{})}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}}}
	agent, err := NewADKConversation(llm, executor, "sqlite:///chinook.db", "- Customer: CustomerId")
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
	call := func() *model.LLMResponse {
		return &model.LLMResponse{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": doc}, genai.RoleModel)}
	}
	llm := &scriptedLLM{responses: []*model.LLMResponse{call(), call(), {Content: genai.NewContentFromText("Done", genai.RoleModel)}}}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 5}}}}}
	agent, err := NewADKConversation(llm, executor, "sqlite:///chinook.db", "- Customer: CustomerId")
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

func TestEmptyToolResultRestoresAsGrid(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	llm := &scriptedLLM{responses: []*model.LLMResponse{
		{Content: genai.NewContentFromFunctionCall("run_dtql", map[string]any{"dtql": "from: {name: Customer}\nlimit: 1"}, genai.RoleModel)},
		{Content: genai.NewContentFromText("No matching rows", genai.RoleModel)},
	}}
	agent, err := NewADKConversation(llm, &fakeExecutor{result: secureread.Result{}}, "sqlite:///chinook.db", "- Customer")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Ask(ctx, "Show no matches"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	reloaded := openTestStore(t, path, testScope())
	chat, err = NewSessionChat(ctx, reloaded, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, chat, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.View().Content, "No rows returned.") {
		t.Fatal("empty persisted grid was not rendered")
	}
}
