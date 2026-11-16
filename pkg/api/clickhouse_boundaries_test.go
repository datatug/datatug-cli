package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dbconnection"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/require"
)

const clickHouseAPIVariable = "DATATUG_CH_API"
const clickHouseAPIEndpoint = "clickhouse+http://127.0.0.1:18123/explore"

func clickHouseAPILookup(name string) (string, bool) {
	return clickHouseAPIEndpoint, name == clickHouseAPIVariable
}

func TestClickHouseScanParamsKeepEndpointOutOfDescriptors(t *testing.T) {
	p, err := NewClickHouseScanParams(clickHouseAPILookup, clickHouseAPIVariable, "local", "warehouse")
	require.NoError(t, err)
	require.Equal(t, DriverClickHouse, p.Driver())
	require.Equal(t, dbconnection.ModeReadOnly, p.Mode())
	require.Equal(t, "127.0.0.1", p.Server())
	require.Equal(t, 18123, p.Port())
	require.Equal(t, "warehouse", p.Catalog())
	require.Empty(t, p.User())
	for _, value := range []string{p.String(), p.GoString(), p.ConnectionString(), p.SourceRef().Raw} {
		require.Equal(t, "env:"+clickHouseAPIVariable, value)
	}
	require.Equal(t, "connections/local/warehouse.json", p.Path())
	require.Contains(t, p.LogTarget(), clickHouseAPIVariable)
	require.NotContains(t, p.LogTarget(), "127.0.0.1")
	dir := t.TempDir()
	undo, err := p.WriteDescriptor(dir)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, p.Path()))
	require.NoError(t, err)
	require.JSONEq(t, `{"dsnEnv":"DATATUG_CH_API"}`, string(body))
	require.NoError(t, undo())
	_, err = os.Stat(filepath.Join(dir, p.Path()))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = NewClickHouseScanParams(clickHouseAPILookup, "DATATUG_UNSET", "local", "warehouse")
	require.Error(t, err)
	_, err = NewClickHouseScanParams(func(string) (string, bool) { return "sqlite:///tmp/synthetic.sqlite", true }, clickHouseAPIVariable, "local", "warehouse")
	require.ErrorContains(t, err, "must hold a ClickHouse")
	_, err = NewClickHouseScanParams(clickHouseAPILookup, clickHouseAPIVariable, "../local", "warehouse")
	require.ErrorContains(t, err, "plain project names")
}

// The fake reports anonymous MergeTree-style columns: no primary key/index
// is borrowed from the SQLite fake whose connection lifecycle it reuses.
type clickHouseScanDB struct{ libSQLScanDB }

func (*clickHouseScanDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewQualifiedRootCollectionRef("explore", "events", "")}, nil
}
func (db *clickHouseScanDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if db.describeErr != nil {
		return nil, db.describeErr
	}
	return &dbschema.CollectionDef{Name: ref.Name(), Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}, {Name: "amount", Type: dbschema.String, Nullable: true}}, SourceDefinition: &dbschema.SourceDefinition{Dialect: "clickhouse", Columns: []dbschema.SourceColumnDef{{Name: "id", DeclaredType: "UInt64", NotNull: true}, {Name: "amount", DeclaredType: "Nullable(Decimal(38, 9))"}}}}, nil
}
func (*clickHouseScanDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func TestClickHouseScanUsesSchemaReaderAndClosesOnFailure(t *testing.T) {
	p, err := NewClickHouseScanParams(clickHouseAPILookup, clickHouseAPIVariable, "local", "warehouse")
	require.NoError(t, err)
	require.Equal(t, "ClickHouse", driverTitle(DriverClickHouse))
	db := &clickHouseScanDB{}
	stubOpenSchemaScan(t, func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		require.Equal(t, "env:"+clickHouseAPIVariable, ref.Raw)
		return db, nil
	})
	catalog, err := scanDbCatalog(datatug.ServerRef{Driver: DriverClickHouse}, p)
	require.NoError(t, err)
	require.Equal(t, "warehouse", catalog.ID)
	require.Equal(t, DriverClickHouse, catalog.Driver)
	require.Equal(t, 1, db.closed)
	require.Len(t, catalog.Schemas, 1)
	require.Equal(t, "explore", catalog.Schemas[0].ID)
	require.Len(t, catalog.Schemas[0].Tables, 1)
	table := catalog.Schemas[0].Tables[0]
	require.Equal(t, "events", table.Name())
	require.Len(t, table.Columns, 2)
	require.Equal(t, "id", table.Columns[0].Name)
	require.Equal(t, "amount", table.Columns[1].Name)
	require.Empty(t, table.PrimaryKey)
	require.Empty(t, table.Indexes)
	db.describeErr = errors.New("private-driver-details")
	_, err = scanClickHouseCatalog(context.Background(), p)
	require.ErrorContains(t, err, "ClickHouse catalog:")
	require.NotContains(t, err.Error(), "private-driver-details")
	require.Equal(t, 2, db.closed)
	_, err = scanClickHouseCatalog(context.Background(), nil)
	require.ErrorContains(t, err, "--dsn-env")
	stubOpenSchemaScan(t, func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		return nil, errors.New("private-driver-details")
	})
	_, err = scanClickHouseCatalog(context.Background(), p)
	require.ErrorContains(t, err, "ClickHouse scan:")
	require.NotContains(t, err.Error(), "private-driver-details")
}

func TestClickHouseRegistrationResolutionAndMapEndpoints(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	t.Setenv(clickHouseAPIVariable, clickHouseAPIEndpoint)
	dir, store := sourceProject(t)
	req := ProjectSourceRequest{Environment: "local", ID: "warehouse", DSNEnv: clickHouseAPIVariable, Driver: DriverClickHouse}
	added, err := AddProjectSource(context.Background(), store, dir, req)
	require.NoError(t, err)
	require.Equal(t, AddedSource{Added: true, ID: "warehouse", Environment: "local", Driver: DriverClickHouse}, added)
	before := treeBytes(t, dir)
	again, err := AddProjectSource(context.Background(), store, dir, req)
	require.NoError(t, err)
	require.False(t, again.Added)
	require.Equal(t, before, treeBytes(t, dir))
	req.Driver = "unsupported"
	_, err = AddProjectSource(context.Background(), nil, dir, req)
	require.ErrorContains(t, err, "driver")

	// Real descriptor/store resolution reaches the refusal before any leaf
	// executor: the endpoint has no ordinal typed result carrier.
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, map[string]string{"p": dir}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return store }}, nil
	}
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	_, err = ExecuteSelect(context.Background(), "files", SelectRequest{Project: "p", Environment: "local", Database: "warehouse", From: "events"})
	require.ErrorContains(t, err, "this map-row endpoint is unsupported")
	_, err = ExecuteCommands(context.Background(), "files", ExecuteCommandsRequest{Project: "p", Commands: []ExecuteCommandRequest{{Type: "SQL", Text: "SELECT 1", Env: "local", DB: "warehouse"}}})
	require.ErrorContains(t, err, "this map-row endpoint is unsupported")
}

func TestClickHouseDescriptorResolutionRejectsWrongOrMissingSources(t *testing.T) {
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	dir := t.TempDir()
	writeDescriptor(t, dir, "connection.json", `{"dsnEnv":"DATATUG_CH_API"}`)
	catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "warehouse"}}, Driver: DriverClickHouse, Path: "connection.json"}}
	t.Setenv(clickHouseAPIVariable, clickHouseAPIEndpoint)
	source, err := sourceURLFromCatalog(catalog, dir)
	require.NoError(t, err)
	require.Equal(t, "env:"+clickHouseAPIVariable, source)
	t.Setenv(clickHouseAPIVariable, "sqlite:///tmp/synthetic.sqlite")
	_, err = sourceURLFromCatalog(catalog, dir)
	require.ErrorContains(t, err, "descriptor must name a ClickHouse source")
	t.Setenv(clickHouseAPIVariable, "not-a-source")
	_, err = sourceURLFromCatalog(catalog, dir)
	require.Error(t, err)
	catalog.Path = "missing.json"
	_, err = sourceURLFromCatalog(catalog, dir)
	require.ErrorContains(t, err, "ClickHouse connection descriptor")
}
