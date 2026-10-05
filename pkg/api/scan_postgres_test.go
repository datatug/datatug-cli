package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pgSecret = "s3cret-p4ss"

func envOf(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func shopEnv() map[string]string {
	return map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@db.example.com:5433/shop?sslmode=require"}
}

func newShopParams(t *testing.T) *PostgresScanParams {
	t.Helper()
	params, err := NewPostgresScanParams(envOf(shopEnv()), "SHOP_PG_URL", "prod", "shop")
	require.NoError(t, err)
	return params
}

func TestNewPostgresScanParams(t *testing.T) {
	params := newShopParams(t)
	var _ dbconnection.Params = params

	assert.Equal(t, "postgres", params.Driver())
	assert.Equal(t, dbconnection.ModeReadOnly, params.Mode())
	assert.Equal(t, "db.example.com", params.Server())
	assert.Equal(t, 5433, params.Port())
	assert.Equal(t, "shop", params.Catalog(), "the catalog is the project's name for the database, as for sqlite")
	assert.Equal(t, "alice", params.User())
	assert.Equal(t, "SHOP_PG_URL", params.DSNEnv())
	assert.Equal(t, "connections/prod/shop.json", params.Path())
	assert.Equal(t, "env:SHOP_PG_URL", params.ConnectionString())
	assert.Equal(t, "env:SHOP_PG_URL", params.String())

	ref := params.SourceRef()
	assert.Equal(t, "postgres", ref.Scheme)
	assert.Equal(t, "env:SHOP_PG_URL", ref.Raw)

	for _, rendered := range []string{
		fmt.Sprintf("%v", params), fmt.Sprintf("%+v", params), fmt.Sprint(params), params.String(),
		fmt.Sprintf("%v", ref), fmt.Sprintf("%+v", ref), fmt.Sprintf("%#v", ref),
	} {
		assert.NotContains(t, rendered, pgSecret)
	}
}

func TestNewPostgresScanParams_Refuses(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		dsnEnv      string
		environment string
		catalog     string
		want        string
	}{
		{"an unset variable", nil, "SHOP_PG_URL", "prod", "shop", "SHOP_PG_URL is not set"},
		{"an empty variable", map[string]string{"SHOP_PG_URL": " "}, "SHOP_PG_URL", "prod", "shop", "SHOP_PG_URL is empty"},
		{"not a variable name", shopEnv(), "shop-pg", "prod", "shop", "variable name must match"},
		{"another source scheme", map[string]string{"SHOP_PG_URL": "sqlite:///tmp/shop.db"}, "SHOP_PG_URL", "prod", "shop", "must hold a postgres:// URL, not a sqlite source"},
		{"not a URL at all", map[string]string{"SHOP_PG_URL": "host=db user=alice password=" + pgSecret}, "SHOP_PG_URL", "prod", "shop", "does not hold a supported source URL"},
		{"a URL that cannot be read", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@h/shop?port=notaport"}, "SHOP_PG_URL", "prod", "shop", "host, port, database and user"},
		{"no host", map[string]string{"SHOP_PG_URL": "postgres:///shop"}, "SHOP_PG_URL", "prod", "shop", "names no host"},
		{"a host a project cannot record", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@-h/shop"}, "SHOP_PG_URL", "prod", "shop", "cannot be recorded"},
		{"a host list", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@a,b/shop"}, "SHOP_PG_URL", "prod", "shop", "cannot be recorded"},
		// The query of a URL overrides its authority and its path (pgx and the parser of the target
		// agree), so the line that names what the scan connects to, built from the authority and the
		// path, would name a place the scan does not connect to: dbcopy.Parse refuses such a URL for
		// every command, and the scan reaches it through the same parser.
		{"a socket directory in the query", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@h/shop?host=%2Fvar%2Frun%2Fpostgresql"}, "SHOP_PG_URL", "prod", "shop", `sets "host" in its query`},
		{"a host in the query", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@a.example.com/shop?host=b.internal"}, "SHOP_PG_URL", "prod", "shop", `sets "host" in its query`},
		{"a host in the query of a URL with none in its authority", map[string]string{"SHOP_PG_URL": "postgres:///shop?host=b.internal"}, "SHOP_PG_URL", "prod", "shop", `sets "host" in its query`},
		{"a port in the query", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@a.example.com:5433/shop?port=6543"}, "SHOP_PG_URL", "prod", "shop", `sets "port" in its query`},
		{"a dbname in the query", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@a.example.com/shop?sslmode=require&dbname=other"}, "SHOP_PG_URL", "prod", "shop", `sets "dbname" in its query`},
		{"a database in the query", map[string]string{"SHOP_PG_URL": "postgres://alice:" + pgSecret + "@a.example.com/shop?database=other"}, "SHOP_PG_URL", "prod", "shop", `sets "database" in its query`},
		{"an environment that is not a plain name", shopEnv(), "SHOP_PG_URL", "../prod", "shop", "plain name"},
		{"a database that is not a plain name", shopEnv(), "SHOP_PG_URL", "prod", "sub/shop", "plain name"},
		{"a database that is the parent directory", shopEnv(), "SHOP_PG_URL", "prod", "..", "plain name"},
		{"an empty database", shopEnv(), "SHOP_PG_URL", "prod", "", "plain name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params, err := NewPostgresScanParams(envOf(tc.env), tc.dsnEnv, tc.environment, tc.catalog)
			assert.Nil(t, params)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.want)
				assert.NotContains(t, err.Error(), pgSecret)
			}
		})
	}
}

func TestPostgresScanParams_WriteDescriptor(t *testing.T) {
	params := newShopParams(t)
	dir := t.TempDir()

	_, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	file := filepath.Join(dir, "connections", "prod", "shop.json")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.JSONEq(t, `{"dsnEnv":"SHOP_PG_URL"}`, string(data))
	assert.True(t, strings.HasSuffix(string(data), "\n"))
	descriptor, err := dbcopy.DecodePostgresDescriptor(data)
	require.NoError(t, err)
	assert.Equal(t, "env:SHOP_PG_URL", descriptor.SourceURL())

	// Nothing the scan wrote names the password, the user or the host.
	for _, secret := range []string{pgSecret, "alice", "db.example.com"} {
		assert.NotContains(t, string(data), secret)
	}

	// Writing again replaces the file.
	again, err := NewPostgresScanParams(envOf(map[string]string{"OTHER_PG_URL": "postgres://u:p@h/shop"}), "OTHER_PG_URL", "prod", "shop")
	require.NoError(t, err)
	_, err = again.WriteDescriptor(dir)
	require.NoError(t, err)
	data, err = os.ReadFile(file)
	require.NoError(t, err)
	assert.JSONEq(t, `{"dsnEnv":"OTHER_PG_URL"}`, string(data))
}

func TestPostgresScanParams_WriteDescriptorErrors(t *testing.T) {
	params := newShopParams(t)

	t.Run("the folder cannot be created", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "connections"), []byte("a file"), 0o600))
		_, err := params.WriteDescriptor(dir)
		assert.ErrorContains(t, err, "create the connection descriptor folder")
	})
	t.Run("the file cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections", "prod", "shop.json"), 0o755))
		_, err := params.WriteDescriptor(dir)
		assert.ErrorContains(t, err, "write the connection descriptor")
	})
}

// fakeScanDB is the handle a schema scan reads through, over a two-table shop.
type fakeScanDB struct {
	dal.DB
	closed  int
	listErr error
}

func (f *fakeScanDB) Close() error { f.closed++; return nil }

func (f *fakeScanDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []dal.CollectionRef{dal.NewRootCollectionRef("Customer", ""), dal.NewRootCollectionRef("Order", "")}, nil
}

func (f *fakeScanDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	switch ref.Name() {
	case "Customer":
		return &dbschema.CollectionDef{Name: "Customer",
			Fields:     []dbschema.FieldDef{{Name: "CustomerId", Type: dbschema.Int}, {Name: "Email", Type: dbschema.String}},
			PrimaryKey: []dal.FieldName{"CustomerId"}}, nil
	case "Order":
		return &dbschema.CollectionDef{Name: "Order",
			Fields:     []dbschema.FieldDef{{Name: "OrderId", Type: dbschema.Int}, {Name: "CustomerId", Type: dbschema.Int}},
			PrimaryKey: []dal.FieldName{"OrderId"},
			ForeignKeys: []dbschema.ForeignKeyDef{{Name: "fk_order_customer", Fields: []dal.FieldName{"CustomerId"},
				ReferencedCollection: "Customer", ReferencedFields: []dal.FieldName{"CustomerId"}}}}, nil
	}
	return nil, fmt.Errorf("collection %q not found", ref.Name())
}

func (*fakeScanDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func (*fakeScanDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints"}
}

func (*fakeScanDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers"}
}

var _ dbcopy.SchemaScanDB = (*fakeScanDB)(nil)

func stubOpenSchemaScan(t *testing.T, stub func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error)) {
	t.Helper()
	original := openSchemaScan
	openSchemaScan = stub
	t.Cleanup(func() { openSchemaScan = original })
}

func TestScanDbCatalog_Postgres(t *testing.T) {
	db := &fakeScanDB{}
	var opened dbcopy.BackendRef
	stubOpenSchemaScan(t, func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		opened = ref
		return db, nil
	})
	params := newShopParams(t)

	catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgres}, params)
	require.NoError(t, err)
	require.NotNil(t, catalog)

	assert.Equal(t, "env:SHOP_PG_URL", opened.Raw)
	assert.Equal(t, "postgres", opened.Scheme)
	assert.Equal(t, 1, db.closed, "the connection pool is released")
	assert.Equal(t, "shop", catalog.ID)
	assert.Equal(t, DriverPostgres, catalog.Driver)

	require.Len(t, catalog.Schemas, 1)
	assert.Equal(t, "public", catalog.Schemas[0].ID)
	tables := catalog.Schemas[0].Tables
	require.Len(t, tables, 2)
	order := tables[1]
	assert.Equal(t, "Order", order.Name())
	require.Len(t, order.Columns, 2)
	assert.Equal(t, "CustomerId", order.Columns[1].Name)
	assert.Equal(t, []string{"OrderId"}, order.PrimaryKey.Columns)
	require.Len(t, order.ForeignKeys, 1)
	assert.Equal(t, "Customer", order.ForeignKeys[0].RefTable.Name())
	assert.Nil(t, order.RecordsCount, "the fake database does not run COUNT(*) natively, so nothing is counted")
	require.Len(t, tables[0].ReferencedBy, 1)

	// The catalog holds names and keys, never the connection.
	encoded, err := json.Marshal(catalog)
	require.NoError(t, err)
	for _, secret := range []string{pgSecret, "alice", "db.example.com", "SHOP_PG_URL"} {
		assert.NotContains(t, string(encoded), secret)
	}
}

func TestScanDbCatalog_PostgresErrors(t *testing.T) {
	t.Run("the connection parameters did not come from a variable", func(t *testing.T) {
		stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
			t.Fatal("nothing may be opened without a variable")
			return nil, nil
		})
		params := dbconnection.NewSQLite3ConnectionParams("/tmp/x.db", "shop", dbconnection.ModeReadOnly)
		catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgres}, params)
		assert.Nil(t, catalog)
		assert.ErrorContains(t, err, "--dsn-env")
	})
	t.Run("the database cannot be opened", func(t *testing.T) {
		cause := errors.New("connection refused")
		stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) { return nil, cause })
		catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgres}, newShopParams(t))
		assert.Nil(t, catalog)
		assert.ErrorIs(t, err, cause)
		assert.ErrorContains(t, err, "failed to open PostgreSQL")
	})
	t.Run("the scan fails and its error is classified, never the driver's words", func(t *testing.T) {
		cause := fmt.Errorf("lost connection to %s", shopEnv()["SHOP_PG_URL"])
		db := &fakeScanDB{listErr: cause}
		stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) { return db, nil })
		_, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgres}, newShopParams(t))
		if assert.Error(t, err) {
			assert.ErrorContains(t, err, "failed to get dbCatalog metadata")
			assert.EqualError(t, err, "failed to get dbCatalog metadata: the catalog could not be read (the server's own message is not shown: a driver can quote the connection string)")
			assert.NotContains(t, err.Error(), pgSecret)
			assert.NotContains(t, err.Error(), "lost connection", "the driver's words are not shown")
			assert.ErrorIs(t, err, cause, "the driver's own error is kept for errors.Is, never printed")
			assert.False(t, dbcopy.IsPostgresConnectionFailure(err), "a read the server refuses is not a failure to connect, so the scan does not exit 4 and the text does not point at the connection string")
		}
		assert.Equal(t, 1, db.closed, "the pool is released after a failed scan too")
	})
	t.Run("a read that fails because the connection failed is the connection failure, with the hint", func(t *testing.T) {
		for name, cause := range map[string]error{
			"the adapter's connection error": &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433", Database: "shop"},
			"a wrapped one":                  fmt.Errorf("list the schemas: %w", &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}),
		} {
			stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
				return &fakeScanDB{listErr: cause}, nil
			})
			_, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgres}, newShopParams(t))
			assert.EqualError(t, err, "failed to get dbCatalog metadata: the server could not be reached; the PostgreSQL connection string is read from the environment variable SHOP_PG_URL", name)
			assert.True(t, dbcopy.IsPostgresConnectionFailure(err), name)
			assert.ErrorIs(t, err, cause, name)
		}
	})
	t.Run("a read that ends by the clock or by a cancel is told as that, with the hint", func(t *testing.T) {
		for name, tc := range map[string]struct {
			cause error
			want  string
		}{
			"deadline": {context.DeadlineExceeded, "the attempt timed out"},
			"cancel":   {fmt.Errorf("list: %w", context.Canceled), "the attempt was cancelled"},
		} {
			stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
				return &fakeScanDB{listErr: tc.cause}, nil
			})
			_, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgres}, newShopParams(t))
			assert.EqualError(t, err, "failed to get dbCatalog metadata: "+tc.want+"; the PostgreSQL connection string is read from the environment variable SHOP_PG_URL", name)
			assert.True(t, dbcopy.IsPostgresConnectionFailure(err), name)
		}
	})
}
