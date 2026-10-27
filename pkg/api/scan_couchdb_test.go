package api

import (
	"context"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.Equal(t, "couchdb+http://127.0.0.1:5984/orders", params.Path())
	assert.NotContains(t, params.LogTarget(), "127.0.0.1")
	assert.Equal(t, "env:DATATUG_TEST_COUCHDB", params.SourceRef().Raw)
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

// testConnectionError stands in for an adapter failure whose own text quotes the
// endpoint; the scan must classify it, not show it.
type testConnectionError struct{ message string }

func (e *testConnectionError) Error() string { return e.message }

func TestSourceURLFromCatalogCouchDB(t *testing.T) {
	url, err := sourceURLFromCatalog(datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "orders"}},
		Driver:      DriverCouchDB,
		Path:        "couchdb+http://127.0.0.1:5984/orders",
	}}, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "couchdb+http://127.0.0.1:5984/orders", url)

	for _, tc := range []struct{ path, want string }{
		{"", "no endpoint"},
		{"env:DATATUG_COUCHDB", "not an environment variable"},
		{"postgres://db.example.com/shop", "not a postgres source"},
		{"couchdb://reader:secret@db.example.com/orders", "credentials are not supported"},
		{"couchdb://db.example.com/a/b", "database name must be one CouchDB database name"},
	} {
		_, err = sourceURLFromCatalog(datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: DriverCouchDB, Path: tc.path}}, t.TempDir())
		require.Error(t, err, tc.path)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, err.Error(), "secret")
	}
}

func TestDefaultSchemaForCouchDB(t *testing.T) {
	assert.Equal(t, ServerDefaultSchema, DefaultSchemaForDriver(DriverCouchDB))
	assert.Equal(t, "orders", PolicyCollectionName("default.orders", DriverCouchDB))
	assert.Equal(t, "orders", PolicyCollectionName("orders", DriverCouchDB))
	unresolved := SourceUnresolvedError{Driver: DriverCouchDB}
	assert.True(t, strings.HasPrefix(unresolved.Error(), "its catalog path"))
}
