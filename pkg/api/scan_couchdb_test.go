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
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCouchDBScanParamsExposesOnlySafeConnectionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
		host     string
	}{
		{"a host without a port", "couchdb://couch.example.com/orders", "couch.example.com"},
		{"an IPv6 loopback without a port", "couchdb://[::1]/orders", "::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				if name == "DATATUG_TEST_COUCHDB" {
					return tc.endpoint, true
				}
				return "", false
			}
			params, err := NewCouchDBScanParams(lookup, "DATATUG_TEST_COUCHDB", "local", "orders")
			require.NoError(t, err)
			assert.Equal(t, tc.host, params.Server())
			assert.Zero(t, params.Port(), "the endpoint names no port")
			assert.Equal(t, dbconnection.ModeReadOnly, params.Mode(), "a scan only reads")
			assert.Equal(t, "env:DATATUG_TEST_COUCHDB", fmt.Sprintf("%#v", params), "formatting the parameters names the variable, never the endpoint")
			assert.NotContains(t, fmt.Sprintf("%#v", params), tc.host)
		})
	}
}

func TestNewCouchDBScanParams(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_COUCHDB" {
			return "couchdb+http://127.0.0.1:5984/orders", true
		}
		return "", false
	}
	params, err := NewCouchDBScanParams(lookup, "DATATUG_TEST_COUCHDB", "local", "orders")
	require.NoError(t, err)
	assert.Equal(t, DriverCouchDB, params.Driver())
	assert.Equal(t, "127.0.0.1", params.Server())
	assert.Equal(t, 5984, params.Port())
	assert.Equal(t, "orders", params.Catalog())
	assert.Equal(t, "", params.User(), "credentials are separate from the endpoint")
	assert.Equal(t, "env:DATATUG_TEST_COUCHDB", params.ConnectionString())
	assert.Equal(t, "env:DATATUG_TEST_COUCHDB", params.String())
	assert.Equal(t, "connections/local/orders.json", params.Path(), "a project records the descriptor, never the endpoint")
	assert.NotContains(t, params.LogTarget(), "127.0.0.1")
	assert.Equal(t, "env:DATATUG_TEST_COUCHDB", params.SourceRef().Raw)

	// The descriptor the scan writes names the variable and nothing else.
	dir := t.TempDir()
	undo, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, "connections", "local", "orders.json"))
	require.NoError(t, err)
	var written map[string]any
	require.NoError(t, json.Unmarshal(body, &written))
	assert.Equal(t, map[string]any{"dsnEnv": "DATATUG_TEST_COUCHDB"}, written)
	assert.NotContains(t, string(body), "127.0.0.1")
	require.NoError(t, undo())
	_, err = os.Stat(filepath.Join(dir, "connections", "local", "orders.json"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewCouchDBScanParamsRefusesAWrongSourceOrName(t *testing.T) {
	lookup := func(name string) (string, bool) {
		switch name {
		case "DATATUG_TEST_COUCHDB":
			return "couchdb+http://127.0.0.1:5984/orders", true
		case "DATATUG_TEST_COUCHDB_PG":
			return "postgres://reader:secret@db.example.com/shop", true
		case "DATATUG_TEST_COUCHDB_CRED":
			return "couchdb://reader:secret@db.example.com/orders", true
		}
		return "", false
	}
	for _, tc := range []struct{ variable, environment, catalog, want string }{
		{"DATATUG_TEST_COUCHDB_PG", "local", "orders", "must hold a couchdb"},
		{"DATATUG_TEST_COUCHDB_CRED", "local", "orders", "does not hold a supported source URL"},
		{"DATATUG_TEST_COUCHDB_UNSET", "local", "orders", "is not set"},
		{"DATATUG_TEST_COUCHDB", "../local", "orders", "must each be a plain name"},
	} {
		_, err := NewCouchDBScanParams(lookup, tc.variable, tc.environment, tc.catalog)
		require.Error(t, err)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, err.Error(), "secret")
	}
}

// couchScanDB is the one collection of a native CouchDB database: no field schema,
// no constraints, no referrers, and real Mango indexes.
type couchScanDB struct {
	dal.DB
	closed int
}

func (f *couchScanDB) Close() error { f.closed++; return nil }

func (*couchScanDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("orders", "")}, nil
}

func (*couchScanDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{Name: ref.Name(), PrimaryKey: []dal.FieldName{"_id"}}, nil
}

func (*couchScanDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return []dbschema.IndexDef{{Name: "status-score-index", Collection: "orders", Fields: []dal.FieldName{"status", "score"}}}, nil
}

func (*couchScanDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "couchdb"}
}

func (*couchScanDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "couchdb"}
}

var _ dbcopy.SchemaScanDB = (*couchScanDB)(nil)

func TestScanDbCatalog_CouchDBUsesTheGenericSchemaReader(t *testing.T) {
	db := &couchScanDB{}
	var opened dbcopy.BackendRef
	stubOpenSchemaScan(t, func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		opened = ref
		return db, nil
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_COUCHDB" {
			return "couchdb://couch.example.com:6984/orders", true
		}
		return "", false
	}
	params, err := NewCouchDBScanParams(lookup, "DATATUG_TEST_COUCHDB", "local", "orders")
	require.NoError(t, err)

	catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverCouchDB, Host: "couch.example.com", Port: 6984}, params)
	require.NoError(t, err)
	require.NotNil(t, catalog)
	assert.Equal(t, "env:DATATUG_TEST_COUCHDB", opened.Raw)
	assert.Equal(t, "orders", catalog.ID)
	assert.Equal(t, DriverCouchDB, catalog.Driver)
	assert.Equal(t, 1, db.closed, "the connection pool is released")

	require.Len(t, catalog.Schemas, 1)
	assert.Equal(t, ServerDefaultSchema, catalog.Schemas[0].ID)
	require.Len(t, catalog.Schemas[0].Tables, 1)
	table := catalog.Schemas[0].Tables[0]
	assert.Equal(t, "orders", table.Name())
	assert.Empty(t, table.Columns, "native CouchDB documents declare no field schema")
	assert.Empty(t, table.ForeignKeys, "CouchDB has no foreign keys")
}

func TestScanDbCatalog_CouchDBFailureIsClassified(t *testing.T) {
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return nil, &testConnectionError{message: "dial tcp couch.example.com:6984: connection refused"}
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_COUCHDB" {
			return "couchdb://couch.example.com:6984/orders", true
		}
		return "", false
	}
	params, err := NewCouchDBScanParams(lookup, "DATATUG_TEST_COUCHDB", "local", "orders")
	require.NoError(t, err)
	_, err = scanDbCatalog(datatug.ServerRef{Driver: DriverCouchDB}, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open CouchDB")
	assert.NotContains(t, err.Error(), "password")
}

func TestScanDbCatalog_CouchDBMetadataFailureIsClassified(t *testing.T) {
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return &fakeScanDB{listErr: errors.New("server said " + "couch.example.com/orders is broken")}, nil
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_COUCHDB" {
			return "couchdb://couch.example.com:6984/orders", true
		}
		return "", false
	}
	params, err := NewCouchDBScanParams(lookup, "DATATUG_TEST_COUCHDB", "local", "orders")
	require.NoError(t, err)
	_, err = scanDbCatalog(datatug.ServerRef{Driver: DriverCouchDB}, params)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "couch.example.com", "a read failure never quotes the endpoint")
	assert.Contains(t, err.Error(), "failed to get dbCatalog metadata")
	assert.Contains(t, err.Error(), "the catalog could not be read")
}

func TestScanDbCatalog_CouchDBNeedsParametersOfAnEnvironmentSource(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_COUCHDB" {
			return "couchdb://couch.example.com:6984/orders", true
		}
		return "", false
	}
	params, err := NewCouchDBScanParams(lookup, "DATATUG_TEST_COUCHDB", "local", "orders")
	require.NoError(t, err)
	// Hiding the concrete type's SourceRef method is what a caller that built
	// the parameters by hand (or from a literal endpoint) looks like to the scan.
	catalog, err := scanCouchDBCatalog(context.Background(), struct{ dbconnection.Params }{params})
	require.Error(t, err)
	assert.Nil(t, catalog)
	assert.Contains(t, err.Error(), "needs connection parameters built from an environment variable")
	assert.NotContains(t, err.Error(), "couch.example.com")
}

// testConnectionError stands in for an adapter failure whose own text quotes the
// endpoint; the scan must classify it, not show it.
type testConnectionError struct{ message string }

func (e *testConnectionError) Error() string { return e.message }

func TestSourceURLFromCatalogCouchDB(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/local/orders.json", `{"dsnEnv":"DATATUG_COUCHDB_ORDERS"}`)
	t.Setenv("DATATUG_COUCHDB_ORDERS", "couchdb+http://127.0.0.1:5984/orders")
	catalog := func(path string) datatug.DbCatalog {
		return datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "orders"}},
			Driver:      DriverCouchDB,
			Path:        path,
		}}
	}

	// A descriptor names the operator's variable; the endpoint is read from the
	// environment only when the source is opened.
	url, err := sourceURLFromCatalog(catalog("connections/local/orders.json"), dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_COUCHDB_ORDERS", url)
	assert.NotContains(t, url, "127.0.0.1")

	// A project that records a literal endpoint (or an env reference) is refused
	// before the URL parser or the environment is read: it never reaches I/O.
	originalParse := dbcopyParse
	t.Cleanup(func() { dbcopyParse = originalParse })
	dbcopyParse = func(raw string) (dbcopy.BackendRef, error) {
		t.Fatalf("a literal catalog path reached the URL parser: %q", raw)
		return dbcopy.BackendRef{}, nil
	}
	for _, literal := range []string{
		"couchdb+http://evil.example.com/orders",
		"couchdb://reader:s3cret@evil.example.com/orders",
		"env:DATATUG_COUCHDB_ORDERS",
		"postgres://evil.example.com/shop",
	} {
		_, err = sourceURLFromCatalog(catalog(literal), dir)
		require.Error(t, err, literal)
		assert.NotContains(t, err.Error(), "s3cret")
	}
	dbcopyParse = originalParse

	// A variable that holds another engine is refused by scheme before any
	// authenticated request.
	writeDescriptor(t, dir, "connections/local/pg.json", `{"dsnEnv":"DATATUG_COUCHDB_PG"}`)
	t.Setenv("DATATUG_COUCHDB_PG", "postgres://alice:s3cret@db.example.com/shop")
	_, err = sourceURLFromCatalog(catalog("connections/local/pg.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold a couchdb:// URL")
	assert.NotContains(t, err.Error(), "s3cret")
	assert.NotContains(t, err.Error(), "alice")

	// A variable that is not set is refused when the catalog is resolved.
	writeDescriptor(t, dir, "connections/local/unset.json", `{"dsnEnv":"DATATUG_COUCHDB_UNSET"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/unset.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATATUG_COUCHDB_UNSET is not set")

	// A missing descriptor is reported against the catalog; the route's answer
	// replaces the path with a fixed sentence (sourceLookupAnswer).
	_, err = sourceURLFromCatalog(catalog("connections/local/missing.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `catalog "orders"`)
	assert.Contains(t, err.Error(), "open CouchDB connection descriptor")
	assert.Equal(t, `catalog "orders": the CouchDB connection descriptor cannot be read`, sourceLookupAnswer(err).Error())

	// A malformed descriptor (here: a literal password field) is refused, and
	// neither the field's value nor a credential reaches the message.
	writeDescriptor(t, dir, "connections/local/bad.json", `{"dsnEnv":"DATATUG_COUCHDB_BAD","password":"s3cret"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/bad.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown CouchDB connection field")
	assert.NotContains(t, err.Error(), "s3cret")

	// A descriptor may not select a variable the operator did not set aside.
	writeDescriptor(t, dir, "connections/local/foreign.json", `{"dsnEnv":"PROD_DATABASE_URL"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/foreign.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROD_DATABASE_URL")
	assert.Contains(t, err.Error(), dbcopy.DescriptorEnvPrefix)
	assert.Contains(t, err.Error(), dbcopy.DescriptorEnvAllowList)

	// Neither a missing descriptor nor a path outside the project is read.
	_, err = sourceURLFromCatalog(catalog(""), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no connection descriptor path")
	for _, outside := range []string{"~/shop.json", "../shop.json", "/etc/shop.json"} {
		_, err = sourceURLFromCatalog(catalog(outside), dir)
		require.Error(t, err, outside)
		assert.NotContains(t, err.Error(), outside)
	}
}

func TestDefaultSchemaForCouchDB(t *testing.T) {
	assert.Equal(t, ServerDefaultSchema, DefaultSchemaForDriver(DriverCouchDB))
	assert.Equal(t, "orders", PolicyCollectionName("default.orders", DriverCouchDB))
	assert.Equal(t, "orders", PolicyCollectionName("orders", DriverCouchDB))
	unresolved := SourceUnresolvedError{Driver: DriverCouchDB}
	assert.True(t, strings.HasPrefix(unresolved.Error(), "its catalog path"))
}
