package dbcopy

import (
	"context"
	"fmt"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The preview switch is one environment variable, and only the value "1" turns it on.
func TestCheckPostgresPreview_OnlyOneTurnsItOn(t *testing.T) {
	for value, wantOff := range map[string]bool{
		"1": false, "": true, "0": true, "true": true, "yes": true, "on": true, " 1": true, "1 ": true, "01": true, "2": true,
	} {
		t.Setenv(PostgresPreviewEnv, value)
		err := CheckPostgresPreview()
		if wantOff {
			assert.ErrorIs(t, err, ErrPostgresPreview, "value %q", value)
		} else {
			assert.NoError(t, err, "value %q", value)
		}
	}
}

func TestCheckPostgresPreview_AVariableThatIsNotSetIsOff(t *testing.T) {
	// t.Setenv registers the restore; the variable is then removed for the rest of the test.
	t.Setenv(PostgresPreviewEnv, "1")
	require.NoError(t, osUnsetenv(PostgresPreviewEnv))
	assert.ErrorIs(t, CheckPostgresPreview(), ErrPostgresPreview)
}

// The sentence says the sources are a preview, names the variable, and holds nothing else.
func TestErrPostgresPreview_SaysItIsAPreviewAndNamesTheVariable(t *testing.T) {
	assert.Equal(t, "PostgreSQL sources are a preview and are switched off: set DATATUG_PREVIEW_POSTGRES=1 to use them", ErrPostgresPreview.Error())
	assert.Equal(t, "policy-enforced reads on PostgreSQL sources are not available in this preview", ErrPostgresPolicyReads.Error())
}

// Every path that would open a PostgreSQL source asks the switch first: with it off, the opener is
// never called, nothing of the URL is parsed further, and the answer is the one fixed sentence.
func TestOpen_APostgresSourceWithThePreviewOffNeverCallsTheOpener(t *testing.T) {
	t.Setenv(PostgresPreviewEnv, "")
	stubPostgresOpener(t, func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		t.Fatal("the opener must not be called while the preview is off")
		return nil, nil
	})
	ref, err := Parse("postgres://alice:" + markerPassword + "@db.example.com/shop?application_name=" + markerQuery)
	require.NoError(t, err)

	for name, open := range map[string]func(context.Context) (dal.DB, error){
		"Open":          ref.Open,
		"OpenProtected": ref.OpenProtected,
		"OpenForWrite":  ref.OpenForWrite,
	} {
		db, openErr := open(context.Background())
		assert.Nil(t, db, name)
		require.Error(t, openErr, name)
		assert.Equal(t, ErrPostgresPreview.Error(), openErr.Error(), name)
		assert.Same(t, ErrPostgresPreview, openErr, name+": the fixed error itself, so a caller can tell it from a failure")
		assertNoMarkers(t, name, openErr)
	}
}

func TestCheckPostgresRead_RefusesAPostgresSourceWithPolicies(t *testing.T) {
	postgres := BackendRef{Scheme: "postgres", Raw: "postgres://db.example.com/shop", Path: "postgres://alice:" + markerPassword + "@db.example.com/shop"}
	sqlite := BackendRef{Scheme: "sqlite", Raw: "sqlite:///x.db", Path: "/x.db"}

	t.Setenv(PostgresPreviewEnv, "1")
	assert.ErrorIs(t, CheckPostgresRead(postgres, 1), ErrPostgresPolicyReads)
	assert.ErrorIs(t, CheckPostgresRead(postgres, 3), ErrPostgresPolicyReads)
	assert.NoError(t, CheckPostgresRead(postgres, 0), "no policy is no policy-enforced read")
	assert.NoError(t, CheckPostgresRead(sqlite, 2), "only a PostgreSQL source is refused")
	assert.NoError(t, CheckPostgresRead(BackendRef{Scheme: "ingitdb"}, 2))
}

// The preview answer comes first: with the switch off, the person is told about the switch, not
// about a refusal that setting the switch would not lift.
func TestCheckPostgresRead_TheSwitchIsAskedFirst(t *testing.T) {
	postgres := BackendRef{Scheme: "postgres", Raw: "postgres://db.example.com/shop", Path: "postgres://db.example.com/shop"}
	t.Setenv(PostgresPreviewEnv, "")
	assert.ErrorIs(t, CheckPostgresRead(postgres, 1), ErrPostgresPreview)
	assert.ErrorIs(t, CheckPostgresRead(postgres, 0), ErrPostgresPreview, "even without a policy: the source cannot be opened at all")
}

// OpenFailure passes the fixed refusals through unchanged: they were built from fixed sentences,
// and wrapping them in "open postgres source ...: the driver could not open the source" would
// bury the one thing the person has to do.
func TestOpenFailure_PassesTheFixedRefusalsThrough(t *testing.T) {
	ref := BackendRef{Scheme: "postgres", Raw: "env:DATATUG_SHOP_PG_URL", Path: "postgres://alice:" + markerPassword + "@db.example.com/shop"}
	for _, refusal := range []error{ErrPostgresPreview, ErrPostgresPolicyReads, errPostgresReadOnlyOff} {
		assert.Same(t, refusal, ref.OpenFailure(refusal))
	}
	wrapped := fmt.Errorf("driver text with %s: %w", markerPassword, ErrPostgresPreview)
	got := ref.OpenFailure(wrapped)
	assert.NotSame(t, ErrPostgresPreview, got, "a driver that wrapped a refusal's text does not make its own text safe")
	assert.NotContains(t, got.Error(), markerPassword, "OpenFailure's message is a fixed sentence (the chain is kept for errors.Is, by design)")
	assert.ErrorIs(t, got, ErrPostgresPreview)
}
