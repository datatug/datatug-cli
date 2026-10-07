package dbcopy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	bqwriter "github.com/dal-go/dalgo2bigquery"
	bq "google.golang.org/api/bigquery/v2"
)

type bigQueryNoRequestTransport struct{}

func (bigQueryNoRequestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected BigQuery request during row preflight")
}

func TestBigQueryValueEncodingRetainsExactWideValuesAndRejectsLoss(t *testing.T) {
	intField := bqwriter.Field{Name: "id", Type: "INT64", Mode: "REQUIRED"}
	value, err := encodeBigQueryValue(intField, dbschema.Int, int64(math.MaxInt64), "INTEGER")
	if err != nil || value != "9223372036854775807" {
		t.Fatalf("wide INT64 = %#v, %v", value, err)
	}
	if _, err := encodeBigQueryValue(intField, dbschema.Int, float64(1<<54), "REAL"); err == nil {
		t.Fatal("lossy floating representation was accepted as INT64")
	}

	floatField := bqwriter.Field{Name: "measurement", Type: "FLOAT64", Mode: "NULLABLE"}
	value, err = encodeBigQueryValue(floatField, dbschema.Int, int64(1<<53), "INTEGER")
	if err != nil || value != float64(1<<53) {
		t.Fatalf("exact FLOAT64 integer boundary = %#v, %v", value, err)
	}
	if _, err := encodeBigQueryValue(floatField, dbschema.Int, int64(1<<53)+1, "INTEGER"); err == nil {
		t.Fatal("integer rounded by FLOAT64 conversion was accepted")
	}

	decimalField := bqwriter.Field{Name: "amount", Type: "NUMERIC", Mode: "NULLABLE", Precision: "38", Scale: "9"}
	value, err = encodeBigQueryValue(decimalField, dbschema.Decimal, "12345678901234567890.12345678", "TEXT")
	if err != nil || value != "12345678901234567890.12345678" {
		t.Fatalf("exact NUMERIC = %#v, %v", value, err)
	}

	bytesField := bqwriter.Field{Name: "payload", Type: "BYTES", Mode: "NULLABLE"}
	value, err = encodeBigQueryValue(bytesField, dbschema.Bytes, []byte{0, 0xff, 0x01}, "BLOB")
	if err != nil || value != "AP8B" {
		t.Fatalf("BYTES = %#v, %v", value, err)
	}
	encoded, err := json.Marshal(map[string]any{"payload": value})
	if err != nil || string(encoded) != `{"payload":"AP8B"}` {
		t.Fatalf("NDJSON bytes field = %s, %v", encoded, err)
	}
	if _, err := encodeBigQueryValue(bqwriter.Field{Name: "text", Type: "STRING"}, dbschema.String, string([]byte{0xff}), "TEXT"); err == nil {
		t.Fatal("invalid UTF-8 source text was accepted")
	}
}

func TestBigQueryCopyPreflightRejectsLossyIntegerBeforeTargetMutation(t *testing.T) {
	writer, err := bqwriter.NewLoadWriter(context.Background(), bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "offline_test", Location: "US",
		HTTPClient: &http.Client{Transport: bigQueryNoRequestTransport{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	table := CopyTable{
		Ref: dal.NewRootCollectionRef("measurements", ""),
		Definition: &dbschema.CollectionDef{
			Name:   "measurements",
			Fields: []dbschema.FieldDef{{Name: "value", Type: dbschema.Float, Nullable: true}},
		},
	}
	plan, err := (BigQueryCopySink{Writer: writer}).Preflight(context.Background(), []CopyTable{table})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plan.EncodeRow(table, dbschema.SourceRow{Values: map[string]any{"value": int64(1<<53) + 1}}); err == nil {
		t.Fatal("lossy source integer was accepted by row staging")
	}
	// Prepare is the first operation that may create BigQuery resources; it is
	// deliberately never reached for a source value that cannot be represented.
}

type bigQueryPrepareTransport struct {
	t      *testing.T
	tables map[string]*bq.Table
}

func (rt *bigQueryPrepareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	if len(parts) < 6 || parts[0] != "bigquery" || parts[1] != "v2" {
		return nil, fmt.Errorf("unexpected BigQuery path %q", req.URL.Path)
	}
	if parts[4] == "datasets" && len(parts) == 6 {
		return bigQueryPrepareResponse(req, http.StatusOK, `{"datasetReference":{"projectId":"demodb","datasetId":"research"},"location":"US"}`), nil
	}
	if parts[4] != "datasets" || len(parts) < 7 || parts[6] != "tables" {
		return nil, fmt.Errorf("unexpected BigQuery request path %q", req.URL.Path)
	}
	if req.Method == http.MethodPost && len(parts) == 7 {
		var table bq.Table
		if err := json.NewDecoder(req.Body).Decode(&table); err != nil {
			return nil, err
		}
		if table.TableConstraints != nil {
			rt.t.Errorf("BigQuery table %q was created with unverified constraints: %#v", table.TableReference.TableId, table.TableConstraints)
		}
		table.Etag = "etag-created"
		rt.tables[table.TableReference.TableId] = &table
		return bigQueryPrepareResponse(req, http.StatusOK, `{}`), nil
	}
	if req.Method == http.MethodGet && len(parts) == 8 {
		if table := rt.tables[parts[7]]; table != nil {
			body, err := json.Marshal(table)
			if err != nil {
				return nil, err
			}
			return bigQueryPrepareResponse(req, http.StatusOK, string(body)), nil
		}
		return bigQueryPrepareResponse(req, http.StatusNotFound, `{"error":{"code":404,"message":"Not found"}}`), nil
	}
	return nil, fmt.Errorf("unexpected BigQuery request %s %s", req.Method, req.URL.Path)
}

func bigQueryPrepareResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestBigQueryPrepareChinookOmitsUnverifiedAndSelfReferentialKeys(t *testing.T) {
	ctx := context.Background()
	ref, err := Parse("sqlite://testdata/chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	source, err := ref.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := dbschema.ListCollections(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	tables := make([]CopyTable, 0, len(refs))
	for _, tableRef := range refs {
		definition, describeErr := dbschema.DescribeCollection(ctx, source, &tableRef)
		if describeErr != nil {
			t.Fatal(describeErr)
		}
		tables = append(tables, CopyTable{Ref: tableRef, Definition: definition})
	}
	transport := &bigQueryPrepareTransport{t: t, tables: map[string]*bq.Table{}}
	writer, err := bqwriter.NewLoadWriter(ctx, bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "research", Location: "US",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (BigQueryCopySink{Writer: writer}).Preflight(ctx, tables)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if len(transport.tables) != len(tables) {
		t.Fatalf("created %d target tables, want %d", len(transport.tables), len(tables))
	}
	var employeeHasSelfReference bool
	for _, table := range tables {
		if table.Ref.Name() != "Employee" {
			continue
		}
		for _, fk := range table.Definition.ForeignKeys {
			if fk.ReferencedCollection == table.Ref.Name() {
				employeeHasSelfReference = true
			}
		}
	}
	if !employeeHasSelfReference {
		t.Fatal("Chinook fixture no longer exercises Employee.ReportsTo self-reference")
	}
	planWarnings := plan.(copyPlanWarnings).Warnings()
	foundFKWarning := false
	for _, warning := range planWarnings {
		if strings.Contains(warning, "Employee") && strings.Contains(warning, "foreign key") && strings.Contains(warning, "not declared") {
			foundFKWarning = true
		}
	}
	if !foundFKWarning {
		t.Fatalf("missing explicit source-constraint warning: %v", planWarnings)
	}
}

func TestBigQueryDecimalSchemaUsesPortableExactPrecision(t *testing.T) {
	precision := &dbschema.Precision{Total: 76, Scale: 38}
	gotType, gotPrecision, gotScale, err := bigQueryFieldType(dbschema.FieldDef{Name: "amount", Type: dbschema.Decimal, Precision: precision}, nil)
	if err != nil || !reflect.DeepEqual([]string{gotType, gotPrecision, gotScale}, []string{"BIGNUMERIC", "76", "38"}) {
		t.Fatalf("BIGNUMERIC mapping = %q(%q,%q), %v", gotType, gotPrecision, gotScale, err)
	}
	precision = &dbschema.Precision{Total: 80, Scale: 20}
	gotType, _, _, err = bigQueryFieldType(dbschema.FieldDef{Name: "amount", Type: dbschema.Decimal, Precision: precision}, nil)
	if err != nil || gotType != "STRING" {
		t.Fatalf("out-of-range decimal mapping = %q, %v; want exact text", gotType, err)
	}
}

func TestBigQueryTemporalMappingRejectsTimeWithZone(t *testing.T) {
	definition := &dbschema.SourceDefinition{Columns: []dbschema.SourceColumnDef{{Name: "clock", DeclaredType: "time with time zone"}}}
	if _, _, _, err := bigQueryFieldType(dbschema.FieldDef{Name: "clock", Type: dbschema.Time}, definition); err == nil {
		t.Fatal("time with time zone was silently mapped to timezone-free BigQuery TIME")
	}
	definition.Columns[0].DeclaredType = "timestamp with time zone"
	typ, _, _, err := bigQueryFieldType(dbschema.FieldDef{Name: "clock", Type: dbschema.Time}, definition)
	if err != nil || typ != "TIMESTAMP" {
		t.Fatalf("timestamp with time zone mapping = %q, %v", typ, err)
	}
}

func TestBigQueryDateOnlyValuesInDateTimeColumnBecomeMidnight(t *testing.T) {
	field := bqwriter.Field{Name: "OrderDate", Type: "DATETIME", Mode: "REQUIRED"}
	got, err := encodeBigQueryValue(field, dbschema.Time, "2016-07-04", "TEXT")
	if err != nil || got != "2016-07-04T00:00:00" {
		t.Fatalf("date-only DATETIME = %#v, %v; want midnight", got, err)
	}
	if _, err := encodeBigQueryValue(field, dbschema.Time, "2016-02-30", "TEXT"); err == nil {
		t.Fatal("invalid date-only DATETIME was accepted")
	}
}
