package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	_ "github.com/microsoft/go-mssqldb" // registers the "sqlserver" database/sql driver for the fixture
	_ "github.com/sijms/go-ora/v3"      // registers the "oracle" database/sql driver for the fixture
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The journey of an Oracle and of a SQL Server source against a REAL server, the same journey the MySQL one
// is (server_journey_mysql_real_test.go): `datatug scan -D oracle|sqlserver --dsn-env NAME`, then
// `datatug query run` with TugQL on --db env:NAME and with saved SQL and TugQL queries of the scanned project.
// Each runs in one CI job only ("Journey (Oracle Database Free)", "Journey (SQL Server 2022)"), whose script
// starts the server and sets the administrator's URL, and skips everywhere else; scripts/lib/source-journey.sh
// fails the job when it skips or prints no PASS line.

// realServer is the fixture database of one test: what the commands connect with, and the parts of that
// connection that no project file and no output may hold.
type realServer struct {
	driver, previewEnv, scanVar, scanURL string
	parts                                []string
	// sql is the saved SQL query that reads the ids and names of the customers that have a score, highest first.
	sql string
}

// serverJourneyDDL is the fixture of a journey: customer, invoice with a foreign key to it, MixedCase with
// mixed-case names and order_line whose primary key is in another order than its columns. q quotes a name
// for the engine, which keeps its case; types holds the engine's spelling of bigint, int, text and decimal.
func serverJourneyDDL(q func(string) string, bigint, integer, text, decimal string) []string {
	statements := []string{
		fmt.Sprintf(`CREATE TABLE %s (%s %s PRIMARY KEY, %s %s(100) NOT NULL, %s %s(200), %s %s)`,
			q("customer"), q("id"), bigint, q("name"), text, q("email"), text, q("score"), integer),
		fmt.Sprintf(`CREATE TABLE %s (%s %s PRIMARY KEY, %s %s NOT NULL REFERENCES %s(%s), %s %s(12,2) DEFAULT 0 NOT NULL)`,
			q("invoice"), q("id"), integer, q("customer_id"), bigint, q("customer"), q("id"), q("total"), decimal),
		fmt.Sprintf(`CREATE TABLE %s (%s %s PRIMARY KEY, %s %s(40))`, q("MixedCase"), q("OrderId"), integer, q("ItemName"), text),
		fmt.Sprintf(`CREATE TABLE %s (%s %s NOT NULL, %s %s NOT NULL, %s %s(20), PRIMARY KEY (%s, %s))`,
			q("order_line"), q("order_id"), integer, q("line_no"), integer, q("sku"), text, q("line_no"), q("order_id")),
		fmt.Sprintf(`INSERT INTO %s (%s, %s) VALUES (7, 'lamp')`, q("MixedCase"), q("OrderId"), q("ItemName")),
	}
	for _, row := range []string{"1, 'Ada Lovelace', 'ada@example.com', 30", "2, 'Alan Turing', NULL, NULL", "3, 'Grace Hopper', 'grace@example.com', 10"} {
		statements = append(statements, fmt.Sprintf(`INSERT INTO %s (%s, %s, %s, %s) VALUES (%s)`, q("customer"), q("id"), q("name"), q("email"), q("score"), row))
	}
	for _, row := range []string{"10, 1, 12.50", "11, 3, 99.99"} {
		statements = append(statements, fmt.Sprintf(`INSERT INTO %s (%s, %s, %s) VALUES (%s)`, q("invoice"), q("id"), q("customer_id"), q("total"), row))
	}
	return statements
}

// execOn runs each statement through driverName at dsn.
func execOn(t *testing.T, driverName, dsn string, statements ...string) {
	t.Helper()
	db, err := sql.Open(driverName, dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		_, err = db.Exec(statement)
		require.NoError(t, err, "statement of the fixture failed: %s", firstLine(statement))
	}
}

// useTheRealServerOpeners puts the real openers back for the length of the test, in place of the ones the
// test binary stops the run with: the open of the scan and the open of every other command.
func useTheRealServerOpeners(t *testing.T) {
	t.Helper()
	t.Cleanup(api.SetOpenSchemaScanForTest(dbcopy.BackendRef.OpenSchemaScan))
	t.Cleanup(dbcopy.SetServerOpenersForTest(dbcopy.RealServerOpeners()))
}

// partsIn is each part of the connection text holds.
func (s realServer) partsIn(text string) (found []string) {
	for _, part := range s.parts {
		if strings.Contains(text, part) {
			found = append(found, part)
		}
	}
	return found
}

// ids runs `datatug query run` for query on --db env:scanVar and returns the "id" of each row, in order.
func (s realServer) ids(t *testing.T, query string) []int {
	t.Helper()
	stdout, stderr, code := runQuery(t, query, "--db", "env:"+s.scanVar, "-f", "-", "--format", "json", "--no-policies")
	require.Zero(t, code, "the query exits 0: %s", stderr)
	assert.Empty(t, s.partsIn(stdout+stderr))
	var ids []int
	for _, row := range decodeObjects(t, stdout) {
		id, ok := row["id"].(float64)
		require.True(t, ok, "a row has a numeric id: %v", row)
		ids = append(ids, int(id))
	}
	return ids
}

// journey scans the fixture database into a new project, queries it ad hoc and through saved queries of the
// project, and returns the project folder and its queries folder for the engine's own checks.
func (s realServer) journey(t *testing.T) (projectDir, queries string) {
	t.Helper()
	projectDir = filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))

	// 1. The scan: exit 0, nothing on stdout, and a project that names the variable and no part of the URL.
	stdout, stderr, err := runScanCommandAllOutputs(t, "-d", projectDir, "-D", s.driver, "--dsn-env", s.scanVar, "--db", "shop", "--env", "local")
	require.NoError(t, err, "the scan of the real server exits 0: %s", stderr)
	assert.Empty(t, stdout)
	assert.Empty(t, s.partsIn(stdout+stderr), "the scan's output names no part of the connection")
	require.NoError(t, filepath.WalkDir(projectDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Empty(t, s.partsIn(path+"\n"+string(content)), "%s holds no part of the connection", path)
		return nil
	}))
	descriptor, err := os.ReadFile(filepath.Join(projectDir, "connections", "local", "shop.json"))
	require.NoError(t, err)
	var named struct {
		DSNEnv string `json:"dsnEnv"`
	}
	require.NoError(t, json.Unmarshal(descriptor, &named))
	assert.Equal(t, s.scanVar, named.DSNEnv)

	customer := storedColumnsOf(t, projectDir, "default/tables/customer")
	require.Len(t, customer, 4)
	assert.Equal(t, "id", customer[0].Name)
	assert.Equal(t, 1, customer[0].PKPosition)
	assert.False(t, customer[1].IsNullable, "name is NOT NULL")
	assert.True(t, customer[2].IsNullable, "email is nullable")
	orderLine := storedColumnsOf(t, projectDir, "default/tables/order_line")
	require.Len(t, orderLine, 3)
	assert.Equal(t, 2, orderLine[0].PKPosition, "order_id is the second column of the key (line_no, order_id)")
	assert.Equal(t, 1, orderLine[1].PKPosition)
	mixed := storedColumnsOf(t, projectDir, "default/tables/MixedCase")
	require.Len(t, mixed, 2)
	assert.Equal(t, "OrderId", mixed[0].Name, "a mixed-case name is recorded as the server reports it")
	storedColumnsOf(t, projectDir, "default/tables/invoice")

	// 2. An ad-hoc TugQL query, behind the preview switch. DALgo's NULL order: first ascending, last descending.
	_, stderr, code := runQuery(t, "from: {name: customer}\n", "--db", "env:"+s.scanVar, "-f", "-", "--format", "json", "--no-policies")
	assert.Equal(t, exitCodeDatabase, code, "with the switch off the query is refused")
	assert.Contains(t, stderr, s.previewEnv)
	t.Setenv(s.previewEnv, "1")
	assert.Equal(t, []int{1, 2, 3}, s.ids(t, "from: {name: customer}\norderBy:\n  - field: id\n"))
	assert.Equal(t, []int{2, 3, 1}, s.ids(t, "from: {name: customer}\norderBy:\n  - field: score\n  - field: id\n"))
	assert.Equal(t, []int{1, 3}, s.ids(t, "from: {name: customer}\norderBy:\n  - field: score\n    desc: true\n  - field: id\nlimit: 2\n"))
	assert.Equal(t, []int{3}, s.ids(t, "from: {name: customer}\nwhere: {op: '==', left: {field: name}, right: {value: Grace Hopper}}\n"))
	assert.Equal(t, []int{11}, s.ids(t, "from: {name: invoice}\nwhere: {op: '==', left: {field: customer_id}, right: {value: 3}}\n"))
	stdout, stderr, code = runQuery(t, "from: {name: MixedCase}\n", "--db", "env:"+s.scanVar, "-f", "-", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	assert.Equal(t, []map[string]any{{"OrderId": float64(7), "ItemName": "lamp"}}, withoutKey(decodeObjects(t, stdout)), "a mixed-case table is read by its exact name")

	// 3. Saved queries of the scanned project, SQL and TugQL, on the one database of environment local.
	queries = filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeSavedQuery(t, queries, "top-customers", "SQL", s.sql)
	writeSavedQuery(t, queries, "graces", "TugQL", "from: {name: customer}\nwhere: {op: '==', left: {field: id}, right: {value: 3}}\n")
	stdout, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "top-customers", "--env", "local", "--format", "json")
	require.Zero(t, code, stderr)
	rows := withoutKey(decodeObjects(t, stdout))
	require.Len(t, rows, 2)
	assert.Equal(t, []any{"Ada Lovelace", "Grace Hopper"}, []any{rows[0]["name"], rows[1]["name"]})
	stdout, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "graces", "--env", "local", "--format", "json")
	require.Zero(t, code, stderr)
	graces := decodeObjects(t, stdout)
	require.Len(t, graces, 1)
	assert.Equal(t, "Grace Hopper", graces[0]["name"])

	// Text that is not one query is refused before it reaches the server.
	writeSavedQuery(t, queries, "delete-all", "SQL", "DELETE FROM customer")
	_, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "delete-all", "--env", "local", "--format", "json")
	assert.NotZero(t, code)
	assert.Contains(t, stderr, "SELECT or WITH")
	return projectDir, queries
}

const (
	realOracleAdminVar = "DATATUG_TEST_ORACLE_URL"
	realOracleScanVar  = "DATATUG_TEST_ORACLE_SCAN_URL"
	realMSSQLAdminVar  = "DATATUG_TEST_MSSQL_URL"
	realMSSQLScanVar   = "DATATUG_TEST_MSSQL_SCAN_URL"
)

// adminURL is the administrator's URL in variable, or a skip of the test when it is not set.
func adminURL(t *testing.T, variable, job string) *url.URL {
	t.Helper()
	raw, ok := os.LookupEnv(variable)
	if !ok || raw == "" {
		t.Skipf("%s is not set: this test runs against a real server, which only the CI job %q provides", variable, job)
	}
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "%s is not a URL", variable)
	return parsed
}

func TestOracleScanAndQueryJourney(t *testing.T) {
	admin := adminURL(t, realOracleAdminVar, "Journey (Oracle Database Free)")
	user := strings.ToUpper("dt_ora_" + randomHex(t, 5))
	password := "Pw" + randomHex(t, 10)
	scan := *admin
	scan.User = url.UserPassword(user, password)
	// The fixture opens go-ora itself, with fast login off as dalgo2oracle opens it: with it on, every later
	// connection of the process to Oracle Database Free ends in EOF. DataTug is given the URL without the option.
	fixture := func(u url.URL) string {
		query := u.Query()
		query.Set("FAST LOGIN", "false")
		u.RawQuery = query.Encode()
		return u.String()
	}
	adminDSN, scanDSN := fixture(*admin), scan.String()

	execOn(t, "oracle", adminDSN,
		fmt.Sprintf(`CREATE USER %s IDENTIFIED BY "%s" QUOTA UNLIMITED ON USERS`, user, password),
		fmt.Sprintf(`GRANT CREATE SESSION, CREATE TABLE TO %s`, user))
	// The commands of the test leave their pools' sessions open (a process of the CLI ends with its command):
	// they are ended before the user is dropped, which Oracle refuses while one is connected (ORA-01940).
	t.Cleanup(func() {
		execOn(t, "oracle", adminDSN,
			fmt.Sprintf(`BEGIN
  FOR s IN (SELECT sid, serial# AS serial FROM v$session WHERE username = '%s') LOOP
    EXECUTE IMMEDIATE 'ALTER SYSTEM KILL SESSION ''' || s.sid || ',' || s.serial || ''' IMMEDIATE';
  END LOOP;
END;`, user))
		// A killed session can take a moment to end: the drop is tried again for a while.
		db, err := sql.Open("oracle", adminDSN)
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		for attempt := 0; ; attempt++ {
			if _, err = db.Exec(fmt.Sprintf(`DROP USER %s CASCADE`, user)); err == nil || attempt == 20 {
				break
			}
			time.Sleep(time.Second)
		}
		require.NoError(t, err, "the fixture's user is dropped")
	})
	quote := func(name string) string { return `"` + name + `"` }
	execOn(t, "oracle", fixture(scan), serverJourneyDDL(quote, "NUMBER(19)", "NUMBER(10)", "VARCHAR2", "NUMBER")...)

	t.Setenv(realOracleScanVar, scanDSN)
	useTheRealServerOpeners(t)
	server := realServer{
		driver: "oracle", previewEnv: dbcopy.OraclePreviewEnv, scanVar: realOracleScanVar, scanURL: scanDSN,
		parts: []string{password, user, strings.ToLower(user), ":" + admin.Port(), "oracle://"},
		sql:   `SELECT "id", "name" FROM "customer" WHERE "score" IS NOT NULL ORDER BY "score" DESC;`,
	}
	projectDir, queries := server.journey(t)

	// A query that locks rows for an update is refused before it reaches the server.
	writeSavedQuery(t, queries, "lock-rows", "SQL", `SELECT "id" FROM "customer" FOR UPDATE`)
	_, stderr, code := runQuery(t, "", "--project", projectDir, "--query", "lock-rows", "--env", "local", "--format", "json")
	assert.NotZero(t, code)
	assert.Contains(t, stderr, "the text holds UPDATE")

	// Past that check, the read runs in a READ ONLY transaction, so the server refuses a write.
	err := writeThroughTheRead(t, server.scanVar, `UPDATE "customer" SET "name" = 'changed' WHERE "id" = 1`)
	assert.Error(t, err, "the server refuses an update in a READ ONLY transaction")
	assert.Equal(t, []int{1}, server.ids(t, "from: {name: customer}\nwhere: {op: '==', left: {field: name}, right: {value: Ada Lovelace}}\n"), "the row is as it was")
}

func TestSQLServerScanAndQueryJourney(t *testing.T) {
	admin := adminURL(t, realMSSQLAdminVar, "Journey (SQL Server 2022)")
	suffix := randomHex(t, 5)
	database, login, password := "dt_ms_db_"+suffix, "dt_ms_user_"+suffix, "Pw-"+randomHex(t, 10)+"-aA9"
	// go-mssqldb takes the database as a parameter; DataTug's URL names it in the path.
	driverDSN := func(user, pass, db string) string {
		u := url.URL{Scheme: "sqlserver", User: url.UserPassword(user, pass), Host: admin.Host}
		query := admin.Query()
		query.Set("database", db)
		u.RawQuery = query.Encode()
		return u.String()
	}
	adminPassword, _ := admin.User.Password()
	adminDSN := driverDSN(admin.User.Username(), adminPassword, "master")
	execOn(t, "sqlserver", adminDSN,
		fmt.Sprintf(`CREATE DATABASE [%s]`, database),
		fmt.Sprintf(`CREATE LOGIN [%s] WITH PASSWORD = '%s', CHECK_POLICY = OFF`, login, password))
	t.Cleanup(func() {
		execOn(t, "sqlserver", adminDSN,
			fmt.Sprintf(`ALTER DATABASE [%s] SET SINGLE_USER WITH ROLLBACK IMMEDIATE`, database),
			fmt.Sprintf(`DROP DATABASE [%s]`, database),
			fmt.Sprintf(`DROP LOGIN [%s]`, login))
	})
	execOn(t, "sqlserver", driverDSN(admin.User.Username(), adminPassword, database),
		fmt.Sprintf(`CREATE USER [%s] FOR LOGIN [%s]`, login, login),
		fmt.Sprintf(`ALTER ROLE db_owner ADD MEMBER [%s]`, login))
	quote := func(name string) string { return "[" + name + "]" }
	userDSN := driverDSN(login, password, database)
	execOn(t, "sqlserver", userDSN, serverJourneyDDL(quote, "BIGINT", "INT", "NVARCHAR", "DECIMAL")...)

	scan := url.URL{Scheme: "sqlserver", User: url.UserPassword(login, password), Host: admin.Host, Path: "/" + database, RawQuery: admin.RawQuery}
	t.Setenv(realMSSQLScanVar, scan.String())
	useTheRealServerOpeners(t)
	server := realServer{
		driver: "sqlserver", previewEnv: dbcopy.SQLServerPreviewEnv, scanVar: realMSSQLScanVar, scanURL: scan.String(),
		parts: []string{password, login, database, ":" + admin.Port(), "sqlserver://"},
		sql:   `SELECT id, name FROM customer WHERE score IS NOT NULL ORDER BY score DESC;`,
	}
	projectDir, queries := server.journey(t)

	// A write in a WITH query, or a batch that commits, is refused before it reaches the server.
	for id, text := range map[string]string{
		"sneaky-update": "WITH c AS (SELECT 1 AS id) UPDATE customer SET name = 'changed' OUTPUT inserted.id WHERE id IN (SELECT id FROM c)",
		"sneaky-commit": "SELECT 1 AS id COMMIT UPDATE customer SET name = 'changed' WHERE id = 1",
	} {
		writeSavedQuery(t, queries, id, "SQL", text)
		_, stderr, code := runQuery(t, "", "--project", projectDir, "--query", id, "--env", "local", "--format", "json")
		assert.NotZero(t, code, id)
		assert.Contains(t, stderr, "the text holds", id)
	}

	// Past that check SQL Server has no read-only session: a read runs in a transaction that is rolled back, so a
	// write that reaches the server is undone, and the row is as it was.
	_ = writeThroughTheRead(t, server.scanVar, "UPDATE customer SET name = 'changed' OUTPUT inserted.id WHERE id = 1")
	assert.Equal(t, []int{1}, server.ids(t, "from: {name: customer}\nwhere: {op: '==', left: {field: name}, right: {value: Ada Lovelace}}\n"), "the update was rolled back")
}

// writeThroughTheRead sends text to the server through the read every query of DataTug makes (the protected open
// of query run), past the check of the text, and returns the error of the read, if any.
func writeThroughTheRead(t *testing.T, scanVar, text string) error {
	t.Helper()
	ref, err := dbcopy.Parse("env:" + scanVar)
	require.NoError(t, err)
	db, err := ref.OpenProtected(context.Background())
	require.NoError(t, err)
	defer func() {
		if closer, ok := db.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), dal.NewTextQuery(text, nil))
	if err != nil {
		return err
	}
	_, err = dal.ReadAllToRecords(context.Background(), reader)
	return err
}
