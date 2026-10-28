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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type couchScanFakeDB struct {
	dal.DB
	closed int
}

func (f *couchScanFakeDB) Close() error { f.closed++; return nil }

func (*couchScanFakeDB) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("orders", "")}, nil
}

func (*couchScanFakeDB) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{Name: ref.Name(), PrimaryKey: []dal.FieldName{"_id"}}, nil
}

func (*couchScanFakeDB) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return []dbschema.IndexDef{{Name: "status-score-index", Collection: "orders", Fields: []dal.FieldName{"status", "score"}}}, nil
}

func (*couchScanFakeDB) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "couchdb"}
}

func (*couchScanFakeDB) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "couchdb"}
}

var _ dbcopy.SchemaScanDB = (*couchScanFakeDB)(nil)

// A CouchDB scan records the same safe descriptor as a PostgreSQL scan: the
// catalog holds a project-relative descriptor path, and the descriptor names the
// operator's variable and nothing else. The endpoint itself never reaches the
// project.
func TestScanJourneyCouchDBWritesTheConnectionDescriptor(t *testing.T) {
	const variable = "DATATUG_JOURNEY_COUCH_URL"
	const endpoint = "couchdb://couch.example.com:6984/orders"
	t.Setenv(variable, endpoint)
	t.Setenv(dbcopy.DescriptorEnvAllowList, "")

	projectDir := filepath.Join(t.TempDir(), "couch-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	fake := &couchScanFakeDB{}
	t.Cleanup(api.SetOpenSchemaScanForTest(func(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
		assert.Equal(t, "env:"+variable, ref.Raw)
		return fake, nil
	}))

	_, err := runScanCommand(t, "-d", projectDir, "-D", "couchdb", "--dsn-env", variable, "--db", "orders", "--env", "local")
	require.NoError(t, err)
	assert.Equal(t, 1, fake.closed, "the schema reader is released")

	catalogFile := readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "orders", "orders.db.json"))
	assert.Equal(t, "couchdb", catalogFile["driver"])
	assert.Equal(t, "orders", catalogFile["dbModel"])
	assert.Equal(t, "connections/local/orders.json", catalogFile["path"])
	descriptor := readJSONMap(t, filepath.Join(projectDir, "connections", "local", "orders.json"))
	assert.Equal(t, map[string]any{"dsnEnv": variable}, descriptor, "the descriptor names the variable and nothing else")
	assertNoSourceIn(t, projectDir)
	for _, name := range projectFiles(t, projectDir, "") {
		body, readErr := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(name)))
		require.NoError(t, readErr)
		assert.NotContains(t, string(body), "couch.example.com", name)
		// The full endpoint, host and port: a CouchDB port such as 6984 alone
		// also occurs inside an unrelated random id or path.
		assert.NotContains(t, string(body), "couch.example.com:6984", name)
	}
}
