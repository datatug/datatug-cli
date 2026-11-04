//go:build linux || darwin

package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const athenaNativeTestBound = 10 * time.Second

type athenaNativeReadOutcome struct {
	data []byte
	n    int
	err  error
}

func athenaNativePipeFixture(t *testing.T) (readFile, writeFile *os.File) {
	t.Helper()
	return athenaNativePipeFixtureWithCleanup(t, nil)
}

func athenaNativePipeFixtureWithCleanup(t *testing.T, cleanup func(readFile, writeFile *os.File)) (readFile, writeFile *os.File) {
	t.Helper()
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating fixture pipe: %v", err)
	}
	if cleanup != nil {
		t.Cleanup(func() { cleanup(readFile, writeFile) })
		return readFile, writeFile
	}
	t.Cleanup(func() {
		if closeErr := writeFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("closing fixture writer: %v", closeErr)
		}
		if closeErr := readFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("closing fixture reader: %v", closeErr)
		}
	})
	return readFile, writeFile
}

func athenaNativeCleanupFile(t *testing.T, file *os.File) {
	t.Helper()
	if file == nil {
		return
	}
	t.Cleanup(func() { athenaNativeCloseTolerant(t, file, "fixture file") })
}

func athenaNativeCloseTolerant(t *testing.T, file *os.File, label string) {
	t.Helper()
	if file == nil {
		return
	}
	if closeErr := file.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		t.Errorf("closing %s: %v", label, closeErr)
	}
}

func athenaNativeFileFlags(t *testing.T, file *os.File) int {
	t.Helper()
	conn, err := file.SyscallConn()
	if err != nil {
		t.Fatalf("getting SyscallConn: %v", err)
	}
	var flags int
	var fcntlErr error
	ran := false
	if controlErr := conn.Control(func(fd uintptr) {
		ran = true
		flags, fcntlErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
	}); controlErr != nil {
		t.Fatalf("controlling descriptor: %v", controlErr)
	}
	if !ran {
		t.Fatal("descriptor control did not run")
	}
	if fcntlErr != nil {
		t.Fatalf("reading descriptor flags: %v", fcntlErr)
	}
	return flags
}

func athenaNativeAwait(t *testing.T, ch <-chan struct{}, what string, release func()) {
	t.Helper()
	timer := time.NewTimer(athenaNativeTestBound)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		if release != nil {
			release()
		}
		t.Fatalf("timed out waiting for %s", what)
	}
}

func athenaNativeJoinBounded(t *testing.T, done <-chan struct{}, what string) bool {
	t.Helper()
	timer := time.NewTimer(athenaNativeTestBound)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		t.Errorf("timed out waiting for %s to join", what)
		return false
	}
}

func athenaNativeReadBounded(t *testing.T, r io.Reader, total, first, chunk int, release, start func(), doneOut *chan struct{}) (athenaNativeReadOutcome, bool) {
	t.Helper()
	results := make(chan athenaNativeReadOutcome, 1)
	done := make(chan struct{})
	if doneOut != nil {
		*doneOut = done
	}
	entered := make(chan struct{})
	go func() {
		defer close(done)
		close(entered)
		data := make([]byte, 0, total)
		buf := make([]byte, chunk)
		request := first
		for len(data) < total {
			want := request
			if want > total-len(data) {
				want = total - len(data)
			}
			if want > len(buf) {
				want = len(buf)
			}
			n, err := r.Read(buf[:want])
			if n > 0 {
				data = append(data, buf[:n]...)
			}
			if err != nil {
				results <- athenaNativeReadOutcome{data: data, n: len(data), err: err}
				return
			}
			if n == 0 {
				results <- athenaNativeReadOutcome{data: data, n: len(data), err: io.ErrNoProgress}
				return
			}
			request = chunk
		}
		results <- athenaNativeReadOutcome{data: data, n: len(data)}
	}()
	timer := time.NewTimer(athenaNativeTestBound)
	defer timer.Stop()
	select {
	case <-entered:
	case <-timer.C:
		if release != nil {
			release()
		}
		athenaNativeJoinBounded(t, done, "bounded read worker")
		return athenaNativeReadOutcome{}, false
	}
	if start != nil {
		start()
	}
	select {
	case outcome := <-results:
		select {
		case <-done:
			return outcome, true
		case <-timer.C:
			if release != nil {
				release()
			}
			athenaNativeJoinBounded(t, done, "bounded read worker")
			return athenaNativeReadOutcome{}, false
		}
	case <-timer.C:
		if release != nil {
			release()
		}
		athenaNativeJoinBounded(t, done, "bounded read worker")
		return athenaNativeReadOutcome{}, false
	}
}

func athenaNativeReadEOF(t *testing.T, r io.Reader, release func(), doneOut *chan struct{}) (athenaNativeReadOutcome, bool) {
	t.Helper()
	results := make(chan athenaNativeReadOutcome, 1)
	done := make(chan struct{})
	if doneOut != nil {
		*doneOut = done
	}
	go func() {
		defer close(done)
		n, err := r.Read(make([]byte, 8))
		results <- athenaNativeReadOutcome{n: n, err: err}
	}()
	timer := time.NewTimer(athenaNativeTestBound)
	defer timer.Stop()
	select {
	case outcome := <-results:
		select {
		case <-done:
			return outcome, true
		case <-timer.C:
			if release != nil {
				release()
			}
			athenaNativeJoinBounded(t, done, "EOF read worker")
			return athenaNativeReadOutcome{}, false
		}
	case <-timer.C:
		if release != nil {
			release()
		}
		athenaNativeJoinBounded(t, done, "EOF read worker")
		return athenaNativeReadOutcome{}, false
	}
}

func TestAthenaNativeControlConstructorWakePair(t *testing.T) {
	t.Run("cancelled while factory runs", func(t *testing.T) {
		borrowedRead, borrowedWrite := athenaNativePipeFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var factoryRead, factoryWrite *os.File
		factoryCalls := 0
		reader, err := athenaNewNativeControlReaderWithWakePair(ctx, borrowedRead, func() (*os.File, *os.File, error) {
			factoryCalls++
			readEnd, writeEnd, pipeErr := os.Pipe()
			if pipeErr != nil {
				return nil, nil, pipeErr
			}
			factoryRead, factoryWrite = readEnd, writeEnd
			athenaNativeCleanupFile(t, readEnd)
			athenaNativeCleanupFile(t, writeEnd)
			cancel()
			return readEnd, writeEnd, nil
		})
		if factoryCalls != 1 {
			t.Fatalf("wake pair factory called %d times, want 1", factoryCalls)
		}
		if reader != nil || err != context.Canceled {
			t.Fatalf("constructor returned %v, %v; want nil, context.Canceled", reader, err)
		}
		for name, file := range map[string]*os.File{"factory read": factoryRead, "factory write": factoryWrite} {
			if file == nil {
				t.Fatalf("%s was not captured", name)
			}
			if _, statErr := file.Stat(); statErr == nil || !errors.Is(statErr, os.ErrClosed) {
				t.Fatalf("%s Stat error = %v, want os.ErrClosed", name, statErr)
			}
		}
		payload := []byte("BORROWED\x00\xff")
		if _, writeErr := borrowedWrite.Write(payload); writeErr != nil {
			t.Fatalf("writing borrowed pipe after constructor refusal: %v", writeErr)
		}
		got := make([]byte, len(payload))
		if _, readErr := io.ReadFull(borrowedRead, got); readErr != nil {
			t.Fatalf("reading borrowed pipe after constructor refusal: %v", readErr)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("borrowed pipe returned %q, want %q", got, payload)
		}
	})

	t.Run("factory returns owned pair plus closed read error", func(t *testing.T) {
		borrowedRead, borrowedWrite := athenaNativePipeFixture(t)
		var factoryRead, factoryWrite *os.File
		var factoryErr error
		factoryCalls := 0
		reader, err := athenaNewNativeControlReaderWithWakePair(context.Background(), borrowedRead, func() (*os.File, *os.File, error) {
			factoryCalls++
			readEnd, writeEnd, pipeErr := os.Pipe()
			if pipeErr != nil {
				return nil, nil, pipeErr
			}
			factoryRead, factoryWrite = readEnd, writeEnd
			athenaNativeCleanupFile(t, readEnd)
			athenaNativeCleanupFile(t, writeEnd)
			if closeErr := readEnd.Close(); closeErr != nil {
				factoryErr = closeErr
				return readEnd, writeEnd, closeErr
			}
			_, statErr := readEnd.Stat()
			factoryErr = statErr
			return readEnd, writeEnd, statErr
		})
		if factoryCalls != 1 {
			t.Fatalf("wake pair factory called %d times, want 1", factoryCalls)
		}
		if !errors.Is(factoryErr, os.ErrClosed) {
			t.Fatalf("factory error = %v, want os.ErrClosed", factoryErr)
		}
		if reader != nil || err != errAthenaControl {
			t.Fatalf("constructor returned %v, %v; want nil, errAthenaControl", reader, err)
		}
		for name, file := range map[string]*os.File{"factory read": factoryRead, "factory write": factoryWrite} {
			if file == nil {
				t.Fatalf("%s was not captured", name)
			}
			if _, statErr := file.Stat(); statErr == nil || !errors.Is(statErr, os.ErrClosed) {
				t.Fatalf("%s Stat error = %v, want os.ErrClosed", name, statErr)
			}
		}
		payload := []byte("BORROWED\x00\xff")
		if _, writeErr := borrowedWrite.Write(payload); writeErr != nil {
			t.Fatalf("writing borrowed pipe after constructor refusal: %v", writeErr)
		}
		got := make([]byte, len(payload))
		if _, readErr := io.ReadFull(borrowedRead, got); readErr != nil {
			t.Fatalf("reading borrowed pipe after constructor refusal: %v", readErr)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("borrowed pipe returned %q, want %q", got, payload)
		}
	})

	t.Run("nil factory", func(t *testing.T) {
		borrowedRead, _ := athenaNativePipeFixture(t)
		before := athenaNativeFileFlags(t, borrowedRead)
		reader, err := athenaNewNativeControlReaderWithWakePair(context.Background(), borrowedRead, nil)
		if reader != nil || err != errAthenaControl {
			t.Fatalf("constructor returned %v, %v; want nil, errAthenaControl", reader, err)
		}
		if after := athenaNativeFileFlags(t, borrowedRead); after != before {
			t.Fatalf("borrowed flags changed from %#x to %#x", before, after)
		}
	})
}

func TestAthenaNativeControlReaderLifecycle(t *testing.T) {
	var owned athenaOwnedControlReader
	var readDone chan struct{}
	borrowedRead, borrowedWrite := athenaNativePipeFixtureWithCleanup(t, func(readFile, writeFile *os.File) {
		if owned != nil {
			_ = owned.Cancel()
		}
		athenaNativeCloseTolerant(t, writeFile, "fixture writer")
		if readDone != nil {
			if !athenaNativeJoinBounded(t, readDone, "native control read worker") {
				return
			}
		}
		if owned != nil {
			if closeErr := owned.Close(); closeErr != nil {
				t.Errorf("closing native control reader in cleanup: %v", closeErr)
			}
		}
		athenaNativeCloseTolerant(t, readFile, "fixture reader")
	})
	flagsBefore := athenaNativeFileFlags(t, borrowedRead)
	owned, err := athenaNewNativeControlReader(context.Background(), borrowedRead)
	if err != nil {
		t.Fatalf("constructing native control reader: %v", err)
	}
	native, ok := owned.(*athenaNativeControlReader)
	if !ok {
		t.Fatalf("constructor returned %T, want *athenaNativeControlReader", owned)
	}
	originalPoll := native.poll
	var enteredOnce sync.Once
	entered := make(chan struct{})
	native.poll = func(fds []unix.PollFd, timeout int) (int, error) {
		enteredOnce.Do(func() { close(entered) })
		return originalPoll(fds, timeout)
	}
	if after := athenaNativeFileFlags(t, borrowedRead); after != flagsBefore {
		t.Fatalf("constructor changed borrowed flags from %#x to %#x", flagsBefore, after)
	}

	type readOutcome struct {
		n   int
		err error
	}
	results := make(chan readOutcome, 1)
	readDone = make(chan struct{})
	go func() {
		defer close(readDone)
		n, readErr := owned.Read(make([]byte, 16))
		results <- readOutcome{n: n, err: readErr}
	}()
	releaseWrite := func() { athenaNativeCloseTolerant(t, borrowedWrite, "fixture writer") }
	athenaNativeAwait(t, entered, "native poll entry", releaseWrite)

	if closeErr := owned.Close(); closeErr != errAthenaControl {
		t.Fatalf("Close while Read active returned %v, want errAthenaControl", closeErr)
	}
	if n, readErr := owned.Read(make([]byte, 4)); n != 0 || readErr != errAthenaControl {
		t.Fatalf("second concurrent Read returned %d, %v; want 0, errAthenaControl", n, readErr)
	}
	if !owned.Cancel() {
		t.Fatal("Cancel was not newly accepted")
	}
	athenaNativeAwait(t, readDone, "canceled read completion", releaseWrite)
	result := <-results
	if result.n != 0 || result.err != errAthenaControl {
		t.Fatalf("canceled Read returned %d, %v; want 0, errAthenaControl", result.n, result.err)
	}
	if closeErr := owned.Close(); closeErr != nil {
		t.Fatalf("aux Close after Read join returned %v, want nil", closeErr)
	}
	if closeErr := owned.Close(); closeErr != nil {
		t.Fatalf("repeated Close returned %v, want nil", closeErr)
	}

	payload := []byte("SECOND\x00WRITE\xff")
	if _, writeErr := borrowedWrite.Write(payload); writeErr != nil {
		t.Fatalf("writing borrowed pipe after owned Close: %v", writeErr)
	}
	got := make([]byte, len(payload))
	if _, readErr := io.ReadFull(borrowedRead, got); readErr != nil {
		t.Fatalf("borrowed reader after owned Close: %v", readErr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("borrowed reader returned %q, want %q", got, payload)
	}
	if after := athenaNativeFileFlags(t, borrowedRead); after != flagsBefore {
		t.Fatalf("borrowed flags changed from %#x to %#x", flagsBefore, after)
	}
}

func TestAthenaNativeControlReaderReadSemantics(t *testing.T) {
	t.Run("pre-cancel and zero length", func(t *testing.T) {
		borrowedRead, _ := athenaNativePipeFixture(t)
		owned, err := athenaNewNativeControlReader(context.Background(), borrowedRead)
		if err != nil {
			t.Fatalf("constructing native control reader: %v", err)
		}
		t.Cleanup(func() {
			_ = owned.Cancel()
			if closeErr := owned.Close(); closeErr != nil {
				t.Errorf("closing native control reader: %v", closeErr)
			}
		})
		if n, readErr := owned.Read(nil); n != 0 || readErr != nil {
			t.Fatalf("zero-length Read before Cancel returned %d, %v; want 0, nil", n, readErr)
		}
		if !owned.Cancel() {
			t.Fatal("Cancel was not newly accepted")
		}
		if n, readErr := owned.Read(nil); n != 0 || readErr != nil {
			t.Fatalf("zero-length Read after Cancel returned %d, %v; want 0, nil", n, readErr)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if n, readErr := owned.Read(make([]byte, 8)); n != 0 || readErr != errAthenaControl {
				t.Fatalf("post-cancel Read %d returned %d, %v; want 0, errAthenaControl", attempt, n, readErr)
			}
		}
		if owned.Cancel() {
			t.Fatal("repeated Cancel was newly accepted")
		}
		if closeErr := owned.Close(); closeErr != nil {
			t.Fatalf("Close after Cancel returned %v, want nil", closeErr)
		}
		if n, readErr := owned.Read(make([]byte, 8)); n != 0 || readErr != errAthenaControl {
			t.Fatalf("post-close Read returned %d, %v; want 0, errAthenaControl", n, readErr)
		}
	})

	t.Run("data then native EOF", func(t *testing.T) {
		borrowedRead, borrowedWrite := athenaNativePipeFixture(t)
		owned, err := athenaNewNativeControlReader(context.Background(), borrowedRead)
		if err != nil {
			t.Fatalf("constructing native control reader: %v", err)
		}
		t.Cleanup(func() {
			athenaNativeCloseTolerant(t, borrowedWrite, "fixture writer")
			_ = owned.Cancel()
			if closeErr := owned.Close(); closeErr != nil {
				t.Errorf("closing native control reader: %v", closeErr)
			}
		})
		payload := []byte("DATA\x00\xffTAIL")
		if _, writeErr := borrowedWrite.Write(payload); writeErr != nil {
			t.Fatalf("writing fixture pipe: %v", writeErr)
		}
		got := make([]byte, len(payload)+8)
		n, readErr := owned.Read(got)
		if readErr != nil || n != len(payload) || !bytes.Equal(got[:n], payload) {
			t.Fatalf("Read returned %d, %v, %q; want %d, nil, %q", n, readErr, got[:n], len(payload), payload)
		}
		if closeErr := borrowedWrite.Close(); closeErr != nil {
			t.Fatalf("closing fixture writer: %v", closeErr)
		}
		if n, readErr = owned.Read(make([]byte, 8)); n != 0 || readErr != io.EOF {
			t.Fatalf("Post-EOF Read returned %d, %v; want 0, io.EOF", n, readErr)
		}
		if n, readErr = owned.Read(make([]byte, 8)); n != 0 || readErr != io.EOF {
			t.Fatalf("repeated Post-EOF Read returned %d, %v; want 0, io.EOF", n, readErr)
		}
		if closeErr := owned.Close(); closeErr != nil {
			t.Fatalf("Close after EOF returned %v, want nil", closeErr)
		}
	})
}

func TestAthenaNativeControlReaderConcurrentCancel(t *testing.T) {
	t.Run("exactly one accepted signal", func(t *testing.T) {
		borrowedRead, _ := athenaNativePipeFixture(t)
		owned, err := athenaNewNativeControlReader(context.Background(), borrowedRead)
		if err != nil {
			t.Fatalf("constructing native control reader: %v", err)
		}
		var workersDone chan struct{}
		t.Cleanup(func() {
			joined := true
			if workersDone != nil {
				joined = athenaNativeJoinBounded(t, workersDone, "concurrent Cancel workers")
			}
			if joined {
				if closeErr := owned.Close(); closeErr != nil {
					t.Errorf("closing native control reader: %v", closeErr)
				}
			}
		})
		const workers = 8
		start := make(chan struct{})
		accepted := make(chan bool, workers)
		var wg sync.WaitGroup
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			go func() {
				defer wg.Done()
				<-start
				accepted <- owned.Cancel()
			}()
		}
		close(start)
		workersDone = make(chan struct{})
		go func() {
			wg.Wait()
			close(workersDone)
		}()
		athenaNativeAwait(t, workersDone, "concurrent Cancel workers", nil)
		close(accepted)
		count := 0
		for ok := range accepted {
			if ok {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("accepted %d cancellation signals, want exactly 1", count)
		}
		if closeErr := owned.Close(); closeErr != nil {
			t.Fatalf("Close after concurrent Cancel returned %v, want nil", closeErr)
		}
	})

	t.Run("blocked read then Cancel and joined Close", func(t *testing.T) {
		var owned athenaOwnedControlReader
		var readDone chan struct{}
		borrowedRead, borrowedWrite := athenaNativePipeFixtureWithCleanup(t, func(readFile, writeFile *os.File) {
			if owned != nil {
				_ = owned.Cancel()
			}
			athenaNativeCloseTolerant(t, writeFile, "fixture writer")
			if readDone != nil {
				if !athenaNativeJoinBounded(t, readDone, "blocked native control read worker") {
					return
				}
			}
			if owned != nil {
				if closeErr := owned.Close(); closeErr != nil {
					t.Errorf("closing native control reader in cleanup: %v", closeErr)
				}
			}
			athenaNativeCloseTolerant(t, readFile, "fixture reader")
		})
		owned, err := athenaNewNativeControlReader(context.Background(), borrowedRead)
		if err != nil {
			t.Fatalf("constructing native control reader: %v", err)
		}
		native, ok := owned.(*athenaNativeControlReader)
		if !ok {
			t.Fatalf("constructor returned %T, want *athenaNativeControlReader", owned)
		}
		originalPoll := native.poll
		var enteredOnce sync.Once
		entered := make(chan struct{})
		native.poll = func(fds []unix.PollFd, timeout int) (int, error) {
			enteredOnce.Do(func() { close(entered) })
			return originalPoll(fds, timeout)
		}
		type readOutcome struct {
			n   int
			err error
		}
		results := make(chan readOutcome, 1)
		readDone = make(chan struct{})
		go func() {
			defer close(readDone)
			n, readErr := owned.Read(make([]byte, 4))
			results <- readOutcome{n: n, err: readErr}
		}()
		releaseWrite := func() { athenaNativeCloseTolerant(t, borrowedWrite, "fixture writer") }
		athenaNativeAwait(t, entered, "blocked read poll entry", releaseWrite)
		if !owned.Cancel() {
			t.Fatal("Cancel was not newly accepted")
		}
		athenaNativeAwait(t, readDone, "blocked read completion", releaseWrite)
		result := <-results
		if result.n != 0 || result.err != errAthenaControl {
			t.Fatalf("canceled blocked Read returned %d, %v; want 0, errAthenaControl", result.n, result.err)
		}
		if closeErr := owned.Close(); closeErr != nil {
			t.Fatalf("Close after joined Read returned %v, want nil", closeErr)
		}
	})
}

func TestAthenaNativeControlConstructorRefusal(t *testing.T) {
	ctx := context.Background()
	readEnd, writeEnd := athenaNativePipeFixture(t)
	_ = readEnd
	if reader, err := athenaNewNativeControlReader(ctx, writeEnd); reader != nil || err != errAthenaControl {
		t.Fatalf("writer endpoint constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}

	regular, err := os.CreateTemp(t.TempDir(), "athena-regular-*")
	if err != nil {
		t.Fatalf("creating regular file: %v", err)
	}
	athenaNativeCleanupFile(t, regular)
	if reader, err := athenaNewNativeControlReader(ctx, regular); reader != nil || err != errAthenaControl {
		t.Fatalf("regular file constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}

	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening directory: %v", err)
	}
	athenaNativeCleanupFile(t, directory)
	if reader, err := athenaNewNativeControlReader(ctx, directory); reader != nil || err != errAthenaControl {
		t.Fatalf("directory constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}

	closedRead, closedWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating closed-file fixture: %v", err)
	}
	athenaNativeCleanupFile(t, closedWrite)
	if closeErr := closedRead.Close(); closeErr != nil {
		t.Fatalf("closing fixture read end: %v", closeErr)
	}
	athenaNativeCleanupFile(t, closedRead)
	if reader, err := athenaNewNativeControlReader(ctx, closedRead); reader != nil || err != errAthenaControl {
		t.Fatalf("closed file constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}

	if reader, err := athenaNewNativeControlReader(ctx, nil); reader != nil || err != errAthenaControl {
		t.Fatalf("nil file constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}
	var nilContext context.Context
	if reader, err := athenaNewNativeControlReader(nilContext, nil); reader != nil || err != errAthenaControl {
		t.Fatalf("nil context constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}
	if reader, err := athenaNewNativeControlReader(nilContext, writeEnd); reader != nil || err != errAthenaControl {
		t.Fatalf("nil context with profile constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, test := range []struct {
		name string
		ctx  context.Context
		file *os.File
		want error
	}{
		{"canceled context nil file", canceled, nil, context.Canceled},
		{"expired context nil file", expired, nil, context.DeadlineExceeded},
		{"canceled context bad profile", canceled, writeEnd, context.Canceled},
		{"expired context bad profile", expired, writeEnd, context.DeadlineExceeded},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			reader, err := athenaNewNativeControlReader(test.ctx, test.file)
			if reader != nil || err != test.want {
				t.Fatalf("constructor returned %v, %v; want nil, %v", reader, err, test.want)
			}
		})
	}
}
