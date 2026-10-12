package dbcopy

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dal-go/dalgo2postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two of the errors UnavailableSource finds hold the display form of the source in their text (the
// path of a file source, the host and the database of a URL): the missing file and the open failure
// of a source that is not a PostgreSQL one. The others are fixed sentences that name no source, the
// failures of a PostgreSQL source among them (the sentence of the adapter and the hint of where the
// connection string is read from). A server that answers a client builds its own sentence from the
// source's ID for the first two and may show the others as they are.
func TestUnavailableSourceShowsSource(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://alice:pw@db.example.com/shop")
	require.NoError(t, err)
	fileRef, err := Parse("sqlite:///some/where/x.db")
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"an open failure of a PostgreSQL source": {ref.OpenFailure(&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}), false},
		"an open failure of a file source":       {fileRef.OpenFailure(errors.New("unable to open database file")), true},
		"a source file that is not there":        {CheckSourceFile("/nonexistent/dir/x.db"), true},
		"the preview is off":                     {ErrPostgresPreview, false},
		"policy reads are refused":               {ErrPostgresPolicyReads, false},
		"a URL that turns read-only off":         {errPostgresReadOnlyOff, false},
		"a service file in the URL":              {errPostgresServiceFile, false},
		"a connection that could not return":     {&postgresConnectionError{sentence: "the server could not be reached", sqlState: "28P01"}, false},
		"an error of no source":                  {errors.New("anything else"), false},
		"no error":                               {nil, false},
	} {
		assert.Equal(t, tc.want, UnavailableSourceShowsSource(tc.err), name)
		if tc.err != nil {
			assert.Equal(t, tc.want, UnavailableSourceShowsSource(fmt.Errorf("a wrapper: %w", tc.err)), name+": under a wrapper")
		}
	}
}
