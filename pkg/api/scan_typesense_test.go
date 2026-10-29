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
	"github.com/dal-go/dalgo2typesense"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTypesenseScanParamsExposesOnlySafeConnectionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
		host     string
		port     int
	}{
		{"an IPv4 origin with its port", "typesense+http://127.0.0.1:8108/books", "127.0.0.1", 8108},
		{"an IPv6 loopback with its port", "typesense+http://[::1]:8108/books", "::1", 8108},
		{"an origin without a port", "typesense://search.example.com/books", "search.example.com", 0},
		{"an IPv6 loopback without a port", "typesense://[::1]/books", "::1", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				if name == "DATATUG_TEST_TYPESENSE" {
					return tc.endpoint, true
				}
				return "", false
			}
			params, err := NewTypesenseScanParams(lookup, "DATATUG_TEST_TYPESENSE", "local", "books")
			require.NoError(t, err)
			assert.Equal(t, DriverTypesense, params.Driver())
			assert.Equal(t, dbconnection.ModeReadOnly, params.Mode(), "a scan only reads")
			assert.Equal(t, tc.host, params.Server())
			assert.Equal(t, tc.port, params.Port())
			assert.Equal(t, "books", params.Catalog())
			assert.Equal(t, "", params.User(), "credentials are separate from the endpoint")
			assert.Equal(t, "env:DATATUG_TEST_TYPESENSE", params.ConnectionString())
			assert.Equal(t, "env:DATATUG_TEST_TYPESENSE", params.String())
			assert.Equal(t, "env:DATATUG_TEST_TYPESENSE", params.GoString(), "%#v names the variable, never the endpoint")
			assert.Equal(t, "env:DATATUG_TEST_TYPESENSE", fmt.Sprintf("%#v", params))
			assert.NotContains(t, params.LogTarget(), tc.host)
			assert.Contains(t, params.LogTarget(), "DATATUG_TEST_TYPESENSE")
			assert.Equal(t, "connections/local/books.json", params.Path())
			assert.Equal(t, "env:DATATUG_TEST_TYPESENSE", params.SourceRef().Raw)
		})
	}
}

func TestNewTypesenseScanParamsWritesAndUndoesTheDescriptor(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_TYPESENSE" {
			return "typesense+http://127.0.0.1:8108/books", true
		}
		return "", false
	}
	params, err := NewTypesenseScanParams(lookup, "DATATUG_TEST_TYPESENSE", "local", "books")
	require.NoError(t, err)

	dir := t.TempDir()
	undo, err := params.WriteDescriptor(dir)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, "connections", "local", "books.json"))
	require.NoError(t, err)
	var written map[string]any
	require.NoError(t, json.Unmarshal(body, &written))
	assert.Equal(t, map[string]any{"dsnEnv": "DATATUG_TEST_TYPESENSE"}, written)
	assert.NotContains(t, string(body), "127.0.0.1")
	require.NoError(t, undo())
	_, err = os.Stat(filepath.Join(dir, "connections", "local", "books.json"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	// A second save replaces the file; its undo restores the previous body.
	writeDescriptor(t, dir, "connections/local/books.json", `{"dsnEnv":"OLD_TYPESENSE_URL"}`)
	undo, err = params.WriteDescriptor(dir)
	require.NoError(t, err)
	require.NoError(t, undo())
	body, err = os.ReadFile(filepath.Join(dir, "connections", "local", "books.json"))
	require.NoError(t, err)
	assert.Equal(t, `{"dsnEnv":"OLD_TYPESENSE_URL"}`, string(body))

	// A project folder that is a file cannot hold a descriptor, and the failure
	// names the write rather than the endpoint.
	file := filepath.Join(t.TempDir(), "not-a-folder")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	undo, err = params.WriteDescriptor(file)
	require.Error(t, err)
	assert.Nil(t, undo)
	assert.Contains(t, err.Error(), "connection descriptor")
	assert.NotContains(t, err.Error(), "127.0.0.1")
}

func TestNewTypesenseScanParamsRefusesAWrongSourceOrName(t *testing.T) {
	lookup := func(name string) (string, bool) {
		switch name {
		case "DATATUG_TEST_TYPESENSE":
			return "typesense+http://127.0.0.1:8108/books", true
		case "DATATUG_TEST_TYPESENSE_PG":
			return "postgres://reader:secret@db.example.com/shop", true
		case "DATATUG_TEST_TYPESENSE_CRED":
			return "typesense://reader:secret@search.example.com/books", true
		}
		return "", false
	}
	for _, tc := range []struct{ variable, environment, catalog, want string }{
		{"DATATUG_TEST_TYPESENSE_PG", "local", "books", "must hold a typesense"},
		{"DATATUG_TEST_TYPESENSE_CRED", "local", "books", "does not hold a supported source URL"},
		{"DATATUG_TEST_TYPESENSE_UNSET", "local", "books", "is not set"},
		{"DATATUG_TEST_TYPESENSE", "../local", "books", "must each be a plain name"},
		{"DATATUG_TEST_TYPESENSE", "local", "../books", "must each be a plain name"},
	} {
		_, err := NewTypesenseScanParams(lookup, tc.variable, tc.environment, tc.catalog)
		require.Error(t, err)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, err.Error(), "secret")
	}
}

// typesenseScanDB is one collection of a native Typesense database: the
// adapter's real declared fields including the implicit string id, a native
// array with no portable scalar counterpart, and no SQL indexes or constraints.
type typesenseScanDB struct {
	dal.DB
	closed      int
	listErr     error
	describeErr error
}

func (f *typesenseScanDB) Close() error { f.closed++; return nil }

func (f *typesenseScanDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []dal.CollectionRef{dal.NewRootCollectionRef("books", "")}, nil
}

func (f *typesenseScanDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if ref.Name() != "books" {
		return nil, fmt.Errorf("collection %q not found", ref.Name())
	}
	return &dbschema.CollectionDef{
		Name: "books",
		Fields: []dbschema.FieldDef{
			{Name: "title", Type: dbschema.String},
			{Name: "tags", Type: dbschema.Null},
			{Name: "id", Type: dbschema.String},
		},
		PrimaryKey: []dal.FieldName{"id"},
		SourceDefinition: &dbschema.SourceDefinition{
			Dialect: "typesense",
			Columns: []dbschema.SourceColumnDef{
				{Name: "title", DeclaredType: "string", NotNull: true},
				{Name: "tags", DeclaredType: "string[]", NotNull: true},
				{Name: "id", DeclaredType: "string", NotNull: true, PrimaryKeyPosition: 1},
			},
		},
	}, nil
}

func (*typesenseScanDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListIndexes", Backend: "typesense"}
}

func (*typesenseScanDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "typesense"}
}

func (*typesenseScanDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "typesense"}
}

var _ dbcopy.SchemaScanDB = (*typesenseScanDB)(nil)

func TestScanDbCatalog_TypesenseUsesTheGenericSchemaReader(t *testing.T) {
	db := &typesenseScanDB{}
	var opened dbcopy.BackendRef
	stubOpenSchemaScan(t, func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		opened = ref
		return db, nil
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_TYPESENSE" {
			return "typesense+http://127.0.0.1:8108/books", true
		}
		return "", false
	}
	params, err := NewTypesenseScanParams(lookup, "DATATUG_TEST_TYPESENSE", "local", "books")
	require.NoError(t, err)

	catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverTypesense, Host: "127.0.0.1", Port: 8108}, params)
	require.NoError(t, err)
	require.NotNil(t, catalog)
	assert.Equal(t, "env:DATATUG_TEST_TYPESENSE", opened.Raw)
	assert.Equal(t, "books", catalog.ID)
	assert.Equal(t, DriverTypesense, catalog.Driver)
	assert.Equal(t, 1, db.closed, "the connection is released")

	require.Len(t, catalog.Schemas, 1)
	assert.Equal(t, ServerDefaultSchema, catalog.Schemas[0].ID)
	require.Len(t, catalog.Schemas[0].Tables, 1)
	table := catalog.Schemas[0].Tables[0]
	assert.Equal(t, "books", table.Name())
	types := map[string]string{}
	for _, column := range table.Columns {
		types[column.Name] = column.DbType
	}
	assert.Equal(t, "string", types["id"], "the implicit id is a non-nullable string")
	assert.Equal(t, "string[]", types["tags"], "a native array keeps its declared type")
	assert.Equal(t, "string", types["title"])
	assert.Empty(t, table.ForeignKeys, "Typesense exposes no foreign keys")
}

func TestScanDbCatalog_TypesenseOpenFailureIsClassified(t *testing.T) {
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return nil, &testConnectionError{message: "dial tcp 127.0.0.1:8108: connection refused"}
	})
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_TYPESENSE" {
			return "typesense+http://127.0.0.1:8108/books", true
		}
		return "", false
	}
	params, err := NewTypesenseScanParams(lookup, "DATATUG_TEST_TYPESENSE", "local", "books")
	require.NoError(t, err)
	_, err = scanDbCatalog(datatug.ServerRef{Driver: DriverTypesense}, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open Typesense")
	assert.NotContains(t, err.Error(), "127.0.0.1")
}

func TestScanDbCatalog_TypesenseMetadataFailuresAreClassified(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_TYPESENSE" {
			return "typesense+http://127.0.0.1:8108/books", true
		}
		return "", false
	}
	params, err := NewTypesenseScanParams(lookup, "DATATUG_TEST_TYPESENSE", "local", "books")
	require.NoError(t, err)

	t.Run("list failure", func(t *testing.T) {
		db := &typesenseScanDB{listErr: errors.New("server said 127.0.0.1:8108/books is broken")}
		stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
			return db, nil
		})
		_, err := scanDbCatalog(datatug.ServerRef{Driver: DriverTypesense}, params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get dbCatalog metadata")
		assert.Contains(t, err.Error(), "the catalog could not be read")
		assert.NotContains(t, err.Error(), "127.0.0.1")
		assert.Equal(t, 1, db.closed)
	})

	t.Run("metadata forbidden", func(t *testing.T) {
		db := &typesenseScanDB{listErr: fmt.Errorf("list refused: %w", dalgo2typesense.ErrMetadataForbidden)}
		stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
			return db, nil
		})
		_, err := scanDbCatalog(datatug.ServerRef{Driver: DriverTypesense}, params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not permitted to read collection metadata")
		assert.Contains(t, err.Error(), "collections:get")
	})

	t.Run("describe failure", func(t *testing.T) {
		db := &typesenseScanDB{describeErr: errors.New("describe failed")}
		stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
			return db, nil
		})
		_, err := scanDbCatalog(datatug.ServerRef{Driver: DriverTypesense}, params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get dbCatalog metadata")
	})
}

func TestScanDbCatalog_TypesenseNeedsParametersOfAnEnvironmentSource(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "DATATUG_TEST_TYPESENSE" {
			return "typesense+http://127.0.0.1:8108/books", true
		}
		return "", false
	}
	params, err := NewTypesenseScanParams(lookup, "DATATUG_TEST_TYPESENSE", "local", "books")
	require.NoError(t, err)
	catalog, err := scanTypesenseCatalog(context.Background(), struct{ dbconnection.Params }{params})
	require.Error(t, err)
	assert.Nil(t, catalog)
	assert.Contains(t, err.Error(), "needs connection parameters built from an environment variable")
	assert.NotContains(t, err.Error(), "127.0.0.1")
}

func TestSourceURLFromCatalogTypesense(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/local/books.json", `{"dsnEnv":"DATATUG_TYPESENSE_BOOKS"}`)
	t.Setenv("DATATUG_TYPESENSE_BOOKS", "typesense+http://127.0.0.1:8108/books")
	catalog := func(path string) datatug.DbCatalog {
		return datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "books"}},
			Driver:      DriverTypesense,
			Path:        path,
		}}
	}

	url, err := sourceURLFromCatalog(catalog("connections/local/books.json"), dir)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_TYPESENSE_BOOKS", url)
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
		"typesense+http://evil.example.com/books",
		"typesense://reader:s3cret@evil.example.com/books",
		"env:DATATUG_TYPESENSE_BOOKS",
		"postgres://evil.example.com/shop",
	} {
		_, err = sourceURLFromCatalog(catalog(literal), dir)
		require.Error(t, err, literal)
		assert.NotContains(t, err.Error(), "s3cret")
	}
	dbcopyParse = originalParse

	// A variable that holds another engine is refused by scheme before any
	// authenticated request.
	writeDescriptor(t, dir, "connections/local/pg.json", `{"dsnEnv":"DATATUG_TYPESENSE_PG"}`)
	t.Setenv("DATATUG_TYPESENSE_PG", "postgres://alice:s3cret@db.example.com/shop")
	_, err = sourceURLFromCatalog(catalog("connections/local/pg.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold a typesense:// URL")
	assert.NotContains(t, err.Error(), "s3cret")

	// A variable that is not set is refused when the catalog is resolved.
	writeDescriptor(t, dir, "connections/local/unset.json", `{"dsnEnv":"DATATUG_TYPESENSE_UNSET"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/unset.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DATATUG_TYPESENSE_UNSET is not set")

	// A missing descriptor is reported against the catalog; the route's answer
	// replaces the path with a fixed sentence.
	_, err = sourceURLFromCatalog(catalog("connections/local/missing.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `catalog "books"`)
	assert.Contains(t, err.Error(), "open Typesense connection descriptor")
	assert.Equal(t, `catalog "books": the Typesense connection descriptor cannot be read`, sourceLookupAnswer(err).Error())

	// A malformed descriptor (here: a literal API key field) is refused, and
	// neither the field's value nor a credential reaches the message.
	writeDescriptor(t, dir, "connections/local/bad.json", `{"dsnEnv":"DATATUG_TYPESENSE_BAD","apiKey":"s3cret"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/bad.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown Typesense connection field")
	assert.NotContains(t, err.Error(), "s3cret")

	// A descriptor may not select a variable the operator did not set aside.
	writeDescriptor(t, dir, "connections/local/foreign.json", `{"dsnEnv":"PROD_TYPESENSE_URL"}`)
	_, err = sourceURLFromCatalog(catalog("connections/local/foreign.json"), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PROD_TYPESENSE_URL")
	assert.Contains(t, err.Error(), dbcopy.DescriptorEnvPrefix)
	assert.Contains(t, err.Error(), dbcopy.DescriptorEnvAllowList)

	// Neither a missing descriptor nor a path outside the project is read.
	_, err = sourceURLFromCatalog(catalog(""), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no connection descriptor path")
	for _, outside := range []string{"~/books.json", "../books.json", "/etc/books.json"} {
		_, err = sourceURLFromCatalog(catalog(outside), dir)
		require.Error(t, err, outside)
		assert.NotContains(t, err.Error(), outside)
	}
}

func TestDefaultSchemaForTypesense(t *testing.T) {
	assert.Equal(t, ServerDefaultSchema, DefaultSchemaForDriver(DriverTypesense))
	assert.Equal(t, "books", PolicyCollectionName("default.books", DriverTypesense))
	assert.Equal(t, "books", PolicyCollectionName("books", DriverTypesense))
	unresolved := SourceUnresolvedError{Driver: DriverTypesense}
	assert.True(t, strings.HasPrefix(unresolved.Error(), "its catalog path"))
}
