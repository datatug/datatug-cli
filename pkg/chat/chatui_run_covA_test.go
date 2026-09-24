package chat

// Coverage lane A (datatug-cli#289): drives ChatUI.Run's branches through
// the RunTeaProgram seam added in chatui.go, so a real terminal is never
// started.

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatUIRunRequiresConversation(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "fake-model")
	if err := u.Run(); err == nil || err.Error() != "chat UI requires a conversation" {
		t.Fatalf("Run() with no conversation = %v, want a conversation-required error", err)
	}
}

func TestChatUIRunReturnsProgramError(t *testing.T) {
	restore := RunTeaProgram
	t.Cleanup(func() { RunTeaProgram = restore })
	wantErr := errors.New("boom")
	var gotProgram *tea.Program
	RunTeaProgram = func(p *tea.Program) (tea.Model, error) {
		gotProgram = p
		return nil, wantErr
	}
	u, _ := newTestChatUI(t, nil, Turn{})
	if err := u.Run(); !errors.Is(err, wantErr) {
		t.Fatalf("Run() = %v, want %v", err, wantErr)
	}
	if gotProgram == nil {
		t.Fatal("RunTeaProgram was never invoked with a program")
	}
}

// TestChatUIRunForwardsBridgeEventsAndStopsBridge covers Run's bridgeEvents
// goroutine (it forwards each event by calling program.Send, entering that
// loop body at least once) and its deferred bridgeStop call.
// RunTeaProgram is overridden to return immediately. A real *tea.Program's
// Send merely queues into its own internal (unexported, unbuffered) msgs
// channel; with no real Run loop underneath to drain it, the forwarding
// goroutine's Send call blocks forever once it has entered — by design,
// since nothing in this package can reach that unexported channel to drain
// it. That's a deliberate, contained trade-off (a single leaked goroutine
// for the remainder of this test binary, not a hang: the test itself never
// waits on it) to get real coverage of the forwarding statement without
// reimplementing tea.Program.
func TestChatUIRunForwardsBridgeEventsAndStopsBridge(t *testing.T) {
	restore := RunTeaProgram
	t.Cleanup(func() { RunTeaProgram = restore })
	RunTeaProgram = func(p *tea.Program) (tea.Model, error) { return nil, nil }

	events := make(chan struct{}, 1)
	events <- struct{}{}
	close(events)
	stopped := false
	u, _ := newTestChatUI(t, nil, Turn{})
	u.bridgeEvents = events
	u.bridgeStop = func() { stopped = true }

	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
	if !stopped {
		t.Fatal("bridgeStop was never called")
	}
}

func TestChatUIRunWithoutBridgeNeverCallsBridgeStop(t *testing.T) {
	restore := RunTeaProgram
	t.Cleanup(func() { RunTeaProgram = restore })
	RunTeaProgram = func(p *tea.Program) (tea.Model, error) { return nil, nil }

	u, _ := newTestChatUI(t, nil, Turn{})
	if u.bridgeEvents != nil || u.bridgeStop != nil {
		t.Fatal("fresh ChatUI unexpectedly has bridge state")
	}
	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}
