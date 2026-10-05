package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dal-go/dalgo2postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseEnvRef is the source "env:NAME" resolved to markedPostgresURL.
func parseEnvRef(t *testing.T, name string) BackendRef {
	t.Helper()
	ref, err := ParseWithEnv("env:"+name, fakeEnv(map[string]string{name: markedPostgresURL}))
	require.NoError(t, err)
	return ref
}

// A person sees the sentence the adapter chose for a failure and never the host, the port or the database it may
// name: those are left out, and so are the step that failed and the prefix of the package.
func TestAdapterSentence(t *testing.T) {
	t.Parallel()
	_, realErr := dalgo2postgres.NewDatabase("postgres://u:p@h:notaport/db")
	var real *dalgo2postgres.ConnectionError
	require.ErrorAs(t, realErr, &real, "an error the adapter makes itself, for a string it cannot parse: no dial")
	require.Contains(t, real.Error(), "PingContext", "and it names the step that failed (else the step is not covered)")

	for name, tc := range map[string]struct {
		err  *dalgo2postgres.ConnectionError
		want string
	}{
		"a network failure that names its parts": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433", Database: "shop"}, "the server could not be reached"},
		"a rejected password":                    {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01", Host: "db.example.com"}, "the server refused the connection: password authentication failed (SQLSTATE 28P01)"},
		"a failure of no known kind":             {&dalgo2postgres.ConnectionError{}, "the connection failed"},
		"a string that cannot be parsed":         {real, "the connection string cannot be parsed, or a file or service it names cannot be read"},
	} {
		got := adapterSentence(tc.err)
		assert.Equal(t, tc.want, got, name)
		assert.NotContains(t, got, "db.example.com", name)
		assert.NotContains(t, got, "5433", name)
		assert.NotContains(t, got, "shop", name)
	}
	original := &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com"}
	_ = adapterSentence(original)
	assert.Equal(t, "db.example.com", original.Host, "the error it was given is left as it was")
}

// The hint says where the connection string is read from, by the name of the variable or of the flag, never by its value.
func TestConnectionHint(t *testing.T) {
	t.Parallel()
	const lead = "the PostgreSQL connection string is read from "
	env := parseEnvRef(t, "DATATUG_SHOP_PG_URL")
	assert.Equal(t, lead+"the environment variable DATATUG_SHOP_PG_URL", env.connectionHint())
	assert.Equal(t, lead+"the environment variable DATATUG_SHOP_PG_URL", env.WithFlag("--db").connectionHint(), "a variable wins: that is where it is read from")

	literal, err := Parse(markedPostgresURL)
	require.NoError(t, err)
	assert.Equal(t, lead+"the --db flag", literal.WithFlag("--db").connectionHint())
	assert.Equal(t, lead+"the --to flag", literal.WithFlag("--to").connectionHint())
	assert.Equal(t, "the PostgreSQL connection string is the one the source was given", literal.connectionHint(), "no flag is known")
	assert.Empty(t, literal.flag, "WithFlag returns a copy: the source it was called on is as it was")
}

// Whatever fails to open a PostgreSQL source, the person reads one sentence and the hint, and never the host, the
// port, the database, the user, the password or the text of a driver. The source is not named by its display form:
// that holds the host.
func TestOpenFailure_APostgresSourceShowsTheAdaptersSentenceAndTheHint(t *testing.T) {
	t.Parallel()
	env := parseEnvRef(t, "DATATUG_SHOP_PG_URL")
	literal, err := Parse(markedPostgresURL)
	require.NoError(t, err)
	literal = literal.WithFlag("--db")
	const envHint = "the PostgreSQL connection string is read from the environment variable DATATUG_SHOP_PG_URL"
	const flagHint = "the PostgreSQL connection string is read from the --db flag"
	hostile := fmt.Errorf("dial %q: password=%s user=%s", markedPostgresURL, markerPassword, markerUser)

	for name, tc := range map[string]struct {
		ref   BackendRef
		cause error
		want  string
	}{
		"a server that is down": {env, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433", Database: "shop"},
			"the server could not be reached; " + envHint},
		"a rejected password": {literal, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01"},
			"the server refused the connection: password authentication failed (SQLSTATE 28P01); " + flagHint},
		"wrapped by the caller": {env, fmt.Errorf("opening: %w", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTLS}),
			"the TLS handshake with the server failed; " + envHint},
		"a context that ended": {env, context.Canceled, "the attempt was cancelled; " + envHint},
		"text a driver wrote":  {literal, hostile, openedFailureReason + "; " + flagHint},
	} {
		got := tc.ref.OpenFailure(tc.cause)
		require.Error(t, got, name)
		assert.Equal(t, tc.want, got.Error(), name)
		// The cause stays in the chain for errors.Is and errors.As (below) and is never printed: the text is checked.
		for _, shown := range []string{got.Error(), fmt.Sprintf("%v", got), fmt.Sprintf("%s", got)} {
			for _, secret := range []string{markerPassword, markerUser, markerQuery, "db.example.com", "5433", "shop"} {
				assert.NotContains(t, shown, secret, name)
			}
		}
		assert.ErrorIs(t, got, tc.cause, name+": the cause is still reachable by errors.Is")
		assert.False(t, UnavailableSourceShowsSource(got), name+": the text names no source, so a route may answer it as it is")
		assert.Same(t, got, tc.ref.OpenFailure(got), name+": classifying twice changes nothing")
	}
}

// The text of an open that failed and the text of a call that failed later are the same sentence with the same
// hint: one behaviour, whichever call finds the server gone.
func TestAFailureHasOneTextWhenItHappensAtTheOpenAndWhenItHappensLater(t *testing.T) {
	t.Parallel()
	env := parseEnvRef(t, "DATATUG_SHOP_PG_URL")
	for _, cause := range []*dalgo2postgres.ConnectionError{
		{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com"},
		{Kind: dalgo2postgres.FailureServer, SQLState: "28P01"},
		{Kind: dalgo2postgres.FailureTimeout},
	} {
		opened := env.OpenFailure(cause)
		later := guardPostgresError(env.connectionHint(), cause)
		require.Error(t, later)
		assert.Equal(t, opened.Error(), later.Error())
	}
}

// A call that fails once the source is open answers the adapter's sentence with the hint of the source it was opened
// from, holds nothing of the cause, and is found by UnavailableSource so that a route answers it as unavailable.
func TestGuardPostgresError_AnAdaptersConnectionErrorKeepsItsSentenceAndGetsTheHint(t *testing.T) {
	t.Parallel()
	const hint = "the PostgreSQL connection string is read from the --db flag"
	plain := errors.New("an error of DataTug's or DALgo's own")
	assert.NoError(t, guardPostgresError(hint, nil))
	assert.Same(t, plain, guardPostgresError(hint, plain))

	for name, tc := range map[string]struct {
		cause     error
		wantText  string
		wantState string
		timedOut  bool
		canceled  bool
	}{
		"a lost connection": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureOther}, "the connection failed; " + hint, "", false, false},
		"a server that is down, with parts": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433", Database: "shop"},
			"the server could not be reached; " + hint, "", false, false},
		"a rejected password": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01"},
			"the server refused the connection: password authentication failed (SQLSTATE 28P01); " + hint, "28P01", false, false},
		// A state of the wrong shape is not shown: the adapter never makes one, and this holds the day it does.
		"a state that is not a state": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "role " + markerUser},
			"the server rejected the connection; " + hint, "", false, false},
		"wrapped by the caller": {fmt.Errorf("list collections: %w", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}),
			"the server could not be reached; " + hint, "", false, false},
		"joined with another error": {errors.Join(plain, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}),
			"the server could not be reached; " + hint, "", false, false},
	} {
		got := guardPostgresError(hint, tc.cause)
		require.Error(t, got, name)
		assert.Equal(t, tc.wantText, got.Error(), name)
		assertNoMarkers(t, name, got)
		var lost *postgresConnectionError
		require.ErrorAs(t, got, &lost, name)
		assert.Equal(t, tc.wantState, lost.SQLState(), name)
		assert.Same(t, lost, UnavailableSource(got), name)
		assert.Nil(t, errors.Unwrap(got), name+": nothing of the cause is left in the chain")
		assert.NotErrorIs(t, got, plain, name)
	}

	timedOut := guardPostgresError(hint, fmt.Errorf("x: %w", contextAdapterError(context.DeadlineExceeded)))
	assert.ErrorIs(t, timedOut, context.DeadlineExceeded, "a deadline stays a deadline")
	assert.NotErrorIs(t, timedOut, context.Canceled)
	canceled := guardPostgresError(hint, contextAdapterError(context.Canceled))
	assert.ErrorIs(t, canceled, context.Canceled, "a cancellation stays one")
}

// contextAdapterError is an adapter's connection error that reports a context error of its attempt, as the adapter
// makes it for an attempt that ended with its context: wrapped in an error whose Is says so.
func contextAdapterError(cause error) error {
	return errors.Join(&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTimeout}, cause)
}

// A command that exits with the code of "failed to connect to the database" asks whether an error is the failure of a
// PostgreSQL source: an open that failed, or a call that lost its connection. The refusals of this package, the failures
// of other sources and any other error are not.
func TestIsPostgresConnectionFailure(t *testing.T) {
	t.Parallel()
	env := parseEnvRef(t, "DATATUG_SHOP_PG_URL")
	file, err := Parse("sqlite:///some/where/x.db")
	require.NoError(t, err)
	lost := guardPostgresError(env.connectionHint(), &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork})
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"an open that failed":        {env.OpenFailure(&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}), true},
		"under a wrapper":            {fmt.Errorf("failed to open PostgreSQL: %w", env.OpenFailure(errors.New("x"))), true},
		"a connection that was lost": {lost, true},
		"a lost connection, wrapped": {fmt.Errorf("read: %w", lost), true},
		"an open of a file source":   {file.OpenFailure(errors.New("unable to open database file")), false},
		"the preview is off":         {ErrPostgresPreview, false},
		"a statement's error":        {errors.New("relation does not exist"), false},
		"no error":                   {nil, false},
	} {
		assert.Equal(t, tc.want, IsPostgresConnectionFailure(tc.err), name)
	}
}
