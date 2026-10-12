package dbcopy

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dal-go/dalgo2postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UnavailableSource finds the error of this package that says why a source cannot be opened or read, so that a
// route can answer "source unavailable" with its fixed sentence, and hands back that error, not the one that wrapped
// it: a wrapper can add text of its own, and a driver can wrap one of these errors around its own text.
func TestUnavailableSource(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://alice:pw@db.example.com/shop")
	require.NoError(t, err)
	classified := ref.OpenFailure(&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork})
	missing := CheckSourceFile("/nonexistent/dir/x.db")
	require.Error(t, missing)
	lost := &postgresConnectionError{sqlState: "28P01"}

	for name, want := range map[string]error{
		"the preview is off":                 ErrPostgresPreview,
		"policy reads are refused":           ErrPostgresPolicyReads,
		"a URL that turns read-only off":     errPostgresReadOnlyOff,
		"a service file in the URL":          errPostgresServiceFile,
		"a classified open failure":          classified,
		"a source file that is not there":    missing,
		"a connection that could not return": lost,
	} {
		assert.Same(t, want, UnavailableSource(want), name)
		wrapper := fmt.Errorf("a driver wrote this, with its password=hunter2: %w", want)
		found := UnavailableSource(wrapper)
		assert.Same(t, want, found, name+": found under a wrapper")
		assert.NotContains(t, found.Error(), "hunter2", name+": and the text is the error's own, not the wrapper's")
		assert.Same(t, want, UnavailableSource(errors.Join(errors.New("another"), wrapper)), name+": found in a joined error")
	}
	assert.Nil(t, UnavailableSource(nil))
	assert.Nil(t, UnavailableSource(errors.New("anything else")))
	assert.Nil(t, UnavailableSource(errUnsupportedBackend))
}
