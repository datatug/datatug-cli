package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// markedHint is the hint of a source that no flag and no variable names.
const markedHint = "the PostgreSQL connection string is the one the source was given"

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
	assert.True(t, identifierModeIsExact(fake.extras[0]), "the adapter is given the exact identifier mode, not its default of folded names")
}

// identifierModeIsExact tells, with no server, whether the options the adapter was given make the identifier mode
// exact and not the adapter's default (names folded to lower case). The adapter refuses, before it opens any
// connection, a DbOptions.IdentifierCase that disagrees with the mode an option set; a string it reads as misread is
// refused after that and before the driver is asked. So the exact mode, given beside the folding case, is the "disagree"
// refusal, and the folding mode, or no mode, is the refusal of the string.
func identifierModeIsExact(options []dalgo2postgres.Option) bool {
	_, err := dalgo2postgres.NewDatabaseWithOptions(`"not a connection string"`, dal.NewSchema(nil, nil),
		dalgo2sql.DbOptions{IdentifierCase: dalgo2sql.IdentifierCaseFoldLower}, options...)
	return err != nil && strings.Contains(err.Error(), "disagree")
}

// The check above is not vacuous: options that set the folding mode, or none, are not the exact mode.
func TestIdentifierModeIsExact_TellsTheModesApart(t *testing.T) {
	assert.True(t, identifierModeIsExact([]dalgo2postgres.Option{dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact)}))
	assert.False(t, identifierModeIsExact([]dalgo2postgres.Option{dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierFoldLower)}), "the folding mode is not the exact one")
	assert.False(t, identifierModeIsExact(nil), "no option is not the exact mode either")
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

// The callers of a source that is being opened share the outcome of that one open, success or failure: a burst of
// callers of an unreachable source dials it once, not once after another, each for as long as the connect timeout.
// The callers join the attempt in flight before it ends (the open is held until all of them have), so the count is
// not a race. A call after the attempt ended opens again: a failure is not remembered.
func TestOpen_TheCallersOfASourceBeingOpenedShareItsOutcome(t *testing.T) {
	previewOn(t)
	for name, tc := range map[string]struct {
		cause   error
		wantErr bool
	}{
		"a failure": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}, true},
		"a success": {nil, false},
	} {
		var calls atomic.Int32
		release := make(chan struct{})
		cache := &handleCache{}
		open := func() (*dalgo2postgres.Database, error) {
			calls.Add(1)
			<-release
			if tc.cause != nil {
				return nil, tc.cause
			}
			return &dalgo2postgres.Database{}, nil
		}
		key := [32]byte{1}

		const callers = 8
		attempts := make([]*openAttempt, callers)
		for i := range attempts {
			db, attempt, err := cache.join(context.Background(), key, "", open)
			require.NoError(t, err, name)
			require.Nil(t, db, name)
			attempts[i] = attempt
			assert.Same(t, attempts[0], attempt, name+": every caller joins the attempt in flight")
		}
		close(release)

		var handles []*sharedPostgres
		for _, attempt := range attempts {
			db, err := attempt.await(context.Background())
			handles = append(handles, db)
			if tc.wantErr {
				assert.Same(t, tc.cause, err, name)
			} else {
				assert.NoError(t, err, name)
			}
		}
		assert.EqualValues(t, 1, calls.Load(), name+": one dial for the whole burst")
		for _, handle := range handles {
			assert.Same(t, handles[0], handle, name)
		}

		// The attempt is over: the next call decides afresh (a failure is not remembered, a handle is reused).
		again, next, err := cache.join(context.Background(), key, "", open)
		require.NoError(t, err, name)
		if tc.wantErr {
			require.NotNil(t, next, name+": a call after a failed attempt opens again")
			_, _ = next.await(context.Background())
			assert.EqualValues(t, 2, calls.Load(), name)
		} else {
			assert.Nil(t, next, name)
			assert.Same(t, handles[0], again, name+": a call after a successful attempt gets the handle")
			assert.EqualValues(t, 1, calls.Load(), name)
		}
	}
}

// A caller whose context ends stops waiting, with the classified sentence for a cancelled or a timed out attempt,
// while the open goes on; the handle it opens is kept, and the next caller gets it with no second open.
func TestOpen_ACallerWhoseContextEndsStopsWaitingAndTheOpenGoesOn(t *testing.T) {
	previewOn(t)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	stubPostgresOpener(t, func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &dalgo2postgres.Database{}, nil
	})
	ref := parseMarked(t)

	// The first caller starts the open, and the test waits until it is under way. The second joins it, whenever it
	// gets to: a context that has ended is answered whether the caller is already waiting or not.
	first, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() { _, err := ref.Open(first); firstResult <- err }()
	<-started
	cancelFirst()
	err := <-firstResult
	require.Error(t, err)
	assert.EqualError(t, err, "the attempt was cancelled; "+markedHint)
	assertNoMarkers(t, "cancelled", err)

	second, cancelSecond := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelSecond()
	_, err = ref.Open(second)
	require.Error(t, err)
	assert.EqualError(t, err, "the attempt timed out; "+markedHint)
	assertNoMarkers(t, "timed out", err)
	assert.EqualValues(t, 1, calls.Load(), "both callers waited on one open")

	close(release)
	handle, err := ref.Open(context.Background())
	require.NoError(t, err, "the open that went on after the callers left is kept")
	assert.NotNil(t, handle)
	assert.EqualValues(t, 1, calls.Load(), "and no second open was made for it")
}

// A caller whose context has ended before there is anything to wait for does not start an open: a request that is
// gone does not dial. A handle that is already open is still answered, and a source whose open is under way is waited
// for only until the context ends.
func TestOpen_ACallerWhoseContextHasEndedDoesNotStartAnOpen(t *testing.T) {
	previewOn(t)
	fake := &fakeOpener{}
	stubPostgresOpener(t, fake.open)
	ref := parseMarked(t)
	ended, cancel := context.WithCancel(context.Background())
	cancel()

	db, err := ref.Open(ended)
	assert.Nil(t, db)
	assert.EqualError(t, err, "the attempt was cancelled; "+markedHint)
	assert.Empty(t, fake.dsns, "no open was started")

	opened, err := ref.Open(context.Background())
	require.NoError(t, err)
	again, err := ref.Open(ended)
	require.NoError(t, err, "a handle that is open is answered whatever the context")
	assert.Same(t, opened, again)
}

// An opener that panics (the stop-the-run opener of a test binary does) panics in the caller, not in a goroutine
// nobody can recover, and in every caller that waited for the same attempt.
func TestOpen_APanicOfTheOpenerReachesEveryCallerThatWaited(t *testing.T) {
	previewOn(t)
	cache := &handleCache{}
	release := make(chan struct{})
	open := func() (*dalgo2postgres.Database, error) {
		<-release
		panic("the opener stops the run")
	}
	key := [32]byte{2}
	_, first, err := cache.join(context.Background(), key, "", open)
	require.NoError(t, err)
	_, second, err := cache.join(context.Background(), key, "", open)
	require.NoError(t, err)
	close(release)
	assert.PanicsWithValue(t, "the opener stops the run", func() { _, _ = first.await(context.Background()) })
	assert.PanicsWithValue(t, "the opener stops the run", func() { _, _ = second.await(context.Background()) })

	_, next, err := cache.join(context.Background(), key, "", func() (*dalgo2postgres.Database, error) { return &dalgo2postgres.Database{}, nil })
	require.NoError(t, err)
	require.NotNil(t, next, "a panic is not remembered either")
	db, err := next.await(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, db)
}

// An opener whose goroutine ends through runtime.Goexit (a failing test does, with FailNow, when it builds its stand-in
// inside the opener) neither returns nor panics. Its attempt still ends, with the fixed failure, for every caller that
// waited, and the entry does not keep the dead attempt: the next call opens again. The callers wait with a context that
// ends, so that a regression is a failure of the test and not a hang.
func TestOpen_AnOpenerThatEndsItsGoroutineEndsTheAttemptWithAFailure(t *testing.T) {
	previewOn(t)
	cache := &handleCache{}
	release := make(chan struct{})
	open := func() (*dalgo2postgres.Database, error) {
		<-release
		runtime.Goexit()
		return &dalgo2postgres.Database{}, nil // not reached
	}
	key := [32]byte{4}
	_, first, err := cache.join(context.Background(), key, "", open)
	require.NoError(t, err)
	_, second, err := cache.join(context.Background(), key, "", open)
	require.NoError(t, err)
	close(release)

	for name, attempt := range map[string]*openAttempt{"the caller that started it": first, "the caller that joined it": second} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		db, awaitErr := attempt.await(ctx)
		cancel()
		assert.Nil(t, db, name)
		assert.Same(t, errPostgresOpenFailed, awaitErr, name+": the attempt ended, with the fixed failure, and was not waited for until the context ended")
	}

	db, next, err := cache.join(context.Background(), key, "", func() (*dalgo2postgres.Database, error) { return &dalgo2postgres.Database{}, nil })
	require.NoError(t, err)
	require.Nil(t, db)
	require.NotNil(t, next, "the dead attempt is not kept: the next call opens again")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened, err := next.await(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, opened)
}

// A constructor that fails with an error that wraps a context error (a driver's text around a deadline) is the bare
// context error: classified as a timeout or a cancellation, and nothing of the driver's text is kept.
func TestOpen_AContextErrorOfTheConstructorIsTheBareContextError(t *testing.T) {
	previewOn(t)
	hostile := func(cause error) error { return fmt.Errorf("dial %q: %w", markedPostgresURL, cause) }
	for name, tc := range map[string]struct {
		cause error
		want  string
	}{
		"a deadline":     {hostile(context.DeadlineExceeded), "the attempt timed out"},
		"a cancellation": {hostile(context.Canceled), "the attempt was cancelled"},
	} {
		stubPostgresOpener(t, (&fakeOpener{err: tc.cause}).open)
		_, err := parseMarked(t).Open(context.Background())
		assert.EqualError(t, err, tc.want+"; "+markedHint, name)
		assertNoMarkers(t, name, err)
	}
}

// Every failure of an open is the adapter's own error, classified by its kind and SQLSTATE, or one
// fixed sentence: nothing a driver wrote is printed, logged or wrapped. The markers stand for the
// password, the user name and a query parameter; each class is tried with a hostile cause that
// quotes them.
func TestOpen_FailuresAreClassifiedAndNeverShowTheSource(t *testing.T) {
	previewOn(t)
	const suffix = "; " + markedHint
	hostile := fmt.Sprintf("dial %q: password=%s user=%s", markedPostgresURL, markerPassword, markerUser)
	for name, tc := range map[string]struct {
		cause error
		want  string
	}{
		"a rejected password":      {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01", Host: "db.example.com"}, "the server refused the connection: password authentication failed (SQLSTATE 28P01)" + suffix},
		"a user that is refused":   {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28000"}, "the server refused the connection: the user is not authorized (SQLSTATE 28000)" + suffix},
		"a database that is not":   {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "3D000", Database: "shop"}, "the server refused the connection: the database does not exist (SQLSTATE 3D000)" + suffix},
		"a server that is down":    {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433"}, "the server could not be reached" + suffix},
		"a timeout":                {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTimeout}, "the connection timed out or was canceled" + suffix},
		"a TLS failure":            {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTLS}, "the TLS handshake with the server failed" + suffix},
		"another server code":      {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "53300"}, "the server refused the connection: too many connections (SQLSTATE 53300)" + suffix},
		"a URL the driver refuses": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureInvalidDSN}, "the connection string cannot be parsed, or a file or service it names cannot be read" + suffix},
		"wrapped by the caller":    {fmt.Errorf("opening: %w", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}), "the server could not be reached" + suffix},
		"text the driver wrote":    {errors.New(hostile), openedFailureReason + suffix},
		"a wrapped driver text":    {fmt.Errorf("pgx: %w", errors.New(hostile)), openedFailureReason + suffix},
		"a joined driver text":     {errors.Join(errors.New(hostile), errors.New(markerQuery)), openedFailureReason + suffix},
	} {
		fake := &fakeOpener{err: tc.cause}
		stubPostgresOpener(t, fake.open)
		ref := parseMarked(t)

		db, err := ref.OpenProtected(context.Background())
		assert.Nil(t, db, name)
		if assert.Error(t, err, name) {
			assert.EqualError(t, err, tc.want, name)
			assertNoMarkers(t, name, err)
			assert.NotContains(t, err.Error(), "db.example.com", name)
			assert.NotContains(t, err.Error(), "5433", name)
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
	assert.EqualError(t, err, "the server could not be reached; the PostgreSQL connection string is read from the environment variable DATATUG_SHOP_PG_URL")
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
