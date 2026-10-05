package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/dal-go/dalgo2postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWithEnv_ResolvesThroughTheInjectedLookup(t *testing.T) {
	t.Parallel()
	ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h/shop"}))
	require.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
	assert.Equal(t, "env:SHOP_PG_URL", ref.Raw)

	_, err = ParseWithEnv("env:SHOP_PG_URL", fakeEnv(nil))
	assert.ErrorContains(t, err, "SHOP_PG_URL is not set")
}

func stubNewPostgresDatabase(t *testing.T, stub func(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error)) {
	t.Helper()
	original := newPostgresDatabase
	newPostgresDatabase = stub
	t.Cleanup(func() { newPostgresDatabase = original })
}

func TestOpenSchemaScan_OnlyPostgresIsOpenedThroughTheAdapter(t *testing.T) {
	for _, ref := range []BackendRef{
		{Scheme: "sqlite", Path: "/tmp/shop.db", Raw: "sqlite:///tmp/shop.db"},
		{Scheme: "ingitdb", Path: "./crm", Raw: "ingitdb://./crm"},
	} {
		stubNewPostgresDatabase(t, func(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
			t.Fatal("a non-PostgreSQL source must never reach the PostgreSQL driver")
			return nil, nil
		})
		db, err := ref.OpenSchemaScan(context.Background())
		assert.Nil(t, db)
		if assert.Error(t, err, ref.Scheme) {
			assert.Contains(t, err.Error(), ref.Scheme)
			assert.Contains(t, err.Error(), "postgres")
		}
	}
}

func TestOpenSchemaScan_OpensThePostgresURLWithExactIdentifiers(t *testing.T) {
	var gotDSN string
	var gotOptions int
	stubNewPostgresDatabase(t, func(dsn string, options ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		gotDSN, gotOptions = dsn, len(options)
		return &dalgo2postgres.Database{}, nil
	})
	ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h/shop"}))
	require.NoError(t, err)

	db, err := ref.OpenSchemaScan(context.Background())
	require.NoError(t, err)
	require.NotNil(t, db)
	assert.Equal(t, "postgres://alice:s3cret@h/shop", gotDSN)
	assert.Equal(t, 1, gotOptions, "exactly one option: the exact identifier mode (DataTug reads names as PostgreSQL reports them)")
	assert.NoError(t, db.Close())
}

// openedSchemaScanFailure is the sentence OpenSchemaScan returns when the driver
// cannot open the source named env:SHOP_PG_URL for a reason that cannot be told
// apart without reading the driver's message.
const openedSchemaScanFailure = `open postgres source "env:SHOP_PG_URL": ` + openedSchemaScanFailureReason

// openedSchemaScanFailureReason is the reason that sentence gives.
const openedSchemaScanFailureReason = `the driver could not open the source (its own message is not shown: a driver can quote the connection string)`

// The error of a failed open is classified, never quoted: whatever the driver
// wrote, a pattern scrubber is not what keeps a secret out.
func TestOpenSchemaScan_ReturnsTheClassifiedOpenFailureNotTheDriversText(t *testing.T) {
	cause := errors.New("connection refused")
	stubNewPostgresDatabase(t, func(dsn string, _ ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		// dalgo2postgres quotes the DSN it was given in its open and ping errors.
		return nil, fmt.Errorf("dalgo2postgres: PingContext(%q): %w (password=s3cret%%2Fx)", dsn, cause)
	})
	ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret%2Fx@h/shop"}))
	require.NoError(t, err)

	db, err := ref.OpenSchemaScan(context.Background())
	assert.Nil(t, db)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
	assert.EqualError(t, err, openedSchemaScanFailure)
	assert.ErrorIs(t, err, cause, "the driver's own error is kept for errors.Is, never printed")
}

// A cause that can be told apart without reading the driver's message is named.
func TestOpenSchemaScan_NamesACauseThatNeedsNoMessageToRecognise(t *testing.T) {
	for want, cause := range map[string]error{
		// A driver that wraps the system error it got (dalgo2postgres does not: see the test below).
		"the connection was refused": fmt.Errorf("dial tcp 10.0.0.5:5432: %w", syscall.ECONNREFUSED),
		"the attempt timed out":      context.DeadlineExceeded,
		"permission denied":          fs.ErrPermission,
	} {
		stubNewPostgresDatabase(t, func(dsn string, _ ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
			return nil, fmt.Errorf("dalgo2postgres: PingContext(%q): %w", dsn, cause)
		})
		ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h/shop"}))
		require.NoError(t, err)
		_, err = ref.OpenSchemaScan(context.Background())
		assert.EqualError(t, err, `open postgres source "env:SHOP_PG_URL": `+want)
		assert.ErrorIs(t, err, cause)
	}
}

// dalgo2postgres returns a *ConnectionError, which hides the driver's error from errors.Is and
// errors.As on purpose (it says what failed by Kind and SQLState instead), so the causes it
// reports are named from those two fields, with this package's own sentences: never from the
// error's text, which names the host, the port and the database.
func TestOpenSchemaScan_NamesTheFailureTheAdapterReportsByKindAndSQLState(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *dalgo2postgres.ConnectionError
		want string
	}{
		{"a wrong password", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01"}, "the server rejected the user or the password"},
		{"a user the server does not authorize", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28000"}, "the server does not authorize this user for this connection (its access rules: user, database, address or encryption)"},
		{"a database that does not exist", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "3D000"}, "the database does not exist"},
		{"a server that cannot be reached", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}, "the server could not be reached"},
		{"an attempt that timed out", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTimeout}, "the attempt timed out"},
		{"a TLS handshake that failed", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTLS}, "the TLS handshake with the server failed"},
		{"a server error of another code", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "53300"}, openedSchemaScanFailureReason},
		{"a failure of another kind", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureInvalidDSN}, openedSchemaScanFailureReason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The adapter names the host, the port and the database in its own text: none may be shown.
			tc.err.Host, tc.err.Port, tc.err.Database = "10.0.0.5", "54329", "shopdb"
			stubNewPostgresDatabase(t, func(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
				return nil, fmt.Errorf("opening: %w", tc.err) // wrapped: errors.As finds it
			})
			ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h/shop"}))
			require.NoError(t, err)

			_, err = ref.OpenSchemaScan(context.Background())

			assert.EqualError(t, err, `open postgres source "env:SHOP_PG_URL": `+tc.want)
			for _, shown := range []string{"10.0.0.5", "54329", "shopdb", "SQLSTATE", "dalgo2postgres"} {
				assert.NotContains(t, err.Error(), shown)
			}
			var kept *dalgo2postgres.ConnectionError
			assert.ErrorAs(t, err, &kept, "the adapter's own error is kept for errors.As, never printed")
		})
	}
}

// pgxConnectText is what pgx v5 writes when a connection attempt fails, as
// pgconn.ConnectError formats it: the user and database the connection used
// (never the password), then the cause.
func pgxConnectText(user, database, cause string) string {
	return fmt.Sprintf("failed to connect to `user=%s database=%s`: %s", user, database, cause)
}

func TestOpenSchemaScan_ADriverErrorHoldsNoPasswordWhateverTheShapeOfTheURL(t *testing.T) { // and none of its words
	// The stub answers with pgx's real text for the connection the driver was
	// handed: the user and the database as they read out of the URL.
	stubNewPostgresDatabase(t, func(dsn string, _ ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		target, err := ParsePostgresTarget(dsn)
		require.NoError(t, err)
		return nil, fmt.Errorf("dalgo2postgres: PingContext(%q): %s", dsn,
			pgxConnectText(target.User, target.Database, "hostname resolving error (lookup "+target.Host+": no such host)"))
	})
	for _, raw := range []string{
		"postgres://alice:s3cret@db.example.com/shop",
		"postgres://alice:s3%2Fcret%3Fx%23y%40z@db.example.com:5433/shop?sslmode=require",
		"postgres://alice:p@ss@db.example.com/shop",
	} {
		ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": raw}))
		require.NoError(t, err, raw)
		_, err = ref.OpenSchemaScan(context.Background())
		if assert.Error(t, err, raw) {
			for _, secret := range []string{"s3cret", "s3/cret", "s3%2Fcret", "p@ss", "ss@"} {
				assert.NotContains(t, err.Error(), secret, raw)
			}
			assert.EqualError(t, err, openedSchemaScanFailure, raw+": the driver's words, which name the user and the database, are not shown")
		}
	}
}

func TestOpenSchemaScan_RefusesAURLThatSplitsThePassword(t *testing.T) {
	for name, raw := range splitPasswordURLs {
		stubNewPostgresDatabase(t, func(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
			t.Fatal("a URL net/url and pgx read differently from how it was written must never reach the driver: " + name)
			return nil, nil
		})
		url := strings.ReplaceAll(raw, "SECRET", "TOPSECRET")
		ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
		require.NoError(t, err, name+": Parse accepts the shape, so the open path must refuse it")

		db, err := ref.OpenSchemaScan(context.Background())
		assert.Nil(t, db, name)
		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "percent-encode", name)
			for _, quoted := range []string{"TOPSECRET", "SECRET", "alice", "db.example.com"} {
				assert.NotContains(t, err.Error(), quoted, name)
			}
		}
	}
}

func TestOpenSchemaScan_NamesTheSourceInsteadOfQuotingItsURLOrTheDriversWords(t *testing.T) {
	const url = "postgres://alice:s3cret@db.example.com:5433/shop?options=-csearch_path%3Dprivate_schema"
	stubNewPostgresDatabase(t, func(dsn string, _ ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		// The driver quotes the URL both ways: as a Go string literal and as text.
		return nil, fmt.Errorf("dalgo2postgres: PingContext(%q): dial %s: connection refused", dsn, dsn)
	})
	ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
	require.NoError(t, err)

	_, err = ref.OpenSchemaScan(context.Background())
	require.Error(t, err)
	assert.Equal(t, openedSchemaScanFailure, err.Error(),
		"the error names the variable: the URL carries every connection option, not only the password")
	assert.NotContains(t, err.Error(), "dalgo2postgres", "the driver's words are not shown")
}

func TestOpenSchemaScan_AnErrorOfADriverGivenNoURLIsClassifiedToo(t *testing.T) {
	stubNewPostgresDatabase(t, func(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return nil, errors.New("no connection string")
	})
	_, err := BackendRef{Scheme: "postgres", Raw: "env:SHOP_PG_URL"}.OpenSchemaScan(context.Background())
	assert.EqualError(t, err, openedSchemaScanFailure)
}

func TestPostgresDefaultSchemaIsTheReadersDefault(t *testing.T) {
	assert.Equal(t, dalgo2postgres.DefaultSchema, PostgresDefaultSchema)
	assert.Equal(t, "public", PostgresDefaultSchema)
}

func TestOpenSchemaScan_RefusesAUserNameThatHoldsAColon(t *testing.T) {
	for name, raw := range colonInUserURLs {
		stubNewPostgresDatabase(t, func(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
			t.Fatal("a URL whose user name is the password must never reach the driver: " + name)
			return nil, nil
		})
		url := strings.ReplaceAll(raw, "SECRET", "TOPSECRET")
		ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
		require.NoError(t, err, name+": Parse accepts the shape, so the open path must refuse it")

		db, err := ref.OpenSchemaScan(context.Background())
		assert.Nil(t, db, name)
		if assert.Error(t, err, name) {
			assert.ErrorContains(t, err, "literal colon", name)
			assert.ErrorContains(t, err, "env:SHOP_PG_URL", name)
			for _, quoted := range []string{"TOPSECRET", "SECRET", "alice", "db.example.com"} {
				assert.NotContains(t, err.Error(), quoted, name)
			}
		}
	}
}

// pgx quotes a URL it cannot parse in an error of its own, with the password
// masked and the URL written again (ParseConfigError), so the text is not the
// URL as it was given. The real driver constructor fails on these before it
// dials anything.
func TestOpenSchemaScan_APgxParseErrorIsClassifiedNotQuoted(t *testing.T) {
	for name, tail := range map[string]string{
		"an unknown sslmode":      "db.example.com:5433/shop?sslmode=bogus",
		"a bad connect_timeout":   "db.example.com:5433/shop?connect_timeout=soon",
		"a port out of range":     "db.example.com:70000/shop",
		"an option and a service": "db.example.com/shop?application_name=billing&sslmode=bogus",
	} {
		ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@" + tail}))
		require.NoError(t, err, name)

		db, err := ref.OpenSchemaScan(context.Background())
		assert.Nil(t, db, name)
		if assert.Error(t, err, name) {
			assert.EqualError(t, err, openedSchemaScanFailure, name)
			// Neither pgx's own words about the option it cannot read nor the URL it
			// quotes are shown.
			for _, shown := range []string{"s3cret", "alice", "db.example.com", "shop", "sslmode=", "connect_timeout=", "application_name", "postgres://", "5433", "cannot parse"} {
				assert.NotContains(t, err.Error(), shown, name)
			}
		}
	}
}
