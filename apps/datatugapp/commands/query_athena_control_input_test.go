package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type athenaControlInputReadFunc func([]byte) (int, error)

func (f athenaControlInputReadFunc) Read(p []byte) (int, error) { return f(p) }

type athenaControlInputProbe struct {
	err           error
	reads, closes int
}

func (p *athenaControlInputProbe) Read([]byte) (int, error) { p.reads++; return 0, p.err }
func (p *athenaControlInputProbe) Close() error             { p.closes++; return nil }

func athenaControlInputAssertZero(t *testing.T, source athenaControlInputSource) {
	t.Helper()
	if !reflect.DeepEqual(source, athenaControlInputSource{}) {
		t.Fatalf("refused admission returned %+v, want zero descriptor", source)
	}
}

func athenaControlInputRefused(t *testing.T, ctx context.Context, reader io.Reader, maxBytes int, want error) {
	t.Helper()
	source, err := athenaAdmitControlInput(ctx, reader, maxBytes)
	if err != want {
		t.Fatalf("refused admission returned %v, want %v", err, want)
	}
	athenaControlInputAssertZero(t, source)
}

func TestAthenaControlInputFiniteOwnership(t *testing.T) {
	ctx := context.Background()
	check := func(name string, reader io.Reader, maxBytes int, want []byte) athenaControlInputSource {
		t.Helper()
		source, err := athenaAdmitControlInput(ctx, reader, maxBytes)
		if err != nil || source.kind != athenaControlInputFinite || source.pendingFile != nil ||
			source.maxBytes != maxBytes || source.data == nil ||
			!bytes.Equal(source.data, want) || cap(source.data) != len(want) {
			t.Fatalf("%s admission returned %+v, %v, want %q", name, source, err, want)
		}
		return source
	}
	backing := []byte{0x00, 'a', 0x00, 'z', 0xff, 'Q'}
	reader := bytes.NewReader(backing)
	if _, err := io.ReadFull(reader, make([]byte, 2)); err != nil {
		t.Fatalf("consuming bytes.Reader prefix: %v", err)
	}
	want := []byte{0x00, 'z', 0xff, 'Q'}
	source := check("bytes.Reader", reader, len(want), want)
	offset, err := reader.Seek(0, io.SeekCurrent)
	if err != nil || reader.Len() != len(want) || offset != 2 {
		t.Fatalf("admission changed bytes.Reader to %d/%d, %v", reader.Len(), offset, err)
	}
	for i := range backing {
		backing[i] ^= 0xff
	}
	if !bytes.Equal(source.data, want) {
		t.Fatal("owned bytes.Reader snapshot followed caller bytes")
	}
	text := "PREFIX\x00TAIL\x00"
	stringsReader := strings.NewReader(text)
	if _, err := stringsReader.Seek(3, io.SeekStart); err != nil {
		t.Fatalf("seeking strings.Reader prefix: %v", err)
	}
	if _, err := io.ReadFull(stringsReader, make([]byte, 2)); err != nil {
		t.Fatalf("reading strings.Reader prefix: %v", err)
	}
	textWant := []byte(text[5:])
	source = check("strings.Reader", stringsReader, len(textWant), textWant)
	if offset, err := stringsReader.Seek(0, io.SeekCurrent); err != nil || offset != 5 {
		t.Fatalf("admission changed strings.Reader offset to %d, %v", offset, err)
	}
	if stringsReader.Len() != len(textWant) {
		t.Fatalf("admission left strings.Reader with %d bytes, want %d", stringsReader.Len(), len(textWant))
	}
	stringsReader.Reset(text)
	if !bytes.Equal(source.data, textWant) {
		t.Fatal("owned strings.Reader snapshot followed Reset")
	}
	buffer := bytes.NewBuffer([]byte{'P', 'R', 'E', 0x00, 'T', 0x00, 'A', 'I', 'L'})
	if prefix := buffer.Next(2); len(prefix) != 2 {
		t.Fatalf("consuming bytes.Buffer prefix returned %q", prefix)
	}
	if _, err := buffer.Read(make([]byte, 2)); err != nil {
		t.Fatalf("reading bytes.Buffer prefix: %v", err)
	}
	bufferWant := []byte{'T', 0x00, 'A', 'I', 'L'}
	source = check("bytes.Buffer", buffer, len(bufferWant), bufferWant)
	if !bytes.Equal(buffer.Bytes(), bufferWant) {
		t.Fatalf("bytes.Buffer left %q, want %q", buffer.Bytes(), bufferWant)
	}
	for i := range buffer.Bytes() {
		buffer.Bytes()[i] ^= 0xff
	}
	if !bytes.Equal(source.data, bufferWant) {
		t.Fatal("owned bytes.Buffer snapshot followed caller bytes")
	}
	check("empty finite", bytes.NewReader(nil), 1, []byte{})
	for _, test := range []struct {
		name string
		size int
	}{{"64KiB", 64 << 10}, {"128KiB", 128 << 10}} {
		data := bytes.Repeat([]byte{'q'}, test.size)
		check(test.name, bytes.NewReader(data), test.size, data)
		athenaControlInputRefused(t, ctx, bytes.NewReader(append(data, 'x')), test.size, errAthenaControl)
	}
	for _, maxBytes := range []int{0, -1, 2*athenaControlMaxBytes + 1} {
		athenaControlInputRefused(t, ctx, strings.NewReader("abc"), maxBytes, errAthenaControl)
	}
	athenaControlInputRefused(t, ctx, strings.NewReader("abcde"), 4, errAthenaControl)
	check("smaller positive cap", strings.NewReader("abcd"), 4, []byte("abcd"))
}

func TestAthenaControlInputRefusal(t *testing.T) {
	ctx := context.Background()
	for _, reader := range []io.Reader{nil, (*bytes.Reader)(nil), (*strings.Reader)(nil), (*bytes.Buffer)(nil), (*os.File)(nil)} {
		athenaControlInputRefused(t, ctx, reader, 8, errAthenaControl)
	}
	private := errors.New("PRIVATE_READ_FAILURE")
	reads := 0
	readFunc := athenaControlInputReadFunc(func([]byte) (int, error) {
		reads++
		return 0, private
	})
	athenaControlInputRefused(t, ctx, readFunc, 8, errAthenaControl)
	probe := &athenaControlInputProbe{err: private}
	athenaControlInputRefused(t, ctx, probe, 8, errAthenaControl)
	if reads != 0 || probe.reads != 0 || probe.closes != 0 {
		t.Fatalf("refused custom readers were touched: %d/%d/%d", reads, probe.reads, probe.closes)
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
	}{{canceled, context.Canceled}, {expired, context.DeadlineExceeded}, {nilContext, errAthenaControl}} {
		athenaControlInputRefused(t, test.ctx, strings.NewReader("abc"), 3, test.want)
		athenaControlInputRefused(t, test.ctx, nil, 0, test.want)
	}
}

func TestAthenaControlInputPendingFile(t *testing.T) {
	ctx := context.Background()
	file, err := os.CreateTemp(t.TempDir(), "athena-control-input-*")
	if err != nil {
		t.Fatalf("creating pending file: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing pending file: %v", closeErr)
		}
	})
	want := []byte("PENDING\x00FILE\xff")
	if _, err := file.Write(want); err != nil {
		t.Fatalf("writing pending file: %v", err)
	}
	if _, err := file.Seek(2, io.SeekStart); err != nil {
		t.Fatalf("seeking pending file: %v", err)
	}
	source, err := athenaAdmitControlInput(ctx, file, len(want))
	if err != nil || source.kind != athenaControlInputPendingFile || source.pendingFile != file ||
		source.data != nil || source.maxBytes != len(want) {
		t.Fatalf("pending file admission returned %+v, %v", source, err)
	}
	var descriptor any = source
	if _, ok := descriptor.(io.Reader); ok {
		t.Fatal("source descriptor exposes Read")
	}
	if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 2 {
		t.Fatalf("admission moved pending file offset to %d, %v", offset, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewinding pending file: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(file, got); err != nil {
		t.Fatalf("caller reading pending file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("caller pending file returned %q, want %q", got, want)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing caller-owned pending file: %v", err)
	}
	closed = true
	closedFile, err := os.CreateTemp(t.TempDir(), "athena-control-closed-*")
	if err != nil {
		t.Fatalf("creating closed file: %v", err)
	}
	closedFileClosed := false
	t.Cleanup(func() {
		if closedFileClosed {
			return
		}
		if closeErr := closedFile.Close(); closeErr != nil {
			t.Errorf("closing owned closed file: %v", closeErr)
		}
	})
	if _, err := closedFile.Write([]byte("closed")); err != nil {
		t.Fatalf("writing closed file: %v", err)
	}
	if err := closedFile.Close(); err != nil {
		t.Fatalf("closing closed file: %v", err)
	}
	closedFileClosed = true
	source, err = athenaAdmitControlInput(ctx, closedFile, 6)
	if err != nil || source.kind != athenaControlInputPendingFile || source.pendingFile != closedFile ||
		source.data != nil || source.maxBytes != 6 {
		t.Fatalf("closed file admission returned %+v, %v", source, err)
	}
}
