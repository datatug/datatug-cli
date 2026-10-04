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
