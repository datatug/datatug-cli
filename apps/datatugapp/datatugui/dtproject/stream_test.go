package dtproject

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type progressTestMsg int

func TestStreamDeliversProgressThenTheResult(t *testing.T) {
	s := newStream(func(_ context.Context, report func(tea.Msg)) tea.Msg {
		report(progressTestMsg(1))
		report(progressTestMsg(2))
		return progressTestMsg(3)
	})
	got := []tea.Msg{s.Start()()}
	for range 2 {
		got = append(got, s.Next()())
	}
	for i, m := range got {
		if m != progressTestMsg(i+1) {
			t.Fatalf("message %d = %v", i, m)
		}
	}
}

func TestStreamCancelStopsDelivery(t *testing.T) {
	released := make(chan struct{})
	s := newStream(func(ctx context.Context, report func(tea.Msg)) tea.Msg {
		report(progressTestMsg(1))
		<-ctx.Done() // the work notices the cancellation
		close(released)
		return progressTestMsg(2) // nobody is listening any more
	})
	if m := s.Start()(); m != progressTestMsg(1) {
		t.Fatalf("first message = %v", m)
	}
	s.Cancel()
	if m := s.Next()(); m != nil {
		t.Fatalf("a cancelled stream delivered %v", m)
	}
	<-released
}
