package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

// Hiding the optional transaction method must cause refusal, never pool reads.
type schemaScanWithoutTransaction struct{ dbcopy.SchemaScanDB }

type schemaScanTransaction struct {
	dbcopy.SchemaScanDB
	run func(context.Context, func(context.Context, dbschema.SchemaReader) error) error
}

func (s *schemaScanTransaction) RunReadonlySchemaTransaction(ctx context.Context, worker func(context.Context, dbschema.SchemaReader) error) error {
	return s.run(ctx, worker)
}

type schemaScanContextKey struct{}

// This narrow reader deliberately exposes namespace capability without a DB or
// Close. If the provider receives the opener's pool instead, the scan fails.
type callbackSchemaReader struct {
	dbschema.SchemaReader
	t      *testing.T
	active atomic.Bool
	calls  atomic.Int32
}

func (r *callbackSchemaReader) observe(ctx context.Context) {
	r.t.Helper()
	require.True(r.t, r.active.Load(), "catalog read escaped the transaction callback")
	require.Equal(r.t, r, ctx.Value(schemaScanContextKey{}), "provider lost the transaction's context")
	require.NoError(r.t, ctx.Err())
	r.calls.Add(1)
}

func (r *callbackSchemaReader) ListSchemas(ctx context.Context) ([]string, error) {
	r.observe(ctx)
	return []string{"public", "sales"}, nil
}

func (r *callbackSchemaReader) ListSchemaCollections(ctx context.Context, schema string) ([]dal.CollectionRef, error) {
	r.observe(ctx)
	return []dal.CollectionRef{dal.NewQualifiedRootCollectionRef(schema, "Customer", "")}, nil
}

func (r *callbackSchemaReader) ListSchemaViews(ctx context.Context, schema string) ([]dal.CollectionRef, error) {
	r.observe(ctx)
	return []dal.CollectionRef{dal.NewQualifiedRootCollectionRef(schema, "Order", "")}, nil
}

func TestPostgresSchemaScanRequiresTransactionCapability(t *testing.T) {
	pool := &fakeScanDB{listErr: errors.New("unguarded pool must not be read")}
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return &schemaScanWithoutTransaction{pool}, nil
	})
	catalog, err := scanPostgresCatalog(context.Background(), DriverPostgres, newShopParams(t))
	require.Nil(t, catalog)
	require.EqualError(t, err, "a PostgreSQL schema scan requires a verified native read-only schema transaction")
	require.Equal(t, 1, pool.closed)
}

func TestPostgresSchemaScanUsesOnlyCallbackReaderUntilTransactionEnds(t *testing.T) {
	for _, afterScan := range []error{nil, context.Canceled, errors.New("rollback " + pgSecret)} {
		t.Run("transaction_result="+errorName(afterScan), func(t *testing.T) {
			pool := &fakeScanDB{listErr: errors.New("unguarded pool must not be read")}
			reader := &callbackSchemaReader{SchemaReader: &fakeScanDB{}, t: t}
			calls := 0
			stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
				return &schemaScanTransaction{SchemaScanDB: pool, run: func(ctx context.Context, worker func(context.Context, dbschema.SchemaReader) error) error {
					calls++
					ctx, cancel := context.WithCancel(context.WithValue(ctx, schemaScanContextKey{}, reader))
					defer cancel()
					reader.active.Store(true)
					defer reader.active.Store(false)
					require.NoError(t, worker(ctx, reader))
					require.EqualValues(t, 5, reader.calls.Load(), "both schemas and views must finish inside callback")
					return afterScan
				}}, nil
			})
			catalog, err := scanPostgresCatalog(context.Background(), DriverPostgres, newShopParams(t))
			require.Equal(t, 1, calls)
			require.False(t, reader.active.Load())
			require.Equal(t, 1, pool.closed)
			if afterScan != nil {
				require.Nil(t, catalog, "metadata cannot escape a failed transaction")
				require.ErrorIs(t, err, afterScan)
				require.NotContains(t, err.Error(), pgSecret)
				return
			}
			require.NoError(t, err)
			require.Len(t, catalog.Schemas, 2)
			for _, schema := range catalog.Schemas {
				require.Len(t, schema.Tables, 1)
				require.Len(t, schema.Views, 1, "callback's optional view capability must survive")
			}
		})
	}
}

func TestPostgresSchemaScanRefusedTransactionDoesNotBuildProvider(t *testing.T) {
	cause := errors.New("native mode refused " + pgSecret)
	pool := &fakeScanDB{listErr: errors.New("unguarded pool must not be read")}
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return &schemaScanTransaction{SchemaScanDB: pool, run: func(context.Context, func(context.Context, dbschema.SchemaReader) error) error { return cause }}, nil
	})
	catalog, err := scanPostgresCatalog(context.Background(), DriverPostgres, newShopParams(t))
	require.Nil(t, catalog)
	require.ErrorIs(t, err, cause)
	require.NotContains(t, err.Error(), pgSecret)
	require.Equal(t, 1, pool.closed)
}

func errorName(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "rollback_failed"
}
