package commands

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type athenaControlsReadFunc func([]byte) (int, error)

func (f athenaControlsReadFunc) Read(p []byte) (int, error) { return f(p) }

type athenaControlsFailReader struct {
	data []byte
	err  error
}

func (r *athenaControlsFailReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func athenaControlsAccepted(t *testing.T, frame []byte, used athenaControlBudget, operation string, bytes, frames, nexts int) athenaControlBudget {
	t.Helper()
	got, next, err := athenaControlFrame(context.Background(), frame, used)
	if err != nil {
		t.Fatalf("valid frame %q was refused: %v", frame, err)
	}
	want := athenaControlBudget{bytes: bytes, frames: frames, nexts: nexts}
	if got != operation || next != want {
		t.Fatalf("frame %q returned (%q,%+v), want (%q,%+v)", frame, got, next, operation, want)
	}
	return next
}

func athenaControlsRefused(t *testing.T, ctx context.Context, frame []byte, used athenaControlBudget) {
	t.Helper()
	operation, next, err := athenaControlFrame(ctx, frame, used)
	if err != errAthenaControl {
		t.Fatalf("invalid frame %q returned %v, want errAthenaControl", frame, err)
	}
	if operation != "" || next != used {
		t.Fatalf("invalid frame %q returned (%q,%+v), want empty and %+v", frame, operation, next, used)
	}
}

func athenaControlsSharedAccepted(t *testing.T, input *bufio.Reader, wantRaw string) []byte {
	t.Helper()
	raw, tail, err := athenaSharedRequest(context.Background(), input)
	if err != nil {
		t.Fatalf("valid shared request was refused: %v", err)
	}
	if string(raw) != wantRaw {
		t.Fatalf("shared request returned raw %q, want %q", raw, wantRaw)
	}
	rest, readErr := io.ReadAll(tail)
	if readErr != nil {
		t.Fatalf("reading the shared request tail failed: %v", readErr)
	}
	return rest
}

func athenaControlsSharedRefused(t *testing.T, ctx context.Context, input *bufio.Reader, want error) {
	t.Helper()
	raw, tail, err := athenaSharedRequest(ctx, input)
	if err != want {
		t.Fatalf("shared request returned %v, want %v", err, want)
	}
	if raw != nil || tail != nil {
		t.Fatal("failed shared request returned output")
	}
}

func TestAthenaControlsFrameProtocol(t *testing.T) {
	for _, test := range []struct {
		name, frame, operation string
		nexts                  int
	}{
		{"LF", "{\"operation\":\"next\"}\n", "next", 1},
		{"CRLF", "{\"operation\":\"cancel\"}\r\n", "cancel", 0},
		{"EOF_final", `{"operation":"close"}`, "close", 0},
		{"escaped_key", `{"opera\u0074ion":"next"}`, "next", 1},
	} {
		athenaControlsAccepted(t, []byte(test.frame), athenaControlBudget{}, test.operation, len(test.frame), 1, test.nexts)
	}
	used := athenaControlBudget{bytes: 7, frames: 3, nexts: 2}
	blank := []byte(" \t\r\n")
	athenaControlsAccepted(t, blank, used, "", used.bytes+len(blank), used.frames, used.nexts)
	operation, next, err := athenaControlFrame(context.Background(), nil, used)
	if err != nil || operation != "" || next != used {
		t.Fatal("empty frame was not a no-event")
	}
	used = athenaControlBudget{bytes: 5, frames: 2, nexts: 1}
	for _, frame := range [][]byte{
		[]byte(`{"operation":"next","operation":"cancel"}`),
		[]byte(`{"operation":"next","opera\u0074ion":"cancel"}`),
		[]byte(`{"Operation":"next"}`),
		[]byte(`{"operation":"NEXT"}`),
		[]byte(`{"operation":null}`),
		[]byte(`{"operation":"next","extra":true}`),
		[]byte(`{"operation":1}`),
		[]byte(`{}`),
		[]byte(`[]`),
		[]byte(`{"operation":"next"}{}`),
		[]byte("{\"operation\":\"\xff\"}"),
		[]byte(`{"operation":"\ud800"}`),
		[]byte("\v{\"operation\":\"next\"}"),
		[]byte("\f{\"operation\":\"next\"}"),
		[]byte("\u00a0{\"operation\":\"next\"}"),
		[]byte("{\"operation\":\"next\"}\n\n"),
		[]byte("{\"operation\":\"next\"}\n{}"),
	} {
		athenaControlsRefused(t, context.Background(), frame, used)
	}
	canceled, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	var nilContext context.Context
	for _, test := range []struct {
		ctx  context.Context
		want error
	}{
		{canceled, context.Canceled},
		{expired, context.DeadlineExceeded},
		{nilContext, errAthenaControl},
	} {
		used = athenaControlBudget{bytes: 3, frames: 1, nexts: 1}
		operation, next, err = athenaControlFrame(test.ctx, []byte("{"), used)
		if err != test.want {
			t.Fatalf("caller context returned %v, want %v", err, test.want)
		}
		if operation != "" || next != used {
			t.Fatal("caller context changed the budget or operation")
		}
	}
}

func TestAthenaControlsBudgets(t *testing.T) {
	frame := []byte(`{"operation":"next"}`)
	used := athenaControlBudget{}
	for i := 0; i < 99; i++ {
		used = athenaControlsAccepted(t, frame, used, "next", (i+1)*len(frame), i+1, i+1)
	}
	if used != (athenaControlBudget{bytes: 99 * len(frame), frames: 99, nexts: 99}) {
		t.Fatalf("99 next frames ended at %+v", used)
	}
	athenaControlsRefused(t, context.Background(), frame, used)
	frames := [][]byte{[]byte(`{"operation":"cancel"}`), []byte(`{"operation":"close"}`)}
	used = athenaControlBudget{}
	bytes := 0
	for i := 0; i < 128; i++ {
		current := frames[i%2]
		bytes += len(current)
		want := "cancel"
		if i%2 == 1 {
			want = "close"
		}
		used = athenaControlsAccepted(t, current, used, want, bytes, i+1, 0)
	}
	if used != (athenaControlBudget{bytes: bytes, frames: 128, nexts: 0}) {
		t.Fatalf("128 cancel/close frames ended at %+v", used)
	}
	athenaControlsRefused(t, context.Background(), frames[0], used)
	used = athenaControlsAccepted(t, []byte(strings.Repeat(" ", 4095)+"\n"), athenaControlBudget{}, "", 4096, 0, 0)
	athenaControlsRefused(t, context.Background(), []byte(strings.Repeat(" ", 4096)+"\n"), used)
	frame = []byte(strings.Repeat(" ", 4095) + "\n")
	used = athenaControlBudget{}
	for i := 0; i < 16; i++ {
		used = athenaControlsAccepted(t, frame, used, "", (i+1)*4096, 0, 0)
	}
	if used != (athenaControlBudget{bytes: 65536, frames: 0, nexts: 0}) {
		t.Fatalf("16 blank frames ended at %+v", used)
	}
	athenaControlsRefused(t, context.Background(), []byte(" "), used)
}

func TestAthenaControlsSharedRequest(t *testing.T) {
	request := "{\n  \"operation\": \"query\"\n}"
	controls := "{\"operation\":\"next\"}\n{\"operation\":\"cancel\"}\r\n{\"operation\":\"close\"}"
	buffered := func(source string) *bufio.Reader {
		return bufio.NewReaderSize(strings.NewReader(source), len(source))
	}
	source := request + " \t\r\n" + controls
	if rest := athenaControlsSharedAccepted(t, buffered(source), request); string(rest) != controls {
		t.Fatalf("tail returned %q, want %q", rest, controls)
	}
	for _, source = range []string{request, request + " \t\r"} {
		if rest := athenaControlsSharedAccepted(t, buffered(source), request); len(rest) != 0 {
			t.Fatalf("EOF tail returned %q, want empty", rest)
		}
	}
	for _, source = range []string{
		`{"operation":"query","operation":"cancel"}` + "\n",
		request + `{"operation":"next"}`,
		"{\"operation\":\"\xff\"}\n",
		`{"operation":"\ud800"}` + "\n",
		"[1]\n",
		"null\n",
		"prefix" + request + "\n",
	} {
		athenaControlsSharedRefused(t, context.Background(), buffered(source), errAthenaControl)
	}
	object := `{"pad":"` + strings.Repeat("a", 65000) + `"}`
	exact := object + strings.Repeat(" ", 65536-len(object)-1) + "\n"
	over := object + strings.Repeat(" ", 65536-len(object)) + "\n"
	if len(exact) != 65536 || len(over) != 65537 {
		t.Fatal("boundary sources are not sized as intended")
	}
	if rest := athenaControlsSharedAccepted(t, buffered(exact), object); len(rest) != 0 {
		t.Fatalf("boundary tail returned %q, want empty", rest)
	}
	athenaControlsSharedRefused(t, context.Background(), buffered(over), errAthenaControl)
	oversize := `{"pad":"` + strings.Repeat("a", 65536) + `"}`
	athenaControlsSharedRefused(t, context.Background(), buffered(oversize), errAthenaControl)
	private := errors.New("PRIVATE_READ_FAILURE")
	input := bufio.NewReader(&athenaControlsFailReader{data: []byte(request), err: private})
	raw, tail, err := athenaSharedRequest(context.Background(), input)
	if err != errAthenaControl || errors.Is(err, private) {
		t.Fatalf("failing reader returned %v, want redacted errAthenaControl", err)
	}
	if raw != nil || tail != nil {
		t.Fatal("failing reader returned output")
	}
	canceled, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	var nilContext context.Context
	athenaControlsSharedRefused(t, context.Background(), nil, errAthenaControl)
	for _, test := range []struct {
		ctx  context.Context
		want error
	}{
		{nilContext, errAthenaControl},
		{canceled, context.Canceled},
		{expired, context.DeadlineExceeded},
	} {
		athenaControlsSharedRefused(t, test.ctx, buffered(request+"\n"), test.want)
	}
	ctx, cancelPriority := context.WithCancel(context.Background())
	defer cancelPriority()
	reader := athenaControlsReadFunc(func([]byte) (int, error) {
		cancelPriority()
		return 0, errors.New("PRIVATE_CANCEL_READ")
	})
	raw, tail, err = athenaSharedRequest(ctx, bufio.NewReader(reader))
	if err != context.Canceled {
		t.Fatalf("cancel priority returned %v, want context.Canceled", err)
	}
	if raw != nil || tail != nil {
		t.Fatal("cancel priority returned output")
	}
}
