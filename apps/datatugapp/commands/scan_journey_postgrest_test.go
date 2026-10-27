package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postgresScanFakeDB is the PostgREST OpenAPI view of the fixture: native
// relations with declared fields and a primary key, and no index, constraint or
// referrer metadata. Its ListIndexes refusal is what a real PostgREST reader
// returns, and the scan must report no indexes rather than fail.
type postgresScanFakeDB struct {
	dal.DB
	closed int
}

func (f *postgresScanFakeDB) Close() error { f.closed++; return nil }

func (*postgresScanFakeDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{
		dal.NewRootCollectionRef("items", ""),
		dal.NewRootCollectionRef("labels", ""),
	}, nil
}

func (*postgresScanFakeDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	def := &dbschema.CollectionDef{Name: ref.Name(), PrimaryKey: []dal.FieldName{"id"}}
	if ref.Name() == "items" {
		def.Fields = []dbschema.FieldDef{
			{Name: "id", Type: dbschema.Int},
			{Name: "title", Type: dbschema.String},
			{Name: "qty", Type: dbschema.Int},
		}
	}
	return def, nil
}

func (*postgresScanFakeDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListIndexes", Backend: "postgrest"}
}

func (*postgresScanFakeDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "postgrest"}
}

func (*postgresScanFakeDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "postgrest"}
}

var _ dbcopy.SchemaScanDB = (*postgresScanFakeDB)(nil)

// A PostgREST scan records the same safe descriptor as a PostgreSQL or CouchDB
// scan: the catalog holds a project-relative descriptor path, and the descriptor
// names the operator's variable and nothing else. The API root itself never
// reaches the project.
func TestScanJourneyPostgRESTWritesTheConnectionDescriptor(t *testing.T) {
	const variable = "DATATUG_JOURNEY_POSTGREST_URL"
	const endpoint = "postgrest+http://127.0.0.1:3000/rest/v1?schema=app"
	t.Setenv(variable, endpoint)
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")

	projectDir := filepath.Join(t.TempDir(), "postgrest-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	fake := &postgresScanFakeDB{}
	t.Cleanup(api.SetOpenSchemaScanForTest(func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		assert.Equal(t, "env:"+variable, ref.Raw)
		return fake, nil
	}))

	_, err := runScanCommand(t, "-d", projectDir, "-D", "postgrest", "--dsn-env", variable, "--db", "items", "--env", "local")
	require.NoError(t, err)
	assert.Equal(t, 1, fake.closed, "the schema reader is released")

	catalogFile := readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "items", "items.db.json"))
	assert.Equal(t, "postgrest", catalogFile["driver"])
	assert.Equal(t, "items", catalogFile["dbModel"])
	assert.Equal(t, "connections/local/items.json", catalogFile["path"])
	descriptor := readJSONMap(t, filepath.Join(projectDir, "connections", "local", "items.json"))
	assert.Equal(t, map[string]any{"dsnEnv": variable}, descriptor, "the descriptor names the variable and nothing else")
	assertNoSourceIn(t, projectDir)
	for _, name := range projectFiles(t, projectDir, "") {
		body, readErr := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(name)))
		require.NoError(t, readErr)
		assert.NotContains(t, string(body), "127.0.0.1", name)
		assert.NotContains(t, string(body), "schema=app", name)
	}

	// The saved project itself passes the shared core validator, and records the
	// server by driver alone: no host, port or API path.
	store, projectID := filestore.NewSingleProjectStore(projectDir, "postgrest-project")
	project, err := store.GetProjectStore(projectID).LoadProject(context.Background())
	require.NoError(t, err)
	require.NoError(t, project.Validate(), "the core validator accepts a saved PostgREST project")
	require.Len(t, project.Environments, 1)
	require.Len(t, project.Environments[0].DbServers, 1)
	assert.Equal(t, "postgrest", project.Environments[0].DbServers[0].Driver)
	assert.Empty(t, project.Environments[0].DbServers[0].Host)
	assert.Zero(t, project.Environments[0].DbServers[0].Port)

	// The scanned DB model keeps the relations and their declared fields; the
	// read is grouped under the server's default schema, as every descriptor-backed
	// source is.
	schema, err := api.GetCatalogSchema(projectDir, "local", "items")
	require.NoError(t, err)
	require.Len(t, schema.Relations, 2)
	var items *api.CatalogRelation
	for i := range schema.Relations {
		if schema.Relations[i].Name == "items" {
			items = &schema.Relations[i]
		}
	}
	require.NotNil(t, items, "the scan records the items relation")
	assert.Equal(t, "default", items.Schema)
	var columns []string
	for _, column := range items.Columns {
		columns = append(columns, column.Name)
	}
	assert.ElementsMatch(t, []string{"id", "title", "qty"}, columns)
}

func TestScanPostgRESTConnectionParamsRefusesAnotherSource(t *testing.T) {
	const variable = "DATATUG_TEST_POSTGREST_WRONG"
	t.Setenv(variable, "couchdb+http://127.0.0.1:5984/orders")
	command := &scanDbCommand{Driver: api.DriverPostgREST, DSNEnv: variable, Environment: "local", Database: "items"}
	_, err := command.connectionParams()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must hold a postgrest")
	assert.NotContains(t, err.Error(), "127.0.0.1")
}
