//go:build linux

package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

func athenaNativeLinuxPacketPipe(t *testing.T, cleanup func(readFile, writeFile *os.File)) (readFile, writeFile *os.File) {
	t.Helper()
	fds := make([]int, 2)
	if err := unix.Pipe2(fds, unix.O_DIRECT|unix.O_CLOEXEC); err != nil {
		t.Fatalf("creating O_DIRECT packet pipe: %v", err)
	}
	readFile = os.NewFile(uintptr(fds[0]), "athena-packet-read")
	writeFile = os.NewFile(uintptr(fds[1]), "athena-packet-write")
	if readFile == nil || writeFile == nil {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
		t.Fatal("wrapping packet pipe descriptors")
	}
	t.Cleanup(func() { cleanup(readFile, writeFile) })
	return readFile, writeFile
}

func athenaNativeLinuxFIFO(t *testing.T) (readFile, writeFile *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "athena-native.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("creating FIFO: %v", err)
	}
	readFD, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("opening FIFO for reading: %v", err)
	}
	readFile = os.NewFile(uintptr(readFD), "athena-fifo-read")
	if readFile == nil {
		_ = unix.Close(readFD)
		t.Fatal("wrapping FIFO read descriptor")
	}
	writeFD, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		athenaNativeCloseTolerant(t, readFile, "FIFO reader")
		t.Fatalf("opening FIFO for writing: %v", err)
	}
	writeFile = os.NewFile(uintptr(writeFD), "athena-fifo-write")
	if writeFile == nil {
		_ = unix.Close(writeFD)
		athenaNativeCloseTolerant(t, readFile, "FIFO reader")
		t.Fatal("wrapping FIFO write descriptor")
	}
	t.Cleanup(func() {
		if closeErr := writeFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("closing FIFO writer: %v", closeErr)
		}
		if closeErr := readFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("closing FIFO reader: %v", closeErr)
		}
	})
	return readFile, writeFile
}

type athenaNativePacketWriteOutcome struct {
	n   int
	err error
}

type athenaNativePacketWriter struct {
	done    chan struct{}
	outcome athenaNativePacketWriteOutcome
}

func athenaNativeStartPacketWriter(file *os.File, data []byte) *athenaNativePacketWriter {
	writer := &athenaNativePacketWriter{done: make(chan struct{})}
	go func() {
		defer close(writer.done)
		writer.outcome.n, writer.outcome.err = file.Write(data)
	}()
	return writer
}

func athenaNativeJoinPacketWriter(t *testing.T, writer *athenaNativePacketWriter, label string) (athenaNativePacketWriteOutcome, bool) {
	t.Helper()
	if writer == nil {
		return athenaNativePacketWriteOutcome{}, true
	}
	if !athenaNativeJoinBounded(t, writer.done, label+" writer") {
		return athenaNativePacketWriteOutcome{}, false
	}
	return writer.outcome, true
}

func TestAthenaNativeControlReaderPacketDrain(t *testing.T) {
	pageSize := os.Getpagesize()
	if pageSize <= 0 || pageSize > 64<<10 {
		t.Fatalf("kernel page size %d outside reviewed 0..64KiB contract", pageSize)
	}
	packetSize := pageSize
	if packetSize < 4096 {
		packetSize = 4096
	}
	if packetSize <= 0 || packetSize > 64<<10 {
		t.Fatalf("packet size %d outside reviewed 0..64KiB contract", packetSize)
	}
	var owned athenaOwnedControlReader
	var packetWriter *athenaNativePacketWriter
	var packetReadDone chan struct{}
	readFile, writeFile := athenaNativeLinuxPacketPipe(t, func(readFile, writeFile *os.File) {
		if owned != nil {
			_ = owned.Cancel()
		}
		readJoined := true
		if packetReadDone != nil {
			readJoined = athenaNativeJoinBounded(t, packetReadDone, "packet native read worker")
		}
		writerJoined := true
		if packetWriter != nil {
			if readJoined {
				athenaNativeCloseTolerant(t, readFile, "packet reader release")
			}
			_, writerJoined = athenaNativeJoinPacketWriter(t, packetWriter, "packet")
		}
		if !readJoined || !writerJoined {
			return
		}
		if owned != nil {
			if closeErr := owned.Close(); closeErr != nil {
				t.Errorf("closing native control reader in cleanup: %v", closeErr)
			}
		}
		athenaNativeCloseTolerant(t, writeFile, "packet writer")
		athenaNativeCloseTolerant(t, readFile, "packet reader")
	})

	writerFlags := athenaNativeFileFlags(t, writeFile)
	if writerFlags&unix.O_DIRECT == 0 {
		t.Fatalf("packet writer flags %#x lack O_DIRECT", writerFlags)
	}
	readerFlagsBefore := athenaNativeFileFlags(t, readFile)
	if readerFlagsBefore&unix.O_DIRECT != 0 {
		t.Fatalf("packet reader flags %#x unexpectedly carry O_DIRECT", readerFlagsBefore)
	}

	owned, err := athenaNewNativeControlReader(context.Background(), readFile)
	if err != nil {
		t.Fatalf("native admission rejected O_DIRECT packet reader: %v", err)
	}
	native, ok := owned.(*athenaNativeControlReader)
	if !ok {
		t.Fatalf("constructor returned %T, want *athenaNativeControlReader", owned)
	}
	var pollCalls int64
	originalPoll := native.poll
	native.poll = func(fds []unix.PollFd, timeout int) (int, error) {
		atomic.AddInt64(&pollCalls, 1)
		return originalPoll(fds, timeout)
	}
	if readerFlagsAfter := athenaNativeFileFlags(t, readFile); readerFlagsAfter != readerFlagsBefore {
		t.Fatalf("packet reader flags changed from %#x to %#x", readerFlagsBefore, readerFlagsAfter)
	}

	first := make([]byte, packetSize)
	for i := range first {
		first[i] = byte((i*37 + 11) % 256)
	}
	first[0] = 0x00
	first[1] = 0xff
	first[packetSize-1] = 0x00

	releaseRead := func() { _ = owned.Cancel() }
	firstOutcome, ok := athenaNativeReadBounded(t, owned, packetSize, 1, 7, releaseRead, func() {
		packetWriter = athenaNativeStartPacketWriter(writeFile, first)
	}, &packetReadDone)
	if !ok {
		t.Fatal("first packet drain did not complete")
	}
	firstWrite, joined := athenaNativeJoinPacketWriter(t, packetWriter, "first packet")
	if !joined {
		t.Fatal("first packet writer did not join")
	}
	if firstWrite.err != nil || firstWrite.n != packetSize {
		t.Fatalf("first packet write returned %d, %v; want %d, nil", firstWrite.n, firstWrite.err, packetSize)
	}
	packetWriter = nil
	if firstOutcome.err != nil || firstOutcome.n != packetSize {
		t.Fatalf("first packet drain returned %d bytes, %v; want %d, nil", firstOutcome.n, firstOutcome.err, packetSize)
	}
	if !bytes.Equal(firstOutcome.data, first) {
		t.Fatalf("first packet drain mismatch: got %d bytes, want %d", len(firstOutcome.data), len(first))
	}
	if calls := atomic.LoadInt64(&pollCalls); calls != 1 {
		t.Fatalf("retained first-packet suffix drained with %d poll calls, want 1", calls)
	}

	second := make([]byte, packetSize)
	for i := range second {
		second[i] = byte((i*53 + 97) % 256)
	}
	second[0] = 0xff
	second[1] = 0x00
	second[packetSize-1] = 0xff
	secondOutcome, ok := athenaNativeReadBounded(t, owned, packetSize, 1, 5, releaseRead, func() {
		packetWriter = athenaNativeStartPacketWriter(writeFile, second)
	}, &packetReadDone)
	if !ok {
		t.Fatal("second packet drain did not complete")
	}
	secondWrite, joined := athenaNativeJoinPacketWriter(t, packetWriter, "second packet")
	if !joined {
		t.Fatal("second packet writer did not join")
	}
	if secondWrite.err != nil || secondWrite.n != packetSize {
		t.Fatalf("second packet write returned %d, %v; want %d, nil", secondWrite.n, secondWrite.err, packetSize)
	}
	packetWriter = nil
	if secondOutcome.err != nil || secondOutcome.n != packetSize {
		t.Fatalf("second packet drain returned %d bytes, %v; want %d, nil", secondOutcome.n, secondOutcome.err, packetSize)
	}
	if !bytes.Equal(secondOutcome.data, second) {
		t.Fatalf("second packet drain mismatch: got %d bytes, want %d", len(secondOutcome.data), len(second))
	}
	if calls := atomic.LoadInt64(&pollCalls); calls != 2 {
		t.Fatalf("after second packet %d poll calls seen, want 2", calls)
	}

	if closeErr := writeFile.Close(); closeErr != nil {
		t.Fatalf("closing packet writer: %v", closeErr)
	}
	eofOutcome, ok := athenaNativeReadEOF(t, owned, func() { _ = owned.Cancel() }, &packetReadDone)
	if !ok {
		t.Fatal("native EOF read did not complete")
	}
	if eofOutcome.n != 0 || eofOutcome.err != io.EOF {
		t.Fatalf("native EOF read returned %d, %v; want 0, io.EOF", eofOutcome.n, eofOutcome.err)
	}
	if closeErr := owned.Close(); closeErr != nil {
		t.Fatalf("closing native control reader: %v", closeErr)
	}
}

func TestAthenaNativeControlReaderOPathDescriptor(t *testing.T) {
	borrowedRead, borrowedWrite := athenaNativeLinuxFIFO(t)
	_ = borrowedWrite
	flagsBefore := athenaNativeFileFlags(t, borrowedRead)

	conn, err := borrowedRead.SyscallConn()
	if err != nil {
		t.Fatalf("getting FIFO SyscallConn: %v", err)
	}
	var oPathFile *os.File
	var openErr error
	ran := false
	controlErr := conn.Control(func(fd uintptr) {
		ran = true
		target := fmt.Sprintf("/proc/self/fd/%d", fd)
		oPathFD, openFDErr := unix.Open(target, unix.O_PATH|unix.O_CLOEXEC, 0)
		if openFDErr != nil {
			openErr = openFDErr
			return
		}
		oPathFile = os.NewFile(uintptr(oPathFD), "athena-o-path")
		if oPathFile == nil {
			_ = unix.Close(oPathFD)
			openErr = fmt.Errorf("wrapping O_PATH descriptor")
		}
	})
	if controlErr != nil {
		t.Fatalf("controlling FIFO descriptor: %v", controlErr)
	}
	if !ran {
		t.Fatal("FIFO descriptor control did not run")
	}
	if openErr != nil {
		t.Fatalf("opening O_PATH descriptor through /proc/self/fd: %v", openErr)
	}
	athenaNativeCleanupFile(t, oPathFile)

	oPathFlagsBefore := athenaNativeFileFlags(t, oPathFile)
	if oPathFlagsBefore&unix.O_PATH == 0 {
		t.Fatalf("O_PATH fixture flags %#x lack unix.O_PATH", oPathFlagsBefore)
	}
	reader, err := athenaNewNativeControlReader(context.Background(), oPathFile)
	if reader != nil || err != errAthenaControl {
		t.Fatalf("O_PATH descriptor constructor returned %v, %v; want nil, errAthenaControl", reader, err)
	}
	if _, statErr := oPathFile.Stat(); statErr != nil {
		t.Fatalf("constructor closed the borrowed O_PATH descriptor: %v", statErr)
	}
	if oPathFlagsAfter := athenaNativeFileFlags(t, oPathFile); oPathFlagsAfter != oPathFlagsBefore {
		t.Fatalf("borrowed O_PATH flags changed from %#x to %#x", oPathFlagsBefore, oPathFlagsAfter)
	}
	if flagsAfter := athenaNativeFileFlags(t, borrowedRead); flagsAfter != flagsBefore {
		t.Fatalf("borrowed FIFO flags changed from %#x to %#x", flagsBefore, flagsAfter)
	}
}
