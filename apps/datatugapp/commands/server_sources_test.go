package commands

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverStandIn is a MySQL, Oracle or SQL Server database a test stands in with: SQLite in memory behind dalgo2sql,
// with a customer table, and the schema of the scan's fake reader.
type serverStandIn struct {
	dal.DB
	dbschema.SchemaReader
	sqlDB *sql.DB
}

func (s *serverStandIn) Close() error { return s.sqlDB.Close() }

// standInForMySQL makes every MySQL and Oracle open of this test, query and scan alike, return a SQLite
// database holding customer(id, name).
func standInForMySQL(t *testing.T) {
	t.Helper()
	open := func() (dbcopy.SchemaScanDB, error) {
		sqlDB, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		_, err = sqlDB.Exec(`CREATE TABLE customer (id INTEGER PRIMARY KEY, name TEXT NOT NULL); INSERT INTO customer VALUES (1, 'Ada'), (2, 'Alan')`)
		require.NoError(t, err)
		return &serverStandIn{DB: dalgo2sql.NewDatabase(sqlDB, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite"}), SchemaReader: &fakeCustomerReader{}, sqlDB: sqlDB}, nil
	}
	t.Cleanup(dbcopy.SetServerOpenersForTest(dbcopy.ServerOpeners{
		MySQL:  func(string) (dbcopy.SchemaScanDB, error) { return open() },
		Oracle: func(context.Context, string) (dbcopy.SchemaScanDB, error) { return open() },
	}))
	t.Cleanup(api.SetOpenSchemaScanForTest(dbcopy.BackendRef.OpenSchemaScan))
}

// fakeCustomerReader is the schema reader of the stand-in: one table, customer.
type fakeCustomerReader struct{}

func (*fakeCustomerReader) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("customer", "")}, nil
}

func (*fakeCustomerReader) DescribeCollection(context.Context, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{Name: "customer", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}, {Name: "name", Type: dbschema.String}}, PrimaryKey: []dal.FieldName{"id"}}, nil
}

func (*fakeCustomerReader) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func (*fakeCustomerReader) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints"}
}

func (*fakeCustomerReader) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers"}
}

const serverSourceSecret = "s3rv3r-Secret"

func TestScanCommand_MySQLAndOracleFlags(t *testing.T) {
	dir := t.TempDir()
	_, err := runScanCommand(t, "-d", dir, "-D", "mysql", "--db", "shop", "--env", "local")
	assert.EqualError(t, err, "scanning a mysql database requires --dsn-env NAME, the environment variable that holds the connection URL (mysql://user:password@host:3306/database)")
	_, err = runScanCommand(t, "-d", dir, "-D", "oracle", "-s", "db.example.com", "--db", "hr", "--env", "local")
	assert.ErrorContains(t, err, "--server not accepted for an oracle scan")
	_, err = runScanCommand(t, "-d", dir, "-D", "sqlite3", "--dsn-env", "DATATUG_X", "--db", "x", "--env", "local")
	assert.EqualError(t, err, "--dsn-env applies only to -D postgres, mysql, oracle or sqlserver")
}

func TestScanAndQuery_MySQLThroughAStandIn(t *testing.T) {
	standInForMySQL(t)
	t.Setenv("DATATUG_CMD_MY_URL", "mysql://alice:"+serverSourceSecret+"@db.example.com/shop")
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))

	_, err := runScanCommand(t, "-d", projectDir, "-D", "mysql", "--dsn-env", "DATATUG_CMD_MY_URL", "--db", "shop", "--env", "local")
	require.NoError(t, err)
	columns := storedColumnsOf(t, projectDir, "default/tables/customer")
	require.Len(t, columns, 2)

	// An ad-hoc query.
	stdout, stderr, code := runQuery(t, "from: {name: customer}\norderBy:\n  - field: id\n", "--db", "env:DATATUG_CMD_MY_URL", "-f", "-", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	assert.Len(t, decodeObjects(t, stdout), 2)

	// Saved queries of the scanned project, SQL and TugQL, through the catalog's connection descriptor.
	queries := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeSavedQuery(t, queries, "ada", "SQL", "SELECT id, name FROM customer WHERE id = 1")
	writeSavedQuery(t, queries, "alan", "TugQL", "from: {name: customer}\nwhere: {op: '==', left: {field: id}, right: {value: 2}}\n")
	stdout, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "ada", "--env", "local", "--format", "json")
	require.Zero(t, code, stderr)
	rows := decodeObjects(t, stdout)
	require.Len(t, rows, 1)
	assert.Equal(t, float64(1), rows[0]["id"])
	assert.Equal(t, "Ada", rows[0]["name"])
	stdout, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "alan", "--env", "local", "--format", "json")
	require.Zero(t, code, stderr)
	assert.Equal(t, "Alan", decodeObjects(t, stdout)[0]["name"])
	assert.NotContains(t, stdout+stderr, serverSourceSecret)
}
