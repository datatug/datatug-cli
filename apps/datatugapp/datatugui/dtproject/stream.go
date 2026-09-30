package dtproject

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// stream is work that reports progress while it runs: a clone, the GitHub device
// flow. It is the Bubble Tea way to follow a long operation. Start is a command
// that launches the work and waits for its first message; every progress message
// the screen receives is answered with Next, which waits for the following one.
// The last message the work returns is delivered like the others, and then
// nothing is pending. Cancel abandons the work: nothing more is delivered.
//
// A stream is a reference (the screen holds a *stream), so screens that are
// copied by Update all follow the same one.
type stream struct {
	work   func(ctx context.Context, report func(tea.Msg)) tea.Msg
	ctx    context.Context
	cancel context.CancelFunc
	msgs   chan tea.Msg
}

// newStream prepares work; nothing runs until Start is executed.
func newStream(work func(ctx context.Context, report func(tea.Msg)) tea.Msg) *stream {
	ctx, cancel := context.WithCancel(context.Background())
	return &stream{work: work, ctx: ctx, cancel: cancel, msgs: make(chan tea.Msg)}
}

// Start launches the work and returns its first message.
func (s *stream) Start() tea.Cmd {
	return func() tea.Msg {
		go func() { s.send(s.work(s.ctx, s.send)) }()
		return s.receive()
	}
}

// Next returns the following message of the work.
func (s *stream) Next() tea.Cmd {
	return func() tea.Msg { return s.receive() }
}

// Cancel abandons the work.
func (s *stream) Cancel() { s.cancel() }

func (s *stream) send(msg tea.Msg) {
	select {
	case s.msgs <- msg:
	case <-s.ctx.Done():
	}
}

func (s *stream) receive() tea.Msg {
	select {
	case msg := <-s.msgs:
		return msg
	case <-s.ctx.Done():
		return nil
	}
}
