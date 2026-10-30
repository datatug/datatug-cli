package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestDatabaseCompareCommandLoadsCatalogDatabasesAndPrintsCompleteSummary(t *testing.T) {
	projectDir := t.TempDir()
	projectStore := filestore.NewProjectStore("compare-command-test", projectDir)
	server := &datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{server}}
	env.ID = "local"
	require.NoError(t, projectStore.SaveEnvironment(context.Background(), env))
	serverID := server.GetID()
	for _, source := range []struct {
		id    string
		value string
	}{{id: "left", value: "before"}, {id: "right", value: "after"}} {
		path := filepath.Join(projectDir, source.id+".sqlite")
		db, err := sql.Open("sqlite3", path)
		require.NoError(t, err)
		_, err = db.Exec(`CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO item VALUES (1, '` + source.value + `')`)
		require.NoError(t, err)
		require.NoError(t, db.Close())
		catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: path}}
		catalog.ID = source.id
		require.NoError(t, projectStore.SaveEnvDbCatalog(context.Background(), env.ID, serverID, source.id, catalog))
	}
	leftPath := filepath.Join(projectDir, "left.sqlite")
	rightPath := filepath.Join(projectDir, "right.sqlite")
	t.Setenv("DATATUG_COMPARE_LEFT_SQLITE", "sqlite://"+leftPath)
	t.Setenv("DATATUG_COMPARE_RIGHT_SQLITE", "sqlite://"+rightPath)
	var output bytes.Buffer
	cmd := compareCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"left", "right", "--project", projectDir, "--environment", "local", "--left-source", "env:DATATUG_COMPARE_LEFT_SQLITE", "--right-source", "env:DATATUG_COMPARE_RIGHT_SQLITE", "--details", "--limit", "1"})
	require.NoError(t, cmd.ExecuteContext(context.Background()), output.String())
	require.Contains(t, output.String(), "local/left -> local/right: +0 added, -0 removed, ~1 changed, 0 unchanged")
	require.Contains(t, output.String(), "relation item (primary-key identity): +0 -0 ~1, 0 unchanged")
	require.Contains(t, output.String(), `changed [id=1]`)
	require.NotContains(t, output.String(), projectDir)
}

func TestCompareCatalogSourceURLRequiresValidBigQueryURI(t *testing.T) {
	catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "bigquery", Path: "bigquery://fixture-project/fixture_dataset?location=US"}}
	got, err := compareCatalogSourceURL(catalog, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, catalog.Path, got)
	for _, path := range []string{"", "postgres://host/db", "bigquery://fixture-project/fixture_dataset?location=US&billingProject=other"} {
		catalog.Path = path
		_, err = compareCatalogSourceURL(catalog, t.TempDir())
		require.Error(t, err, "path %q", path)
		require.NotContains(t, err.Error(), "other")
	}
}

func TestResolveCompareSourceBindingSupportsEnvDSNWithoutEchoingItOnErrors(t *testing.T) {
	const envName = "DATATUG_COMPARE_TEST_DSN"
	const dsn = "postgres://user:secret@localhost/sample?sslmode=disable"
	t.Setenv(envName, dsn)
	got, err := resolveCompareSourceBinding("env:" + envName)
	require.NoError(t, err)
	require.Equal(t, dsn, got)

	_, err = resolveCompareSourceBinding("env:NOT A VARIABLE")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "NOT A VARIABLE")
	_, err = resolveCompareSourceBinding("postgres://user:secret@localhost/sample")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
}

func TestResolveCompareSourceBindingRequiresSetEnvironmentVariable(t *testing.T) {
	t.Setenv("DATATUG_COMPARE_MISSING_DSN", "")
	_, err := resolveCompareSourceBinding("env:DATATUG_COMPARE_MISSING_DSN")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "DATATUG_COMPARE_MISSING_DSN")
}

func TestCompareDriverMatchesSourceScheme(t *testing.T) {
	for _, test := range []struct {
		driver string
		scheme string
		match  bool
	}{
		{driver: "sqlite3", scheme: "sqlite", match: true},
		{driver: "postgresql", scheme: "postgres", match: true},
		{driver: "bigquery", scheme: "bigquery", match: true},
		{driver: "sqlite3", scheme: "postgres", match: false},
		{driver: "postgres", scheme: "bigquery", match: false},
	} {
		require.Equal(t, test.match, compareDriverMatchesScheme(test.driver, test.scheme), "%s -> %s", test.driver, test.scheme)
	}
}

func TestCompareEditionSourceURLUsesOnlyExplicitBindingsOrStructuredBigQueryIdentity(t *testing.T) {
	pg := datatug.EditionConnection{ID: "chinook-postgresql", Storage: "postgres", Readiness: "hosted-api-pending"}
	if _, err := compareEditionSourceURL(pg, ""); err == nil {
		t.Fatal("pending PostgreSQL edition without an explicit binding must not become a URL")
	}
	t.Setenv("DATATUG_COMPARE_EDITION_PG", "postgres://user:password@localhost/chinook?sslmode=disable")
	got, err := compareEditionSourceURL(pg, "env:DATATUG_COMPARE_EDITION_PG")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://user:password@localhost/chinook?sslmode=disable" {
		t.Fatalf("explicit binding = %q", got)
	}
	if _, err := compareEditionSourceURL(pg, "sqlite:///tmp/wrong.db"); err == nil {
		t.Fatal("a binding with the wrong declared provider must fail")
	}
	bq := datatug.EditionConnection{ID: "chinook-bigquery", Storage: "bigquery", Readiness: "public-read-user-project-required", SourceProjectID: "demodb-dev", DatasetID: "chinook", Location: "US"}
	got, err = compareEditionSourceURL(bq, "")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := dbcopy.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ProjectID != "demodb-dev" || ref.DatasetID != "chinook" || ref.Location != "US" {
		t.Fatalf("structured BigQuery source = %#v", ref)
	}
	bq.Readiness = "setup-required"
	if _, err := compareEditionSourceURL(bq, ""); err == nil {
		t.Fatal("structured BigQuery metadata must not bypass a not-ready declaration")
	}
}

func TestCompareUsesModernProjectEditionWithExplicitBindingAndRefusesPendingWithoutOne(t *testing.T) {
	projectDir := t.TempDir()
	connectionsDir := filepath.Join(projectDir, "connections")
	require.NoError(t, os.MkdirAll(connectionsDir, 0o700))
	connections := `{"format":"datatug-demo-connections/v1","connections":[{"id":"chinook-sqlite","dataset":"chinook","storage":"sqlite","readiness":"public-api","environments":["dev"]},{"id":"chinook-postgresql","dataset":"chinook","storage":"postgres","readiness":"hosted-api-pending","environments":["QA"]}],"bigQueryEditions":[],"bigQueryPlans":[]}`
	require.NoError(t, os.WriteFile(filepath.Join(connectionsDir, "demo-db.json"), []byte(connections), 0o600))
	for environment, editions := range map[string][]string{"dev": {"chinook-sqlite"}, "QA": {"chinook-postgresql"}} {
		environmentDir := filepath.Join(projectDir, "environments", environment)
		require.NoError(t, os.MkdirAll(environmentDir, 0o700))
		contents, err := json.Marshal(map[string]any{"id": environment, "dbServers": []any{}, "editionConnections": editions})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(environmentDir, environment+".env.json"), contents, 0o600))
	}
	sqlitePath := filepath.Join(projectDir, "chinook.sqlite")
	db, err := sql.Open("sqlite3", sqlitePath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO item VALUES (1, 'ok')")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	store := filestore.NewProjectStore("compare-edition-test", projectDir)
	t.Setenv("DATATUG_COMPARE_EDITION_SQLITE", "sqlite://"+sqlitePath)
	opened, _, err := openProjectCompareDatabase(context.Background(), store, projectDir, "dev", "chinook-sqlite", "", "env:DATATUG_COMPARE_EDITION_SQLITE")
	require.NoError(t, err)
	closeCompareDB(opened)

	_, _, err = openProjectCompareDatabase(context.Background(), store, projectDir, "QA", "chinook-postgresql", "", "")
	require.ErrorIs(t, err, datatug.ErrConnectionNotReady)
	t.Setenv("DATATUG_COMPARE_PENDING_SQLITE", "sqlite://"+sqlitePath)
	_, _, err = openProjectCompareDatabase(context.Background(), store, projectDir, "QA", "chinook-postgresql", "", "env:DATATUG_COMPARE_PENDING_SQLITE")
	require.Error(t, err, "the pending postgres storage declaration must reject a SQLite binding")
	_, _, err = openProjectCompareDatabase(context.Background(), store, projectDir, "QA", "chinook-sqlite", "", "env:DATATUG_COMPARE_EDITION_SQLITE")
	require.ErrorIs(t, err, datatug.ErrConnectionNotInEnvironment)
}

func TestCompareReportNamesDoNotEchoSourceURLsOrCredentials(t *testing.T) {
	name := compareReportName("postgres://user:secret@example.test/db", "bigquery://project/dataset")
	require.NotContains(t, name, "secret")
	require.NotContains(t, name, "example.test")
	require.NotContains(t, name, "project")
}

func TestCompareArgsPreservesAgentModeAndRequiresTwoDatabaseNames(t *testing.T) {
	require.NoError(t, compareArgs(nil, nil))
	require.NoError(t, compareArgs(nil, []string{"left", "right"}))
	require.Error(t, compareArgs(nil, []string{"left"}))
	require.Error(t, compareArgs(nil, []string{"left", "right", "extra"}))
}

func TestParseComparisonKeyMapsValidatesMappings(t *testing.T) {
	got, err := parseComparisonKeyMaps([]string{"orders=id", "item=tenant_id,id"})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"orders": {"id"}, "item": {"tenant_id", "id"}}, got)
	for _, input := range [][]string{
		{"missing-equals"},
		{"orders="},
		{"orders=id,,name"},
		{"orders=id,id"},
		{"orders=id", "orders=name"},
		{" orders=id"},
	} {
		_, err := parseComparisonKeyMaps(input)
		require.Error(t, err, "input %v", input)
	}
}

func TestParseComparisonTableMapsValidatesOneToOneNames(t *testing.T) {
	got, err := parseComparisonTableMaps([]string{"Production.Document=production_document", "customer=Customer"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Production.Document": "production_document", "customer": "Customer"}, got)
	for _, input := range [][]string{
		{"missing-equals"},
		{"left="},
		{"=right"},
		{" left=right"},
		{"left=right", "left=other"},
		{"left=right", "other=right"},
	} {
		_, err := parseComparisonTableMaps(input)
		require.Error(t, err, "input %v", input)
	}
}

func TestReadCompareMappingFileLoadsExplicitRelationAndKeyMappings(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "compare-map.json"), []byte(`{
		"format":"datatug-database-compare-mappings/v1",
		"relations":[
			{"left":"Production.Document","right":"production_document","keyColumns":["ProductID","DocumentID"]},
			{"left":"customer","right":"Customer"}
		]
	}`), 0o600))
	tables, keys, err := readCompareMappingFile(projectDir, "compare-map.json")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Production.Document": "production_document", "customer": "Customer"}, tables)
	require.Equal(t, map[string][]string{"Production.Document": {"ProductID", "DocumentID"}}, keys)
}

func TestReadCompareMappingFileRejectsMalformedOrOversizedInput(t *testing.T) {
	projectDir := t.TempDir()
	for name, contents := range map[string]string{
		"unknown-field.json":   `{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"a","right":"b","guess":true}]}`,
		"wrong-format.json":    `{"format":"v2","relations":[{"left":"a","right":"b"}]}`,
		"trailing.json":        `{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"a","right":"b"}]} {}`,
		"duplicate-left.json":  `{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"a","right":"b"},{"left":"a","right":"c"}]}`,
		"duplicate-right.json": `{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"a","right":"b"},{"left":"c","right":"b"}]}`,
		"duplicate-key.json":   `{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"a","right":"b","keyColumns":["id","id"]}]}`,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, name), []byte(contents), 0o600))
		_, _, err := readCompareMappingFile(projectDir, name)
		require.Error(t, err, "%s", name)
	}
	oversized := make([]byte, maxCompareMappingFileBytes+1)
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "large.json"), oversized, 0o600))
	_, _, err := readCompareMappingFile(projectDir, "large.json")
	require.Error(t, err)
}

func TestComparisonMappingFileAndFlagConflictsAreRejected(t *testing.T) {
	tableMappings := map[string]string{"left": "other"}
	require.Error(t, mergeComparisonTableMaps(tableMappings, map[string]string{"left": "right"}))
	require.Error(t, mergeComparisonTableMaps(tableMappings, map[string]string{"another": "other"}))
	keyMappings := map[string][]string{"left": {"id"}}
	require.Error(t, mergeComparisonKeyMaps(keyMappings, map[string][]string{"left": {"other_id"}}))
}

func TestOpenBigQueryCompareDatabaseUsesReadOnlyPhysicalAPI(t *testing.T) {
	configDir := t.TempDir()
	oldConfigDir := chatUserConfigDir
	chatUserConfigDir = func() (string, error) { return configDir, nil }
	t.Cleanup(func() { chatUserConfigDir = oldConfigDir })
	clock := &cliBQClock{now: time.Now()}
	provider := &cliBQProvider{principal: bigqueryPrincipalFixture(), clock: clock}
	var requests []string
	oldDeps := bigQueryDeps
	bigQueryDeps = bigQueryDependencies{
		provider: func(string, bool, string) (bigquery.Provider, error) { return provider, nil },
		transport: cliBQTransport(func(req *http.Request) (*http.Response, error) {
			requests = append(requests, req.Method+" "+req.URL.String())
			switch {
			case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset"):
				return cliBQResponse(`{"datasetReference":{"projectId":"fixture-project","datasetId":"fixture_dataset"}}`), nil
			case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset/tables"):
				return cliBQResponse(`{"totalItems":1,"tables":[{"tableReference":{"projectId":"fixture-project","datasetId":"fixture_dataset","tableId":"sample"},"type":"TABLE"}]}`), nil
			case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset/tables/sample"):
				return cliBQResponse(`{"tableReference":{"projectId":"fixture-project","datasetId":"fixture_dataset","tableId":"sample"},"type":"TABLE","etag":"e1","lastModifiedTime":"1000","numRows":"1","schema":{"fields":[{"name":"id","type":"INT64","mode":"REQUIRED"}]}}`), nil
			case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset/tables/sample/data"):
				return cliBQResponse(`{"kind":"bigquery#tableDataList","totalRows":"1","rows":[{"f":[{"v":"7"}]}]}`), nil
			default:
				t.Fatalf("unexpected BigQuery route %s", req.URL.String())
				return nil, nil
			}
		}),
	}
	t.Cleanup(func() { bigQueryDeps = oldDeps })
	ref, err := dbcopy.Parse("bigquery://fixture-project/fixture_dataset?location=US")
	require.NoError(t, err)
	db, err := openBigQueryCompareDatabase(ref)
	require.NoError(t, err)
	defer closeCompareDB(db)
	collections, err := dbschema.ListCollections(context.Background(), db, nil)
	require.NoError(t, err)
	require.Len(t, collections, 1)
	require.Equal(t, "sample", collections[0].Name())
	definition, err := dbschema.DescribeCollection(context.Background(), db, &collections[0])
	require.NoError(t, err)
	require.Equal(t, "bigquery", definition.SourceDefinition.Dialect)
	reader, ok := dal.As[dbschema.SourceRowsReader](db)
	require.True(t, ok)
	cursor, err := reader.OpenSourceRows(context.Background(), &collections[0])
	require.NoError(t, err)
	row, err := cursor.Next()
	require.NoError(t, err)
	require.Equal(t, int64(7), row.Values["id"])
	require.NoError(t, cursor.Close())
	require.NotEmpty(t, requests)
	for _, request := range requests {
		require.True(t, strings.HasPrefix(request, "GET https://bigquery.googleapis.com/"), request)
		require.NotContains(t, request, "/jobs", request)
	}
}

func bigqueryPrincipalFixture() bigquery.Principal {
	return bigquery.Principal{Kind: "google-user", Subject: "verified-source-reader", Generation: "grant-1"}
}
