package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"syscall"

	"github.com/dal-go/dalgo2postgres"
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
// permission, a timeout, a cancelled attempt, a refused connection, and for PostgreSQL a rejected
// password, a connection the server's access rules do not allow, a missing database, an
// unreachable server, a failed TLS handshake), why.
// errors.Is and errors.As still see the driver's own error.
//
// The errors this package wrote itself (the missing-file error CheckSourceFile
// returns, an error OpenFailure returned before, the fixed refusals such as
// ErrPostgresPreview) carry text built from the display form of the source or from
// no source at all, and pass through unchanged. They are recognised by what they are, never
// by errors.Is: a driver can wrap one of them around its own text. A nil err
// stays nil.
func (r BackendRef) OpenFailure(err error) error {
	if err == nil {
		return nil
	}
	switch err.(type) { //nolint:errorlint // an exact match is the point: see the doc comment
	case *missingSourceFileError, *openError, *refusedError:
		return err
	}
	if err == errUnsupportedBackend { //nolint:errorlint // an exact match is the point: see the doc comment
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
	if reason, ok := connectionErrorReason(err); ok {
		return reason
	}
	return "the driver could not open the source (its own message is not shown: a driver can quote the connection string)"
}

// connectionErrorReason names the failure a *dalgo2postgres.ConnectionError reports, from its
// Kind and SQLState only. The adapter hides the driver's error from errors.Is and errors.As
// on purpose, so the cases above never see a PostgreSQL failure; and the error's own text names
// the host, the port and the database, so it is never printed. A kind or code it does not know
// is not named.
func connectionErrorReason(err error) (string, bool) {
	var pg *dalgo2postgres.ConnectionError
	if !errors.As(err, &pg) {
		return "", false
	}
	switch {
	case pg.Kind == dalgo2postgres.FailureServer && pg.SQLState == "28P01":
		return "the server rejected the user or the password", true
	case pg.Kind == dalgo2postgres.FailureServer && pg.SQLState == "28000":
		// The server refuses this user on this connection before it asks for a password: by the
		// user, the database, the address or the encryption of the connection (its access rules).
		return "the server does not authorize this user for this connection (its access rules: user, database, address or encryption)", true
	case pg.Kind == dalgo2postgres.FailureServer && pg.SQLState == "3D000":
		return "the database does not exist", true
	case pg.Kind == dalgo2postgres.FailureNetwork:
		return "the server could not be reached", true
	case pg.Kind == dalgo2postgres.FailureTimeout:
		return "the attempt timed out", true
	case pg.Kind == dalgo2postgres.FailureTLS:
		return "the TLS handshake with the server failed", true
	}
	return "", false
}
