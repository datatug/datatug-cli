package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"syscall"
)

// errUnsupportedBackend is what open returns for a BackendRef whose scheme Parse
// would have refused: a bug in the caller, never user input.
var errUnsupportedBackend = errors.New("unsupported scheme (this is a bug; Parse should have caught it)")

// openError is the error OpenFailure returns: a fixed sentence built from the
// display form of the source, with the driver's own error kept for errors.Is and
// errors.As but never printed.
type openError struct {
	message string
	cause   error
}

func (e *openError) Error() string { return e.message }
func (e *openError) Unwrap() error { return e.cause }

// OpenFailure turns err, what a driver returned when it opened this source, into
// an error that is safe to show. A driver formats the connection string it was
// given into its open and ping errors in whatever shape it likes (dalgo2postgres
// quotes the whole DSN), so a driver's message is never shown, whatever it holds:
// the error says which source could not be opened and, for the few causes that
// can be told apart without reading the message (a missing file, a refused
// permission, a timeout, a cancelled attempt, a refused connection), why.
// errors.Is and errors.As still see the driver's own error.
//
// The errors this package wrote itself (the missing-file error CheckSourceFile
// returns, an error OpenFailure returned before, ErrPostgresNotWired) carry text
// built from the display form of the source and pass through unchanged. They are recognised by what they are, never
// by errors.Is: a driver can wrap one of them around its own text. A nil err
// stays nil.
func (r BackendRef) OpenFailure(err error) error {
	if err == nil {
		return nil
	}
	switch err.(type) { //nolint:errorlint // an exact match is the point: see the doc comment
	case *missingSourceFileError, *openError:
		return err
	}
	if err == ErrPostgresNotWired || err == errUnsupportedBackend { //nolint:errorlint // an exact match is the point: see the doc comment
		return err
	}
	return &openError{
		message: fmt.Sprintf("open %s source %q: %s", r.Scheme, r.Display(), openFailureReason(err)),
		cause:   err,
	}
}

// openFailureReason says why err happened in a fixed sentence, or says only that
// the driver could not open the source.
func openFailureReason(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "the file or directory does not exist"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "the attempt timed out"
	case errors.Is(err, context.Canceled):
		return "the attempt was cancelled"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "the connection was refused"
	}
	return "the driver could not open the source (its own message is not shown: a driver can quote the connection string)"
}
