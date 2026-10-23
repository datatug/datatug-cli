package dbcopy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const serverSecret = "s3cr3t-Pw"

// fakeServerDB is a SchemaScanDB that a test stands in with: it embeds the interfaces it does not use.
type fakeServerDB struct {
	dal.DB
	dbschema.SchemaReader
	closed bool
}

func (f *fakeServerDB) Close() error { f.closed = true; return nil }

// standInForServers makes the openers of this test record the DSN they were given and return a fake.
func standInForServers(t *testing.T) (dsns *[]string) {
	t.Helper()
	var got []string
	t.Cleanup(SetServerOpenersForTest(ServerOpeners{
		MySQL: func(dsn string) (SchemaScanDB, error) { got = append(got, dsn); return &fakeServerDB{}, nil },
		Oracle: func(_ context.Context, dsn string) (SchemaScanDB, error) {
			got = append(got, dsn)
			return &fakeServerDB{}, nil
		},
		SQLServer: func(_ context.Context, dsn string) (SchemaScanDB, error) {
			got = append(got, dsn)
			return &fakeServerDB{}, nil
		},
	}))
	return &got
}

func TestServerSchemesAndTitles(t *testing.T) {
	for _, scheme := range []string{"postgres", "mysql", "oracle"} {
		assert.True(t, IsServerScheme(scheme), scheme)
	}
	assert.False(t, IsServerScheme("sqlite"))
	assert.Equal(t, "PostgreSQL", ServerTitle("postgres"))
	assert.Equal(t, "MySQL", ServerTitle("mysql"))
	assert.Equal(t, "Oracle", ServerTitle("oracle"))
	assert.Equal(t, "sqlite", ServerTitle("sqlite"))
	assert.Contains(t, SupportedSchemes(), "mysql")
	assert.Contains(t, SupportedSchemes(), "oracle")
}

func TestServerPreviewSwitches(t *testing.T) {
	assert.ErrorIs(t, CheckServerPreview("postgres"), ErrPostgresPreview)
	assert.EqualError(t, CheckServerPreview("mysql"), "MySQL sources are a preview and are switched off: set DATATUG_PREVIEW_MYSQL=1 to use them")
	assert.EqualError(t, CheckServerPreview("oracle"), "Oracle sources are a preview and are switched off: set DATATUG_PREVIEW_ORACLE=1 to use them")
	assert.NoError(t, CheckServerPreview("sqlite"))
	t.Setenv(MySQLPreviewEnv, "1")
	assert.NoError(t, CheckServerPreview("mysql"))
	t.Setenv(OraclePreviewEnv, "yes")
	assert.Error(t, CheckServerPreview("oracle"), "only 1 turns it on")
}

func TestCheckServerRead(t *testing.T) {
	mysqlRef := BackendRef{Scheme: "mysql"}
	assert.ErrorIs(t, CheckServerRead(mysqlRef, 0), mysqlEngine.preview, "the switch is asked first")
	t.Setenv(MySQLPreviewEnv, "1")
	assert.NoError(t, CheckServerRead(mysqlRef, 0))
	assert.EqualError(t, CheckServerRead(mysqlRef, 1), "policy-enforced reads on MySQL sources are not available in this preview")
	assert.ErrorIs(t, CheckServerRead(BackendRef{Scheme: "postgres"}, 0), ErrPostgresPreview, "a PostgreSQL source is CheckPostgresRead's")
	assert.NoError(t, CheckServerRead(BackendRef{Scheme: "sqlite"}, 3))
}

func TestParseServerURLs(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      ServerTarget
	}{
		{"mysql with a port", "mysql://alice:" + serverSecret + "@db.example.com:3307/shop", ServerTarget{"db.example.com", 3307, "alice", "shop"}},
		{"mysql default port", "mysql://alice:" + serverSecret + "@db.example.com/shop?tls=true&timeout=5s", ServerTarget{"db.example.com", 3306, "alice", "shop"}},
		{"oracle default port", "oracle://scott:" + serverSecret + "@10.0.0.5/FREEPDB1?SSL=enable", ServerTarget{"10.0.0.5", 1521, "scott", "FREEPDB1"}},
		{"oracle IPv6", "oracle://scott:" + serverSecret + "@[::1]:1522/FREEPDB1", ServerTarget{"::1", 1522, "scott", "FREEPDB1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme, _, _ := strings.Cut(tc.raw, ":")
			got, err := ParseServerTarget(scheme, tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			ref, err := Parse(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, scheme, ref.Scheme)
			assert.Equal(t, tc.raw, ref.Path)
			assert.NotContains(t, ref.Display(), serverSecret)
		})
	}
	_, err := ParseServerTarget("postgres", "postgres://a@b/c")
	assert.EqualError(t, err, "postgres is not a MySQL, Oracle or SQL Server scheme")
}

func TestParseServerURLRefusals(t *testing.T) {
	for _, tc := range []struct{ raw, says string }{
		{"mysql://alice:pa/ss@db.example.com/shop", "literal '@' after its host"},
		{"mysql://alice:" + serverSecret + "@db.example.com:99999/shop", "port that is not a number"},
		{"mysql://alice:" + serverSecret + "@db.example.com:0/shop", "port that is not a number"},
		{"mysql://alice:" + serverSecret + "@db example.com/shop", "cannot be read"},
		{"mysql://alice:" + serverSecret + "@/shop", "cannot be read"},
		{"mysql://alice:" + serverSecret + "@db.example.com/shop#" + serverSecret, "has a fragment"},
		{"mysql://alice:" + serverSecret + "@db,other/shop", "names no host"},
		{"mysql://:" + serverSecret + "@db.example.com/shop", "names no user"},
		{"mysql://alice%3A" + serverSecret + "@db.example.com/shop", "holds a colon or a percent sign"},
		{"mysql://alice:" + serverSecret + "@db.example.com", "must name one database as its path (/database)"},
		{"oracle://scott:" + serverSecret + "@db.example.com/a/b", "must name one service name as its path (/service_name)"},
		{"mysql://alice:" + serverSecret + "@db.example.com/shop?transaction_read_only=0", "accepts only timeout, tls"},
		{"oracle://scott:" + serverSecret + "@db.example.com/x?server=evil", "accepts only connection timeout, ssl, ssl verify, timeout, wallet"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			_, err := Parse(tc.raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.says)
			assert.NotContains(t, err.Error(), serverSecret, "no refusal quotes the password")
			var refusal *serverURLRefusal
			assert.ErrorAs(t, err, &refusal)
		})
	}
}

func TestServerURLInAnEnvironmentVariable(t *testing.T) {
	env := map[string]string{"DATATUG_MY": "mysql://alice:" + serverSecret + "@db.example.com/shop", "DATATUG_BAD": "mysql://alice:" + serverSecret + "@db.example.com/shop?x=" + serverSecret}
	lookup := func(name string) (string, bool) { value, ok := env[name]; return value, ok }
	ref, err := ParseWithEnv("env:DATATUG_MY", lookup)
	require.NoError(t, err)
	assert.Equal(t, "env:DATATUG_MY", ref.Raw)
	_, err = ParseWithEnv("env:DATATUG_BAD", lookup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environment variable DATATUG_BAD does not hold a usable connection URL: the MySQL URL has a query parameter")
	assert.NotContains(t, err.Error(), serverSecret)
}

func TestMySQLDSNIsReadOnly(t *testing.T) {
	parsed, err := parseServerURL(mysqlEngine, "mysql://alice:"+serverSecret+"@db.example.com:3307/shop?tls=skip-verify&timeout=3s")
	require.NoError(t, err)
	dsn, err := mysqlDSN(parsed)
	require.NoError(t, err)
	assert.Equal(t, "alice:"+serverSecret+"@tcp(db.example.com:3307)/shop?parseTime=true&timeout=3s&tls=skip-verify&transaction_read_only=1", dsn)

	parsed, err = parseServerURL(mysqlEngine, "mysql://alice:"+serverSecret+"@db.example.com/shop")
	require.NoError(t, err)
	dsn, err = mysqlDSN(parsed)
	require.NoError(t, err)
	assert.Contains(t, dsn, "timeout=10s")

	parsed, err = parseServerURL(mysqlEngine, "mysql://alice:"+serverSecret+"@db.example.com/shop?timeout=soon")
	require.NoError(t, err)
	_, err = mysqlDSN(parsed)
	assert.EqualError(t, err, "the MySQL URL has a timeout that is not a duration such as 5s")
}

func TestOracleURLIsBuiltFromItsParts(t *testing.T) {
	parsed, err := parseServerURL(oracleEngine, "oracle://scott:"+serverSecret+"@db.example.com/FREEPDB1")
	require.NoError(t, err)
	assert.Equal(t, "oracle://scott:"+serverSecret+"@db.example.com:1521/FREEPDB1?TIMEOUT=10", oracleURL(parsed))
	parsed, err = parseServerURL(oracleEngine, "oracle://scott:"+serverSecret+"@db.example.com/FREEPDB1?timeout=30")
	require.NoError(t, err)
	assert.Equal(t, "oracle://scott:"+serverSecret+"@db.example.com:1521/FREEPDB1?TIMEOUT=30", oracleURL(parsed))
}

func TestOpenServerSources(t *testing.T) {
	dsns := standInForServers(t)
	mysqlRef, err := Parse("mysql://alice:" + serverSecret + "@db.example.com/shop")
	require.NoError(t, err)
	oracleRef, err := Parse("oracle://scott:" + serverSecret + "@db.example.com/FREEPDB1")
	require.NoError(t, err)
	ctx := context.Background()

	_, err = mysqlRef.Open(ctx)
	assert.ErrorIs(t, err, mysqlEngine.preview, "off until the switch is on")
	t.Setenv(MySQLPreviewEnv, "1")
	t.Setenv(OraclePreviewEnv, "1")
	_, err = mysqlRef.OpenForWrite(ctx)
	assert.EqualError(t, err, "MySQL sources are read only in DataTug: only a read opens one")

	db, err := mysqlRef.OpenProtected(ctx)
	require.NoError(t, err)
	assert.NotNil(t, db)
	_, err = oracleRef.Open(ctx)
	require.NoError(t, err)
	require.Len(t, *dsns, 2)
	assert.Contains(t, (*dsns)[0], "transaction_read_only=1")
	assert.True(t, strings.HasPrefix((*dsns)[1], "oracle://scott:"))

	// A scan is not behind the switch.
	t.Setenv(MySQLPreviewEnv, "")
	scanDB, err := mysqlRef.OpenSchemaScan(ctx)
	require.NoError(t, err)
	assert.NotNil(t, scanDB)
	_, err = BackendRef{Scheme: "sqlite"}.OpenSchemaScan(ctx)
	assert.EqualError(t, err, "a schema scan through DALgo is available for postgres, mysql, oracle and sqlserver sources only, not sqlite")
}

func TestServerOpenFailuresNameNoPartOfTheURL(t *testing.T) {
	t.Setenv(MySQLPreviewEnv, "1")
	cause := errors.New("dial tcp db.example.com:3306 alice:" + serverSecret)
	t.Cleanup(SetServerOpenersForTest(ServerOpeners{MySQL: func(string) (SchemaScanDB, error) { return nil, cause }}))

	ref, err := ParseWithEnv("env:DATATUG_MY", func(string) (string, bool) { return "mysql://alice:" + serverSecret + "@db.example.com/shop", true })
	require.NoError(t, err)
	_, err = ref.Open(context.Background())
	assert.EqualError(t, err, "the MySQL source could not be opened: the driver could not open the source (its own message is not shown: a driver can quote the connection string); the MySQL connection string is read from the environment variable DATATUG_MY")
	assert.ErrorIs(t, err, cause)
	assert.True(t, IsServerConnectionFailure(err))

	_, err = ref.OpenSchemaScan(context.Background())
	assert.True(t, IsServerConnectionFailure(err))
	assert.NotContains(t, err.Error(), serverSecret)

	typed, err := Parse("mysql://alice:" + serverSecret + "@db.example.com/shop?timeout=never")
	require.NoError(t, err)
	_, err = typed.WithFlag("--db").Open(context.Background())
	assert.EqualError(t, err, "the MySQL URL has a timeout that is not a duration such as 5s", "a refusal of this package passes through")
	_, err = typed.OpenSchemaScan(context.Background())
	assert.EqualError(t, err, "the MySQL URL has a timeout that is not a duration such as 5s")

	assert.Equal(t, "the Oracle connection string is read from the --db flag", BackendRef{Scheme: "oracle"}.WithFlag("--db").serverConnectionHint(oracleEngine))
	assert.Equal(t, "the Oracle connection string is the one the source was given", BackendRef{Scheme: "oracle"}.serverConnectionHint(oracleEngine))
	assert.Equal(t, "the MySQL connection string is read from the environment variable DATATUG_MY", ref.ConnectionHint(), "what a command logs names the engine")
	assert.False(t, UnavailableSourceShowsSource(ref.OpenFailure(cause)), "a server's failure is not shown with its source")
}

func TestTheRealServerOpenersAreTheAdapters(t *testing.T) {
	real := RealServerOpeners()
	// Each URL is one its driver refuses to read, so nothing is dialled.
	_, err := real.MySQL("not a dsn")
	assert.Error(t, err)
	_, err = real.Oracle(context.Background(), "oracle://%zz")
	assert.Error(t, err)
	_, err = real.SQLServer(context.Background(), "sqlserver://%zz")
	assert.Error(t, err)

	restore := SetServerOpenersForTest(ServerOpeners{})
	restore()
	assert.PanicsWithValue(t, "a test of pkg/dbcopy opened a MySQL database for real: it must stand in and never dial",
		func() { _, _ = newMySQLDatabase("x") })
	assert.Panics(t, func() { _, _ = newOracleDatabase(context.Background(), "x") })
	assert.Panics(t, func() { _, _ = newSQLServerDatabase(context.Background(), "x") })
}

// A ref that did not come from Parse is read again before it is opened, and refused the same way.
func TestAServerRefNotBuiltByParseIsReadAgain(t *testing.T) {
	_, err := BackendRef{Scheme: "oracle", Path: "oracle://scott:" + serverSecret + "@db.example.com"}.OpenSchemaScan(context.Background())
	assert.EqualError(t, err, "the Oracle URL must name one service name as its path (/service_name)")
}
