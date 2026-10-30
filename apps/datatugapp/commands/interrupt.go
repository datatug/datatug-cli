package commands

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// interruptContext returns a context that is cancelled by the first SIGINT or SIGTERM, a function that says which signal
// it was (nil while none came), and a stop function that gives the signals back to the process. A second signal after
// the first is not caught: it ends the process as usual, for a command that does not stop. A command that creates
// something it must remove (a staging folder) uses it to stop through its own return path, where the deferred cleanup
// runs, instead of being killed in the middle of the work. A test replaces it.
var interruptContext = func(parent context.Context) (context.Context, func() os.Signal, func()) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	var (
		mu       sync.Mutex
		received os.Signal
		once     sync.Once
		done     = make(chan struct{})
	)
	go func() {
		select {
		case sig := <-ch:
			mu.Lock()
			received = sig
			mu.Unlock()
			signal.Stop(ch)
			cancel()
		case <-done:
		}
	}()
	which := func() os.Signal {
		mu.Lock()
		defer mu.Unlock()
		return received
	}
	stop := func() {
		once.Do(func() {
			signal.Stop(ch)
			close(done)
			cancel()
		})
	}
	return ctx, which, stop
}
