package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

type clickHouseListingDB struct{ typesenseListingDB }

func (db *clickHouseListingDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	if db.listErr != nil {
		return nil, db.listErr
	}
	return []dal.CollectionRef{dal.NewQualifiedRootCollectionRef("explore", "events", "")}, nil
}
func (*clickHouseListingDB) DescribeCollection(context.Context, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{Name: "events", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}, {Name: "amount", Type: dbschema.String, Nullable: true}}, SourceDefinition: &dbschema.SourceDefinition{Dialect: "clickhouse", Columns: []dbschema.SourceColumnDef{{Name: "id", DeclaredType: "UInt64", NotNull: true}, {Name: "amount", DeclaredType: "Nullable(Decimal(38, 9))"}}}}, nil
}

func TestClickHouseSchemaListsAnonymousRelationsAndUsesPrivateTargets(t *testing.T) {
	fake := &clickHouseListingDB{}
	withSchemaSource(t, func(_ context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		require.Equal(t, "explore", ref.ClickHouseDatabase)
		return fake, nil
	})
	for _, scheme := range []string{"clickhouse", "clickhouse+https", "clickhouse+http"} {
		t.Run(scheme, func(t *testing.T) {
			out, err := runDBSchema(t, "--db", scheme+"://127.0.0.1:18123/explore", "--columns")
			require.NoError(t, err)
			var got schemaListing
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			require.Equal(t, "ClickHouse source", got.Target)
			require.Len(t, got.Tables, 1)
			require.Equal(t, "explore", got.Tables[0].Schema)
			require.Equal(t, "events", got.Tables[0].Name)
			require.NotContains(t, out, "$key")
			require.NotContains(t, out, "127.0.0.1")
		})
	}
	require.Equal(t, 3, fake.closed)
	t.Setenv("DATATUG_CH_SCHEMA_BOUNDARY", "clickhouse+http://127.0.0.1:18123/explore")
	out, err := runDBSchema(t, "--db", "env:DATATUG_CH_SCHEMA_BOUNDARY")
	require.NoError(t, err)
	require.Contains(t, out, "env:DATATUG_CH_SCHEMA_BOUNDARY")
	fake.listErr = errors.New("synthetic list failure")
	_, err = runDBSchema(t, "--db", "env:DATATUG_CH_SCHEMA_BOUNDARY")
	require.Error(t, err)
	require.Equal(t, 5, fake.closed, "error paths release the owned schema source")
}

func TestClickHouseSchemaCountsAndScanParamsRefuseBeforeOpening(t *testing.T) {
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		t.Fatal("unsupported counts opened a source")
		return nil, nil
	})
	_, err := runDBSchema(t, "--db", "clickhouse+http://127.0.0.1:18123/explore", "--counts")
	require.ErrorContains(t, err, "--counts is unavailable for ClickHouse")
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")
	t.Setenv("DATATUG_CH_SCAN_BOUNDARY", "clickhouse+http://127.0.0.1:18123/explore")
	v := &scanDbCommand{Driver: api.DriverClickHouse, DSNEnv: "DATATUG_CH_SCAN_BOUNDARY", Environment: "local", Database: "warehouse"}
	p, err := v.descriptorConnectionParams()
	require.NoError(t, err)
	require.Equal(t, api.DriverClickHouse, p.Driver())
	require.Equal(t, "env:DATATUG_CH_SCAN_BOUNDARY", p.ConnectionString())
	require.Equal(t, "warehouse", p.Catalog())
	t.Setenv("DATATUG_CH_SCAN_BOUNDARY", "postgres://private.example/synthetic")
	p, err = v.descriptorConnectionParams()
	require.Nil(t, p, "a refused typed nil must not become a Params interface")
	require.ErrorContains(t, err, "must hold a ClickHouse")
	require.NotContains(t, err.Error(), "private.example")
}
