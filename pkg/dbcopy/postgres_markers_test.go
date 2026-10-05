package dbcopy

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/stretchr/testify/assert"
)

// The three parts of a source that must never be shown: a marker in the password, one in the
// user name and one in a query parameter. A test puts them in a URL and then looks for them in
// everything the code under test produced.
const (
	markerPassword = "PWMARKER-dt02"
	markerUser     = "USERMARKER-dt02"
	markerQuery    = "QUERYMARKER-dt02"
)

// markedPostgresURL is a postgres:// URL that carries all three markers.
const markedPostgresURL = "postgres://" + markerUser + ":" + markerPassword + "@db.example.com:5433/shop?application_name=" + markerQuery + "&sslmode=disable"

// assertNoMarkers fails when any of the markers appears in what an error shows: its text, its
// %+v and %#v forms, and the text of every error in its Unwrap chain (and of every branch of a
// joined error).
func assertNoMarkers(t *testing.T, name string, errs ...error) {
	t.Helper()
	for _, err := range errs {
		if err == nil {
			continue
		}
		for _, shown := range errorForms(err) {
			for _, marker := range []string{markerPassword, markerUser, markerQuery} {
				assert.NotContains(t, shown, marker, "%s: an error shows %s", name, marker)
			}
		}
	}
}

// errorForms lists everything printing err can show: the verbs, and each error of its chain.
func errorForms(err error) []string {
	forms := []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprintf("%s", err)}
	switch wrapped := err.(type) { //nolint:errorlint // walking the chain by hand is the point
	case interface{ Unwrap() error }:
		if inner := wrapped.Unwrap(); inner != nil {
			forms = append(forms, errorForms(inner)...)
		}
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			forms = append(forms, errorForms(inner)...)
		}
	}
	return forms
}

// TestErrorForms_ReadsTheWholeChain makes sure that assertNoMarkers would catch a marker that sits
// at the bottom of a chain, so that the other tests that use it are not vacuous.
func TestErrorForms_ReadsTheWholeChain(t *testing.T) {
	deep := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", errors.New("inner "+markerUser)))
	joined := errors.Join(errors.New("first"), fmt.Errorf("second: %w", errors.New(markerQuery)))
	var found int
	for _, err := range []error{deep, joined} {
		for _, shown := range errorForms(err) {
			if strings.Contains(shown, markerUser) || strings.Contains(shown, markerQuery) {
				found++
			}
		}
	}
	assert.Greater(t, found, 2, "a marker below the first wrapper is found")
}

// resetPostgresHandles forgets every handle the process cache holds, so a test starts from none.
func resetPostgresHandles() { postgresHandles = &handleCache{} }

// osUnsetenv removes a variable for the rest of the test; the test restores it with t.Setenv first.
func osUnsetenv(name string) error { return os.Unsetenv(name) }

// stubPostgresOpener replaces the constructor of a PostgreSQL database for the test.
func stubPostgresOpener(t *testing.T, stub postgresOpener) {
	t.Helper()
	original := newPostgresDatabaseWithOptions
	newPostgresDatabaseWithOptions = stub
	resetPostgresHandles()
	t.Cleanup(func() {
		newPostgresDatabaseWithOptions = original
		resetPostgresHandles()
	})
}

// fakeOpener records every open and answers with a database that holds nothing.
type fakeOpener struct {
	dsns    []string
	schemas []dal.Schema
	options []dalgo2sql.DbOptions
	extras  [][]dalgo2postgres.Option
	err     error
}

func (f *fakeOpener) open(dsn string, schema dal.Schema, opts dalgo2sql.DbOptions, extras ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
	f.dsns = append(f.dsns, dsn)
	f.schemas = append(f.schemas, schema)
	f.options = append(f.options, opts)
	f.extras = append(f.extras, extras)
	if f.err != nil {
		return nil, f.err
	}
	return &dalgo2postgres.Database{}, nil
}
