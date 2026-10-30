package api

import (
	"errors"
	"fmt"
	"log"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// SourceUnavailableError is the answer of a serve route for a source that could not be used: its
// data file is not there, it could not be opened, it is refused, or its connection was lost. Its
// text is a sentence built from the ID of the source for a file source, and the fixed sentence of
// the failure for a PostgreSQL one (see SourceUnavailable), so that no answer holds the path of a
// data file or the host of a database. A route answers it as SOURCE_UNAVAILABLE.
type SourceUnavailableError struct{ message string }

// Error returns the sentence.
func (e *SourceUnavailableError) Error() string { return e.message }

// sourceFileMissingHint says how to get the data file of the demo project.
const sourceFileMissingHint = "run `datatug demo` to fetch the demo project's data fixtures"

// SourceUnavailable turns err, the error of using the source whose ID is sourceID, into the answer
// of a route, or returns nil when err is not a failure of the source itself (a missing file, an open
// failure, a refusal, a lost connection: see dbcopy.UnavailableSource).
//
// An answer names a source by its ID only. The sentence of a missing data file and the sentence of an
// open failure of a file source are built here from the ID, as it may be shown (dbcopy.SourceIDDisplay:
// a client may send a whole source string where an ID belongs). The text of pkg/dbcopy, which holds the
// display form of the source (the path of its file), is logged. The sentences of pkg/dbcopy that name no
// source are answered as they are, and logged as they are: the failure of a PostgreSQL source (the
// adapter's sentence and where its connection string is read from, the variable: no host, port or
// database), the preview of PostgreSQL sources being off, a read through policies, a URL that turns
// read-only off, a lost connection. The CLI keeps all of the sentences of pkg/dbcopy: a person at their
// own terminal sees their own paths.
func SourceUnavailable(sourceID string, err error) *SourceUnavailableError {
	if err == nil {
		return nil
	}
	missing := errors.Is(err, dbcopy.ErrSourceFileMissing)
	cause := dbcopy.UnavailableSource(err)
	if cause == nil {
		if !missing {
			return nil
		}
		cause = err
	}
	shown := dbcopy.SourceIDDisplay(sourceID)
	log.Printf("api: source %q is unavailable: %s", shown, dbcopy.RedactText(cause.Error()))
	switch {
	case missing:
		return &SourceUnavailableError{fmt.Sprintf("the data file of source %q does not exist (%s)", shown, sourceFileMissingHint)}
	case dbcopy.UnavailableSourceShowsSource(cause):
		return &SourceUnavailableError{fmt.Sprintf("source %q could not be opened", shown)}
	}
	return &SourceUnavailableError{cause.Error()}
}

// sourceFailureAnswer is SourceUnavailable for a function that returns err when it is not a failure of
// the source.
func sourceFailureAnswer(sourceID string, err error) error {
	if answer := SourceUnavailable(sourceID, err); answer != nil {
		return answer
	}
	return err
}
