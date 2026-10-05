package dbcopy

import "errors"

// UnavailableSourceShowsSource reports whether the text of err, an error that UnavailableSource
// found, holds the display form of the source: the path of the data file of a file source, or the
// scheme, the host, the port and the database of a PostgreSQL one. That is the text of the error
// of a source file that is not there and the text of an open failure (BackendRef.OpenFailure).
// The fixed refusals and the lost connection of a PostgreSQL source name no source, and are
// reported false.
//
// A server that answers a client builds its own sentence from the ID of the source for the
// errors that report true, and logs the text of the error; the CLI, whose person sits at their own
// terminal, shows the text as it is.
func UnavailableSourceShowsSource(err error) bool {
	var (
		failed  *openError
		missing *missingSourceFileError
	)
	return errors.As(err, &failed) || errors.As(err, &missing)
}
