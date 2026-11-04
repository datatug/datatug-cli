package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

type athenaSessionLifecycleACK struct {
	budget  athenaWireBudget
	cursor  string
	receipt athenasdk.Receipt
}

type athenaSessionLifecycleState struct {
	pending  []byte
	unknown  bool
	terminal bool
	eof      bool
}

func athenaSessionLifecycleSnapshot(s *athenaPageSession) athenaSessionLifecycleACK {
	s.mu.Lock()
	defer s.mu.Unlock()
	return athenaSessionLifecycleACK{budget: s.budget, cursor: string(s.cursor), receipt: s.receipt}
}

func athenaSessionLifecycleStateOf(s *athenaPageSession) athenaSessionLifecycleState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return athenaSessionLifecycleState{
		pending:  append([]byte(nil), s.pending...),
		unknown:  s.unknown,
		terminal: s.terminal,
		eof:      s.eof,
	}
}

func athenaSessionLifecycleSameACK(a, b athenaSessionLifecycleACK) bool {
	return a.budget == b.budget && a.cursor == b.cursor && reflect.DeepEqual(a.receipt, b.receipt)
}

func athenaSessionLifecycleWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("%s did not complete", what)
	}
}

func athenaSessionLifecycleAwaitNext(t *testing.T, result <-chan error, joined <-chan struct{}, what string) error {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var err error
	select {
	case err = <-result:
	case <-timer.C:
		t.Fatalf("%s did not return", what)
	}
	select {
	case <-joined:
	case <-timer.C:
		t.Fatalf("%s goroutine did not join", what)
	}
	return err
}

func athenaSessionLifecycleConfig(t *testing.T) athenaNativeConfig {
	t.Helper()
	cfg := athenaOptionsTestConfig(t)
	cfg.Identity.SessionTokenEnv = "ATHENA_TEST_SESSION"
	t.Setenv("ATHENA_TEST_ACCESS", athenaSessionCredential.AccessKeyID)
	t.Setenv("ATHENA_TEST_SECRET", athenaSessionCredential.SecretAccessKey)
	t.Setenv("ATHENA_TEST_SESSION", athenaSessionCredential.SessionToken)
	return cfg
}

func athenaSessionLifecyclePageFixture(t *testing.T, terminal bool) *athenaSessionFixture {
	t.Helper()
	return athenaSessionNewFixture(t, func(_ *athenaSessionFixture, w http.ResponseWriter, _ *http.Request, event athenaSessionEvent) bool {
		if event.op != "GetQueryResults" {
			return false
		}
		if !athenaSessionResultRequest(t, event.body) {
			w.WriteHeader(400)
			return true
		}
		if _, hasToken := event.body["NextToken"]; hasToken {
			t.Error("lifecycle page read invented a successor token")
			w.WriteHeader(400)
			return true
		}
		response := map[string]any{
			"ResultSet": map[string]any{
				"ResultSetMetadata": map[string]any{"ColumnInfo": athenaSessionColumns()},
				"Rows": []any{
					athenaSessionRow("id", athenaSessionPointer("amount")),
					athenaSessionRow("a", athenaSessionPointer("1")),
				},
			},
		}
		if !terminal {
			response["NextToken"] = "owned-page-1"
		}
		athenaSessionWrite(t, w, 200, response)
		return true
	})
}

func athenaSessionLifecycleStart(t *testing.T, terminal bool) (*athenaSessionFixture, *athenaPageSession, context.CancelCauseFunc) {
	t.Helper()
	cfg := athenaSessionLifecycleConfig(t)
	f := athenaSessionLifecyclePageFixture(t, terminal)
	client, closeTransport := athenaSessionClient(t, f, cfg)
	parent, cancel := context.WithCancelCause(context.Background())
	session, err := newAthenaPageSession(parent, client, closeTransport, athenaSessionRequest(t, cfg))
	if err != nil {
		cancel(nil)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel(errors.New("PRIVATE_LIFECYCLE_CLEANUP"))
		_ = session.Dispose()
	})
	return f, session, cancel
}

type athenaSessionLifecycleWriterFunc func([]byte) (int, error)

func (f athenaSessionLifecycleWriterFunc) Write(p []byte) (int, error) { return f(p) }

type athenaSessionLifecycleBlockedWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *athenaSessionLifecycleBlockedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestAthenaSessionActualCallerAndDeadlineStop(t *testing.T) {
	run := func(t *testing.T, deadline bool) {
		cfg := athenaSessionLifecycleConfig(t)
		dispatched := make(chan struct{})
		disconnected := make(chan struct{})
		stopped := make(chan struct{})
		escape := make(chan struct{})
		var dispatchedOnce, disconnectedOnce, stoppedOnce, escapeOnce sync.Once
		f := athenaSessionNewFixture(t, func(_ *athenaSessionFixture, w http.ResponseWriter, r *http.Request, event athenaSessionEvent) bool {
			switch event.op {
			case "GetQueryExecution":
				if len(event.body) != 1 || event.body["QueryExecutionId"] != athenaSessionQueryID {
					t.Error("blocked execution request changed")
					w.WriteHeader(400)
					return true
				}
				dispatchedOnce.Do(func() { close(dispatched) })
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
					disconnectedOnce.Do(func() { close(disconnected) })
				case <-escape:
				case <-timer.C:
					t.Error("blocked execution was not canceled")
				}
				return true
			case "StopQueryExecution":
				if len(event.body) != 1 || event.body["QueryExecutionId"] != athenaSessionQueryID {
					t.Error("stop request changed")
					w.WriteHeader(400)
					return true
				}
				stoppedOnce.Do(func() { close(stopped) })
				return false
			}
			return false
		})
		t.Cleanup(func() { escapeOnce.Do(func() { close(escape) }) })
		client, closeTransport := athenaSessionClient(t, f, cfg)
		var parent context.Context
		var cancel context.CancelFunc
		if deadline {
			timeoutCtx, timeoutCancel := context.WithTimeoutCause(context.Background(), time.Second, errors.New("PRIVATE_DEADLINE_CAUSE"))
			parent, cancel = timeoutCtx, timeoutCancel
		} else {
			causeCtx, causeCancel := context.WithCancelCause(context.Background())
			parent, cancel = causeCtx, func() { causeCancel(errors.New("PRIVATE_CALLER_CAUSE")) }
		}
		session, err := newAthenaPageSession(parent, client, closeTransport, athenaSessionRequest(t, cfg))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		nextResult := make(chan error, 1)
		nextJoined := make(chan struct{})
		t.Cleanup(func() {
			cancel()
			escapeOnce.Do(func() { close(escape) })
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-nextJoined:
			case <-timer.C:
				t.Error("Next goroutine did not join during cleanup")
			}
			_ = session.Dispose()
		})
		go func() {
			defer close(nextJoined)
			nextResult <- session.Next()
		}()
		athenaSessionLifecycleWait(t, dispatched, "execution dispatch")
		if !deadline {
			cancel()
		}
		nextErr := athenaSessionLifecycleAwaitNext(t, nextResult, nextJoined, "Next")
		athenaSessionLifecycleWait(t, disconnected, "request disconnect")
		want := context.Canceled
		if deadline {
			want = context.DeadlineExceeded
		}
		if !errors.Is(nextErr, want) {
			t.Fatalf("Next error = %v, want %v", nextErr, want)
		}
		state := athenaSessionLifecycleStateOf(session)
		if len(state.pending) != 0 || state.unknown || state.eof || !state.terminal {
			t.Fatalf("cancellation state changed: %+v", state)
		}
		ack := athenaSessionLifecycleSnapshot(session)
		if ack.budget != (athenaWireBudget{}) || ack.cursor != "" || !reflect.DeepEqual(ack.receipt, athenasdk.Receipt{}) {
			t.Fatal("cancellation committed page budget, cursor, or receipt")
		}
		if err := session.Dispose(); err != nil {
			t.Fatal("disposal failed: ", err)
		}
		athenaSessionLifecycleWait(t, stopped, "stop request")
		if !errors.Is(nextErr, want) {
			t.Fatal("cleanup replaced the original Next classification")
		}
		if f.count("StartQueryExecution") != 1 || f.count("GetQueryExecution") != 1 ||
			f.count("StopQueryExecution") != 1 || f.count("GetQueryResults") != 0 {
			t.Fatalf("unexpected RPC counts: start=%d execution=%d stop=%d results=%d",
				f.count("StartQueryExecution"), f.count("GetQueryExecution"),
				f.count("StopQueryExecution"), f.count("GetQueryResults"))
		}
	}
	t.Run("caller_cancel", func(t *testing.T) { run(t, false) })
	t.Run("caller_deadline", func(t *testing.T) { run(t, true) })
}

func TestAthenaSessionACKedFrameCallerAndWriterFailures(t *testing.T) {
	t.Run("caller_cancel_before_write", func(t *testing.T) {
		f, session, cancel := athenaSessionLifecycleStart(t, false)
		if err := session.Next(); err != nil {
			t.Fatal(err)
		}
		ack := athenaSessionLifecycleSnapshot(session)
		if len(athenaSessionLifecycleStateOf(session).pending) == 0 {
			t.Fatal("successful Next committed no acknowledged frame")
		}
		starts := f.count("StartQueryExecution")
		results := f.count("GetQueryResults")
		cancel(errors.New("PRIVATE_CALLER_CAUSE"))
		var calls int
		var output bytes.Buffer
		writer := athenaSessionLifecycleWriterFunc(func(p []byte) (int, error) {
			calls++
			return output.Write(p)
		})
		if err := session.WritePending(writer); err != context.Canceled {
			t.Fatalf("WritePending after caller cancel = %v, want context.Canceled", err)
		}
		if calls != 0 || output.Len() != 0 {
			t.Fatal("caller cancel invoked the writer or wrote bytes")
		}
		if !athenaSessionLifecycleSameACK(ack, athenaSessionLifecycleSnapshot(session)) {
			t.Fatal("caller cancel changed acknowledged page facts")
		}
		if err := session.Next(); err != context.Canceled {
			t.Fatalf("Next after caller cancel = %v, want context.Canceled", err)
		}
		if err := session.WritePending(nil); err != context.Canceled {
			t.Fatalf("nil writer after caller cancel = %v, want caller priority", err)
		}
		if f.count("StartQueryExecution") != starts || f.count("GetQueryResults") != results {
			t.Fatal("caller cancel caused replay or a new native read")
		}
	})

	for _, test := range []struct {
		name  string
		write athenaSessionLifecycleWriterFunc
	}{
		{
			name: "short_writer",
			write: func(p []byte) (int, error) {
				return len(p) - 1, nil
			},
		},
		{
			name: "private_writer_error",
			write: func([]byte) (int, error) {
				return 0, errors.New("PRIVATE_WRITER_ERROR")
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			f, session, _ := athenaSessionLifecycleStart(t, false)
			if err := session.Next(); err != nil {
				t.Fatal(err)
			}
			ack := athenaSessionLifecycleSnapshot(session)
			starts := f.count("StartQueryExecution")
			results := f.count("GetQueryResults")
			var calls int
			writer := athenaSessionLifecycleWriterFunc(func(p []byte) (int, error) {
				calls++
				return test.write(p)
			})
			if err := session.WritePending(writer); err != errAthenaSessionOutput {
				t.Fatal("writer failure did not return unknown local delivery")
			}
			if calls != 1 {
				t.Fatalf("writer calls = %d, want 1", calls)
			}
			state := athenaSessionLifecycleStateOf(session)
			if !state.unknown || !state.terminal || len(state.pending) != 0 {
				t.Fatalf("writer failure state changed: %+v", state)
			}
			if !athenaSessionLifecycleSameACK(ack, athenaSessionLifecycleSnapshot(session)) {
				t.Fatal("writer failure changed acknowledged page facts")
			}
			if err := session.Next(); err != errAthenaSessionTerminal {
				t.Fatalf("Next after writer failure = %v, want terminal", err)
			}
			var retryCalls int
			retry := athenaSessionLifecycleWriterFunc(func(p []byte) (int, error) {
				retryCalls++
				return len(p), nil
			})
			if err := session.WritePending(retry); err != errAthenaSessionOutput {
				t.Fatalf("WritePending after writer failure = %v, want unknown local delivery", err)
			}
			if retryCalls != 0 {
				t.Fatal("terminal write attempt invoked the writer")
			}
			if f.count("StartQueryExecution") != starts || f.count("GetQueryResults") != results {
				t.Fatal("writer failure caused replay or a new native read")
			}
		})
	}

	t.Run("full_write_then_caller_cancel", func(t *testing.T) {
		f, session, cancel := athenaSessionLifecycleStart(t, false)
		if err := session.Next(); err != nil {
			t.Fatal(err)
		}
		ack := athenaSessionLifecycleSnapshot(session)
		starts := f.count("StartQueryExecution")
		results := f.count("GetQueryResults")
		frame := athenaSessionLifecycleStateOf(session).pending
		if len(frame) == 0 {
			t.Fatal("successful Next committed no acknowledged frame")
		}
		var calls int
		var output bytes.Buffer
		writer := athenaSessionLifecycleWriterFunc(func(p []byte) (int, error) {
			calls++
			n, writeErr := output.Write(p)
			cancel(errors.New("PRIVATE_WRITE_CAUSE"))
			return n, writeErr
		})
		if err := session.WritePending(writer); err != context.Canceled {
			t.Fatalf("full write with caller cancel = %v, want context.Canceled", err)
		}
		if calls != 1 {
			t.Fatalf("writer calls = %d, want 1", calls)
		}
		if !bytes.Equal(output.Bytes(), frame) {
			t.Fatal("acknowledged frame was not written exactly once")
		}
		state := athenaSessionLifecycleStateOf(session)
		if state.unknown || len(state.pending) != 0 {
			t.Fatalf("full write did not retire the pending frame: %+v", state)
		}
		if !athenaSessionLifecycleSameACK(ack, athenaSessionLifecycleSnapshot(session)) {
			t.Fatal("full write with caller cancel changed acknowledged page facts")
		}
		if err := session.Next(); err != context.Canceled {
			t.Fatalf("Next after full write cancel = %v, want context.Canceled", err)
		}
		if err := session.WritePending(nil); err != context.Canceled {
			t.Fatalf("nil writer after full write cancel = %v, want caller priority", err)
		}
		if f.count("StartQueryExecution") != starts || f.count("GetQueryResults") != results {
			t.Fatal("full write with caller cancel caused replay or a new native read")
		}
	})

	t.Run("terminal_eof_complete_before_dispose", func(t *testing.T) {
		f, session, _ := athenaSessionLifecycleStart(t, true)
		if err := session.Next(); err != nil {
			t.Fatal(err)
		}
		state := athenaSessionLifecycleStateOf(session)
		if !state.eof || len(state.pending) == 0 {
			t.Fatal("terminal page did not acknowledge an EOF frame")
		}
		output := &bytes.Buffer{}
		if err := session.WritePending(output); err != nil {
			t.Fatal(err)
		}
		if err := session.Next(); err != errAthenaSessionComplete {
			t.Fatalf("Next after terminal flush = %v, want complete", err)
		}
		if f.count("StartQueryExecution") != 1 || f.count("GetQueryResults") != 1 {
			t.Fatal("terminal flush issued a new native read")
		}
		if !athenaSessionLifecycleSnapshot(session).receipt.Complete {
			t.Fatal("terminal acknowledgement did not retain completeness")
		}
	})

	t.Run("nonterminal_dispose_refuses_new_write", func(t *testing.T) {
		_, session, _ := athenaSessionLifecycleStart(t, false)
		if err := session.Next(); err != nil {
			t.Fatal(err)
		}
		ack := athenaSessionLifecycleSnapshot(session)
		if err := session.Dispose(); err != nil {
			t.Fatal(err)
		}
		var calls int
		var output bytes.Buffer
		writer := athenaSessionLifecycleWriterFunc(func(p []byte) (int, error) {
			calls++
			return output.Write(p)
		})
		if err := session.WritePending(writer); err != errAthenaSessionDisposed {
			t.Fatalf("WritePending after disposed = %v, want disposed", err)
		}
		if calls != 0 || output.Len() != 0 {
			t.Fatal("disposed session wrote a new frame")
		}
		if !athenaSessionLifecycleSameACK(ack, athenaSessionLifecycleSnapshot(session)) {
			t.Fatal("disposal changed acknowledged page facts")
		}
	})
}

func TestAthenaSessionDisposeDoesNotWaitForOwnedBlockedWriter(t *testing.T) {
	cfg := athenaSessionLifecycleConfig(t)
	f := athenaSessionLifecyclePageFixture(t, false)
	client, closeTransport := athenaSessionClient(t, f, cfg)
	var closes atomic.Int64
	parent, cancelParent := context.WithCancelCause(context.Background())
	session, err := newAthenaPageSession(parent, client, func() {
		closes.Add(1)
		closeTransport()
	}, athenaSessionRequest(t, cfg))
	if err != nil {
		cancelParent(nil)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Dispose() })
	if err = session.Next(); err != nil {
		cancelParent(nil)
		t.Fatal(err)
	}
	ack := athenaSessionLifecycleSnapshot(session)
	starts := f.count("StartQueryExecution")
	results := f.count("GetQueryResults")

	writer := &athenaSessionLifecycleBlockedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	writeResult := make(chan error, 1)
	writeJoined := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(writer.release) }) }
	go func() {
		defer close(writeJoined)
		writeResult <- session.WritePending(writer)
	}()
	t.Cleanup(func() {
		release()
		cancelParent(errors.New("PRIVATE_LIFECYCLE_CLEANUP"))
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-writeJoined:
		case <-timer.C:
			t.Error("owned writer did not join during cleanup")
		}
		_ = session.Dispose()
	})

	athenaSessionLifecycleWait(t, writer.entered, "owned writer entry")
	if err := session.Next(); err != errAthenaSessionBusy {
		t.Fatalf("live-parent Next = %v, want busy", err)
	}
	if err := session.WritePending(nil); err != errAthenaSessionBusy {
		t.Fatalf("live-parent nil write = %v, want busy", err)
	}

	cancelParent(errors.New("PRIVATE_CALLER_CAUSE"))
	if err := session.Next(); err != context.Canceled {
		t.Fatalf("canceled Next = %v, want caller error", err)
	}
	if err := session.WritePending(nil); err != context.Canceled {
		t.Fatalf("canceled nil write = %v, want caller error", err)
	}

	disposeA := make(chan error, 1)
	disposeB := make(chan error, 1)
	disposeAJoined := make(chan struct{})
	disposeBJoined := make(chan struct{})
	go func() {
		defer close(disposeAJoined)
		disposeA <- session.Dispose()
	}()
	go func() {
		defer close(disposeBJoined)
		disposeB <- session.Dispose()
	}()
	t.Cleanup(func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for _, joined := range []<-chan struct{}{disposeAJoined, disposeBJoined} {
			select {
			case <-joined:
			case <-timer.C:
				t.Error("Dispose goroutine did not join during cleanup")
				return
			}
		}
	})
	var errA, errB error
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for pending := 2; pending > 0; pending-- {
		select {
		case errA = <-disposeA:
			disposeA = nil
		case errB = <-disposeB:
			disposeB = nil
		case <-timer.C:
			t.Fatal("concurrent disposal waited for the owned writer")
		}
	}
	if errA != nil || errB != nil {
		t.Fatalf("disposal errors: %v / %v", errA, errB)
	}
	athenaSessionLifecycleWait(t, disposeAJoined, "first disposal join")
	athenaSessionLifecycleWait(t, disposeBJoined, "second disposal join")
	if closes.Load() != 1 {
		t.Fatalf("transport close calls = %d, want 1", closes.Load())
	}
	select {
	case earlyErr := <-writeResult:
		t.Fatalf("disposal completed the owned writer (error present: %t)", earlyErr != nil)
	default:
	}
	if !athenaSessionLifecycleSameACK(ack, athenaSessionLifecycleSnapshot(session)) {
		t.Fatal("disposal changed acknowledged page facts")
	}

	release()
	athenaSessionLifecycleWait(t, writeJoined, "owned writer join")
	var writeErr error
	resultTimer := time.NewTimer(time.Second)
	defer resultTimer.Stop()
	select {
	case writeErr = <-writeResult:
	case <-resultTimer.C:
		t.Fatal("owned writer result missing")
	}
	if writeErr != context.Canceled {
		t.Fatalf("owned writer result = %v, want caller priority", writeErr)
	}
	if !athenaSessionLifecycleSameACK(ack, athenaSessionLifecycleSnapshot(session)) {
		t.Fatal("writer completion changed acknowledged page facts")
	}
	if f.count("StartQueryExecution") != starts || f.count("GetQueryResults") != results {
		t.Fatal("owned writer lifecycle caused replay or a new native read")
	}
}
