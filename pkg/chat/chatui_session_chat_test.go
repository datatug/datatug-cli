package chat

// TestChatUIEmptyToolResultRestoresAsGrid is ported from session_chat_test.go's
// TestEmptyToolResultRestoresAsGrid — the only case in that file depending on
// the legacy UI struct.

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
)

func TestChatUIEmptyToolResultRestoresAsGrid(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	llm := &scriptedProvider{steps: []scriptedStep{
		{toolCalls: []ai.ToolCall{toolCall("1", toolRunDTQL, map[string]any{"dtql": "from: {name: Customer}\nlimit: 1"})}},
		{text: "No matching rows"},
	}}
	agent, err := NewAIConversation(llm, &fakeExecutor{result: secureread.Result{}}, "sqlite:///chinook.db", "- Customer")
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
	u, err := NewSessionChatUI(ctx, chat, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(u.shell.View().Content, "No rows returned.") {
		t.Fatal("empty persisted grid was not rendered")
	}
}
