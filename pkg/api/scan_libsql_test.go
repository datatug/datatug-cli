package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLibSQLScanParams(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_LIBSQL" {
			return "libsql+http://127.0.0.1:8080/team/db", true
		}
		return "", false
	}
	params, err := NewLibSQLScanParams(lookup, "DATATUG_TEST_LIBSQL", "local", "main")
	require.NoError(t, err)
	assert.Equal(t, DriverLibSQL, params.Driver())
	assert.Equal(t, "127.0.0.1", params.Server())
	assert.Equal(t, 8080, params.Port())
	assert.Equal(t, "main", params.Catalog())
	assert.Equal(t, "", params.User(), "credentials are separate from the endpoint")
	assert.Equal(t, "env:DATATUG_TEST_LIBSQL", params.ConnectionString())
	assert.Equal(t, "env:DATATUG_TEST_LIBSQL", params.String())
	assert.Equal(t, "connections/local/main.json", params.Path(), "a project records the descriptor, never the endpoint")
	assert.NotContains(t, params.LogTarget(), "127.0.0.1")
	assert.Equal(t, "env:DATATUG_TEST_LIBSQL", params.SourceRef().Raw)

	// The descriptor the scan writes names the variable and nothing else: no
	// endpoint, and no token (the token is never in the descriptor at all).
	dir := t.TempDir()
	undo, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, "connections", "local", "main.json"))
	require.NoError(t, err)
	var written map[string]any
	require.NoError(t, json.Unmarshal(body, &written))
	assert.Equal(t, map[string]any{"dsnEnv": "DATATUG_TEST_LIBSQL"}, written)
	assert.NotContains(t, string(body), "127.0.0.1")
	require.NoError(t, undo())
	_, err = os.Stat(filepath.Join(dir, "connections", "local", "main.json"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewLibSQLScanParamsRefusesAWrongSourceOrName(t *testing.T) {
	lookup := func(name string) (string, bool) {
		switch name {
		case "DATATUG_TEST_LIBSQL":
			return "libsql+http://127.0.0.1:8080", true
		case "DATATUG_TEST_LIBSQL_POSTGREST":
			return "postgrest://api.example.com/rest/v1", true
		case "DATATUG_TEST_LIBSQL_TOKEN":
			return "libsql://reader:secret@db.example.com", true
		}
		return "", false
	}
	for _, tc := range []struct{ variable, environment, catalog, want string }{
		{"DATATUG_TEST_LIBSQL_POSTGREST", "local", "main", "must hold a libsql"},
		{"DATATUG_TEST_LIBSQL_TOKEN", "local", "main", "does not hold a supported source URL"},
		{"DATATUG_TEST_LIBSQL_UNSET", "local", "main", "is not set"},
		{"DATATUG_TEST_LIBSQL", "../local", "main", "must each be a plain name"},
	} {
		_, err := NewLibSQLScanParams(lookup, tc.variable, tc.environment, tc.catalog)
		require.Error(t, err)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, err.Error(), "secret")
	}
}

// libSQLScanDB is a libSQL database's schema view: SQLite tables with their real
// keys and indexes, and no invented "id" column.
type libSQLScanDB struct {
	dal.DB
	closed int
}

func (f *libSQLScanDB) Close() error { f.closed++; return nil }

func (*libSQLScanDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("items", "")}, nil
}

func (*libSQLScanDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{
		Name:       ref.Name(),
		PrimaryKey: []dal.FieldName{"item_id"},
		Fields: []dbschema.FieldDef{
			{Name: "item_id", Type: dbschema.Int},
			{Name: "title", Type: dbschema.String, Nullable: true},
			{Name: "metric", Type: dbschema.String, Nullable: true},
		},
	}, nil
}

func (*libSQLScanDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return []dbschema.IndexDef{{Name: "items_title", Collection: "items", Fields: []dal.FieldName{"title"}}}, nil
}

func (*libSQLScanDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, nil
}

func (*libSQLScanDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, nil
}

var _ dbcopy.SchemaScanDB = (*libSQLScanDB)(nil)

func TestScanDbCatalog_LibSQLUsesTheGenericSchemaReader(t *testing.T) {
	db := &libSQLScanDB{}
	var opened dbcopy.BackendRef
	stubOpenSchemaScan(t, func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		opened = ref
		return db, nil
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_LIBSQL" {
			return "libsql://db.example.com", true
		}
		return "", false
	}
	params, err := NewLibSQLScanParams(lookup, "DATATUG_TEST_LIBSQL", "local", "main")
	require.NoError(t, err)

	catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverLibSQL}, params)
	require.NoError(t, err)
	require.NotNil(t, catalog)
	assert.Equal(t, "env:DATATUG_TEST_LIBSQL", opened.Raw)
	assert.Equal(t, "main", catalog.ID)
	assert.Equal(t, DriverLibSQL, catalog.Driver)
	assert.Equal(t, 1, db.closed, "the connection pool is released")

	require.Len(t, catalog.Schemas, 1)
	assert.Equal(t, "main", catalog.Schemas[0].ID,
		"a libSQL scan records SQLite's implicit database as \"main\", not the fixed \"default\" schema")
	require.Len(t, catalog.Schemas[0].Tables, 1)
	table := catalog.Schemas[0].Tables[0]
	assert.Equal(t, "items", table.Name())
	assert.Equal(t, "main", catalog.Schemas[0].ID, "the persisted columns path is dbmodels/<catalog>/main/...")
	require.Len(t, table.Columns, 3, "the native SQLite columns are reported")
	assert.Equal(t, "title", table.Columns[1].Name)
	assert.Equal(t, "item_id", table.Columns[0].Name)
	assert.NotEqual(t, "id", table.Columns[0].Name, "no rowid pseudo-key is invented")
	require.NotEmpty(t, table.PrimaryKey, "the declared primary key is reported")
	require.Len(t, table.Indexes, 1, "native SQLite indexes are reported")
	assert.Equal(t, "items_title", table.Indexes[0].Name)
}

func TestScanDbCatalog_LibSQLFailureIsClassified(t *testing.T) {
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return nil, &testConnectionError{message: "dial tcp db.example.com:8080: connection refused"}
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_LIBSQL" {
			return "libsql://db.example.com", true
		}
		return "", false
	}
	params, err := NewLibSQLScanParams(lookup, "DATATUG_TEST_LIBSQL", "local", "main")
	require.NoError(t, err)
	_, err = scanDbCatalog(datatug.ServerRef{Driver: DriverLibSQL}, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open libSQL")
	assert.NotContains(t, err.Error(), "db.example.com")
}

func TestSourceURLFromCatalogLibSQL(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/local/main.json", `{"dsnEnv":"DATATUG_LIBSQL_MAIN"}`)
	t.Setenv("DATATUG_LIBSQL_MAIN", "libsql+http://127.0.0.1:8080")
	catalog := func(path string) datatug.DbCatalog {
		return datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "main"}},
			Driver:      DriverLibSQL,
			Path:        path,
		}}
	}

	// A descriptor names the operator's variable; the base address is read from
	// the environment only when the source is opened.
	source, err := sourceURLFromCatalog(catalog("connections/local/main.json"), dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_LIBSQL_MAIN", source)
	assert.NotContains(t, source, "127.0.0.1")

	// A project that records a literal endpoint (or an env reference) is refused
	// before the URL parser or the environment is read: it never reaches I/O.
	originalParse := dbcopyParse
	t.Cleanup(func() { dbcopyParse = originalParse })
	dbcopyParse = func(raw string) (dbcopy.BackendRef, error) {
		t.Fatalf("a literal catalog path reached the URL parser: %q", raw)
		return dbcopy.BackendRef{}, nil
	}
	for _, literal := range []string{
		"libsql+http://evil.example.com",
		"libsql://reader:s3cret@evil.example.com",
		"env:DATATUG_LIBSQL_MAIN",
		"postgres://evil.example.com/shop",
	} {
		_, err = sourceURLFromCatalog(catalog(literal), dir)
		require.Error(t, err, literal)
		assert.NotContains(t, err.Error(), "s3cret")
	}
	dbcopyParse = originalParse

	// A variable that holds another engine is refused by scheme before any
	// authenticated request.
	writeDescriptor(t, dir, "connections/local/pg.json", `{"dsnEnv":"DATATUG_LIBSQL_PG"}`)
	t.Setenv("DATATUG_LIBSQL_PG", "postgres://db.example.com/shop")
	_, err = sourceURLFromCatalog(catalog("connections/local/pg.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold a libsql:// URL")
	assert.NotContains(t, err.Error(), "db.example.com")

	// A variable that is not set is refused when the catalog is resolved.
	writeDescriptor(t, dir, "connections/local/unset.json", `{"dsnEnv":"DATATUG_LIBSQL_UNSET"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/unset.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATATUG_LIBSQL_UNSET is not set")

	// A missing descriptor is reported against the catalog; the route's answer
	// replaces the path with a fixed sentence (sourceLookupAnswer).
	_, err = sourceURLFromCatalog(catalog("connections/local/missing.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `catalog "main"`)
	assert.Equal(t, `catalog "main": the libSQL connection descriptor cannot be read`, sourceLookupAnswer(err).Error())

	// A malformed descriptor (here: a literal token field) is refused, and the
	// token's value never reaches the message.
	writeDescriptor(t, dir, "connections/local/bad.json", `{"dsnEnv":"DATATUG_LIBSQL_BAD","token":"s3cret"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/bad.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown libSQL connection field")
	assert.NotContains(t, err.Error(), "s3cret")
}

func TestLibSQLServerRecordsNoHost(t *testing.T) {
	assert.False(t, serverHasHost(DriverLibSQL), "a libSQL catalog records a descriptor, not an endpoint")
	assert.Equal(t, "main", DefaultSchemaForDriver(DriverLibSQL))
	assert.Equal(t, "libSQL", driverTitle(DriverLibSQL))
}
