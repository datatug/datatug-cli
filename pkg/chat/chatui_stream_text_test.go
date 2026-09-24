package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
)

// TestChatUIStreamedGridTurnDoesNotLeakRawStreamedProse is the M3 regression
// test (r1 adversarial review of #289). It drives ChatUI through a REAL
// *AIConversation backed by a scripted ai.LLMProvider (the same real
// streaming path production uses, per M7's request for such a test) instead
// of a fixed-Turn stub, so the live view actually goes through
// StartStream's own progressive text-delta rendering before OnStreamDone
// resolves the turn.
//
// The scripted provider's one step emits BOTH a <think> reasoning block
// (see agent.go's stripThinkTags) and a successful run_dtql tool call in the
// same step, mirroring what a local/open model streamed over
// ai/openaicompat can do. Because the turn also produced a query result,
// AskWithContext/StreamAskWithContext deliberately clear the turn's final
// text ("the grid is the answer"), and store.go's AppendTurn never persists
// a message row for an empty turn.Text -- so a session reload shows no
// prose at all beside the grid. Before the M3 fix, the LIVE view still
// showed whatever raw text chatshell streamed for the placeholder entry
// (the <think> block included, unstripped, since chatshell renders deltas
// as they arrive), which is exactly the live-view/reload mismatch this test
// guards against.
func TestChatUIStreamedGridTurnDoesNotLeakRawStreamedProse(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	executor := &fakeExecutor{result: secureread.Result{
		Columns: []string{"CustomerId", "City"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1, "City": "Prague"}}},
	}}
	doc := "from: {schema: main, name: Customer}\nlimit: 1\n"
	provider := &scriptedProvider{steps: []scriptedStep{
		{
			text:      "<think>let me pick the right table</think>Checking the Customer table for Prague.",
			toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"dtql": doc})},
		},
		// agent.Loop calls the model again after a successful tool result;
		// this step's text is dropped from the final Turn ("the grid is the
		// answer" once len(turn.Queries) > 0 -- see AskWithContext/
		// StreamAskWithContext), so its content never needs asserting here,
		// only that it doesn't leak into the live view either.
		{text: "Here you go."},
	}}
	conversation, err := NewAIConversation(provider, executor, "sqlite:///fixture.db", "main.Customer: CustomerId, City")
	if err != nil {
		t.Fatalf("NewAIConversation: %v", err)
	}
	sessions, err := NewSessionChat(ctx, store, conversation, "sqlite:///fixture.db")
	if err != nil {
		t.Fatalf("NewSessionChat: %v", err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatalf("NewSessionChatUI: %v", err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	cmd := u.Submit("Find a Prague customer")
	drainCmd(t, u, cmd)

	view := u.shell.View().Content
	for _, leaked := range []string{"let me pick the right table", "Checking the Customer table for Prague", "<think>", "Here you go"} {
		if strings.Contains(view, leaked) {
			t.Errorf("live view leaked stream-time text that the resolved turn dropped: %q in:\n%s", leaked, view)
		}
	}
	if !strings.Contains(view, "CustomerId") || !strings.Contains(view, "Prague") {
		t.Errorf("live view missing the grid result:\n%s", view)
	}

	// A reload must show the same thing the live view now does: the grid,
	// and no text message for this turn (store.go's AppendTurn never
	// inserts one when turn.Text == "").
	reloaded, err := store.Load(ctx, sessions.activeID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, message := range reloaded.Messages {
		if message.Role == "DataTug" && message.Text != "" {
			t.Errorf("reload persisted an assistant text message for a grid-only turn: %+v", message)
		}
	}
	if len(reloaded.RecordSets) != 1 {
		t.Fatalf("reload RecordSets = %d, want 1", len(reloaded.RecordSets))
	}
}
