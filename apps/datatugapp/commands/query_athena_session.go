package commands

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

var (
	errAthenaSessionInput       = errors.New("athena page session input is invalid")
	errAthenaSessionBusy        = errors.New("athena page session is busy")
	errAthenaSessionUnsent      = errors.New("athena page session has an unsent acknowledged page")
	errAthenaSessionComplete    = errors.New("athena page session is complete")
	errAthenaSessionTerminal    = errors.New("athena page session is terminal")
	errAthenaSessionDisposed    = errors.New("athena page session is disposed")
	errAthenaSessionUnavailable = errors.New("athena page session has no page to write")
	errAthenaSessionWriter      = errors.New("athena page session writer is unavailable")
	errAthenaSessionOutput      = errors.New("athena page output has unknown local delivery")
	errAthenaSessionCleanup     = errors.New("athena page session cleanup failed")
)

const athenaSessionCleanupTimeout = 2 * time.Second

type athenaPageSession struct {
	parent context.Context
	ctx    context.Context
	cancel context.CancelFunc

	client         *athenasdk.Client
	closeTransport func()
	request        admittedAthenaRead

	gate chan struct{}

	mu         sync.Mutex
	started    bool
	eof        bool
	terminal   bool
	cancelled  bool
	disposed   bool
	cleaned    bool
	unknown    bool
	finalFlush bool
	cursor     athenasdk.Cursor
	budget     athenaWireBudget
	receipt    athenasdk.Receipt
	pending    []byte
	cleanupErr error

	disposeDeadline time.Time
	disposeDone     chan struct{}
}

func newAthenaPageSession(ctx context.Context, client *athenasdk.Client, closeTransport func(), request admittedAthenaRead) (*athenaPageSession, error) {
	if ctx == nil || client == nil || request.operation != "query" ||
		request.pageSize < 1 || request.pageSize > 1000 {
		return nil, errAthenaSessionInput
	}
	if err := athenaFresh(ctx); err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	s := &athenaPageSession{
		parent: ctx, ctx: child, cancel: cancel,
		client: client, closeTransport: closeTransport,
		request: request,
		gate:    make(chan struct{}, 1),
	}
	s.gate <- struct{}{}
	return s, nil
}

func athenaSDKDeliveryError(err error) bool {
	var sdkErr *athenasdk.Error
	return errors.As(err, &sdkErr) && sdkErr.Code == "delivery"
}

func athenaSDKClosedOrExpired(err error) bool {
	var sdkErr *athenasdk.Error
	if !errors.As(err, &sdkErr) {
		return false
	}
	return sdkErr.Code == "closed" || sdkErr.Code == "expired"
}

// Next performs one native read. The SDK invokes Prepare before acknowledging,
// so a successfully returned page always has its local frame prepared already.
func (s *athenaPageSession) Next() error {
	if parentErr := athenaFresh(s.parent); parentErr != nil {
		return parentErr
	}

	select {
	case <-s.gate:
	default:
		return errAthenaSessionBusy
	}
	defer func() { s.gate <- struct{}{} }()

	parentErr := athenaFresh(s.parent)

	s.mu.Lock()
	if parentErr != nil {
		s.terminal = true
		s.pending = nil
		s.mu.Unlock()
		return parentErr
	}
	switch {
	case s.cancelled:
		s.mu.Unlock()
		return context.Canceled
	case s.disposed:
		s.mu.Unlock()
		return errAthenaSessionDisposed
	case s.terminal:
		s.mu.Unlock()
		return errAthenaSessionTerminal
	case len(s.pending) != 0:
		s.mu.Unlock()
		return errAthenaSessionUnsent
	case s.eof:
		s.mu.Unlock()
		return errAthenaSessionComplete
	}
	first := !s.started
	s.started = true
	cursor, committed := s.cursor, s.budget
	s.mu.Unlock()

	var frame []byte
	var candidate athenaWireBudget
	var prepareErr error
	prepare := func(page athenasdk.NativePage) error {
		nextFrame, nextBudget, err := athenaPageFrame(s.ctx, page, committed)
		if err != nil {
			prepareErr = err
			return err
		}
		frame, candidate = nextFrame, nextBudget
		return nil
	}

	var page athenasdk.NativePage
	var apiErr error
	if first {
		page, apiErr = s.client.QueryPage(s.ctx, s.request.query, athenasdk.ReadOptions{PageSize: s.request.pageSize, Prepare: prepare})
	} else {
		page, apiErr = s.client.NextPageWith(s.ctx, cursor, prepare)
	}
	if apiErr != nil {
		s.mu.Lock()
		if first {
			s.terminal = true
		}
		s.mu.Unlock()

		parentErr = athenaFresh(s.parent)

		s.mu.Lock()
		cancelled, disposed := s.cancelled, s.disposed
		if parentErr != nil {
			s.terminal = true
			s.pending = nil
		}
		s.mu.Unlock()

		if parentErr != nil {
			return parentErr
		}
		if cancelled {
			return context.Canceled
		}
		if disposed {
			return errAthenaSessionDisposed
		}
		if errors.Is(apiErr, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(apiErr, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if athenaSDKClosedOrExpired(apiErr) {
			return apiErr
		}
		if prepareErr != nil && athenaSDKDeliveryError(apiErr) {
			return prepareErr
		}
		return apiErr
	}

	// A successful native delivery is acknowledged first, without any fallible
	// work in between. Caller/disposal classification happens only afterwards
	// and can abandon the unsent local frame but never the ACK state.
	s.mu.Lock()
	s.budget = candidate
	s.cursor = page.NextCursor
	s.receipt = page.Receipt
	s.eof = !page.HasMore
	s.pending = frame
	s.mu.Unlock()

	parentErr = athenaFresh(s.parent)

	s.mu.Lock()
	cancelled, disposed := s.cancelled, s.disposed
	if parentErr != nil || cancelled || disposed {
		s.terminal = true
		s.pending = nil
	}
	s.mu.Unlock()
	if parentErr != nil {
		return parentErr
	}
	if cancelled {
		return context.Canceled
	}
	if disposed {
		return errAthenaSessionDisposed
	}
	return nil
}

func (s *athenaPageSession) WritePending(w io.Writer) error {
	if parentErr := athenaFresh(s.parent); parentErr != nil {
		return parentErr
	}

	select {
	case <-s.gate:
	default:
		return errAthenaSessionBusy
	}
	defer func() { s.gate <- struct{}{} }()

	if w == nil {
		return errAthenaSessionWriter
	}

	parentErr := athenaFresh(s.parent)

	s.mu.Lock()
	if parentErr != nil {
		s.terminal = true
		s.pending = nil
		s.mu.Unlock()
		return parentErr
	}
	switch {
	case s.cancelled:
		s.mu.Unlock()
		return context.Canceled
	case s.unknown:
		s.mu.Unlock()
		return errAthenaSessionOutput
	case s.cleanupErr != nil:
		s.mu.Unlock()
		return errAthenaSessionCleanup
	case s.terminal:
		s.mu.Unlock()
		return errAthenaSessionTerminal
	case s.disposed && (!s.cleaned || !s.finalFlush):
		// Only an already acknowledged terminal frame committed before normal
		// disposal may still be flushed after cleanup succeeds.
		s.mu.Unlock()
		return errAthenaSessionDisposed
	case s.pending == nil:
		s.mu.Unlock()
		return errAthenaSessionUnavailable
	}
	frame := s.pending
	s.mu.Unlock()

	n, writeErr := w.Write(frame)
	if writeErr != nil || n != len(frame) {
		s.mu.Lock()
		s.unknown = true
		s.terminal = true
		s.pending = nil
		s.mu.Unlock()
		if parentErr := athenaFresh(s.parent); parentErr != nil {
			return parentErr
		}
		if s.isCancelled() {
			return context.Canceled
		}
		return errAthenaSessionOutput
	}

	s.mu.Lock()
	s.pending = nil
	s.unknown = false
	s.mu.Unlock()
	if parentErr := athenaFresh(s.parent); parentErr != nil {
		return parentErr
	}
	if s.isCancelled() {
		return context.Canceled
	}
	return nil
}

func (s *athenaPageSession) isCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

func (s *athenaPageSession) Cancel() {
	s.mu.Lock()
	if s.cancelled {
		s.mu.Unlock()
		return
	}
	s.cancelled = true
	s.terminal = true
	s.pending = nil
	s.mu.Unlock()
	s.cancel()
}

// Dispose shuts down the SDK under a fixed background cleanup budget that is
// recorded once at disposal admission, then closes the transport even when
// shutdown fails. It never waits for the caller-owned writer gate: Shutdown is
// concurrency-safe and an arbitrary blocked writer is neither joined nor
// declared complete. Only an already acknowledged terminal frame committed
// before admission may be flushed after successful cleanup.
func (s *athenaPageSession) Dispose() error {
	s.mu.Lock()
	if s.disposed {
		done, deadline := s.disposeDone, s.disposeDeadline
		s.mu.Unlock()
		if remaining := time.Until(deadline); done != nil && remaining > 0 {
			timer := time.NewTimer(remaining)
			select {
			case <-done:
				timer.Stop()
			case <-timer.C:
			}
		}
		s.mu.Lock()
		cleaned, err := s.cleaned, s.cleanupErr
		s.mu.Unlock()
		if !cleaned {
			return errAthenaSessionCleanup
		}
		return err
	}
	s.disposed = true
	s.disposeDone = make(chan struct{})
	done := s.disposeDone
	deadline := time.Now().Add(athenaSessionCleanupTimeout)
	s.disposeDeadline = deadline
	s.finalFlush = s.eof && s.pending != nil && !s.cancelled && !s.unknown &&
		!s.terminal && s.cleanupErr == nil
	s.mu.Unlock()

	s.cancel()

	shutdownCtx, shutdownCancel := context.WithDeadline(context.Background(), deadline)
	shutdownErr := s.client.Shutdown(shutdownCtx)
	shutdownCancel()
	if s.closeTransport != nil {
		s.closeTransport()
	}
	var cleanupErr error
	if shutdownErr != nil {
		cleanupErr = errAthenaSessionCleanup
	}

	s.mu.Lock()
	s.cleaned = true
	s.cleanupErr = cleanupErr
	if cleanupErr != nil {
		s.finalFlush = false
	}
	s.mu.Unlock()
	close(done)
	return cleanupErr
}
