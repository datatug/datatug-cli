package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the journey of scan_journey_postgres_test.go run against a REAL PostgreSQL
// server: the real cobra command, the real opener (dbcopy.BackendRef.OpenSchemaScan), the
// real DALgo reader, and the readers behind chat, serve and the web app over what the scan
// wrote. It runs in one CI job only ("Journey (PostgreSQL)" of .github/workflows/golangci.yml,
// which has a postgres:17 service and sets DATATUG_TEST_POSTGRES_URL), and skips everywhere
// else: no other test dials. scripts/check-journey-postgres.sh runs it there and fails the job
// when any of its tests skips or does not print its PASS line.
//
// The server is an administrator's connection URL. The test makes a role and a database of its
// own on it, with names and a password that no other part of the URL has, so that a leak of any
// part of the connection into a project or into the output can be told from the text the scan
// is expected to write ("postgres", the driver).

const (
	// realPgAdminVar holds the URL of the server of the CI job: an administrator's, to the
	// maintenance database. The name is part of the contract with the workflow.
	realPgAdminVar = "DATATUG_TEST_POSTGRES_URL"

	// realPgScanVar is the variable the scan under test reads its URL from: the role and the
	// database the test made. A project may name only a variable that starts with DATATUG_.
	realPgScanVar = "DATATUG_TEST_POSTGRES_SCAN_URL"
)

// realPgServer is the fixture database of one test and the parts of the connection to it.
type realPgServer struct {
	scanURL                              string // role, password and fixture database
	host, port, user, password, database string
}

// parts are the pieces of the connection that no file of a project may hold, and that the
// output of a scan may hold only inside the display form of its source.
func (s realPgServer) parts() []string {
	return []string{s.password, s.user, s.host, ":" + s.port, "port " + s.port, "port=" + s.port, "sslmode", "postgres://"}
}

// urlFor is the connection URL of the fixture role with the parts replaced: the database, the
// password, the host and port, whichever is not empty.
func (s realPgServer) urlFor(database, password, hostPort string) string {
	parsed, err := url.Parse(s.scanURL)
	if err != nil {
		panic(err) // the URL was built by newRealPgServer
	}
	if database != "" {
		parsed.Path = "/" + database
	}
	if password != "" {
		parsed.User = url.UserPassword(s.user, password)
	}
	if hostPort != "" {
		parsed.Host = hostPort
	}
	return parsed.String()
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}

// execAll runs each statement on the database at rawURL.
func execAll(t *testing.T, rawURL string, statements ...string) {
	t.Helper()
	db, err := sql.Open("pgx", rawURL) // the driver dalgo2postgres registers
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		_, err = db.Exec(statement)
		require.NoError(t, err, "statement of the fixture or of its clean-up failed: %s", firstLine(statement))
	}
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

// realPgFixtureDDL is the database every real journey scans. In the schema public: the
// customer and invoice tables with a foreign key, a mixed-case table with mixed-case columns,
// a composite primary key whose order differs from the order of the columns, a table without a
// primary key, a view, the type matrix of the adapter, columns with a default (now(), a
// constant) and NOT NULL columns. And a second schema, sales, with a table of the same name as
// one of public (customer, with other columns: an identity column, a constant default, now(), a
// sequence and a generated column), a table of its own, a view and a materialized view.
var realPgFixtureDDL = []string{
	`CREATE TABLE customer (
		id bigint PRIMARY KEY,
		name text NOT NULL,
		email text,
		created timestamptz NOT NULL DEFAULT now())`,
	`CREATE TABLE invoice (
		id integer PRIMARY KEY,
		customer_id bigint NOT NULL REFERENCES customer(id),
		total numeric(12,2) NOT NULL DEFAULT 0,
		status text NOT NULL DEFAULT 'open')`,
	`CREATE TABLE "MixedCase" (
		"OrderId" integer PRIMARY KEY,
		"ItemName" text,
		"Qty" smallint)`,
	`CREATE TABLE order_line (
		order_id integer NOT NULL,
		line_no integer NOT NULL,
		sku text,
		PRIMARY KEY (line_no, order_id))`,
	`CREATE TABLE audit_log (at timestamp, message text)`,
	`CREATE VIEW customer_names AS SELECT id, name FROM customer`,
	`CREATE TABLE type_matrix (
		id integer PRIMARY KEY,
		c_smallint smallint,
		c_integer integer,
		c_bigint bigint,
		c_numeric numeric(10,3),
		c_numeric_plain numeric,
		c_real real,
		c_double double precision,
		c_boolean boolean,
		c_date date,
		c_timestamp timestamp,
		c_timestamptz timestamptz,
		c_uuid uuid,
		c_jsonb jsonb,
		c_text text,
		c_varchar varchar(40))`,
	`INSERT INTO customer (id, name) VALUES (1, 'Ada Lovelace'), (2, 'Alan Turing')`,
	`CREATE SCHEMA sales`,
	`CREATE SEQUENCE sales.ticket_seq`,
	`CREATE TABLE sales.customer (
		id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		label text NOT NULL DEFAULT 'new',
		opened timestamptz NOT NULL DEFAULT now(),
		ticket integer NOT NULL DEFAULT nextval('sales.ticket_seq'),
		doubled integer GENERATED ALWAYS AS (id * 2) STORED)`,
	`CREATE TABLE sales.refund (id integer PRIMARY KEY, amount numeric(12,2))`,
	`CREATE VIEW sales.customer_labels AS SELECT id, label FROM sales.customer`,
	`CREATE MATERIALIZED VIEW sales.customer_tickets AS SELECT id, ticket FROM sales.customer`,
}

// newRealPgServer skips the test unless the CI job's server is there, then makes a role and a
// database of the test's own on it, fills the database with realPgFixtureDDL and returns the
// parts of the connection to it. The role and the database are dropped when the test ends. It
// also puts the real opener behind the scan, in place of the one that stops the run (see
// scan_never_dials_test.go), and the URL in the variable the scan reads.
func newRealPgServer(t *testing.T) realPgServer {
	t.Helper()
	adminURL, ok := os.LookupEnv(realPgAdminVar)
	if !ok || adminURL == "" {
		t.Skipf("%s is not set: this test runs against a real PostgreSQL server, which only the CI job \"Journey (PostgreSQL)\" provides", realPgAdminVar)
	}
	admin, err := url.Parse(adminURL)
	require.NoError(t, err, "%s is not a URL", realPgAdminVar)
	require.Equal(t, "postgres", admin.Scheme, "%s must be a postgres:// URL", realPgAdminVar)

	suffix := randomHex(t, 6)
	server := realPgServer{
		host:     admin.Hostname(),
		port:     admin.Port(),
		user:     "dt_scan_user_" + suffix,
		password: "Pw" + randomHex(t, 12),
		database: "dt_scan_db_" + suffix,
	}
	require.NotEmpty(t, server.port, "%s must name the port", realPgAdminVar)
	scan := *admin
	scan.User = url.UserPassword(server.user, server.password)
	scan.Path = "/" + server.database
	server.scanURL = scan.String()

	execAll(t, adminURL,
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, server.user, server.password),
		fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, server.database, server.user))
	t.Cleanup(func() {
		execAll(t, adminURL,
			fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, server.database),
			fmt.Sprintf(`DROP ROLE IF EXISTS %s`, server.user))
	})
	execAll(t, server.scanURL, realPgFixtureDDL...)

	t.Setenv(realPgScanVar, server.scanURL)
	t.Cleanup(api.SetOpenSchemaScanForTest(dbcopy.BackendRef.OpenSchemaScan))
	return server
}

// realCol is a column as the scan is expected to record it from the fixture.
type realCol struct {
	name     string
	dbType   string // as the scan records it: the portable type of the adapter
	pk       int    // position in the primary key, 0 for none
	nullable bool
}

// realPgWant is what the scan records of schema public of the fixture, by table, in the order
// of the columns of the table. Types are the portable types of the adapter's type matrix:
// integers are int, numeric is decimal, real and double precision are float, date and the
// timestamps are time, and uuid, jsonb and text are string.
var realPgWant = map[string][]realCol{
	"customer": {
		{"id", "int", 1, false}, {"name", "string", 0, false}, {"email", "string", 0, true}, {"created", "time", 0, false},
	},
	"invoice": {
		{"id", "int", 1, false}, {"customer_id", "int", 0, false}, {"total", "decimal", 0, false}, {"status", "string", 0, false},
	},
	"MixedCase": {
		{"OrderId", "int", 1, false}, {"ItemName", "string", 0, true}, {"Qty", "int", 0, true},
	},
	// The key is (line_no, order_id): not the order of the columns.
	"order_line": {
		{"order_id", "int", 2, false}, {"line_no", "int", 1, false}, {"sku", "string", 0, true},
	},
	"audit_log": {
		{"at", "time", 0, true}, {"message", "string", 0, true},
	},
	"type_matrix": {
		{"id", "int", 1, false},
		{"c_smallint", "int", 0, true}, {"c_integer", "int", 0, true}, {"c_bigint", "int", 0, true},
		{"c_numeric", "decimal", 0, true}, {"c_numeric_plain", "decimal", 0, true},
		{"c_real", "float", 0, true}, {"c_double", "float", 0, true},
		{"c_boolean", "bool", 0, true},
		{"c_date", "time", 0, true}, {"c_timestamp", "time", 0, true}, {"c_timestamptz", "time", 0, true},
		{"c_uuid", "string", 0, true}, {"c_jsonb", "string", 0, true}, {"c_text", "string", 0, true}, {"c_varchar", "string", 0, true},
	},
}

// realPgWantViews is what the scan records of the views of schema public: a view is saved
// as a view, where the layout keeps views.
var realPgWantViews = map[string][]realCol{
	"customer_names": {
		{"id", "int", 0, true}, {"name", "string", 0, true},
	},
}

// realPgWantSales is what the scan records of the tables of schema sales, and realPgWantSalesViews
// of its views and materialized views. The table customer is not the customer of schema public.
var (
	realPgWantSales = map[string][]realCol{
		"customer": {
			{"id", "int", 1, false}, {"label", "string", 0, false}, {"opened", "time", 0, false}, {"ticket", "int", 0, false}, {"doubled", "int", 0, true},
		},
		"refund": {
			{"id", "int", 1, false}, {"amount", "decimal", 0, true},
		},
	}
	realPgWantSalesViews = map[string][]realCol{
		"customer_labels":  {{"id", "int", 0, true}, {"label", "string", 0, true}},
		"customer_tickets": {{"id", "int", 0, true}, {"ticket", "int", 0, true}},
	}
)

// realPgWantDefaults is the default the scan records of each column that has one, by the folder
// of the relation in the model (<schema>/<tables|views>/<name>): the text of the SQL expression as
// the server stores it, and nothing for a column that has none. An identity column has none; a
// generated column is the text the reader builds from its expression.
var realPgWantDefaults = map[string]map[string]string{
	"public/tables/customer":       {"created": "now()"},
	"public/tables/invoice":        {"total": "0", "status": "'open'::text"},
	"public/tables/MixedCase":      {},
	"public/tables/order_line":     {},
	"public/tables/audit_log":      {},
	"public/tables/type_matrix":    {},
	"public/views/customer_names":  {},
	"sales/tables/customer":        {"label": "'new'::text", "opened": "now()", "ticket": "nextval('sales.ticket_seq'::regclass)", "doubled": "GENERATED ALWAYS AS ((id * 2))"},
	"sales/tables/refund":          {},
	"sales/views/customer_labels":  {},
	"sales/views/customer_tickets": {},
}

// realPgRelations is every relation the scan is expected to save, by the folder of it in the model
// (<schema>/<tables|views>/<name>), with the columns it records.
func realPgRelations() map[string][]realCol {
	relations := map[string][]realCol{}
	for folder, group := range map[string]map[string][]realCol{
		"public/tables": realPgWant, "public/views": realPgWantViews, "sales/tables": realPgWantSales, "sales/views": realPgWantSalesViews,
	} {
		for name, columns := range group {
			relations[folder+"/"+name] = columns
		}
	}
	return relations
}

// storedColumn is a column of a columns file of the project, as the file holds it.
type storedColumn struct {
	Name       string  `json:"name"`
	Ordinal    int     `json:"ordinalPosition"`
	PKPosition int     `json:"pkPosition"`
	IsNullable bool    `json:"isNullable"`
	DbType     string  `json:"dbType"`
	Default    *string `json:"default"`
}

// storedColumns reads the columns file of the table of schema public of the model shop.
func storedColumns(t *testing.T, projectDir, table string) []storedColumn {
	t.Helper()
	return storedColumnsOf(t, projectDir, "public/tables/"+table)
}

// storedColumnsOf reads the columns file of the relation at folder (<schema>/<tables|views>/<name>)
// of the model shop.
func storedColumnsOf(t *testing.T, projectDir, folder string) []storedColumn {
	t.Helper()
	schema, _, _ := strings.Cut(folder, "/")
	name := folder[strings.LastIndex(folder, "/")+1:]
	data, err := os.ReadFile(filepath.Join(projectDir, "dbmodels", "shop", filepath.FromSlash(folder), schema+"."+name+".columns.json"))
	require.NoError(t, err, folder)
	var file struct {
		Columns []storedColumn `json:"columns"`
	}
	require.NoError(t, json.Unmarshal(data, &file), string(data))
	return file.Columns
}

// storedColumnMaps reads the columns file of the table the way storedColumns does, but keeps
// every key of each column, so that a key the file must not hold can be looked for.
func storedColumnMaps(t *testing.T, projectDir, table string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, "dbmodels", "shop", "public", "tables", table, "public."+table+".columns.json"))
	require.NoError(t, err, table)
	var file struct {
		Columns []map[string]any `json:"columns"`
	}
	require.NoError(t, json.Unmarshal(data, &file), string(data))
	byName := map[string]map[string]any{}
	for _, column := range file.Columns {
		byName[fmt.Sprint(column["name"])] = column
	}
	return byName
}

// runScanCommandAllOutputs runs the scan command as runScanCommand does and also returns what it
// wrote to its standard output, which the scan is expected to leave empty.
func runScanCommandAllOutputs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := DatatugCommand()
	var out, errOut bytes.Buffer
	root.SetArgs(append([]string{"scan"}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SilenceUsage, root.SilenceErrors = true, true
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// realPgScanArgs are the flags of a scan of the fixture into projectDir as environment local.
func realPgScanArgs(projectDir, variable string) []string {
	return []string{"-d", projectDir, "-D", "postgres", "--dsn-env", variable, "--db", "shop", "--env", "local"}
}

// leaksIn is each file under dir, or name of one, that holds a part of the connection, each
// key of a decoded .json file, at any depth, that only a connection has (connectionKeys), and
// each value of one, under whatever key, that is the port.
func (s realPgServer) leaksIn(t *testing.T, dir string) (leaks []string) {
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
			var decoded any
			if strings.HasSuffix(path, ".json") && json.Unmarshal(content, &decoded) == nil {
				for _, key := range keysNamed(decoded, connectionKeys) {
					leaks = append(leaks, fmt.Sprintf("%s holds the key %q", path, key))
				}
				if numbersEqual(decoded, s.port) > 0 {
					leaks = append(leaks, fmt.Sprintf("%s holds the value %s", path, s.port))
				}
			}
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

// numbersEqual counts the values of a decoded JSON document, at any depth and under any key,
// that are the number port, or the text of it.
func numbersEqual(value any, port string) (count int) {
	switch v := value.(type) {
	case map[string]any:
		for _, inner := range v {
			count += numbersEqual(inner, port)
		}
	case []any:
		for _, inner := range v {
			count += numbersEqual(inner, port)
		}
	case string:
		if v == port {
			count++
		}
	case float64:
		if number, err := strconv.ParseFloat(port, 64); err == nil && v == number {
			count++
		}
	}
	return count
}

// partsIn is each part of the connection that text holds.
func (s realPgServer) partsIn(text string) (found []string) {
	for _, part := range s.parts() {
		if strings.Contains(text, part) {
			found = append(found, part)
		}
	}
	return found
}

// outputLeaks is each part of the connection that output holds, other than inside the display
// form of source, the one line a scan is meant to name the server in (scheme, host, port and
// database: never the user, the password or the query).
func (s realPgServer) outputLeaks(output, source string) []string {
	return s.partsIn(strings.ReplaceAll(output, dbcopy.SourceDisplay(source), "<the display form of the source>"))
}

// This is the first stage of the whole-journey test of the plan: the PostgreSQL scan, read by
// the readers of chat, serve and the web app, against a real server.
func TestPostgresScanJourney(t *testing.T) {
	server := newRealPgServer(t)
	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	logged := captureScanLog(t)
	var stderrs []string
	scan := func() string {
		t.Helper()
		stdout, stderr, err := runScanCommandAllOutputs(t, realPgScanArgs(projectDir, realPgScanVar)...)
		require.NoError(t, err, "the scan of the real server exits 0")
		assert.Empty(t, stdout, "the scan writes nothing to its standard output")
		stderrs = append(stderrs, stdout, stderr) // stdout is searched for the connection too
		return stderr
	}

	// 1. I scan the database into an empty folder: exit 0, and the folder is a project.
	stderr := scan()
	assert.Empty(t, stderr, "a scan with nothing to leave out says nothing on stderr")
	t.Logf("log of the first scan:\n%s", logged.String())

	var wantFiles []string
	for folder := range realPgRelations() {
		schema, _, _ := strings.Cut(folder, "/")
		wantFiles = append(wantFiles, "dbmodels/shop/"+folder+"/"+schema+"."+folder[strings.LastIndex(folder, "/")+1:]+".columns.json")
	}
	wantFiles = append(wantFiles,
		"README.md",
		"connections/local/shop.json",
		"datatug-project.json",
		"dbmodels/shop/shop.dbmodel.json",
		"environments/local/catalogs/shop/shop.db.json",
		"environments/local/local.env.json")
	slices.Sort(wantFiles)
	assert.Equal(t, wantFiles, projectFiles(t, projectDir, ""),
		"the files of the scan: one columns file per table and view of each schema, a view and a materialized view among the views, and the two tables called customer each in its own schema")

	store, id := filestore.NewSingleProjectStore(projectDir, "")
	projStore := store.GetProjectStore(id)
	project, err := projStore.LoadProject(ctx)
	require.NoError(t, err)
	require.NoError(t, project.Validate())
	assert.Equal(t, []string{"local"}, project.Environments.IDs())
	assert.Equal(t, []string{"shop"}, project.DbModels.IDs())
	assert.Equal(t, map[string]any{"dsnEnv": realPgScanVar}, readJSONMap(t, filepath.Join(projectDir, "connections", "local", "shop.json")),
		"the descriptor names the variable and nothing else")

	// 2. Every table by its exact name, every column by its exact name with the type the scan
	// records, the primary-key positions (the composite key in its order) and the nullability.
	schema, err := api.GetCatalogSchema(projectDir, "local", "shop")
	require.NoError(t, err)
	wantRelations := map[string][]journeyColumn{}
	for folder, columns := range realPgRelations() {
		parts := strings.Split(folder, "/")
		dbType := map[string]string{"tables": "BASE TABLE", "views": "VIEW"}[parts[1]]
		key := parts[0] + "." + parts[2] + " (" + dbType + ")"
		for _, column := range columns {
			wantRelations[key] = append(wantRelations[key], journeyColumn{Name: column.name, PKPos: column.pk, DbType: column.dbType})
		}
	}
	assert.Equal(t, wantRelations, relationColumns(schema))
	for folder, columns := range realPgRelations() {
		var stored []realCol
		defaults := map[string]string{}
		for i, column := range storedColumnsOf(t, projectDir, folder) {
			assert.Equal(t, i+1, column.Ordinal, "%s.%s keeps its place in the table", folder, column.Name)
			stored = append(stored, realCol{column.Name, column.DbType, column.PKPosition, column.IsNullable})
			if column.Default != nil {
				defaults[column.Name] = *column.Default
			}
		}
		assert.Equal(t, columns, stored, "the columns file of %s", folder)
		assert.Equal(t, realPgWantDefaults[folder], defaults, "the defaults recorded in the columns file of %s", folder)
	}
	assert.Len(t, realPgWantDefaults, len(realPgRelations()), "every relation has its defaults listed, the empty ones too")

	// The length of a bounded text column is recorded, and a text column with no bound has none:
	// varchar(40) is 40, text has no charMaxLength at all.
	matrix := storedColumnMaps(t, projectDir, "type_matrix")
	assert.EqualValues(t, 40, matrix["c_varchar"]["charMaxLength"], "type_matrix.c_varchar is varchar(40)")
	assert.NotContains(t, matrix["c_text"], "charMaxLength", "type_matrix.c_text is text, which has no length")

	// A column that has no default has no "default" key in its file at all (a column of the
	// type matrix, and the identity column of sales.customer, which the server reports apart).
	assert.NotContains(t, matrix["c_text"], "default")
	assert.NotContains(t, storedColumnMaps(t, projectDir, "customer")["email"], "default")

	// 3. The readers of serve, chat and the web app see the same tables, in the one schema.
	listed, err := api.GetCatalogTables(projectDir, "local", "shop")
	require.NoError(t, err)
	var listedTables []string
	for _, table := range listed.Tables {
		listedTables = append(listedTables, table.Schema+"."+table.Name)
	}
	wantTables := []string{"public.MixedCase", "public.audit_log", "public.customer", "public.invoice", "public.order_line", "public.type_matrix", "sales.customer", "sales.refund"}
	wantViews := []string{"public.customer_names", "sales.customer_labels", "sales.customer_tickets"}
	assert.Equal(t, wantTables, listedTables)
	var listedViews []string
	for _, view := range listed.Views {
		listedViews = append(listedViews, view.Schema+"."+view.Name)
	}
	assert.Equal(t, wantViews, listedViews, "a view and a materialized view are views, in the schema they are in")
	envs, catalogs, tables, views := webReaderTables(t, projectDir)
	assert.Equal(t, []string{"local"}, envs)
	assert.Equal(t, []string{"shop"}, catalogs)
	assert.Equal(t, wantTables, tables, "every schema is read: the table customer of sales is not the one of public")
	assert.Equal(t, wantViews, views)
	sources, err := api.ListSources(ctx, projStore, projectDir, "local")
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "env:"+realPgScanVar, sources[0].URL, "the source is the variable, never the URL")

	// 4. A second scan of an unchanged database leaves the folder byte-identical.
	first := treeHashes(t, projectDir, "")
	require.Len(t, first, len(wantFiles))
	stderr = scan()
	assert.Empty(t, stderr)
	assert.Equal(t, first, treeHashes(t, projectDir, ""), "a rescan of an unchanged database leaves the folder byte-identical")

	// 5. A table is dropped; the next scan removes its folder with its line, and nothing else changes.
	execAll(t, server.scanURL, `DROP TABLE audit_log`)
	stderr = scan()
	assert.Equal(t, `removed: dbmodels/shop/public/tables/audit_log: table "audit_log" of schema "public" is no longer in the database`+"\n", stderr,
		"one line for the folder that was removed, which names it")
	added, removed, changed := diffTrees(first, treeHashes(t, projectDir, ""))
	assert.Empty(t, added)
	assert.Equal(t, []string{"dbmodels/shop/public/tables/audit_log/public.audit_log.columns.json"}, removed)
	assert.Empty(t, changed, "every other file is as the first scan wrote it")
	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "public", "tables", "audit_log"), "the folder of the dropped table is removed, not left empty")
	project, err = projStore.LoadProject(ctx)
	require.NoError(t, err)
	require.NoError(t, project.Validate())

	// 6. Nothing of the connection is in a file of the project, or in what the scan said.
	assert.Empty(t, server.leaksIn(t, projectDir), "no file of the project holds the host, the port, the user or the password of the server")
	for _, output := range append(stderrs, logged.String()) {
		assert.Empty(t, server.outputLeaks(output, server.scanURL), "the output holds no part of the connection but the display form of the source")
	}
	assert.Contains(t, logged.String(), dbcopy.SourceDisplay(server.scanURL), "the line that names what the scan connects to")
}

// The scan of a server it cannot use fails with the classified message and none of the
// driver's text, and makes nothing: a wrong password, a database that does not exist, a host
// that does not answer, and a variable that is not set.
func TestPostgresScanJourneyFailures(t *testing.T) {
	server := newRealPgServer(t)

	closedPort := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		address := listener.Addr().String()
		require.NoError(t, listener.Close(), "the port is closed again, so nothing answers on it")
		return address
	}
	// driverText is what the server, the driver and the adapter say: none of it may reach the user.
	driverText := []string{"FATAL", "SQLSTATE", "28P01", "3D000", "password authentication", `" does not exist`, "dial tcp", "connection refused", "dalgo2postgres", "pgconn", "failed to connect", "role ", "ConnectionError"}

	wrongPassword := "Wrong" + randomHex(t, 6)
	for _, failure := range []struct {
		name      string
		sourceURL string // the URL in the variable; empty leaves it unset
		reason    string // what the scan says of the cause, after the name of the source
		secret    string // a part of the URL of this failure that may not be shown, beyond those of the fixture
	}{
		{"a wrong password", server.urlFor("", wrongPassword, ""), "the server rejected the user or the password", wrongPassword},
		{"a database that does not exist", server.urlFor("dt_scan_missing_"+randomHex(t, 6), "", ""), "the database does not exist", ""},
		{"a host that does not answer", server.urlFor("", "", closedPort()), "the server could not be reached", ""},
		{"a variable that is not set", "", "", ""},
	} {
		t.Run(failure.name, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "shop-project")
			logged := captureScanLog(t)
			variable := "DATATUG_TEST_POSTGRES_FAILURE_URL"
			if failure.sourceURL != "" {
				t.Setenv(variable, failure.sourceURL)
			} else {
				t.Setenv(variable, "") // restored when the test ends
				require.NoError(t, os.Unsetenv(variable))
			}

			stdout, stderr, err := runScanCommandAllOutputs(t, realPgScanArgs(projectDir, variable)...)

			require.Error(t, err, "the scan exits non-zero")
			message := err.Error()
			t.Logf("message of %s: %s", failure.name, message)
			if failure.sourceURL == "" {
				assert.Equal(t, "environment variable "+variable+" is not set", message)
			} else {
				// The whole message, as the user reads it: the scan's, with no count of workers in front of
				// it (two workers run and one fails, and the error is the one's own), the source named by the
				// variable it was read from, never by the URL, and the cause in this repository's own
				// sentence, told apart by the adapter's Kind and SQLSTATE and not by its words.
				assert.Equal(t, `failed to open PostgreSQL: open postgres source "env:`+variable+`": `+failure.reason, message)
			}
			assert.Empty(t, stdout, "a scan that fails writes nothing to its standard output")
			for _, driver := range driverText {
				assert.NotContains(t, message, driver, "the driver's text is never shown")
				assert.NotContains(t, stderr, driver)
				assert.NotContains(t, logged.String(), driver)
			}
			for _, output := range []string{message, stdout, stderr} {
				assert.Empty(t, server.partsIn(output), "no part of the connection is in what a scan that failed says to the user")
			}
			if failure.secret != "" {
				for name, output := range map[string]string{"message": message, "stdout": stdout, "stderr": stderr, "log": logged.String()} {
					assert.NotContains(t, output, failure.secret, "the secret of the URL that failed is not in the %s", name)
				}
			}
			assert.Empty(t, server.outputLeaks(logged.String(), failure.sourceURL), "the log names the server by its display form at most")
			assert.NoDirExists(t, projectDir, "a scan that fails makes nothing")
		})
	}
}

// The checks of the real journey are what say that no part of the connection was written, and
// they are run against a real server only in CI: this proves them here, with no server, on a
// fixed connection, so that a check that cannot fail is not trusted.
func TestRealPgLeakChecksFindEachPartOfTheConnection(t *testing.T) {
	server := realPgServer{
		scanURL: "postgres://dt_user:s3cret@10.1.2.3:54329/dt_db?sslmode=disable",
		host:    "10.1.2.3", port: "54329", user: "dt_user", password: "s3cret", database: "dt_db",
	}

	t.Run("output may name the server only by the display form of the source", func(t *testing.T) {
		display := dbcopy.SourceDisplay(server.scanURL)
		assert.Equal(t, "postgres://10.1.2.3:54329/dt_db", display, "the display form holds the host, port and database")
		assert.Empty(t, server.outputLeaks("open postgres source \""+display+"\": the driver could not open it", server.scanURL))
		for _, part := range []string{"s3cret", "dt_user", "10.1.2.3", ":54329", "port 54329", "port=54329", "sslmode"} {
			assert.NotEmpty(t, server.outputLeaks("connecting to "+display+" with "+part, server.scanURL), part)
		}
	})

	t.Run("files of a project", func(t *testing.T) {
		dir := t.TempDir()
		assert.Empty(t, server.leaksIn(t, dir), "an empty folder holds nothing")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"driver":"postgres","catalogs":["shop"]}`), 0o644))
		assert.Empty(t, server.leaksIn(t, dir), "the driver name is not a part of the connection")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "c.json"), []byte(`{"count":5432,"rows":[1,2.5,"54"],"nested":{"ok":true,"none":null}}`), 0o644))
		assert.Empty(t, server.leaksIn(t, dir), "numbers and texts that are not the port are not a leak")
		require.NoError(t, os.Remove(filepath.Join(dir, "c.json")))
		for _, body := range []string{`{"password":"x"}`, `{"a":[{"Port":1}]}`, "uses s3cret", "dt_user", "at 10.1.2.3", "port :54329", "listens on port 54329", "port=54329", `{"x":54329}`, `{"x":[54329]}`, `{"a":[{"b":"54329"}]}`, "postgres://x"} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte(body), 0o644))
			assert.NotEmpty(t, server.leaksIn(t, dir), body)
		}
		require.NoError(t, os.Remove(filepath.Join(dir, "b.json")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "dt_user.json"), []byte(`{}`), 0o644))
		assert.NotEmpty(t, server.leaksIn(t, dir), "the name of a file is searched too")
	})

	t.Run("a URL with a part replaced", func(t *testing.T) {
		assert.Equal(t, "postgres://dt_user:s3cret@10.1.2.3:54329/other?sslmode=disable", server.urlFor("other", "", ""))
		assert.Equal(t, "postgres://dt_user:wrong@10.1.2.3:54329/dt_db?sslmode=disable", server.urlFor("", "wrong", ""))
		assert.Equal(t, "postgres://dt_user:s3cret@127.0.0.1:1/dt_db?sslmode=disable", server.urlFor("", "", "127.0.0.1:1"))
	})
}
