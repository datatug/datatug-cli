package commands

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The journey of a MySQL source against a REAL server: `datatug scan -D mysql --dsn-env NAME`, then
// `datatug query run` with a TugQL query on --db env:NAME and with a saved SQL query of the scanned
// project, with the real commands, the real opener and the real adapter (dalgo2mysql). It runs in one CI
// job only ("Journey (MySQL 8.4)" of .github/workflows/journey-mysql.yml, which starts a server and sets
// DATATUG_TEST_MYSQL_URL) and skips everywhere else. scripts/check-journey-mysql.sh fails the job when a
// test of it skips or prints no PASS line.
//
// The server URL is an administrator's. The test makes a database and a user of its own, with names and
// a password that no other part of the URL has, so that a leak of any part of the connection into the
// project or the output can be told apart.

const (
	// realMySQLAdminVar holds the URL of the server of the CI job: mysql://root:password@host:port/mysql.
	realMySQLAdminVar = "DATATUG_TEST_MYSQL_URL"

	// realMySQLScanVar is the variable the commands under test read their URL from.
	realMySQLScanVar = "DATATUG_TEST_MYSQL_SCAN_URL"
)

// realMySQLServer is the fixture database of one test and the parts of the connection to it.
type realMySQLServer struct {
	scanURL                        string
	port, user, password, database string
}

// parts are the pieces of the connection that no file of a project and no output may hold.
func (s realMySQLServer) parts() []string {
	return []string{s.password, s.user, s.database, ":" + s.port, "mysql://"}
}

// mysqlConfigOf is the go-sql-driver configuration of a mysql:// URL.
func mysqlConfigOf(t *testing.T, rawURL string) *mysql.Config {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	cfg := mysql.NewConfig()
	cfg.User = parsed.User.Username()
	cfg.Passwd, _ = parsed.User.Password()
	cfg.Net = "tcp"
	cfg.Addr = parsed.Host
	cfg.DBName = strings.TrimPrefix(parsed.Path, "/")
	cfg.MultiStatements = false
	return cfg
}

// execMySQL runs each statement on the database at rawURL.
func execMySQL(t *testing.T, rawURL string, statements ...string) {
	t.Helper()
	db, err := sql.Open("mysql", mysqlConfigOf(t, rawURL).FormatDSN())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		_, err = db.Exec(statement)
		require.NoError(t, err, "statement of the fixture failed: %s", firstLine(statement))
	}
}

// realMySQLFixtureDDL is the database the journey scans and queries.
var realMySQLFixtureDDL = []string{
	`CREATE TABLE customer (
		id BIGINT PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		email VARCHAR(200) NULL,
		score INT NULL)`,
	`CREATE TABLE invoice (
		id INT PRIMARY KEY,
		customer_id BIGINT NOT NULL,
		total DECIMAL(12,2) NOT NULL DEFAULT 0,
		CONSTRAINT invoice_customer FOREIGN KEY (customer_id) REFERENCES customer(id))`,
	"CREATE TABLE `MixedCase` (`OrderId` INT PRIMARY KEY, `ItemName` VARCHAR(40))",
	"INSERT INTO `MixedCase` (`OrderId`, `ItemName`) VALUES (7, 'lamp')",
	`CREATE TABLE order_line (order_id INT NOT NULL, line_no INT NOT NULL, sku VARCHAR(20), PRIMARY KEY (line_no, order_id))`,
	`INSERT INTO customer (id, name, email, score) VALUES (1, 'Ada Lovelace', 'ada@example.com', 30), (2, 'Alan Turing', NULL, NULL), (3, 'Grace Hopper', 'grace@example.com', 10)`,
	`INSERT INTO invoice (id, customer_id, total) VALUES (10, 1, 12.50), (11, 3, 99.99)`,
}

// newRealMySQLServer skips the test unless the CI job's server is there, then makes a database and a
// user of the test's own, fills the database and returns the parts of the connection to it. The user may
// write the database (ALL PRIVILEGES): what keeps a read from writing is DataTug's read-only session, and
// the journey checks it. Both are dropped when the test ends.
func newRealMySQLServer(t *testing.T) realMySQLServer {
	t.Helper()
	adminURL, ok := os.LookupEnv(realMySQLAdminVar)
	if !ok || adminURL == "" {
		t.Skipf("%s is not set: this test runs against a real MySQL server, which only the CI job \"Journey (MySQL 8.4)\" provides", realMySQLAdminVar)
	}
	admin, err := url.Parse(adminURL)
	require.NoError(t, err, "%s is not a URL", realMySQLAdminVar)
	require.Equal(t, "mysql", admin.Scheme, "%s must be a mysql:// URL", realMySQLAdminVar)
	_, port, err := net.SplitHostPort(admin.Host)
	require.NoError(t, err, "%s must name the port", realMySQLAdminVar)

	suffix := randomHex(t, 6)
	server := realMySQLServer{port: port, user: "dt_my_user_" + suffix, password: "Pw" + randomHex(t, 12), database: "dt_my_db_" + suffix}
	scan := *admin
	scan.User = url.UserPassword(server.user, server.password)
	scan.Path = "/" + server.database
	server.scanURL = scan.String()

	execMySQL(t, adminURL,
		fmt.Sprintf("CREATE DATABASE `%s`", server.database),
		fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY '%s'", server.user, server.password),
		fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%%'", server.database, server.user))
	t.Cleanup(func() {
		execMySQL(t, adminURL,
			fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", server.database),
			fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", server.user))
	})
	execMySQL(t, server.scanURL, realMySQLFixtureDDL...)

	t.Setenv(realMySQLScanVar, server.scanURL)
	t.Cleanup(api.SetOpenSchemaScanForTest(dbcopy.BackendRef.OpenSchemaScan))
	t.Cleanup(dbcopy.SetServerOpenersForTest(dbcopy.RealServerOpeners()))
	return server
}

// partsIn is each part of the connection that text holds.
func (s realMySQLServer) partsIn(text string) (found []string) {
	for _, part := range s.parts() {
		if strings.Contains(text, part) {
			found = append(found, part)
		}
	}
	return found
}

// leaksIn is each file under dir that holds a part of the connection, by name or content.
func (s realMySQLServer) leaksIn(t *testing.T, dir string) (leaks []string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		text := path
		if !entry.IsDir() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text += "\n" + string(content)
		}
		for _, part := range s.parts() {
			if strings.Contains(text, part) {
				leaks = append(leaks, fmt.Sprintf("%s holds %q", path, part))
			}
		}
		return nil
	}))
	return leaks
}

// realMySQLIDs runs `datatug query run` for query (a TugQL document) on --db env:realMySQLScanVar and
// returns the "id" of each row, in order.
func realMySQLIDs(t *testing.T, query string) []int {
	t.Helper()
	stdout, stderr, code := runQuery(t, query, "--db", "env:"+realMySQLScanVar, "-f", "-", "--format", "json", "--no-policies")
	require.Zero(t, code, "the query exits 0: %s", stderr)
	var ids []int
	for _, row := range decodeObjects(t, stdout) {
		id, ok := row["id"].(float64)
		require.True(t, ok, "a row has a numeric id: %v", row)
		ids = append(ids, int(id))
	}
	return ids
}

func TestMySQLScanAndQueryJourney(t *testing.T) {
	server := newRealMySQLServer(t)
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))

	// 1. The scan: exit 0, nothing on stdout, and a project that names the variable and no part of the URL.
	stdout, stderr, err := runScanCommandAllOutputs(t, "-d", projectDir, "-D", "mysql", "--dsn-env", realMySQLScanVar, "--db", "shop", "--env", "local")
	require.NoError(t, err, "the scan of the real server exits 0: %s", stderr)
	assert.Empty(t, stdout)
	assert.Empty(t, server.partsIn(stdout+stderr), "the scan's output names no part of the connection")
	assert.Empty(t, server.leaksIn(t, projectDir), "no file of the project holds a part of the connection")

	descriptor, err := os.ReadFile(filepath.Join(projectDir, "connections", "local", "shop.json"))
	require.NoError(t, err)
	var named struct {
		DSNEnv string `json:"dsnEnv"`
	}
	require.NoError(t, json.Unmarshal(descriptor, &named))
	assert.Equal(t, realMySQLScanVar, named.DSNEnv, "the descriptor names the variable")

	// The tables are recorded under the schema "default", each with its columns and primary key.
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
	stdout, stderr, code := runQuery(t, "from: {name: customer}\n", "--db", "env:"+realMySQLScanVar, "-f", "-", "--format", "json", "--no-policies")
	assert.Equal(t, exitCodeDatabase, code, "with the switch off the query is refused: %s", stdout)
	assert.Contains(t, stderr, dbcopy.MySQLPreviewEnv)
	t.Setenv(dbcopy.MySQLPreviewEnv, "1")
	assert.Equal(t, []int{1, 2, 3}, realMySQLIDs(t, "from: {name: customer}\norderBy:\n  - field: id\n"))
	assert.Equal(t, []int{2, 3, 1}, realMySQLIDs(t, "from: {name: customer}\norderBy:\n  - field: score\n  - field: id\n"))
	assert.Equal(t, []int{1, 3}, realMySQLIDs(t, "from: {name: customer}\norderBy:\n  - field: score\n    desc: true\n  - field: id\nlimit: 2\n"))
	assert.Equal(t, []int{3}, realMySQLIDs(t, "from: {name: customer}\nwhere: {op: '==', left: {field: name}, right: {value: Grace Hopper}}\n"))
	assert.Equal(t, []int{11}, realMySQLIDs(t, "from: {name: invoice}\nwhere: {op: '==', left: {field: customer_id}, right: {value: 3}}\n"))
	stdout, stderr, code = runQuery(t, "from: {name: MixedCase}\n", "--db", "env:"+realMySQLScanVar, "-f", "-", "--format", "json", "--no-policies")
	require.Zero(t, code, stderr)
	assert.Equal(t, []map[string]any{{"OrderId": float64(7), "ItemName": "lamp"}}, withoutKey(decodeObjects(t, stdout)), "a mixed-case table is read by its exact name")
	assert.Empty(t, server.partsIn(stdout+stderr))

	// 3. Saved queries of the scanned project: a SQL one and a TugQL one, on the one database of environment local.
	queries := filepath.Join(projectDir, "queries")
	require.NoError(t, os.MkdirAll(queries, 0o755))
	writeSavedQuery(t, queries, "top-customers", "SQL", "SELECT id, name FROM customer WHERE score IS NOT NULL ORDER BY score DESC;\n")
	writeSavedQuery(t, queries, "graces", "TugQL", "from: {name: customer}\nwhere: {op: '==', left: {field: id}, right: {value: 3}}\n")
	stdout, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "top-customers", "--env", "local", "--format", "json")
	require.Zero(t, code, stderr)
	assert.Equal(t, []map[string]any{{"id": float64(1), "name": "Ada Lovelace"}, {"id": float64(3), "name": "Grace Hopper"}}, withoutKey(decodeObjects(t, stdout)))
	stdout, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "graces", "--env", "local", "--format", "json")
	require.Zero(t, code, stderr)
	rows := decodeObjects(t, stdout)
	require.Len(t, rows, 1)
	assert.Equal(t, "Grace Hopper", rows[0]["name"])

	// 4. The session is read only: a statement that passes the SELECT/WITH check but writes is refused by the
	// server, and the row is as it was. Anything else is refused before it reaches the server.
	writeSavedQuery(t, queries, "sneaky-update", "SQL", "WITH c AS (SELECT 1 AS id) UPDATE customer SET name = 'changed' WHERE id IN (SELECT id FROM c)")
	_, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "sneaky-update", "--env", "local", "--format", "json")
	assert.NotZero(t, code, "a write through a saved query fails: %s", stderr)
	writeSavedQuery(t, queries, "set-session", "SQL", "SET SESSION transaction_read_only = 0")
	_, stderr, code = runQuery(t, "", "--project", projectDir, "--query", "set-session", "--env", "local", "--format", "json")
	assert.NotZero(t, code)
	assert.Contains(t, stderr, "SELECT or WITH")
	assert.Equal(t, []int{1}, realMySQLIDs(t, "from: {name: customer}\nwhere: {op: '==', left: {field: name}, right: {value: Ada Lovelace}}\n"), "the row the update named is as it was")
}

// writeSavedQuery writes the saved query id of type queryType (SQL or TugQL) with text into the queries folder:
// a SQL body as <id>.query.sql, a structured TugQL document as <id>.query.tugql.yaml.
func writeSavedQuery(t *testing.T, queriesDir, id, queryType, text string) {
	t.Helper()
	meta := fmt.Sprintf(`{"createdAt":"2026-10-23T00:00:00Z","id":%q,"type":%q}`, id, queryType)
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, id+".query.json"), []byte(meta), 0o644))
	body := id + ".query." + strings.ToLower(queryType)
	if queryType == "TugQL" {
		body += ".yaml"
	}
	require.NoError(t, os.WriteFile(filepath.Join(queriesDir, body), []byte(text), 0o644))
}

// withoutKey is rows without the "$key" column the output adds to every row.
func withoutKey(rows []map[string]any) []map[string]any {
	for _, row := range rows {
		delete(row, "$key")
	}
	return rows
}
