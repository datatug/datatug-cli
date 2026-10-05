package secureread

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/internal/pgstandin"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three parts of a PostgreSQL source that must never be shown.
const (
	pgPassword = "PWMARKER-dt02-secureread"
	pgUser     = "USERMARKER-dt02-secureread"
	pgQuery    = "QUERYMARKER-dt02-secureread"
	pgSource   = "postgres://" + pgUser + ":" + pgPassword + "@db.example.com:5433/shop?application_name=" + pgQuery
)

const dtqlOverCustomers = "from: {name: customers}\n"

// standInForPostgres replaces the constructor of a PostgreSQL database for the length of a test with one that
// counts its calls and answers err (nil: a database that holds nothing).
func standInForPostgres(t *testing.T, err error) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	restore := dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		calls.Add(1)
		if err != nil {
			return nil, err
		}
		return &dalgo2postgres.Database{}, nil
	})
	t.Cleanup(restore)
	return &calls
}

// sessions are the two kinds of session a read runs under: one that reads through a policy and one that
// reads through none.
func sessions(t *testing.T) map[string]Session {
	t.Helper()
	unrestricted, err := NewSession(SessionOptions{NoPolicies: true})
	require.NoError(t, err)
	return map[string]Session{"a session with a policy": aliceSession(t, productsOnlyPolicy), "an unrestricted session": unrestricted}
}

// reads runs every way the executor reads a source, against the PostgreSQL source.
func reads(executor *Executor) map[string]func() error {
	ctx := context.Background()
	federated := `from:
  database: shop
  name: customers
`
	return map[string]func() error{
		"RunStructured": func() error { _, err := executor.RunStructured(ctx, pgSource, customersQuery(), nil); return err },
		"RunDTQL":       func() error { _, err := executor.RunDTQL(ctx, pgSource, []byte(dtqlOverCustomers), nil); return err },
		"RunFederatedDTQL": func() error {
			_, err := executor.RunFederatedDTQL(ctx, []byte(federated), map[string]string{"shop": pgSource}, nil)
			return err
		},
	}
}

// With the preview switch off, every read of a PostgreSQL source answers the preview sentence and never
// calls the opener, whatever the session.
func TestExecutor_APostgresSourceWithThePreviewOffAnswersThePreviewSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	calls := standInForPostgres(t, nil)
	for sessionName, session := range sessions(t) {
		for readName, read := range reads(NewExecutor(session)) {
			err := read()
			if assert.Error(t, err, sessionName+": "+readName) {
				assert.ErrorIs(t, err, dbcopy.ErrPostgresPreview, sessionName+": "+readName)
				assert.NotContains(t, err.Error(), pgPassword)
				assert.NotContains(t, err.Error(), pgUser)
				assert.NotContains(t, err.Error(), pgQuery)
			}
		}
	}
	assert.Zero(t, calls.Load(), "the opener is never called")
}

// A read through one or more policies is refused before the source is opened, on every way in, and the
// opener is never called. The sentence is the fixed one.
func TestExecutor_APostgresSourceReadThroughPoliciesIsRefusedBeforeItIsOpened(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	calls := standInForPostgres(t, nil)
	executor := NewExecutor(sessions(t)["a session with a policy"])
	for name, read := range reads(executor) {
		err := read()
		if assert.Error(t, err, name) {
			assert.ErrorIs(t, err, dbcopy.ErrPostgresPolicyReads, name)
			assert.Contains(t, err.Error(), "policy-enforced reads on PostgreSQL sources are not available in this preview", name)
			assert.NotContains(t, err.Error(), pgPassword, name)
			assert.NotContains(t, err.Error(), pgUser, name)
			assert.NotContains(t, err.Error(), pgQuery, name)
		}
	}
	assert.Zero(t, calls.Load(), "the opener is never called")
}

// A session with no policy has nothing to refuse: with the preview on, the source is opened (here by a
// stand-in that says the server is down), and the failure is the adapter's sentence and the hint of where the
// connection string is read from, naming no part of the connection.
func TestExecutor_APostgresSourceWithNoPolicyIsOpenedOnceThePreviewIsOn(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	calls := standInForPostgres(t, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433", Database: "shop"})
	executor := NewExecutor(sessions(t)["an unrestricted session"])
	for name, read := range reads(executor) {
		err := read()
		if assert.Error(t, err, name) {
			assert.Contains(t, err.Error(), "the server could not be reached; the PostgreSQL connection string is the one the source was given", name)
			assert.NotContains(t, err.Error(), "db.example.com", name)
			assert.NotContains(t, err.Error(), pgPassword, name)
			assert.NotContains(t, err.Error(), pgUser, name)
			assert.NotContains(t, err.Error(), pgQuery, name)
		}
	}
	assert.EqualValues(t, 3, calls.Load(), "each read tried to open the source: a failure is not remembered")
}

// Native SQL is refused for a PostgreSQL source, with a message that names PostgreSQL, and nothing is opened.
func TestRunNativeSQL_APostgresSourceIsRefusedAndTheMessageNamesPostgreSQL(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	calls := standInForPostgres(t, nil)
	unrestricted, err := NewSession(SessionOptions{NoPolicies: true})
	require.NoError(t, err)

	_, err = NewExecutor(unrestricted).RunNativeSQL(context.Background(), pgSource, "SELECT 1")
	require.ErrorIs(t, err, ErrNativeSQLUnsupported)
	assert.Contains(t, err.Error(), "PostgreSQL")
	assert.NotContains(t, err.Error(), pgPassword)
	assert.Zero(t, calls.Load())
}

// A session with no policy reads a source that opened and whose pool cannot make a connection again (the server was
// restarted, the password was changed): every way the executor reads shows the adapter's fixed sentence and the hint, and nothing of what
// pgx writes, which names the user and holds the password.
func TestExecutor_AReadThatLosesItsConnectionShowsOneFixedSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	// The stand-in is built here, on the goroutine of the test: the opener runs on another one, where a failure of
	// the setup (FailNow) would end that goroutine and not the test.
	standIn := pgstandin.Unreachable(t, pgUser, pgPassword)
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return standIn, nil
	}))
	executor := NewExecutor(sessions(t)["an unrestricted session"])
	for name, read := range reads(executor) {
		err := read()
		if assert.Error(t, err, name) {
			assert.Contains(t, err.Error(), "the connection failed; the PostgreSQL connection string is the one the source was given", name)
			for _, shown := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
				assert.NotContains(t, shown, pgPassword, name)
				assert.NotContains(t, shown, pgUser, name)
				assert.NotContains(t, shown, pgQuery, name)
			}
		}
	}
}
