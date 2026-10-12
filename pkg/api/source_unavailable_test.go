package api

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// An answer names a source by its ID only. Where a route answers that the data file of a source is
// not there, or that a source could not be opened, the sentence is built from the ID of the source;
// the sentence of pkg/dbcopy, which holds the display form of the source (the path of its file, the
// host and database of a PostgreSQL one), goes to the log of the server.

func logOf(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &buf
}

// pgFailure is the text of an open of the PostgreSQL source of the test below that failed for a cause that is told
// apart by nothing, with the hint of a source that no flag and no variable names.
const pgFailure = "the driver could not open the source (its own message is not shown: a driver can quote the connection string); the PostgreSQL connection string is the one the source was given"

func TestSourceUnavailable_BuildsTheSentenceFromTheID(t *testing.T) {
	logged := logOf(t)
	fileRef, err := dbcopy.Parse("sqlite:///private/data/MARKER-path/x.db")
	if err != nil {
		t.Fatal(err)
	}
	pgRef, err := dbcopy.Parse("postgres://alice:pw@MARKER-host.example.com:5433/shop")
	if err != nil {
		t.Fatal(err)
	}
	missing := dbcopy.CheckSourceFile("/private/data/MARKER-path/missing.db")

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a data file that is not there", missing, `the data file of source "chinook" does not exist (run ` + "`datatug demo`" + ` to fetch the demo project's data fixtures)`},
		{"the sentinel alone", fmt.Errorf("wrapped: %w", dbcopy.ErrSourceFileMissing), `the data file of source "chinook" does not exist (run ` + "`datatug demo`" + ` to fetch the demo project's data fixtures)`},
		{"an open failure of a file source", fileRef.OpenFailure(errors.New("unable to open database file")), `source "chinook" could not be opened`},
		// A failure of a PostgreSQL source names no source (the sentence and the hint of where its connection string is read
		// from): it is answered as it is.
		{"an open failure of a PostgreSQL source", pgRef.OpenFailure(errors.New("dial tcp: refused")), pgFailure},
		{"the same under a wrapper", fmt.Errorf("open: %w", fileRef.OpenFailure(errors.New("x"))), `source "chinook" could not be opened`},
		// The sentences that name no source are shown as they are.
		{"the preview is off", dbcopy.ErrPostgresPreview, dbcopy.ErrPostgresPreview.Error()},
		{"policy reads are refused", dbcopy.ErrPostgresPolicyReads, dbcopy.ErrPostgresPolicyReads.Error()},
	} {
		logged.Reset()
		got := SourceUnavailable("chinook", tc.err)
		if got == nil || got.Error() != tc.want {
			t.Errorf("%s: got %v, want %q", tc.name, got, tc.want)
			continue
		}
		if strings.Contains(got.Error(), "MARKER") {
			t.Errorf("%s: the answer holds the path or the host of the source: %q", tc.name, got)
		}
		if !strings.Contains(logged.String(), `source "chinook" is unavailable`) {
			t.Errorf("%s: the log does not say which source: %q", tc.name, logged.String())
		}
	}

	// The text of dbcopy, with the display form, is in the log.
	logged.Reset()
	_ = SourceUnavailable("chinook", missing)
	if !strings.Contains(logged.String(), "MARKER-path/missing.db") {
		t.Errorf("the log does not hold the text of dbcopy: %q", logged.String())
	}
	logged.Reset()
	_ = SourceUnavailable("shop", pgRef.OpenFailure(errors.New("dial tcp: refused")))
	if !strings.Contains(logged.String(), pgFailure) || strings.Contains(logged.String(), "MARKER-host.example.com") || strings.Contains(logged.String(), "alice") {
		t.Errorf("the log holds the sentence of the PostgreSQL failure and nothing of the host, the user or the password: %q", logged.String())
	}
}

func TestSourceUnavailable_NamesAnIDAsItMayBeShown(t *testing.T) {
	logOf(t)
	got := SourceUnavailable("postgres://alice:SECRET-pw@db.example.com/shop", dbcopy.CheckSourceFile("/nonexistent/x.db"))
	if got == nil || strings.Contains(got.Error(), "SECRET") || strings.Contains(got.Error(), "db.example.com") || !strings.Contains(got.Error(), dbcopy.SourceIDNotShown) {
		t.Errorf("a source string where an ID belongs was answered as %v", got)
	}
}

func TestSourceUnavailable_AnythingElseIsNotASourceFailure(t *testing.T) {
	logged := logOf(t)
	for _, err := range []error{nil, errors.New("no such table: Customer"), ErrItemNotFound} {
		if got := SourceUnavailable("chinook", err); got != nil {
			t.Errorf("%v was answered as %v", err, got)
		}
	}
	if logged.Len() != 0 {
		t.Errorf("an error that is no source failure was logged: %q", logged.String())
	}
}

// sourceFailureAnswer is SourceUnavailable for a function that returns err: a failure of the source
// is the answer, and any other error comes back as it is.
func TestSourceFailureAnswer(t *testing.T) {
	logOf(t)
	other := errors.New("no such table: Customer")
	if got := sourceFailureAnswer("chinook", other); got != other {
		t.Errorf("an error that is no source failure came back as %v", got)
	}
	if got := sourceFailureAnswer("chinook", nil); got != nil {
		t.Errorf("nil came back as %v", got)
	}
	var unavailable *SourceUnavailableError
	if got := sourceFailureAnswer("chinook", dbcopy.CheckSourceFile("/nonexistent/x.db")); !errors.As(got, &unavailable) {
		t.Errorf("a missing data file came back as %v", got)
	}
}
