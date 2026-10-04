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

func TestPostgresDefaultSchemaIsTheReadersDefault(t *testing.T) {
	assert.Equal(t, dalgo2postgres.DefaultSchema, PostgresDefaultSchema)
	assert.Equal(t, "public", PostgresDefaultSchema)
}
