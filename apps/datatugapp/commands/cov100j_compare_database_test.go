package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/dal-go/dalgo2postgres"
	"github.com/datatug/datatug-cli/pkg/dbcompare"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/require"
)

// Not parallel: uses t.Setenv to configure source environment variables.
func TestCompareDatabaseKeyMapsAndBindings(t *testing.T) {

	// 1. parseComparisonKeyMaps
	// valid
	m, err := parseComparisonKeyMaps([]string{"users=id,name", "orders=order_id"})
	require.NoError(t, err)
	require.Equal(t, []string{"id", "name"}, m["users"])
	require.Equal(t, []string{"order_id"}, m["orders"])

	// invalid format missing '='
	_, err = parseComparisonKeyMaps([]string{"invalid"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected TABLE=column")

	// duplicate table
	_, err = parseComparisonKeyMaps([]string{"users=id", "users=name"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "more than once")

	// invalid empty column or duplicate column
	_, err = parseComparisonKeyMaps([]string{"users=id,id"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate key column")

	_, err = parseComparisonKeyMaps([]string{"users=id,"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid or duplicate key column")

	// 2. resolveCompareSourceBinding
	// empty
	_, err = resolveCompareSourceBinding("")
	require.Error(t, err)

	// invalid env name
	_, err = resolveCompareSourceBinding("env:INVALID-NAME!")
	require.Error(t, err)
	require.Contains(t, err.Error(), "valid variable name")

	// unset env
	_, err = resolveCompareSourceBinding("env:UNSET_ENV_TEST_VAR_12345")
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable is not set")

	// postgres without env
	_, err = resolveCompareSourceBinding("postgres://user:pass@localhost:5432/db")
	require.Error(t, err)
	require.Contains(t, err.Error(), "PostgreSQL source bindings must use env:VARIABLE")

	// unsupported scheme
	_, err = resolveCompareSourceBinding("ftp://localhost/db")
	require.Error(t, err)

	// valid from env
	t.Setenv("TEST_COMPARE_DSN", "sqlite:///tmp/test.db")
	resolved, err := resolveCompareSourceBinding("env:TEST_COMPARE_DSN")
	require.NoError(t, err)
	require.Equal(t, "sqlite:///tmp/test.db", resolved)

	// 3. compareCatalogSourceURL
	// BigQuery catalog with empty path
	_, err = compareCatalogSourceURL(datatug.DbCatalog{Driver: "bigquery", Path: ""}, "/tmp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "BigQuery catalog must contain")

	// BigQuery catalog with invalid path
	_, err = compareCatalogSourceURL(datatug.DbCatalog{Driver: "bigquery", Path: "sqlite:///tmp/notbq"}, "/tmp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "BigQuery catalog source URL is invalid")

	// BigQuery catalog valid
	u, err := compareCatalogSourceURL(datatug.DbCatalog{Driver: "bigquery", Path: "bigquery://demodb/research?location=US"}, "/tmp")
	require.NoError(t, err)
	require.Equal(t, "bigquery://demodb/research?location=US", u)

	// 4. openCompareSource errors
	// invalid source url
	_, _, err = openCompareSource(context.Background(), ":::bad url", "", "")
	require.Error(t, err)

	// storage mismatch
	_, _, err = openCompareSource(context.Background(), "sqlite:///tmp/test.db", "", "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "source provider does not match")

	// schema specified for non-postgres
	_, _, err = openCompareSource(context.Background(), "sqlite:///tmp/test.db", "public", "sqlite")
	require.Error(t, err)
	require.Contains(t, err.Error(), "schema selection is supported only for PostgreSQL sources")
}

type dummyCloserDB struct {
	dal.DB
	closed bool
}

func (d *dummyCloserDB) Close() error {
	d.closed = true
	return nil
}

func TestCompareDatabaseTextAndCloser(t *testing.T) {
	t.Parallel()

	// closeCompareDB
	closeCompareDB(nil)
	closer := &dummyCloserDB{}
	closeCompareDB(closer)
	require.True(t, closer.closed)

	// writeDatabaseCompareText with full branches
	report := dbcompare.Report{
		LeftName:      "leftDB",
		RightName:     "rightDB",
		SchemaChanges: []string{"added table X"},
		Warnings:      []string{"warning 1"},
		Relations: []dbcompare.RelationReport{
			{
				Kind:      "table",
				Name:      "users",
				RightName: "app_users",
				Identity:  "primary-key",
				LeftRows:  10,
				RightRows: 12,
				Counts:    dbcompare.DifferenceCounts{Added: 2, Removed: 0, Changed: 1, Unchanged: 9},
				Details: []dbcompare.RecordDiff{
					{
						Status: "changed",
						ID:     "row-1",
						Fields: []dbcompare.FieldDiff{
							{
								Name:         "col1",
								Before:       "old",
								After:        "new",
								BeforeAbsent: false,
								AfterAbsent:  false,
							},
							{
								Name:         "col2",
								Before:       nil,
								After:        "added",
								BeforeAbsent: true,
								AfterAbsent:  false,
							},
							{
								Name:         "col3",
								Before:       "deleted",
								After:        nil,
								BeforeAbsent: false,
								AfterAbsent:  true,
							},
						},
					},
				},
			},
		},
		DetailsLimited: true,
	}

	var buf bytes.Buffer
	err := writeDatabaseCompareText(&buf, report, true)
	require.NoError(t, err)
	out := buf.String()
	require.Contains(t, out, "leftDB -> rightDB")
	require.Contains(t, out, "schema: added table X")
	require.Contains(t, out, "warning: warning 1")
	require.Contains(t, out, "users -> app_users")
	require.Contains(t, out, "changed [row-1]")
	require.Contains(t, out, "<absent>")
	require.Contains(t, out, "record details are limited")
}

// Not parallel: tests openBigQueryCompareDatabase and modifies bigQueryDeps.
func TestOpenBigQueryCompareDatabase(t *testing.T) {
	oldDeps := bigQueryDeps
	t.Cleanup(func() { bigQueryDeps = oldDeps })

	// Scheme mismatch
	_, err := openBigQueryCompareDatabase(dbcopy.BackendRef{Scheme: "sqlite"})
	require.Error(t, err)

	// Missing location
	_, err = openBigQueryCompareDatabase(dbcopy.BackendRef{Scheme: "bigquery", ProjectID: "p", DatasetID: "d"})
	require.Error(t, err)

	// Provider error
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return nil, errors.New("provider error")
	}
	ref := dbcopy.BackendRef{Scheme: "bigquery", ProjectID: "demodb", DatasetID: "research", Location: "US"}
	_, err = openBigQueryCompareDatabase(ref)
	require.Error(t, err)

	// Valid provider returning mock provider
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return &cliBQProvider{principal: bigquery.Principal{Kind: "google-user", Subject: "sub"}, clock: &cliBQClock{}}, nil
	}
	db, err := openBigQueryCompareDatabase(ref)
	require.NoError(t, err)
	require.NotNil(t, db)
	closeCompareDB(db)
}

func TestCompareDatabaseTableMapsAndMappingFile(t *testing.T) {
	t.Parallel()

	// parseComparisonTableMaps
	valid, err := parseComparisonTableMaps([]string{"left=right", "orders=purchases"})
	require.NoError(t, err)
	require.Equal(t, "right", valid["left"])
	require.Equal(t, "purchases", valid["orders"])

	// invalid missing '='
	_, err = parseComparisonTableMaps([]string{"invalid"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected LEFT=RIGHT")

	// whitespace in left or right
	_, err = parseComparisonTableMaps([]string{" left=right"})
	require.Error(t, err)

	// duplicate left
	_, err = parseComparisonTableMaps([]string{"u=users", "u=people"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "more than once")

	// duplicate right
	_, err = parseComparisonTableMaps([]string{"u=users", "p=users"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "to right relation")

	// mergeComparisonTableMaps
	base := map[string]string{"u": "users"}
	require.Error(t, mergeComparisonTableMaps(base, map[string]string{"u": "other"}))
	require.Error(t, mergeComparisonTableMaps(base, map[string]string{"x": "users"}))
	require.NoError(t, mergeComparisonTableMaps(base, map[string]string{"orders": "purchases"}))
	require.Equal(t, "purchases", base["orders"])

	// mergeComparisonKeyMaps
	kbase := map[string][]string{"u": {"id"}}
	require.Error(t, mergeComparisonKeyMaps(kbase, map[string][]string{"u": {"email"}}))
	require.NoError(t, mergeComparisonKeyMaps(kbase, map[string][]string{"orders": {"id"}}))
	require.Equal(t, []string{"id"}, kbase["orders"])

	// readCompareMappingFile
	// empty path
	tm, km, err := readCompareMappingFile("", "")
	require.NoError(t, err)
	require.Empty(t, tm)
	require.Empty(t, km)

	// missing file
	_, _, err = readCompareMappingFile("/tmp", "nonexistent-file.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not be opened")

	// invalid json
	dir := t.TempDir()
	badJSON := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(badJSON, []byte("{not json"), 0600))
	_, _, err = readCompareMappingFile(dir, badJSON)
	require.Error(t, err)

	// trailing json
	trailingJSON := filepath.Join(dir, "trailing.json")
	require.NoError(t, os.WriteFile(trailingJSON, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"u","right":"r"}]}{"extra":1}`), 0600))
	_, _, err = readCompareMappingFile(dir, trailingJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one JSON object")

	// invalid format
	wrongFormat := filepath.Join(dir, "wrong.json")
	require.NoError(t, os.WriteFile(wrongFormat, []byte(`{"format":"wrong-format","relations":[{"left":"u","right":"r"}]}`), 0600))
	_, _, err = readCompareMappingFile(dir, wrongFormat)
	require.Error(t, err)

	// empty relations
	emptyRel := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(emptyRel, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[]}`), 0600))
	_, _, err = readCompareMappingFile(dir, emptyRel)
	require.Error(t, err)

	// whitespace in relation names
	wsRel := filepath.Join(dir, "ws.json")
	require.NoError(t, os.WriteFile(wsRel, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":" u ","right":"r"}]}`), 0600))
	_, _, err = readCompareMappingFile(dir, wsRel)
	require.Error(t, err)

	// duplicate left
	dupLeft := filepath.Join(dir, "dup-left.json")
	require.NoError(t, os.WriteFile(dupLeft, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"u","right":"r1"},{"left":"u","right":"r2"}]}`), 0600))
	_, _, err = readCompareMappingFile(dir, dupLeft)
	require.Error(t, err)

	// duplicate right
	dupRight := filepath.Join(dir, "dup-right.json")
	require.NoError(t, os.WriteFile(dupRight, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"u1","right":"r"},{"left":"u2","right":"r"}]}`), 0600))
	_, _, err = readCompareMappingFile(dir, dupRight)
	require.Error(t, err)

	// invalid key column in mapping file
	dupKey := filepath.Join(dir, "dup-key.json")
	require.NoError(t, os.WriteFile(dupKey, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"u","right":"r","keyColumns":["id","id"]}]}`), 0600))
	_, _, err = readCompareMappingFile(dir, dupKey)
	require.Error(t, err)

	// valid mapping file
	good := filepath.Join(dir, "good.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"u","right":"users","keyColumns":["id"]}]}`), 0600))
	tm, km, err = readCompareMappingFile(dir, good)
	require.NoError(t, err)
	require.Equal(t, "users", tm["u"])
	require.Equal(t, []string{"id"}, km["u"])

	// compareDriverMatchesScheme
	require.True(t, compareDriverMatchesScheme("sqlite3", "sqlite"))
	require.True(t, compareDriverMatchesScheme("postgresql", "postgres"))
	require.True(t, compareDriverMatchesScheme("postgres", "postgres"))
	require.False(t, compareDriverMatchesScheme("sqlite", "postgres"))

	// compareReportName
	require.Equal(t, "<source id not shown>/db", compareReportName("", "db"))
	require.Equal(t, "env/db", compareReportName("env", "db"))
}

type stepErrorWriter struct {
	maxBytes int
	written  int
}

func (w *stepErrorWriter) Write(p []byte) (n int, err error) {
	if w.written+len(p) > w.maxBytes {
		return 0, errors.New("write error")
	}
	w.written += len(p)
	return len(p), nil
}

func TestCompareDatabaseTextErrors(t *testing.T) {
	t.Parallel()

	report := dbcompare.Report{
		LeftName:      "leftDB",
		RightName:     "rightDB",
		SchemaChanges: []string{"change1"},
		Warnings:      []string{"warn1"},
		Relations: []dbcompare.RelationReport{
			{
				Kind:      "table",
				Name:      "tbl",
				RightName: "tbl2",
				Identity:  "id",
				LeftRows:  1,
				RightRows: 1,
				Counts:    dbcompare.DifferenceCounts{Added: 1},
				Details: []dbcompare.RecordDiff{
					{
						Status: "add",
						ID:     "1",
						Fields: []dbcompare.FieldDiff{
							{Name: "f1", Before: "a", After: "b"},
						},
					},
				},
			},
		},
		DetailsLimited: true,
	}

	for max := 0; max < 400; max++ {
		_ = writeDatabaseCompareText(&stepErrorWriter{maxBytes: max}, report, true)
	}
}

type mockAdapterDB struct {
	dal.DB
	adapterName string
}

func (m *mockAdapterDB) Adapter() dal.Adapter {
	if m.adapterName == "" {
		return nil
	}
	return &mockAdapter{name: m.adapterName}
}

type mockAdapter struct {
	dal.Adapter
	name string
}

func (m *mockAdapter) Name() string { return m.name }

func TestEffectiveCompareSchema(t *testing.T) {
	t.Parallel()

	pgDB := &mockAdapterDB{adapterName: "dalgo2postgres"}
	require.Equal(t, "public", effectiveCompareSchema(pgDB, ""))
	require.Equal(t, "custom", effectiveCompareSchema(pgDB, "custom"))

	otherDB := &mockAdapterDB{adapterName: "other"}
	require.Equal(t, "", effectiveCompareSchema(otherDB, ""))
	require.Equal(t, "custom", effectiveCompareSchema(otherDB, "custom"))

	nilAdapterDB := &mockAdapterDB{adapterName: ""}
	require.Equal(t, "", effectiveCompareSchema(nilAdapterDB, ""))
}

type mockCompareCatalogStore struct {
	datatug.ProjectStore
	catalogs datatug.DbCatalogs
	err      error
}

func (m *mockCompareCatalogStore) LoadEnvDbCatalogs(ctx context.Context, envID string, options ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	return m.catalogs, m.err
}

func TestOpenProjectCompareDatabaseCatalogErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// empty database name
	_, _, err := openProjectCompareDatabase(ctx, nil, "/tmp", "env", "  ", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "database name is required")

	// store load catalogs error
	storeErr := &mockCompareCatalogStore{err: errors.New("cannot load catalogs")}
	_, _, err = openProjectCompareDatabase(ctx, storeErr, "/tmp", "env", "db", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "load database catalogs")

	// database not configured in environment
	storeEmpty := &mockCompareCatalogStore{catalogs: []*datatug.DbCatalog{}}
	_, _, err = openProjectCompareDatabase(ctx, storeEmpty, "/tmp", "env", "db", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not configured in environment")

	// database ambiguous in environment
	cat1 := &datatug.DbCatalog{ID: "db", Driver: "sqlite"}
	cat2 := &datatug.DbCatalog{ID: "db", Driver: "sqlite"}
	storeAmbiguous := &mockCompareCatalogStore{catalogs: []*datatug.DbCatalog{cat1, cat2}}
	_, _, err = openProjectCompareDatabase(ctx, storeAmbiguous, "/tmp", "env", "db", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "is ambiguous in environment")
}

func TestRunDatabaseCompareCommandValidation(t *testing.T) {
	t.Parallel()

	// query agent flag changed
	cmdAgent := compareCommand()
	require.NoError(t, cmdAgent.Flags().Set("agent", "http://localhost:8989"))
	err := runDatabaseCompareCommand(cmdAgent, "left", "right")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not use query-agent flags")

	// missing project flag
	cmdNoProj := compareCommand()
	err = runDatabaseCompareCommand(cmdNoProj, "left", "right")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires --project")

	// non-existent project
	cmdBadProj := compareCommand()
	require.NoError(t, cmdBadProj.Flags().Set("project", "/nonexistent/project/dir/12345"))
	err = runDatabaseCompareCommand(cmdBadProj, "left", "right")
	require.Error(t, err)

	proj := writeChatRunProjectFixture(t)
	// Create second environment so ambiguous environment can trigger
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "environments", "prod"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "environments", "prod", "prod.env.json"), []byte(`{"id":"prod"}`), 0644))

	// left environment error (neither env is specified, left is ambiguous)
	cmdLeftEnv := compareCommand()
	require.NoError(t, cmdLeftEnv.Flags().Set("project", proj))
	err = runDatabaseCompareCommand(cmdLeftEnv, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), "left environment")

	// right environment error (left env is specified, right env is ambiguous)
	cmdRightEnv := compareCommand()
	require.NoError(t, cmdRightEnv.Flags().Set("project", proj))
	require.NoError(t, cmdRightEnv.Flags().Set("left-environment", "local"))
	err = runDatabaseCompareCommand(cmdRightEnv, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), "right environment")

	// Now create the orders.sqlite file so database opens can succeed
	require.NoError(t, os.WriteFile(filepath.Join(proj, "orders.sqlite"), nil, 0600))

	// invalid table map flag
	cmdBadTableMap := compareCommand()
	require.NoError(t, cmdBadTableMap.Flags().Set("project", proj))
	require.NoError(t, cmdBadTableMap.Flags().Set("environment", "local"))
	require.NoError(t, cmdBadTableMap.Flags().Set("table-map", "invalid-no-equal"))
	err = runDatabaseCompareCommand(cmdBadTableMap, "orders", "orders")
	require.Error(t, err)

	// invalid key map flag
	cmdBadKeyMap := compareCommand()
	require.NoError(t, cmdBadKeyMap.Flags().Set("project", proj))
	require.NoError(t, cmdBadKeyMap.Flags().Set("environment", "local"))
	require.NoError(t, cmdBadKeyMap.Flags().Set("key-map", "invalid-no-equal"))
	err = runDatabaseCompareCommand(cmdBadKeyMap, "orders", "orders")
	require.Error(t, err)

	// invalid mapping file path
	cmdBadMapFile := compareCommand()
	require.NoError(t, cmdBadMapFile.Flags().Set("project", proj))
	require.NoError(t, cmdBadMapFile.Flags().Set("environment", "local"))
	require.NoError(t, cmdBadMapFile.Flags().Set("mapping-file", "/nonexistent/map.json"))
	err = runDatabaseCompareCommand(cmdBadMapFile, "orders", "orders")
	require.Error(t, err)

	// duplicate table map between file and CLI
	mapFile := filepath.Join(t.TempDir(), "map.json")
	require.NoError(t, os.WriteFile(mapFile, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"orders","right":"orders_target"}]}`), 0600))
	cmdTableMapDup := compareCommand()
	require.NoError(t, cmdTableMapDup.Flags().Set("project", proj))
	require.NoError(t, cmdTableMapDup.Flags().Set("environment", "local"))
	require.NoError(t, cmdTableMapDup.Flags().Set("mapping-file", mapFile))
	require.NoError(t, cmdTableMapDup.Flags().Set("table-map", "orders=orders_other"))
	err = runDatabaseCompareCommand(cmdTableMapDup, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), `relation "orders" has both a --table-map flag and a mapping-file entry`)

	// duplicate key map between file and CLI
	mapFileKey := filepath.Join(t.TempDir(), "mapkey.json")
	require.NoError(t, os.WriteFile(mapFileKey, []byte(`{"format":"datatug-database-compare-mappings/v1","relations":[{"left":"orders","right":"orders","keyColumns":["id"]}]}`), 0600))
	cmdKeyMapDup := compareCommand()
	require.NoError(t, cmdKeyMapDup.Flags().Set("project", proj))
	require.NoError(t, cmdKeyMapDup.Flags().Set("environment", "local"))
	require.NoError(t, cmdKeyMapDup.Flags().Set("mapping-file", mapFileKey))
	require.NoError(t, cmdKeyMapDup.Flags().Set("key-map", "orders=other_id"))
	err = runDatabaseCompareCommand(cmdKeyMapDup, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), `relation "orders" has both a --key-map flag and a mapping-file key`)

	// open left database error
	cmdLeftOpenErr := compareCommand()
	require.NoError(t, cmdLeftOpenErr.Flags().Set("project", proj))
	require.NoError(t, cmdLeftOpenErr.Flags().Set("environment", "local"))
	require.NoError(t, cmdLeftOpenErr.Flags().Set("left-source", ":::bad-url"))
	err = runDatabaseCompareCommand(cmdLeftOpenErr, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), "open left database")

	// open right database error
	cmdRightOpenErr := compareCommand()
	require.NoError(t, cmdRightOpenErr.Flags().Set("project", proj))
	require.NoError(t, cmdRightOpenErr.Flags().Set("environment", "local"))
	require.NoError(t, cmdRightOpenErr.Flags().Set("right-source", ":::bad-url"))
	err = runDatabaseCompareCommand(cmdRightOpenErr, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), "open right database")

	// negative limit flag
	cmdNegLimit := compareCommand()
	require.NoError(t, cmdNegLimit.Flags().Set("project", proj))
	require.NoError(t, cmdNegLimit.Flags().Set("environment", "local"))
	require.NoError(t, cmdNegLimit.Flags().Set("limit", "-5"))
	err = runDatabaseCompareCommand(cmdNegLimit, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--limit cannot be negative")

	// Compare error due to unknown relation key mapping
	cmdCompareErr := compareCommand()
	require.NoError(t, cmdCompareErr.Flags().Set("project", proj))
	require.NoError(t, cmdCompareErr.Flags().Set("environment", "local"))
	require.NoError(t, cmdCompareErr.Flags().Set("key-map", "unknown_relation_xyz=id"))
	err = runDatabaseCompareCommand(cmdCompareErr, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), "comparison key mapping names unknown relation")

	// Compare success with --json
	var outJSON bytes.Buffer
	cmdJSON := compareCommand()
	cmdJSON.SetOut(&outJSON)
	require.NoError(t, cmdJSON.Flags().Set("project", proj))
	require.NoError(t, cmdJSON.Flags().Set("environment", "local"))
	require.NoError(t, cmdJSON.Flags().Set("json", "true"))
	err = runDatabaseCompareCommand(cmdJSON, "orders", "orders")
	require.NoError(t, err)
	require.Contains(t, outJSON.String(), `"leftName"`)

	// Compare with --json and failing output
	cmdJSONErr := compareCommand()
	cmdJSONErr.SetOut(&stepErrorWriter{maxBytes: 5})
	require.NoError(t, cmdJSONErr.Flags().Set("project", proj))
	require.NoError(t, cmdJSONErr.Flags().Set("environment", "local"))
	require.NoError(t, cmdJSONErr.Flags().Set("json", "true"))
	err = runDatabaseCompareCommand(cmdJSONErr, "orders", "orders")
	require.Error(t, err)
	require.Contains(t, err.Error(), "write database comparison report")

	// Compare success with text output
	var outText bytes.Buffer
	cmdText := compareCommand()
	cmdText.SetOut(&outText)
	require.NoError(t, cmdText.Flags().Set("project", proj))
	require.NoError(t, cmdText.Flags().Set("environment", "local"))
	err = runDatabaseCompareCommand(cmdText, "orders", "orders")
	require.NoError(t, err)
}

type mockEnvConnResolverStore struct {
	mockCompareCatalogStore
	edition datatug.EditionConnection
	resErr  error
}

func (m *mockEnvConnResolverStore) ResolveEnvironmentConnection(ctx context.Context, envID, database string) (datatug.EditionConnection, error) {
	return m.edition, m.resErr
}

func TestOpenProjectCompareDatabaseRemainingEdges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// store implementing EnvironmentConnectionResolver returning ErrConnectionNotReady with empty EditionID
	storeNotReady := &mockEnvConnResolverStore{
		resErr: datatug.ErrConnectionNotReady,
	}
	_, _, err := openProjectCompareDatabase(ctx, storeNotReady, "/tmp", "env", "db", "", "")
	require.Error(t, err)
	require.True(t, errors.Is(err, datatug.ErrConnectionNotReady))

	// Resolver returns ready connection with non-BigQuery storage and no source binding
	storeModernSourceErr := &mockEnvConnResolverStore{
		edition: datatug.EditionConnection{ID: "ed1", Storage: "sqlite"},
	}
	_, _, err = openProjectCompareDatabase(ctx, storeModernSourceErr, "/tmp", "env", "db", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "this project edition has no executable source URL")

	// compareEditionSourceURL with invalid source binding
	_, err = compareEditionSourceURL(datatug.EditionConnection{ID: "ed1", Storage: "sqlite"}, "env:UNSET_VAR_XYZ_123")
	require.Error(t, err)

	// catalog driver BigQuery with empty path
	storeBadCat := &mockCompareCatalogStore{
		catalogs: datatug.DbCatalogs{&datatug.DbCatalog{ID: "db", Driver: "bigquery", Path: ""}},
	}
	_, _, err = openProjectCompareDatabase(ctx, storeBadCat, "/tmp", "env", "db", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve database source")

	// readCompareMappingFile on a directory
	_, _, err = readCompareMappingFile(t.TempDir(), ".")
	require.Error(t, err)
	require.Contains(t, err.Error(), "comparison mapping file could not be read")

	// compareCatalogSourceURL on sqlite catalog
	sourceURL, err := compareCatalogSourceURL(datatug.DbCatalog{Driver: "sqlite", Path: "test.sqlite"}, t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, sourceURL)

	// openCompareSource postgres open error
	standInForPostgres(t, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork})
	_, _, err = openCompareSource(ctx, "postgres://user:pass@127.0.0.1:0/db", "", "postgres")
	require.Error(t, err)

	// openCompareSource sqlite OpenForCopy error on directory
	_, _, err = openCompareSource(ctx, "sqlite://"+t.TempDir(), "", "sqlite")
	require.Error(t, err)
}

// Not parallel: tests openCompareSource with bigquery and modifies bigQueryDeps and chatUserConfigDir.
func TestOpenBigQueryCompareSourceRemainingBranches(t *testing.T) {
	oldDeps := bigQueryDeps
	t.Cleanup(func() { bigQueryDeps = oldDeps })
	oldCfgDir := chatUserConfigDir
	t.Cleanup(func() { chatUserConfigDir = oldCfgDir })

	// 1. chatUserConfigDir returns error
	chatUserConfigDir = func() (string, error) { return "", errors.New("cfg error") }
	_, _, err := openCompareSource(context.Background(), "bigquery://sample-project/sample_dataset?location=US", "", "bigquery")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires the user's private configuration directory")

	// 2. provider returns error
	chatUserConfigDir = func() (string, error) { return t.TempDir(), nil }
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return nil, errors.New("provider failure")
	}
	_, _, err = openCompareSource(context.Background(), "bigquery://sample-project/sample_dataset?location=US", "", "bigquery")
	require.Error(t, err)

	// 3. provider returns nil, nil -> NewReadOnlyDatabase returns error
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return nil, nil
	}
	_, _, err = openCompareSource(context.Background(), "bigquery://sample-project/sample_dataset?location=US", "", "bigquery")
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not initialize the read-only BigQuery source")

	// 4. valid provider -> openCompareSource succeeds
	bigQueryDeps.provider = func(string, bool, string) (bigquery.Provider, error) {
		return &cliBQProvider{principal: bigquery.Principal{Kind: "google-user", Subject: "sub"}, clock: &cliBQClock{}}, nil
	}
	db, u, err := openCompareSource(context.Background(), "bigquery://sample-project/sample_dataset?location=US", "", "bigquery")
	require.NoError(t, err)
	require.NotNil(t, db)
	require.Equal(t, "bigquery://sample-project/sample_dataset?location=US", u)
	closeCompareDB(db)
}
