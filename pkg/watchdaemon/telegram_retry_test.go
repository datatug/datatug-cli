package watchdaemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Observe the retry select without racing a deadline against the poll request.
// Client{} fails validation synchronously, so no HTTP transport calls Done.
type telegramRetryWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *telegramRetryWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestWatchOwnBotRetryWaitCancellation(t *testing.T) {
	original := telegramRetryInterval
	telegramRetryInterval = time.Hour
	t.Cleanup(func() { telegramRetryInterval = original })

	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &telegramRetryWaitContext{Context: base, entered: make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- (&telegramBot{}).run(ctx, Client{}) }()

	select {
	case <-ctx.entered:
		// The worker has passed its pre-wait cancellation check and is now
		// evaluating the retry select. Cancellation must leave through it.
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not enter the retry wait")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("retry wait lost cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after retry wait cancellation")
	}
}
