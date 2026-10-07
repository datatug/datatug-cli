package dbcompare

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dbschema"
	bigquery "github.com/dal-go/dalgo2bigquery"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func openTestDatabase(t *testing.T, ddl string, statements ...string) *dalgo2sqlite.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compare.sqlite")
	sqlDB, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = sqlDB.Exec(ddl)
	require.NoError(t, err)
	for _, statement := range statements {
		_, err = sqlDB.Exec(statement)
		require.NoError(t, err)
	}
	require.NoError(t, sqlDB.Close())
	db, err := dalgo2sqlite.NewDatabase(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		if closer, ok := any(db).(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
	return db
}

func TestCompareCountsAllDifferencesPastDetailLimitAndPreservesBytes(t *testing.T) {
	left := openTestDatabase(t,
		`CREATE TABLE Item (a TEXT NOT NULL, b TEXT NOT NULL, amount NUMERIC, payload BLOB, note TEXT, PRIMARY KEY(a,b))`,
		`INSERT INTO Item VALUES ('left__part','1',123456789012345, X'00FF', NULL)`,
		`INSERT INTO Item VALUES ('same','2',7, X'00', '')`,
		`INSERT INTO Item VALUES ('gone','3',4, X'01', 'x')`,
	)
	right := openTestDatabase(t,
		`CREATE TABLE Item (a TEXT NOT NULL, b TEXT NOT NULL, amount NUMERIC, payload BLOB, note TEXT, PRIMARY KEY(a,b))`,
		`INSERT INTO Item VALUES ('left__part','1',123456789012345, X'00FE', NULL)`,
		`INSERT INTO Item VALUES ('same','2',7, X'00', '')`,
		`INSERT INTO Item VALUES ('new','4',4, X'01', 'x')`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{Details: true, DetailLimit: 1})
	require.NoError(t, err)
	require.Equal(t, DifferenceCounts{Added: 1, Removed: 1, Changed: 1, Unchanged: 1}, report.Summary)
	require.True(t, report.DetailsLimited)
	require.Len(t, report.Relations, 1)
	require.Equal(t, int64(3), report.Relations[0].LeftRows)
	require.Equal(t, int64(3), report.Relations[0].RightRows)
	require.Len(t, report.Relations[0].Details, 1)
	require.NotEmpty(t, report.Relations[0].Details[0].ID)
}

func TestCompareSQLiteToBigQueryPreservesTypedPhysicalRowsWithoutJobs(t *testing.T) {
	sqlite := openTestDatabase(t,
		`CREATE TABLE item (id INTEGER NOT NULL, flag BOOLEAN NOT NULL, amount NUMERIC NOT NULL, payload BLOB, empty_blob BLOB NOT NULL, day DATE NOT NULL)`,
		`INSERT INTO item VALUES (1, 1, 123.25, X'00FF00', X'', '2026-10-07')`,
		`INSERT INTO item VALUES (2, 0, 1.00, NULL, X'', '2026-10-08')`,
	)
	var requests []string
	provider := compareBigQueryProvider{}
	transport := compareRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method+" "+req.URL.String())
		response := func(body string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset"):
			return response(`{"datasetReference":{"projectId":"fixture-project","datasetId":"fixture_dataset"}}`)
		case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset/tables"):
			return response(`{"totalItems":1,"tables":[{"tableReference":{"projectId":"fixture-project","datasetId":"fixture_dataset","tableId":"item"},"type":"TABLE"}]}`)
		case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset/tables/item/data"):
			return response(`{"kind":"bigquery#tableDataList","totalRows":"2","rows":[{"f":[{"v":"1"},{"v":"true"},{"v":"123.25"},{"v":"AP8A"},{"v":""},{"v":"2026-10-07"}]},{"f":[{"v":"2"},{"v":"false"},{"v":"1.00"},{"v":null},{"v":""},{"v":"2026-10-08"}]}]}`)
		case strings.HasSuffix(req.URL.Path, "/datasets/fixture_dataset/tables/item"):
			return response(`{"tableReference":{"projectId":"fixture-project","datasetId":"fixture_dataset","tableId":"item"},"type":"TABLE","etag":"etag-1","lastModifiedTime":"1000","numRows":"2","schema":{"fields":[{"name":"id","type":"INTEGER","mode":"REQUIRED"},{"name":"flag","type":"BOOL","mode":"REQUIRED"},{"name":"amount","type":"NUMERIC","mode":"REQUIRED","precision":"38","scale":"9"},{"name":"payload","type":"BYTES","mode":"NULLABLE"},{"name":"empty_blob","type":"BYTES","mode":"REQUIRED"},{"name":"day","type":"DATE","mode":"REQUIRED"}]}}`)
		default:
			t.Fatalf("unexpected BigQuery request %s", req.URL.String())
			return nil, nil
		}
	})
	bq, err := bigquery.NewReadOnlyDatabase(bigquery.SourceConfig{ProjectID: "fixture-project", DatasetID: "fixture_dataset", Provider: provider, Transport: transport})
	require.NoError(t, err)
	report, err := Compare(context.Background(), "sqlite", sqlite, "bigquery", bq, Options{})
	require.NoError(t, err)
	require.Equal(t, int64(2), report.Summary.Unchanged)
	require.Equal(t, int64(0), report.Summary.Added+report.Summary.Removed+report.Summary.Changed)
	require.Equal(t, "multiset", report.Relations[0].Identity)
	require.Contains(t, report.Warnings, "index metadata for item is unsupported by the source adapter; index differences were not compared")
	require.Len(t, requests, 9)
	for _, request := range requests {
		require.True(t, strings.HasPrefix(request, "GET https://bigquery.googleapis.com/"), request)
		require.NotContains(t, request, "/jobs", request)
	}
}

func TestCompareSQLiteToNativeInGitDBPreservesKeylessDuplicates(t *testing.T) {
	sqlite := openTestDatabase(t, `CREATE TABLE event (body TEXT, payload BLOB)`,
		`INSERT INTO event VALUES ('same', X'00FF')`,
		`INSERT INTO event VALUES ('same', X'00FF')`,
		`INSERT INTO event VALUES ('other', X'')`,
	)
	destination := filepath.Join(t.TempDir(), "snapshot")
	_, err := dbcopy.ExportInGitDB(context.Background(), sqlite, destination)
	require.NoError(t, err)
	gitDB, err := (dbcopy.BackendRef{Scheme: "ingitdb", Path: destination}).OpenForCopy(context.Background(), "")
	require.NoError(t, err)
	defer func() {
		if closer, ok := gitDB.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()
	report, err := Compare(context.Background(), "sqlite", sqlite, "ingitdb", gitDB, Options{})
	require.NoError(t, err)
	require.Equal(t, int64(3), report.Summary.Unchanged)
	require.Zero(t, report.Summary.Added+report.Summary.Removed+report.Summary.Changed)
	require.Equal(t, "multiset", report.Relations[0].Identity)
}

func TestCompareUsesExplicitTableMapForTransformedProviderNames(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE "Production.Document" (id INTEGER PRIMARY KEY, FileExtension BLOB)`,
		`INSERT INTO "Production.Document" VALUES (1, X'00FF')`,
	)
	right := openTestDatabase(t, `CREATE TABLE production_document (id INTEGER PRIMARY KEY, FileExtension BLOB)`,
		`INSERT INTO production_document VALUES (1, X'00FF')`,
	)

	report, err := Compare(context.Background(), "source", left, "target", right, Options{TableMaps: map[string]string{"Production.Document": "production_document"}})
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Summary.Unchanged)
	require.Zero(t, report.Summary.Added+report.Summary.Removed+report.Summary.Changed)
	require.Len(t, report.Relations, 1)
	require.Equal(t, "Production.Document", report.Relations[0].Name)
	require.Equal(t, "production_document", report.Relations[0].RightName)
	require.Empty(t, report.SchemaChanges)
}

func TestCompareTableMapDoesNotSilentlyReuseExplicitlyMappedRightRelation(t *testing.T) {
	left := map[string]relation{"same": {}, "renamed": {}}
	right := map[string]relation{"same": {}, "target": {}}
	pairs, err := matchRelations(left, right, map[string]string{"renamed": "same"})
	require.NoError(t, err)
	require.Len(t, pairs, 3)
	var leftOnly, mapped, rightOnly int
	for _, pair := range pairs {
		switch {
		case pair.hasLeft && !pair.hasRight:
			leftOnly++
		case pair.hasLeft && pair.hasRight && pair.name == "renamed" && pair.rightName == "same":
			mapped++
		case !pair.hasLeft && pair.hasRight && pair.name == "target":
			rightOnly++
		}
	}
	require.Equal(t, 1, leftOnly)
	require.Equal(t, 1, mapped)
	require.Equal(t, 1, rightOnly)
}

func TestCompareRejectsUnknownOrAmbiguousTableMaps(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE alpha (id INTEGER PRIMARY KEY)`, `INSERT INTO alpha VALUES (1)`)
	right := openTestDatabase(t, `CREATE TABLE beta (id INTEGER PRIMARY KEY)`, `INSERT INTO beta VALUES (1)`)
	for _, mappings := range []map[string]string{
		{"missing": "beta"},
		{"alpha": "missing"},
		{"alpha": "beta", "other": "beta"},
	} {
		_, err := Compare(context.Background(), "left", left, "right", right, Options{TableMaps: mappings})
		require.Error(t, err, "mappings %v", mappings)
	}
}

type compareRoundTripper func(*http.Request) (*http.Response, error)

func (f compareRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type compareBigQueryProvider struct{}

func (compareBigQueryProvider) Authorize(_ context.Context, transport http.RoundTripper) (bigquery.Identity, http.RoundTripper, error) {
	return bigquery.Identity{Principal: bigquery.Principal{Kind: "google-user", Subject: "fixture-reader", Generation: "fixture-grant"}, ExpiresAt: time.Now().Add(time.Hour), Read: true}, transport, nil
}

func TestCompareCompositeKeysWithSeparatorsRemainDistinct(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE item (a TEXT NOT NULL, b TEXT NOT NULL, value TEXT, PRIMARY KEY(a,b))`,
		`INSERT INTO item VALUES ('a__b','c','first')`,
		`INSERT INTO item VALUES ('a','b__c','second')`,
	)
	right := openTestDatabase(t, `CREATE TABLE item (a TEXT NOT NULL, b TEXT NOT NULL, value TEXT, PRIMARY KEY(a,b))`,
		`INSERT INTO item VALUES ('a__b','c','first')`,
		`INSERT INTO item VALUES ('a','b__c','second changed')`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{Details: true, DetailLimit: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Summary.Changed)
	require.Len(t, report.Relations[0].Details, 1)
	require.Contains(t, report.Relations[0].Details[0].ID, "b__c")
}

func TestCompareKeylessRowsIsOrderIndependentAndCountsDuplicateMultiplicity(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE event (body BLOB, description TEXT)`,
		`INSERT INTO event VALUES (X'00FF', NULL)`,
		`INSERT INTO event VALUES (X'00FF', NULL)`,
		`INSERT INTO event VALUES (X'01', '')`,
	)
	right := openTestDatabase(t, `CREATE TABLE event (body BLOB, description TEXT)`,
		`INSERT INTO event VALUES (X'01', '')`,
		`INSERT INTO event VALUES (X'00FF', NULL)`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{})
	require.NoError(t, err)
	require.Equal(t, DifferenceCounts{Removed: 1, Unchanged: 2}, report.Summary)
	require.False(t, report.DetailsLimited)
}

func TestCompareExplicitKeyClassifiesKeylessSourceChanges(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE item (id INTEGER NOT NULL, value TEXT)`,
		`INSERT INTO item VALUES (1, 'before')`,
	)
	right := openTestDatabase(t, `CREATE TABLE item (id INTEGER NOT NULL, value TEXT)`,
		`INSERT INTO item VALUES (1, 'after')`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{
		Details: true, DetailLimit: 2, KeyMaps: map[string][]string{"item": {"id"}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Summary.Changed)
	require.Equal(t, "explicit-key", report.Relations[0].Identity)
	require.Equal(t, "id=1", report.Relations[0].Details[0].ID)
}

func TestCompareExplicitKeyRejectsDuplicateAndNullValues(t *testing.T) {
	t.Run("duplicate", func(t *testing.T) {
		left := openTestDatabase(t, `CREATE TABLE item (id INTEGER, value TEXT)`,
			`INSERT INTO item VALUES (1, 'a')`, `INSERT INTO item VALUES (1, 'b')`,
		)
		right := openTestDatabase(t, `CREATE TABLE item (id INTEGER, value TEXT)`,
			`INSERT INTO item VALUES (1, 'a')`,
		)
		_, err := Compare(context.Background(), "left", left, "right", right, Options{KeyMaps: map[string][]string{"item": {"id"}}})
		require.ErrorContains(t, err, "duplicate ID")
	})
	t.Run("null", func(t *testing.T) {
		left := openTestDatabase(t, `CREATE TABLE item (id INTEGER, value TEXT)`,
			`INSERT INTO item VALUES (NULL, 'a')`,
		)
		right := openTestDatabase(t, `CREATE TABLE item (id INTEGER, value TEXT)`,
			`INSERT INTO item VALUES (NULL, 'a')`,
		)
		_, err := Compare(context.Background(), "left", left, "right", right, Options{KeyMaps: map[string][]string{"item": {"id"}}})
		require.ErrorContains(t, err, "contains NULL")
	})
}

func TestRowStoreUsesPrivateDiskSpoolAndRemovesIt(t *testing.T) {
	store, err := newRowStore(false)
	require.NoError(t, err)
	dir := store.tempDir
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.NoError(t, store.Close())
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCompareReportsMissingRelationsAndColumnChanges(t *testing.T) {
	left := openTestDatabase(t,
		`CREATE TABLE alpha (id INTEGER PRIMARY KEY, old_name TEXT)`,
		`CREATE TABLE old_only (id INTEGER PRIMARY KEY)`,
		`INSERT INTO old_only VALUES (5)`,
		`CREATE VIEW summary AS SELECT id FROM alpha`,
	)
	right := openTestDatabase(t,
		`CREATE TABLE alpha (id INTEGER PRIMARY KEY, new_name TEXT)`,
		`CREATE TABLE extra (id INTEGER PRIMARY KEY)`,
		`INSERT INTO extra VALUES (6)`,
		`CREATE VIEW summary AS SELECT id FROM alpha`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{})
	require.NoError(t, err)
	require.Contains(t, report.SchemaChanges, "relation only in right: extra")
	require.Contains(t, report.SchemaChanges, "relation only in left: old_only")
	require.Contains(t, report.SchemaChanges, "alpha.new_name exists only in right")
	require.Contains(t, report.SchemaChanges, "alpha.old_name exists only in left")
	require.Equal(t, int64(1), report.Summary.Added)
	require.Equal(t, int64(1), report.Summary.Removed)
}

func TestCompareViewDefinitionsOnlyWithinSameProviderFamily(t *testing.T) {
	left := relationInventory{adapterName: "postgres", views: map[string]dbschema.SourceViewDef{
		"summary": {Name: "summary", Columns: []string{"id"}, CreateSQL: "SELECT id FROM item"},
	}}
	right := relationInventory{adapterName: "bigquery", views: map[string]dbschema.SourceViewDef{
		"summary": {Name: "summary", Columns: []string{"id"}, CreateSQL: "SELECT id FROM item"},
	}}
	report := Report{LeftName: "postgres", RightName: "bigquery"}
	appendInventoryChanges(&report, left, right, nil)
	require.Empty(t, report.SchemaChanges, "cross-dialect SQL bodies are not treated as comparable DDL")

	right.adapterName = "postgres"
	right.views["summary"] = dbschema.SourceViewDef{Name: "summary", Columns: []string{"id"}, CreateSQL: "SELECT id FROM other_item"}
	report = Report{LeftName: "left", RightName: "right"}
	appendInventoryChanges(&report, left, right, nil)
	require.Equal(t, []string{"view summary definition differs"}, report.SchemaChanges)
}

func TestNormalizeExactValuesAndRejectsUnrepresentableTypes(t *testing.T) {
	field := dbschema.FieldDef{Type: dbschema.Decimal}
	value, err := normalizeValue(field, "12345678901234567890.12345678")
	require.NoError(t, err)
	require.Equal(t, "decimal", value.Kind)
	require.Equal(t, "12345678901234567890.12345678", value.Text) // canonical decimal, never float-converted
	equivalent, err := normalizeValue(field, "1e0")
	require.NoError(t, err)
	one, err := normalizeValue(field, "1.00")
	require.NoError(t, err)
	require.Equal(t, one, equivalent)
	_, err = normalizeValue(field, "1/2")
	require.Error(t, err)

	value, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Int}, uint64(9007199254740993))
	require.NoError(t, err)
	require.Equal(t, "9007199254740993", value.Text)

	value, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Bytes}, []byte{0, 0xff})
	require.NoError(t, err)
	require.Equal(t, "00ff", value.Text)

	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Bool}, int64(2))
	require.Error(t, err)
	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Float}, big.NewInt(9007199254740993))
	require.Error(t, err)
	_, ok := floatValue(json.Number("0.10000000000000001"))
	require.False(t, ok)
	_, err = normalizeValue(dbschema.FieldDef{Type: dbschema.Decimal}, float64(0.1))
	require.Error(t, err, "binary floating point cannot be promoted to an exact decimal")
}

func TestNormalizeProviderEquivalentBooleansIntegersDecimalsAndTemporalValues(t *testing.T) {
	boolFromPostgres, err := normalizeValue(dbschema.FieldDef{Type: dbschema.Bool}, true)
	require.NoError(t, err)
	boolFromSQLite, err := normalizeValue(dbschema.FieldDef{Type: dbschema.Bool}, int64(1))
	require.NoError(t, err)
	require.Equal(t, boolFromPostgres, boolFromSQLite)

	wideInteger, err := normalizeValue(dbschema.FieldDef{Type: dbschema.Int}, "9007199254740993")
	require.NoError(t, err)
	require.Equal(t, "9007199254740993", wideInteger.Text)

	decimalFromBigQuery, err := normalizeValue(dbschema.FieldDef{Type: dbschema.Decimal}, "12345678901234567890.12345678")
	require.NoError(t, err)
	decimalFromPostgres, err := normalizeValue(dbschema.FieldDef{Type: dbschema.Decimal}, []byte("12345678901234567890.12345678"))
	require.NoError(t, err)
	require.Equal(t, decimalFromBigQuery, decimalFromPostgres)
	require.Equal(t, "12345678901234567890.12345678", publicValue(decimalFromBigQuery))

	zone := time.FixedZone("source", 13*60*60)
	localDate := time.Date(2026, 10, 7, 1, 15, 0, 0, zone)
	dateValue, err := normalizeValueForSource(dbschema.FieldDef{Type: dbschema.Time}, "postgres", "date", localDate)
	require.NoError(t, err)
	require.Equal(t, "2026-10-07", dateValue.Text)

	wallTime := time.Date(2026, 10, 7, 1, 15, 0, 0, zone)
	wallValue, err := normalizeValueForSource(dbschema.FieldDef{Type: dbschema.Time}, "postgres", "timestamp without time zone", wallTime)
	require.NoError(t, err)
	wallText, ok := wallTimestampValue("2026-10-07 01:15:00")
	require.True(t, ok)
	require.Equal(t, wallText, wallValue.Text)

	instant, err := normalizeValueForSource(dbschema.FieldDef{Type: dbschema.Time}, "bigquery", "TIMESTAMP", wallTime)
	require.NoError(t, err)
	require.Equal(t, "2026-10-06T12:15:00Z", instant.Text)

	timeOnly, err := normalizeValueForSource(dbschema.FieldDef{Type: dbschema.Time}, "postgres", "time with time zone", wallTime)
	require.NoError(t, err)
	require.Equal(t, "01:15:00+13:00", timeOnly.Text)
}

func TestDetailsDisabledStillComparesWholeTables(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO item VALUES (1, 'old')`,
	)
	right := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO item VALUES (1, 'new')`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{})
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Summary.Changed)
	require.Empty(t, report.Relations[0].Details)
}

func TestRecordDetailContainsBeforeAndAfterValues(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO item VALUES (1, 'before')`,
	)
	right := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO item VALUES (1, 'after')`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{Details: true, DetailLimit: 1})
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Summary.Changed)
	require.Len(t, report.Relations[0].Details, 1)
	require.Equal(t, "changed", report.Relations[0].Details[0].Status)
	require.Equal(t, FieldDiff{Name: "value", Before: "before", After: "after"}, report.Relations[0].Details[0].Fields[0])

	zero, err := Compare(context.Background(), "left", left, "right", right, Options{Details: true, DetailLimit: 0})
	require.NoError(t, err)
	require.Equal(t, int64(1), zero.Summary.Changed)
	require.Empty(t, zero.Relations[0].Details)
	require.True(t, zero.DetailsLimited)
}

func TestRecordDetailJSONDistinguishesNullFromEmptyString(t *testing.T) {
	left := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO item VALUES (1, NULL)`,
	)
	right := openTestDatabase(t, `CREATE TABLE item (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO item VALUES (1, '')`,
	)
	report, err := Compare(context.Background(), "left", left, "right", right, Options{Details: true, DetailLimit: 1})
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Summary.Changed)
	require.Len(t, report.Relations[0].Details[0].Fields, 1)
	encoded, err := json.Marshal(report.Relations[0].Details[0].Fields[0])
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"value","before":null,"after":""}`, string(encoded))
}
