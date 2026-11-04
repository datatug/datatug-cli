//go:build linux || darwin

package commands

import (
	"context"
	"io"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const athenaMaxSignedInt32 = 1<<31 - 1

const (
	athenaControlStagingFloor = 4096
	athenaControlStagingMax   = 64 << 10
)

// athenaNativeControlReader implements the exclusive native profile: a
// read-only FIFO or anonymous pipe with no other reader, no duplicate consumer,
// and no caller Close or flag mutation for its lifetime. The caller retains
// ownership of the input file: this reader never closes, duplicates, changes
// flags or deadlines on the borrowed file.
type athenaNativeControlReader struct {
	fileConn syscall.RawConn

	cancelRead  *os.File
	cancelWrite *os.File
	cancelConn  syscall.RawConn

	staging    []byte
	stagingOff int
	stagingLen int

	poll func(fds []unix.PollFd, timeout int) (int, error)

	mu         sync.Mutex
	closed     bool
	canceled   bool
	activeRead bool
}

func athenaNewNativeControlReader(ctx context.Context, file *os.File) (reader athenaOwnedControlReader, err error) {
	return athenaNewNativeControlReaderWithWakePair(ctx, file, os.Pipe)
}

func athenaNewNativeControlReaderWithWakePair(ctx context.Context, file *os.File, wakePair func() (*os.File, *os.File, error)) (reader athenaOwnedControlReader, err error) {
	if ctx == nil {
		return nil, errAthenaControl
	}
	var cancelRead, cancelWrite *os.File
	closeOwned := func() {
		if cancelRead != nil {
			_ = cancelRead.Close()
			cancelRead = nil
		}
		if cancelWrite != nil {
			_ = cancelWrite.Close()
			cancelWrite = nil
		}
	}
	defer func() {
		if err != nil {
			reader = nil
		}
		if reader == nil {
			closeOwned()
			if ctxErr := athenaFresh(ctx); ctxErr != nil {
				err = ctxErr
			} else {
				err = errAthenaControl
			}
			return
		}
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			closeOwned()
			reader = nil
			if finalErr := athenaFresh(ctx); finalErr != nil {
				err = finalErr
			} else {
				err = ctxErr
			}
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errAthenaControl
	}
	if wakePair == nil {
		return nil, errAthenaControl
	}
	pageSize := os.Getpagesize()
	if pageSize <= 0 {
		return nil, errAthenaControl
	}
	stagingSize := pageSize
	if stagingSize < athenaControlStagingFloor {
		stagingSize = athenaControlStagingFloor
	}
	if stagingSize > athenaControlStagingMax {
		return nil, errAthenaControl
	}
	conn, connErr := file.SyscallConn()
	if connErr != nil {
		return nil, errAthenaControl
	}
	if profileErr := athenaQualifyReadOnlyPipe(ctx, conn); profileErr != nil {
		return nil, profileErr
	}
	staging := make([]byte, stagingSize)
	var pipeErr error
	cancelRead, cancelWrite, pipeErr = wakePair()
	if pipeErr != nil {
		return nil, errAthenaControl
	}
	cancelConn, cancelErr := cancelRead.SyscallConn()
	if cancelErr != nil {
		return nil, errAthenaControl
	}
	reader = &athenaNativeControlReader{
		fileConn:    conn,
		cancelRead:  cancelRead,
		cancelWrite: cancelWrite,
		cancelConn:  cancelConn,
		staging:     staging,
		poll:        unix.Poll,
	}
	return reader, nil
}

func athenaControlConstructorError(ctx context.Context) error {
	if err := athenaFresh(ctx); err != nil {
		return err
	}
	return errAthenaControl
}

func athenaQualifyReadOnlyPipe(ctx context.Context, conn syscall.RawConn) error {
	ran := false
	var qualifyErr error
	if controlErr := conn.Control(func(fd uintptr) {
		ran = true
		if err := athenaFresh(ctx); err != nil {
			qualifyErr = err
			return
		}
		if fd > athenaMaxSignedInt32 {
			qualifyErr = errAthenaControl
			return
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(fd), &stat); err != nil {
			qualifyErr = errAthenaControl
			return
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFIFO {
			qualifyErr = errAthenaControl
			return
		}
		flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
		if err != nil || !athenaReadOnlyControlFlags(flags) {
			qualifyErr = errAthenaControl
			return
		}
	}); controlErr != nil {
		return athenaControlConstructorError(ctx)
	}
	if !ran {
		return athenaControlConstructorError(ctx)
	}
	return qualifyErr
}

func (r *athenaNativeControlReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	if r.closed || r.canceled || r.activeRead {
		r.mu.Unlock()
		return 0, errAthenaControl
	}
	if r.stagingOff < r.stagingLen {
		n := copy(p, r.staging[r.stagingOff:r.stagingLen])
		r.stagingOff += n
		if r.stagingOff == r.stagingLen {
			r.stagingOff = 0
			r.stagingLen = 0
		}
		r.mu.Unlock()
		return n, nil
	}
	r.activeRead = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.activeRead = false
		r.mu.Unlock()
	}()
	return r.readNative(p)
}

func (r *athenaNativeControlReader) readNative(p []byte) (n int, err error) {
	wakeRan := false
	controlErr := r.cancelConn.Control(func(wakeFD uintptr) {
		wakeRan = true
		if wakeFD > athenaMaxSignedInt32 {
			err = errAthenaControl
			return
		}
		inputRan := false
		inputControlErr := r.fileConn.Control(func(inputFD uintptr) {
			inputRan = true
			if inputFD > athenaMaxSignedInt32 {
				err = errAthenaControl
				return
			}
			n, err = r.readPinned(p, int32(inputFD), int32(wakeFD))
		})
		if err == nil && (!inputRan || inputControlErr != nil) {
			n = 0
			err = errAthenaControl
		}
	})
	if !wakeRan || controlErr != nil {
		return 0, errAthenaControl
	}
	if err != nil {
		if err == io.EOF {
			return 0, io.EOF
		}
		return 0, errAthenaControl
	}
	return n, nil
}

func (r *athenaNativeControlReader) readPinned(p []byte, inputFD, wakeFD int32) (int, error) {
	for {
		if r.cancelOrClosed() {
			return 0, errAthenaControl
		}
		fds := []unix.PollFd{
			{Fd: inputFD, Events: unix.POLLIN},
			{Fd: wakeFD, Events: unix.POLLIN},
		}
		_, pollErr := r.poll(fds, -1)
		if pollErr != nil {
			if pollErr == unix.EINTR {
				if r.cancelOrClosed() {
					return 0, errAthenaControl
				}
				continue
			}
			return 0, errAthenaControl
		}
		if fds[1].Revents != 0 {
			return 0, errAthenaControl
		}
		if r.cancelOrClosed() {
			return 0, errAthenaControl
		}
		revents := fds[0].Revents
		if revents&(unix.POLLNVAL|unix.POLLERR) != 0 {
			return 0, errAthenaControl
		}
		if revents&(unix.POLLIN|unix.POLLHUP) == 0 {
			continue
		}
		var readN int
		var readErr error
		for {
			if r.cancelOrClosed() {
				return 0, errAthenaControl
			}
			readN, readErr = unix.Read(int(inputFD), r.staging)
			if readErr != unix.EINTR {
				break
			}
		}
		if readErr == unix.EAGAIN {
			continue
		}
		if readErr != nil {
			return 0, errAthenaControl
		}
		if readN == 0 {
			return 0, io.EOF
		}
		if readN < 0 || readN > len(r.staging) {
			return 0, errAthenaControl
		}
		n := copy(p, r.staging[:readN])
		if n < readN {
			r.stagingOff = n
			r.stagingLen = readN
		} else {
			r.stagingOff = 0
			r.stagingLen = 0
		}
		return n, nil
	}
}

func (r *athenaNativeControlReader) cancelOrClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canceled || r.closed
}

// Cancel signals cancellation by writing at most one byte to the reader's own
// wake pipe. It is a signal, not a join: false means the signal was not newly
// accepted, not that an active Read has finished or released resources.
func (r *athenaNativeControlReader) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.canceled {
		return false
	}
	r.canceled = true
	n, err := r.cancelWrite.Write([]byte{0})
	return err == nil && n == 1
}

func (r *athenaNativeControlReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if r.activeRead {
		return errAthenaControl
	}
	r.closed = true
	readErr := r.cancelRead.Close()
	writeErr := r.cancelWrite.Close()
	if readErr != nil || writeErr != nil {
		return errAthenaControl
	}
	return nil
}
