package chat

// Coverage lane A (datatug-cli#289): drives ChatUI.Run's branches through
// the runTeaProgram seam added in chatui.go (SetRunTeaProgramForTest, m3
// r5 fix round), so a real terminal is never started.

import (
	"context"
	"errors"
	"io"
	"strings"
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
	wantErr := errors.New("boom")
	var gotProgram *tea.Program
	t.Cleanup(SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) {
		gotProgram = p
		return nil, wantErr
	}))
	u, _ := newTestChatUI(t, nil, Turn{})
	if err := u.Run(); !errors.Is(err, wantErr) {
		t.Fatalf("Run() = %v, want %v", err, wantErr)
	}
	if gotProgram == nil {
		t.Fatal("runTeaProgram was never invoked with a program")
	}
}

// TestChatUIRunForwardsBridgeEventsAndStopsBridge covers Run's bridgeEvents
// goroutine (it forwards each event by calling program.Send, entering that
// loop body at least once) and its deferred bridgeStop call.
// runTeaProgram is overridden to return immediately. A real *tea.Program's
// Send merely queues into its own internal (unexported, unbuffered) msgs
// channel; with no real Run loop underneath to drain it, the forwarding
// goroutine's Send call blocks forever once it has entered — by design,
// since nothing in this package can reach that unexported channel to drain
// it. That's a deliberate, contained trade-off (a single leaked goroutine
// for the remainder of this test binary, not a hang: the test itself never
// waits on it) to get real coverage of the forwarding statement without
// reimplementing tea.Program.
func TestChatUIRunForwardsBridgeEventsAndStopsBridge(t *testing.T) {
	t.Cleanup(SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))

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

// quitOnInitModel is the smallest possible tea.Model: it asks to quit
// immediately from Init, never renders anything meaningful, so a real
// *tea.Program built around it returns from Run() as fast as a Bubble Tea
// event loop can process one message.
type quitOnInitModel struct{}

func (quitOnInitModel) Init() tea.Cmd                       { return tea.Quit }
func (quitOnInitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return quitOnInitModel{}, nil }
func (quitOnInitModel) View() tea.View                      { return tea.NewView("") }

// TestRunTeaProgramCallsRealRun covers the DEFAULT runTeaProgram value
// itself (p.Run()) -- every other test in this file overrides it via
// SetRunTeaProgramForTest, which is how ChatUI.Run's own tests avoid ever
// starting a real Bubble Tea program, but that means the seam's actual
// production body was otherwise never exercised. Redirecting the program's
// input/output to an empty reader/io.Discard keeps this hermetic: no real
// TTY is touched, and quitOnInitModel makes Run() return immediately.
func TestRunTeaProgramCallsRealRun(t *testing.T) {
	p := tea.NewProgram(quitOnInitModel{}, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithoutRenderer())
	if _, err := runTeaProgram(p); err != nil {
		t.Fatalf("runTeaProgram(real program) = %v, want nil", err)
	}
}

func TestChatUIRunWithoutBridgeNeverCallsBridgeStop(t *testing.T) {
	t.Cleanup(SetRunTeaProgramForTest(func(p *tea.Program) (tea.Model, error) { return nil, nil }))

	u, _ := newTestChatUI(t, nil, Turn{})
	if u.bridgeEvents != nil || u.bridgeStop != nil {
		t.Fatal("fresh ChatUI unexpectedly has bridge state")
	}
	if err := u.Run(); err != nil {
		t.Fatalf("Run() = %v, want nil", err)
	}
}
