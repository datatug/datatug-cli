package api

import (
	"context"
	"encoding/json"
	"errors"
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

func TestNewPostgRESTScanParams(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_POSTGREST" {
			return "postgrest+http://127.0.0.1:3000/rest/v1?schema=app", true
		}
		return "", false
	}
	params, err := NewPostgRESTScanParams(lookup, "DATATUG_TEST_POSTGREST", "local", "items")
	require.NoError(t, err)
	assert.Equal(t, DriverPostgREST, params.Driver())
	assert.Equal(t, "127.0.0.1", params.Server())
	assert.Equal(t, 3000, params.Port())
	assert.Equal(t, "items", params.Catalog())
	assert.Equal(t, "", params.User(), "credentials are separate from the endpoint")
	assert.Equal(t, "env:DATATUG_TEST_POSTGREST", params.ConnectionString())
	assert.Equal(t, "env:DATATUG_TEST_POSTGREST", params.String())
	assert.Equal(t, "connections/local/items.json", params.Path(), "a project records the descriptor, never the endpoint")
	assert.NotContains(t, params.LogTarget(), "127.0.0.1")
	assert.Equal(t, "env:DATATUG_TEST_POSTGREST", params.SourceRef().Raw)

	// The descriptor the scan writes names the variable and nothing else.
	dir := t.TempDir()
	undo, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, "connections", "local", "items.json"))
	require.NoError(t, err)
	var written map[string]any
	require.NoError(t, json.Unmarshal(body, &written))
	assert.Equal(t, map[string]any{"dsnEnv": "DATATUG_TEST_POSTGREST"}, written)
	assert.NotContains(t, string(body), "127.0.0.1")
	require.NoError(t, undo())
	_, err = os.Stat(filepath.Join(dir, "connections", "local", "items.json"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewPostgRESTScanParamsRefusesAWrongSourceOrName(t *testing.T) {
	lookup := func(name string) (string, bool) {
		switch name {
		case "DATATUG_TEST_POSTGREST":
			return "postgrest+http://127.0.0.1:3000/rest/v1", true
		case "DATATUG_TEST_POSTGREST_COUCH":
			return "couchdb://127.0.0.1:5984/orders", true
		case "DATATUG_TEST_POSTGREST_CRED":
			return "postgrest://reader:secret@api.example.com/rest/v1", true
		}
		return "", false
	}
	for _, tc := range []struct{ variable, environment, catalog, want string }{
		{"DATATUG_TEST_POSTGREST_COUCH", "local", "items", "must hold a postgrest"},
		{"DATATUG_TEST_POSTGREST_CRED", "local", "items", "does not hold a supported source URL"},
		{"DATATUG_TEST_POSTGREST_UNSET", "local", "items", "is not set"},
		{"DATATUG_TEST_POSTGREST", "../local", "items", "must each be a plain name"},
	} {
		_, err := NewPostgRESTScanParams(lookup, tc.variable, tc.environment, tc.catalog)
		require.Error(t, err)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, err.Error(), "secret")
	}
}

// postgrestScanDB is the API root's schema view: native relations with a declared
// primary key, and no index, constraint or referrer metadata.
type postgrestScanDB struct {
	dal.DB
	closed int
}

func (f *postgrestScanDB) Close() error { f.closed++; return nil }

func (*postgrestScanDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("items", "")}, nil
}

func (*postgrestScanDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name:       ref.Name(),
		PrimaryKey: []dal.FieldName{"id"},
		Fields: []dbschema.FieldDef{
			{Name: "id", Type: dbschema.Int},
			{Name: "title", Type: dbschema.String, Nullable: true},
		},
	}, nil
}

func (*postgrestScanDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListIndexes", Backend: "postgrest"}
}

func (*postgrestScanDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "postgrest"}
}

func (*postgrestScanDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "postgrest"}
}

var _ dbcopy.SchemaScanDB = (*postgrestScanDB)(nil)

func TestScanDbCatalog_PostgRESTUsesTheGenericSchemaReader(t *testing.T) {
	db := &postgrestScanDB{}
	var opened dbcopy.BackendRef
	stubOpenSchemaScan(t, func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		opened = ref
		return db, nil
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_POSTGREST" {
			return "postgrest://api.example.com/rest/v1", true
		}
		return "", false
	}
	params, err := NewPostgRESTScanParams(lookup, "DATATUG_TEST_POSTGREST", "local", "items")
	require.NoError(t, err)

	catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverPostgREST, Host: "api.example.com"}, params)
	require.NoError(t, err)
	require.NotNil(t, catalog)
	assert.Equal(t, "env:DATATUG_TEST_POSTGREST", opened.Raw)
	assert.Equal(t, "items", catalog.ID)
	assert.Equal(t, DriverPostgREST, catalog.Driver)
	assert.Equal(t, 1, db.closed, "the connection pool is released")

	require.Len(t, catalog.Schemas, 1)
	assert.Equal(t, ServerDefaultSchema, catalog.Schemas[0].ID)
	require.Len(t, catalog.Schemas[0].Tables, 1)
	table := catalog.Schemas[0].Tables[0]
	assert.Equal(t, "items", table.Name())
	require.Len(t, table.Columns, 2, "the OpenAPI document declares the fields")
	assert.Equal(t, "title", table.Columns[1].Name)
	assert.Empty(t, table.ForeignKeys, "PostgREST exposes no foreign keys")
}

func TestScanDbCatalog_PostgRESTFailureIsClassified(t *testing.T) {
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return nil, &testConnectionError{message: "dial tcp api.example.com:3000: connection refused"}
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_POSTGREST" {
			return "postgrest://api.example.com/rest/v1", true
		}
		return "", false
	}
	params, err := NewPostgRESTScanParams(lookup, "DATATUG_TEST_POSTGREST", "local", "items")
	require.NoError(t, err)
	_, err = scanDbCatalog(datatug.ServerRef{Driver: DriverPostgREST}, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open PostgREST")
	assert.NotContains(t, err.Error(), "api.example.com")
}

func TestSourceURLFromCatalogPostgREST(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/local/items.json", `{"dsnEnv":"DATATUG_POSTGREST_ITEMS"}`)
	t.Setenv("DATATUG_POSTGREST_ITEMS", "postgrest+http://127.0.0.1:3000/rest/v1")
	catalog := func(path string) datatug.DbCatalog {
		return datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "items"}},
			Driver:      DriverPostgREST,
			Path:        path,
		}}
	}

	// A descriptor names the operator's variable; the API root is read from the
	// environment only when the source is opened.
	url, err := sourceURLFromCatalog(catalog("connections/local/items.json"), dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_POSTGREST_ITEMS", url)
	assert.NotContains(t, url, "127.0.0.1")

	// A project that records a literal API root (or an env reference) is refused
	// before the URL parser or the environment is read: it never reaches I/O.
	originalParse := dbcopyParse
	t.Cleanup(func() { dbcopyParse = originalParse })
	dbcopyParse = func(raw string) (dbcopy.BackendRef, error) {
		t.Fatalf("a literal catalog path reached the URL parser: %q", raw)
		return dbcopy.BackendRef{}, nil
	}
	for _, literal := range []string{
		"postgrest+http://evil.example.com/rest/v1",
		"postgrest://reader:s3cret@evil.example.com/rest/v1",
		"env:DATATUG_POSTGREST_ITEMS",
		"postgres://evil.example.com/shop",
	} {
		_, err = sourceURLFromCatalog(catalog(literal), dir)
		require.Error(t, err, literal)
		assert.NotContains(t, err.Error(), "s3cret")
	}
	dbcopyParse = originalParse

	// A variable that holds another engine is refused by scheme before any
	// authenticated request.
	writeDescriptor(t, dir, "connections/local/couch.json", `{"dsnEnv":"DATATUG_POSTGREST_COUCH"}`)
	t.Setenv("DATATUG_POSTGREST_COUCH", "couchdb://127.0.0.1:5984/orders")
	_, err = sourceURLFromCatalog(catalog("connections/local/couch.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold a postgrest:// URL")
	assert.NotContains(t, err.Error(), "127.0.0.1")

	// A variable that is not set is refused when the catalog is resolved.
	writeDescriptor(t, dir, "connections/local/unset.json", `{"dsnEnv":"DATATUG_POSTGREST_UNSET"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/unset.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATATUG_POSTGREST_UNSET is not set")

	// A missing descriptor is reported against the catalog; the route's answer
	// replaces the path with a fixed sentence (sourceLookupAnswer).
	_, err = sourceURLFromCatalog(catalog("connections/local/missing.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `catalog "items"`)
	assert.Contains(t, err.Error(), "open PostgREST connection descriptor")
	assert.Equal(t, `catalog "items": the PostgREST connection descriptor cannot be read`, sourceLookupAnswer(err).Error())

	// A malformed descriptor (here: a literal token field) is refused, and neither
	// the field's value nor a credential reaches the message.
	writeDescriptor(t, dir, "connections/local/bad.json", `{"dsnEnv":"DATATUG_POSTGREST_BAD","token":"s3cret"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/bad.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown PostgREST connection field")
	assert.NotContains(t, err.Error(), "s3cret")

	// A descriptor may not select a variable the operator did not set aside.
	writeDescriptor(t, dir, "connections/local/foreign.json", `{"dsnEnv":"PROD_POSTGREST_URL"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/foreign.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROD_POSTGREST_URL")
	assert.Contains(t, err.Error(), dbcopy.DescriptorEnvPrefix)
	assert.Contains(t, err.Error(), dbcopy.DescriptorEnvAllowList)

	// Neither a missing descriptor nor a path outside the project is read.
	_, err = sourceURLFromCatalog(catalog(""), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no connection descriptor path")
	for _, outside := range []string{"~/api.json", "../api.json", "/etc/api.json"} {
		_, err = sourceURLFromCatalog(catalog(outside), dir)
		require.Error(t, err, outside)
		assert.NotContains(t, err.Error(), outside)
	}
}

func TestDefaultSchemaForPostgREST(t *testing.T) {
	assert.Equal(t, ServerDefaultSchema, DefaultSchemaForDriver(DriverPostgREST))
	assert.Equal(t, "items", PolicyCollectionName("default.items", DriverPostgREST))
	assert.Equal(t, "items", PolicyCollectionName("items", DriverPostgREST))
	unresolved := SourceUnresolvedError{Driver: DriverPostgREST}
	assert.True(t, strings.HasPrefix(unresolved.Error(), "its catalog path"))
}

func TestPostgRESTScanParamsAccessorsAndTitles(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_POSTGREST" {
			return "postgrest://api.example.com/rest/v1", true
		}
		return "", false
	}
	params, err := NewPostgRESTScanParams(lookup, "DATATUG_TEST_POSTGREST", "local", "items")
	require.NoError(t, err)
	assert.Equal(t, dbconnection.ModeReadOnly, params.Mode())
	assert.Equal(t, params.String(), params.GoString(), "%#v must not print the URL")
	assert.Equal(t, "CouchDB", driverTitle(DriverCouchDB))
	assert.Equal(t, "PostgREST", driverTitle(DriverPostgREST))
}

func TestScanDbCatalog_PostgRESTNeedsItsOwnParameters(t *testing.T) {
	_, err := scanPostgRESTCatalog(context.Background(), dbconnection.NewSQLite3ConnectionParams("x.db", "items", dbconnection.ModeReadOnly))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs connection parameters built from an environment variable")
}

// postgrestScanFailingDB lists no collections: the generic scanner must classify
// the failure the way every other scan does.
type postgrestScanFailingDB struct{ postgrestScanDB }

func (*postgrestScanFailingDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return nil, errors.New("boom")
}

func TestScanDbCatalog_PostgRESTScannerFailureIsClassified(t *testing.T) {
	db := &postgrestScanFailingDB{}
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return db, nil
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_POSTGREST" {
			return "postgrest://api.example.com/rest/v1", true
		}
		return "", false
	}
	params, err := NewPostgRESTScanParams(lookup, "DATATUG_TEST_POSTGREST", "local", "items")
	require.NoError(t, err)
	_, err = scanDbCatalog(datatug.ServerRef{Driver: DriverPostgREST}, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get dbCatalog metadata")
}
