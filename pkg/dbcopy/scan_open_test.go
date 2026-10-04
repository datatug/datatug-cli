package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestOpenSchemaScan_ScrubsTheRealURLFromDriverErrors(t *testing.T) {
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
	assert.Contains(t, err.Error(), "xxxxx")
	assert.ErrorIs(t, err, cause)
}

// pgxConnectText is what pgx v5 writes when a connection attempt fails, as
// pgconn.ConnectError formats it: the user and database the connection used
// (never the password), then the cause.
func pgxConnectText(user, database, cause string) string {
	return fmt.Sprintf("failed to connect to `user=%s database=%s`: %s", user, database, cause)
}

func TestOpenSchemaScan_ADriverErrorHoldsNoPasswordWhateverTheShapeOfTheURL(t *testing.T) {
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
			assert.Contains(t, err.Error(), "database=shop", raw+": the target is named, only the password is hidden")
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

func TestOpenSchemaScan_NamesTheSourceInsteadOfQuotingItsURL(t *testing.T) {
	const url = "postgres://alice:s3cret@db.example.com:5433/shop?options=-csearch_path%3Dprivate_schema"
	stubNewPostgresDatabase(t, func(dsn string, _ ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		// The driver quotes the URL both ways: as a Go string literal and as text.
		return nil, fmt.Errorf("dalgo2postgres: PingContext(%q): dial %s: connection refused", dsn, dsn)
	})
	ref, err := ParseWithEnv("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": url}))
	require.NoError(t, err)

	_, err = ref.OpenSchemaScan(context.Background())
	require.Error(t, err)
	assert.Equal(t, `dalgo2postgres: PingContext("env:SHOP_PG_URL"): dial env:SHOP_PG_URL: connection refused`, err.Error(),
		"the error names the variable: the URL carries every connection option, not only the password")
}

func TestOpenSchemaScan_AnEmptyURLIsLeftAloneInTheErrorText(t *testing.T) {
	stubNewPostgresDatabase(t, func(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return nil, errors.New("no connection string")
	})
	_, err := BackendRef{Scheme: "postgres", Raw: "env:SHOP_PG_URL"}.OpenSchemaScan(context.Background())
	assert.EqualError(t, err, "no connection string")
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
func TestOpenSchemaScan_APgxParseErrorNamesTheSourceNotItsURL(t *testing.T) {
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
			assert.ErrorContains(t, err, "cannot parse", name+": this is pgx's own parse error")
			assert.ErrorContains(t, err, "env:SHOP_PG_URL", name)
			// pgx's own words about the option it cannot read ("sslmode is invalid")
			// stay; the URL it quotes does not.
			for _, shown := range []string{"s3cret", "alice", "db.example.com", "shop", "sslmode=", "connect_timeout=", "application_name", "postgres://", "5433"} {
				assert.NotContains(t, err.Error(), shown, name)
			}
		}
	}
}

func TestNamedSourceError_NamesEveryPostgresURLInTheText(t *testing.T) {
	t.Parallel()
	const url = "postgres://alice:s3cret@db.example.com:5433/shop"
	for name, tc := range map[string]struct{ text, want string }{
		"the URL as given, as text and as a literal": {`open ` + url + ` and "` + url + `"`, `open env:X and "env:X"`},
		"pgx's rendering of the URL":                 {"cannot parse `postgres://alice:xxxxx@db.example.com:5433/shop?sslmode=bogus`: bad", "cannot parse `env:X`: bad"},
		"the postgresql alias":                       {"cannot parse `postgresql://alice:xxxxx@db.example.com/shop`: bad", "cannot parse `env:X`: bad"},
		"an upper-case scheme":                       {"dial POSTGRES://alice:xxxxx@db.example.com/shop: refused", "dial env:X: refused"},
		"a URL in quotes":                            {`open "postgres://alice:xxxxx@db.example.com/shop?sslmode=bogus" failed`, `open "env:X" failed`},
		"two URLs":                                   {"from postgres://a@h1/d to postgresql://b@h2/d done", "from env:X to env:X done"},
		"no URL":                                     {"connection refused", "connection refused"},
	} {
		got := namedSourceError{err: errors.New(tc.text), url: url, name: "env:X"}.Error()
		assert.Equal(t, tc.want, got, name)
	}
	// A source with no URL of its own still never shows another, and punctuation
	// that follows a URL in the text stays.
	assert.Equal(t, "dial env:X: refused (env:X).", namedSourceError{err: errors.New("dial postgres://h/d: refused (postgres://h/d)."), name: "env:X"}.Error())
	assert.Equal(t, "a bare postgres:// is no URL", namedSourceError{err: errors.New("a bare postgres:// is no URL"), name: "env:X"}.Error())
}
