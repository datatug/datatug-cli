package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// previewOn turns the preview switch on for one test.
func previewOn(t *testing.T) { t.Helper(); t.Setenv(PostgresPreviewEnv, "1") }

func parseMarked(t *testing.T) BackendRef {
	t.Helper()
	ref, err := Parse(markedPostgresURL)
	require.NoError(t, err)
	return ref
}

// What reaches the adapter: the URL with the session defaults, an empty schema and no recordset (a
// source is keyed by the primary key the adapter reads from the catalog), no dalgo2sql option of
// DataTug's own, and one adapter option, the exact identifier mode.
func TestOpen_APostgresSourceReachesTheAdapterWithTheSessionDefaults(t *testing.T) {
	previewOn(t)
	fake := &fakeOpener{}
	stubPostgresOpener(t, fake.open)
	ref := parseMarked(t)

	db, err := ref.OpenProtected(context.Background())
	require.NoError(t, err)
	require.NotNil(t, db)

	require.Len(t, fake.dsns, 1)
	want, wantErr := postgresConnectionString(ref.Path, false)
	require.NoError(t, wantErr)
	assert.Equal(t, want, fake.dsns[0])
	// The URL names the application, so that default is not added; the rest are.
	assert.Equal(t, markedPostgresURL+"&default_transaction_read_only=on&statement_timeout=30000&timezone=UTC&connect_timeout=10", fake.dsns[0], "what was typed, then the defaults")
	assert.Equal(t, dal.NewSchema(nil, nil), fake.schemas[0], "no schema is declared: records are keyed by the key the adapter reads")
	assert.Equal(t, dalgo2sql.DbOptions{}, fake.options[0], "no recordset is declared, as for SQLite")
	assert.Len(t, fake.extras[0], 1, "exactly one adapter option: the exact identifier mode (names are used as the scan stored them)")
}

// The same four parameters reach the adapter for every read, and a copy target is the one open that
// is not read-only.
func TestOpen_EveryReadIsReadOnlyAndTheCopyTargetIsNot(t *testing.T) {
	previewOn(t)
	for name, tc := range map[string]struct {
		open       func(BackendRef, context.Context) (dal.DB, error)
		wantReadOn bool
	}{
		"Open":          {func(r BackendRef, c context.Context) (dal.DB, error) { return r.Open(c) }, true},
		"OpenProtected": {func(r BackendRef, c context.Context) (dal.DB, error) { return r.OpenProtected(c) }, true},
		"OpenForWrite":  {func(r BackendRef, c context.Context) (dal.DB, error) { return r.OpenForWrite(c) }, false},
	} {
		fake := &fakeOpener{}
		stubPostgresOpener(t, fake.open)
		ref, err := Parse("postgres://alice:pw@db.example.com/shop")
		require.NoError(t, err)
		_, err = tc.open(ref, context.Background())
		require.NoError(t, err, name)
		require.Len(t, fake.dsns, 1, name)
		if tc.wantReadOn {
			assert.Contains(t, fake.dsns[0], "default_transaction_read_only=on", name)
		} else {
			assert.NotContains(t, fake.dsns[0], "default_transaction_read_only", name)
		}
		for _, always := range []string{"statement_timeout=30000", "timezone=UTC", "application_name=datatug", "connect_timeout=10"} {
			assert.Contains(t, fake.dsns[0], always, name)
		}
	}
}

// A URL that turns read-only off is refused on every read, before the opener, and the person who
// really means to write through it can do so only with the copy target.
func TestOpen_AURLThatTurnsReadOnlyOffIsRefusedOnEveryRead(t *testing.T) {
	previewOn(t)
	fake := &fakeOpener{}
	stubPostgresOpener(t, fake.open)
	ref, err := Parse("postgres://" + markerUser + ":" + markerPassword + "@db.example.com/shop?default_transaction_read_only=off&x=" + markerQuery)
	require.NoError(t, err)

	for name, open := range map[string]func(context.Context) (dal.DB, error){"Open": ref.Open, "OpenProtected": ref.OpenProtected, "OpenProtectedForTest": ref.OpenProtectedForTest, "OpenForTest": ref.OpenForTest} {
		db, openErr := open(context.Background())
		assert.Nil(t, db, name)
		assert.Same(t, errPostgresReadOnlyOff, openErr, name)
		assertNoMarkers(t, name, openErr)
	}
	assert.Empty(t, fake.dsns, "the opener is never called for a refused URL")

	db, err := ref.OpenForWrite(context.Background())
	require.NoError(t, err, "the copy target may turn read-only off")
	assert.NotNil(t, db)
	require.Len(t, fake.dsns, 1)
	assert.Contains(t, fake.dsns[0], "default_transaction_read_only=off")
}

// One handle per resolved connection string for the process: a second open of the same source reuses it,
// another source or another access gets its own, and a failure is not remembered.
func TestOpen_OneHandlePerConnectionString(t *testing.T) {
	previewOn(t)
	fake := &fakeOpener{}
	stubPostgresOpener(t, fake.open)
	first, err := Parse("postgres://alice:pw@db.example.com/shop")
	require.NoError(t, err)
	other, err := Parse("postgres://alice:pw@db.example.com/other")
	require.NoError(t, err)

	a, err := first.OpenProtected(context.Background())
	require.NoError(t, err)
	b, err := first.OpenProtected(context.Background())
	require.NoError(t, err)
	c, err := first.Open(context.Background())
	require.NoError(t, err)
	assert.Same(t, a, b, "a second open of the same source reuses the handle")
	assert.Same(t, a, c, "Open and OpenProtected read the same way: one handle")
	assert.Len(t, fake.dsns, 1)

	d, err := other.Open(context.Background())
	require.NoError(t, err)
	assert.NotSame(t, a, d)
	assert.Len(t, fake.dsns, 2, "another database is another handle")

	w, err := first.OpenForWrite(context.Background())
	require.NoError(t, err)
	assert.NotSame(t, a, w, "a connection that is not read-only is never the one that is")
	assert.Len(t, fake.dsns, 3)
}

func TestOpen_AFailureIsNotRemembered(t *testing.T) {
	previewOn(t)
	fake := &fakeOpener{err: &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}}
	stubPostgresOpener(t, fake.open)
	ref := parseMarked(t)

	_, err := ref.Open(context.Background())
	require.Error(t, err)
	fake.err = nil
	db, err := ref.Open(context.Background())
	require.NoError(t, err, "the server is back")
	assert.NotNil(t, db)
	assert.Len(t, fake.dsns, 2)
}

// Callers close what they open (the protected read of secureread does), but a handle is the
// process's for its whole life: closing it is a no-op, and the process's exit is what closes it.
func TestOpen_ClosingASharedHandleDoesNothing(t *testing.T) {
	previewOn(t)
	fake := &fakeOpener{}
	stubPostgresOpener(t, fake.open)
	ref := parseMarked(t)

	db, err := ref.OpenProtected(context.Background())
	require.NoError(t, err)
	closer, ok := db.(io.Closer)
	require.True(t, ok, "callers type-assert io.Closer and close what they opened")
	assert.NoError(t, closer.Close())
	again, err := ref.OpenProtected(context.Background())
	require.NoError(t, err)
	assert.Same(t, db, again, "and the handle is still the one the cache holds")
}

// The decorator keeps everything the adapter offers that callers look for by type assertion.
func TestOpen_TheHandleKeepsTheAdaptersCapabilities(t *testing.T) {
	previewOn(t)
	stubPostgresOpener(t, (&fakeOpener{}).open)
	db, err := parseMarked(t).OpenProtected(context.Background())
	require.NoError(t, err)

	_, isReader := db.(dbschema.SchemaReader)
	assert.True(t, isReader, "the schema reader")
	_, isModifier := db.(ddl.SchemaModifier)
	assert.True(t, isModifier, "the schema modifier, for a copy target")
	concurrent, isConcurrent := db.(dal.ConcurrencyAware)
	require.True(t, isConcurrent)
	assert.True(t, concurrent.SupportsConcurrentConnections())
	_, hasCapabilities := db.(dal.QueryCapabilitiesProvider)
	assert.True(t, hasCapabilities, "what the dialect runs on the server")
}

// Many goroutines opening one source at once open it once.
func TestOpen_ConcurrentOpensOfOneSourceOpenOnce(t *testing.T) {
	previewOn(t)
	var calls atomic.Int32
	stubPostgresOpener(t, func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		calls.Add(1)
		return &dalgo2postgres.Database{}, nil
	})
	ref := parseMarked(t)

	var wg sync.WaitGroup
	handles := make([]dal.DB, 16)
	for i := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handles[i], _ = ref.OpenProtected(context.Background())
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, calls.Load())
	for _, handle := range handles {
		assert.Same(t, handles[0], handle)
	}
}

// Every failure of an open is the adapter's own error, classified by its kind and SQLSTATE, or one
// fixed sentence: nothing a driver wrote is printed, logged or wrapped. The markers stand for the
// password, the user name and a query parameter; each class is tried with a hostile cause that
// quotes them.
func TestOpen_FailuresAreClassifiedAndNeverShowTheSource(t *testing.T) {
	previewOn(t)
	const prefix = `open postgres source "postgres://db.example.com:5433/shop": `
	hostile := fmt.Sprintf("dial %q: password=%s user=%s", markedPostgresURL, markerPassword, markerUser)
	for name, tc := range map[string]struct {
		cause error
		want  string
	}{
		"a rejected password":      {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01", Host: "db.example.com"}, prefix + "the server rejected the user or the password"},
		"a user that is refused":   {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28000"}, prefix + "the server rejected the user or the password"},
		"a database that is not":   {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "3D000", Database: "shop"}, prefix + "the database does not exist"},
		"a server that is down":    {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433"}, prefix + "the server could not be reached"},
		"a timeout":                {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTimeout}, prefix + "the attempt timed out"},
		"a TLS failure":            {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTLS}, prefix + "the TLS handshake with the server failed"},
		"another server code":      {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "53300"}, prefix + openedFailureReason},
		"a URL the driver refuses": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureInvalidDSN}, prefix + openedFailureReason},
		"wrapped by the caller":    {fmt.Errorf("opening: %w", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}), prefix + "the server could not be reached"},
		"text the driver wrote":    {errors.New(hostile), prefix + openedFailureReason},
		"a wrapped driver text":    {fmt.Errorf("pgx: %w", errors.New(hostile)), prefix + openedFailureReason},
		"a joined driver text":     {errors.Join(errors.New(hostile), errors.New(markerQuery)), prefix + openedFailureReason},
	} {
		fake := &fakeOpener{err: tc.cause}
		stubPostgresOpener(t, fake.open)
		ref := parseMarked(t)

		db, err := ref.OpenProtected(context.Background())
		assert.Nil(t, db, name)
		if assert.Error(t, err, name) {
			assert.EqualError(t, err, tc.want, name)
			assertNoMarkers(t, name, err)
		}
	}
}

// openedFailureReason is the reason an open gives when the cause cannot be told apart.
const openedFailureReason = "the driver could not open the source (its own message is not shown: a driver can quote the connection string)"

// The adapter's own error stays reachable for errors.As, with nothing of the driver in it.
func TestOpen_TheAdaptersErrorStaysReachable(t *testing.T) {
	previewOn(t)
	stubPostgresOpener(t, (&fakeOpener{err: fmt.Errorf("x: %w", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTimeout})}).open)
	_, err := parseMarked(t).Open(context.Background())
	var connection *dalgo2postgres.ConnectionError
	require.True(t, errors.As(err, &connection))
	assert.Equal(t, dalgo2postgres.FailureTimeout, connection.Kind)
}

// An env source is named by its variable, never by what the variable holds.
func TestOpen_AnEnvSourceIsNamedByItsVariable(t *testing.T) {
	previewOn(t)
	stubPostgresOpener(t, (&fakeOpener{err: &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}}).open)
	ref, err := ParseWithEnv("env:DATATUG_SHOP_PG_URL", fakeEnv(map[string]string{"DATATUG_SHOP_PG_URL": markedPostgresURL}))
	require.NoError(t, err)
	_, err = ref.OpenProtected(context.Background())
	assert.EqualError(t, err, `open postgres source "env:DATATUG_SHOP_PG_URL": the server could not be reached`)
	assertNoMarkers(t, "env source", err)
}

// The cache never holds the connection string: it is keyed by its hash, so no dump of it (a debugger, a %+v of a
// struct that holds it) shows a user name, a password, a host or a parameter.
func TestOpen_TheCacheHoldsNoConnectionString(t *testing.T) {
	previewOn(t)
	stubPostgresOpener(t, (&fakeOpener{}).open)
	_, err := parseMarked(t).OpenProtected(context.Background())
	require.NoError(t, err)

	require.Len(t, postgresHandles.entries, 1)
	for _, shown := range []string{fmt.Sprintf("%v", postgresHandles.entries), fmt.Sprintf("%+v", postgresHandles.entries), fmt.Sprintf("%#v", postgresHandles.entries)} {
		for _, secret := range []string{markerPassword, markerUser, markerQuery, "db.example.com", "shop"} {
			assert.NotContains(t, shown, secret)
		}
	}
}
