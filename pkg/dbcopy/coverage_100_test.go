package dbcopy

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	bqwriter "github.com/dal-go/dalgo2bigquery"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingr-io/ingr-go/ingr"
	"github.com/stretchr/testify/require"
)

type mockINGRWriter struct {
	writeErr error
	closeErr error
}

func (m mockINGRWriter) WriteHeader(title string, cols []ingr.ColDef) (int, error) { return 0, nil }
func (m mockINGRWriter) WriteRecords(recordsDelimiter int, r ...ingr.Record) (int, error) {
	return 0, m.writeErr
}
func (m mockINGRWriter) Close() error { return m.closeErr }

type failingWriter struct {
	err error
}

func (w failingWriter) Write(p []byte) (n int, err error) {
	if w.err != nil {
		return 0, w.err
	}
	return 0, errors.New("write error")
}

type testStringer struct{ s string }

func (t testStringer) String() string { return t.s }

type mockSink struct {
	plan CopySinkPlan
	err  error
}

func (m mockSink) Preflight(ctx context.Context, tables []CopyTable) (CopySinkPlan, error) {
	return m.plan, m.err
}

type mockSinkPlan struct {
	targetName string
	prepErr    error
	loadName   string
	loadRows   int64
	loadErr    error
	encodeLine []byte
	encodeErr  error
	warns      []string
}

func (m mockSinkPlan) TargetName(table CopyTable) string {
	if m.targetName != "" {
		return m.targetName
	}
	return table.Ref.Path()
}
func (m mockSinkPlan) Prepare(ctx context.Context) error { return m.prepErr }
func (m mockSinkPlan) LoadTable(ctx context.Context, table CopyTable, rows io.Reader) (string, int64, error) {
	return m.loadName, m.loadRows, m.loadErr
}
func (m mockSinkPlan) EncodeRow(table CopyTable, row dbschema.SourceRow) ([]byte, error) {
	if m.encodeErr != nil {
		return nil, m.encodeErr
	}
	if len(m.encodeLine) > 0 {
		return m.encodeLine, nil
	}
	return []byte("{}\n"), nil
}
func (m mockSinkPlan) Warnings() []string { return m.warns }

// Not parallel: mutates package seams.
func TestBigQueryTarget_Coverage100(t *testing.T) {
	// sourceText
	str, ok := sourceText("hello")
	require.True(t, ok)
	require.Equal(t, "hello", str)
	_, ok = sourceText(string([]byte{0xff}))
	require.False(t, ok)

	str, ok = sourceText([]byte("world"))
	require.True(t, ok)
	require.Equal(t, "world", str)
	_, ok = sourceText([]byte{0xff})
	require.False(t, ok)

	str, ok = sourceText(testStringer{s: "stringer"})
	require.True(t, ok)
	require.Equal(t, "stringer", str)
	_, ok = sourceText(testStringer{s: string([]byte{0xff})})
	require.False(t, ok)

	_, ok = sourceText(12345)
	require.False(t, ok)

	// integerSourceText
	intVals := []any{
		int(1), int8(2), int16(3), int32(4), int64(5),
		uint(6), uint8(7), uint16(8), uint32(9), uint64(10),
		"11", json.Number("12"), big.NewInt(13), float64(14),
	}
	for _, v := range intVals {
		_, ok := integerSourceText(v)
		require.True(t, ok, "failed for %T", v)
	}
	// uint > MaxInt64
	_, ok = integerSourceText(uint(math.MaxUint64))
	require.False(t, ok)
	// uint64 > MaxInt64
	_, ok = integerSourceText(uint64(math.MaxUint64))
	require.False(t, ok)
	// *big.Int nil or !IsInt64()
	var nilBigInt *big.Int
	_, ok = integerSourceText(nilBigInt)
	require.False(t, ok)
	hugeBigInt := new(big.Int).Add(big.NewInt(math.MaxInt64), big.NewInt(100))
	_, ok = integerSourceText(hugeBigInt)
	require.False(t, ok)
	// float64 edge cases
	_, ok = integerSourceText(float64(math.NaN()))
	require.False(t, ok)
	_, ok = integerSourceText(math.Inf(1))
	require.False(t, ok)
	_, ok = integerSourceText(12.34)
	require.False(t, ok)
	_, ok = integerSourceText(float64(1 << 54))
	require.False(t, ok)
	_, ok = integerSourceText("not_int")
	require.True(t, ok)
	_, ok = integerSourceText(true)
	require.False(t, ok)

	// exactDecimalText
	decVals := []any{
		"1.23", []byte("2.34"), int(1), int8(2), int16(3), int32(4), int64(5), uint64(6),
		json.Number("7.89"), big.NewInt(10), big.NewRat(12, 1), testStringer{s: "13.45"},
	}
	for _, v := range decVals {
		_, ok := exactDecimalText(v)
		require.True(t, ok, "failed for %T", v)
	}
	_, ok = exactDecimalText(nilBigInt)
	require.False(t, ok)
	var nilBigRat *big.Rat
	_, ok = exactDecimalText(nilBigRat)
	require.False(t, ok)
	_, ok = exactDecimalText(big.NewRat(1, 3))
	require.False(t, ok)
	_, ok = exactDecimalText(struct{}{})
	require.False(t, ok)

	// floatingSource
	_, ok = floatingSource(float32(1.5))
	require.True(t, ok)
	_, ok = floatingSource(float64(2.5))
	require.True(t, ok)
	_, ok = floatingSource(int(100))
	require.True(t, ok)
	_, ok = floatingSource(int(1 << 54))
	require.False(t, ok)
	_, ok = floatingSource(int(-(1 << 54)))
	require.False(t, ok)
	_, ok = floatingSource(int64(100))
	require.True(t, ok)
	_, ok = floatingSource(int64(1 << 54))
	require.False(t, ok)
	_, ok = floatingSource(int64(-(1 << 54)))
	require.False(t, ok)
	_, ok = floatingSource("123.45")
	require.True(t, ok)
	_, ok = floatingSource("not_a_float")
	require.False(t, ok)
	_, ok = floatingSource(true)
	require.False(t, ok)

	// temporalSourceText
	now := time.Now().Truncate(time.Microsecond)
	str, ok = temporalSourceText("DATE", now)
	require.True(t, ok)
	require.NotEmpty(t, str)
	str, ok = temporalSourceText("TIME", now)
	require.True(t, ok)
	require.NotEmpty(t, str)
	// non-microsecond nanoseconds
	notMicro := now.Add(123 * time.Nanosecond)
	_, ok = temporalSourceText("TIME", notMicro)
	require.False(t, ok)
	str, ok = temporalSourceText("DATETIME", now)
	require.True(t, ok)
	require.NotEmpty(t, str)
	_, ok = temporalSourceText("DATETIME", notMicro)
	require.False(t, ok)
	str, ok = temporalSourceText("TIMESTAMP", now)
	require.True(t, ok)
	require.NotEmpty(t, str)
	_, ok = temporalSourceText("TIMESTAMP", notMicro)
	require.False(t, ok)

	// string branch
	str, ok = temporalSourceText("DATETIME", "2026-01-02")
	require.True(t, ok)
	require.Equal(t, "2026-01-02T00:00:00", str)
	_, ok = temporalSourceText("DATETIME", string([]byte{0xff}))
	require.False(t, ok)
	_, ok = temporalSourceText("DATETIME", 12345)
	require.False(t, ok)

	// bigQueryIdentifier
	_, err := bigQueryIdentifier(string([]byte{0xff}), 100)
	require.ErrorContains(t, err, "not valid UTF-8")
	id, err := bigQueryIdentifier("a-b.c", 100)
	require.NoError(t, err)
	require.Contains(t, id, "_x2D_")
	_, err = bigQueryIdentifier("", 100)
	require.ErrorContains(t, err, "source name is empty")
	id, err = bigQueryIdentifier("1table", 100)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(id, "_1"))
	_, err = bigQueryIdentifier("very_long_name", 5)
	require.ErrorContains(t, err, "exceeds BigQuery's 5-character limit")

	// bigQueryFieldType
	typ, _, _, err := bigQueryFieldType(dbschema.FieldDef{Type: dbschema.Bool}, nil)
	require.NoError(t, err)
	require.Equal(t, "BOOL", typ)
	typ, _, _, err = bigQueryFieldType(dbschema.FieldDef{Type: dbschema.Bytes}, nil)
	require.NoError(t, err)
	require.Equal(t, "BYTES", typ)
	typ, _, _, err = bigQueryFieldType(dbschema.FieldDef{Type: dbschema.Decimal}, nil)
	require.NoError(t, err)
	require.Equal(t, "STRING", typ)
	_, _, _, err = bigQueryFieldType(dbschema.FieldDef{Type: dbschema.Decimal, Precision: &dbschema.Precision{Total: 0, Scale: 0}}, nil)
	require.ErrorContains(t, err, "invalid decimal precision/scale")
	// Time mappings
	srcDef := &dbschema.SourceDefinition{
		Columns: []dbschema.SourceColumnDef{
			{Name: "col_date", DeclaredType: "date"},
			{Name: "col_time", DeclaredType: "time"},
			{Name: "col_datetime", DeclaredType: "datetime"},
			{Name: "col_ts_notz", DeclaredType: "timestamp without time zone"},
			{Name: "col_unknown", DeclaredType: "unknown_temporal"},
		},
	}
	typ, _, _, err = bigQueryFieldType(dbschema.FieldDef{Name: "col_date", Type: dbschema.Time}, srcDef)
	require.NoError(t, err)
	require.Equal(t, "DATE", typ)
	typ, _, _, err = bigQueryFieldType(dbschema.FieldDef{Name: "col_time", Type: dbschema.Time}, srcDef)
	require.NoError(t, err)
	require.Equal(t, "TIME", typ)
	typ, _, _, err = bigQueryFieldType(dbschema.FieldDef{Name: "col_datetime", Type: dbschema.Time}, srcDef)
	require.NoError(t, err)
	require.Equal(t, "DATETIME", typ)
	typ, _, _, err = bigQueryFieldType(dbschema.FieldDef{Name: "col_ts_notz", Type: dbschema.Time}, srcDef)
	require.NoError(t, err)
	require.Equal(t, "DATETIME", typ)
	_, _, _, err = bigQueryFieldType(dbschema.FieldDef{Name: "col_unknown", Type: dbschema.Time}, srcDef)
	require.ErrorContains(t, err, "cannot be mapped without changing its meaning")
	_, _, _, err = bigQueryFieldType(dbschema.FieldDef{Type: dbschema.Type(120)}, nil)
	require.ErrorContains(t, err, "has no lossless BigQuery mapping")

	// sourceDeclaredType
	require.Equal(t, "", sourceDeclaredType(nil, "foo"))
	require.Equal(t, "", sourceDeclaredType(srcDef, "missing"))

	// encodeBigQueryValue
	// required NULL
	_, err = encodeBigQueryValue(bqwriter.Field{Mode: "REQUIRED"}, dbschema.String, nil, "")
	require.ErrorContains(t, err, "required value is NULL")
	val, err := encodeBigQueryValue(bqwriter.Field{Mode: "NULLABLE"}, dbschema.String, nil, "")
	require.NoError(t, err)
	require.Nil(t, val)

	// STRING error
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "STRING"}, dbschema.String, 12345, "")
	require.ErrorContains(t, err, "cannot be converted to exact text")

	// BYTES error
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "BYTES"}, dbschema.Bytes, "not_bytes", "")
	require.ErrorContains(t, err, "binary value has unsupported representation")

	// INT64 error
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "INT64"}, dbschema.Int, struct{}{}, "")
	require.ErrorContains(t, err, "integer value has unsupported representation")
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "INT64"}, dbschema.Int, "9999999999999999999999999999999", "")
	require.ErrorContains(t, err, "integer is outside BigQuery INT64")

	// NUMERIC error
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "NUMERIC"}, dbschema.Decimal, struct{}{}, "")
	require.ErrorContains(t, err, "decimal value has unsupported or lossy representation")
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "NUMERIC", Precision: "38", Scale: "9"}, dbschema.Decimal, "invalid_num", "")
	require.ErrorContains(t, err, "decimal is outside the declared BigQuery range")

	// BOOL
	bVal, err := encodeBigQueryValue(bqwriter.Field{Type: "BOOL"}, dbschema.Bool, true, "")
	require.NoError(t, err)
	require.Equal(t, true, bVal)
	bVal, err = encodeBigQueryValue(bqwriter.Field{Type: "BOOL"}, dbschema.Bool, false, "")
	require.NoError(t, err)
	require.Equal(t, false, bVal)
	bVal, err = encodeBigQueryValue(bqwriter.Field{Type: "BOOL"}, dbschema.Bool, "true", "")
	require.NoError(t, err)
	require.Equal(t, true, bVal)
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "BOOL"}, dbschema.Bool, "invalid_bool", "")
	require.ErrorContains(t, err, "boolean value has unsupported representation")

	// FLOAT64
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "FLOAT64"}, dbschema.Float, math.NaN(), "")
	require.ErrorContains(t, err, "float value has unsupported representation")

	// TIMESTAMP
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "TIMESTAMP"}, dbschema.Time, struct{}{}, "")
	require.ErrorContains(t, err, "temporal value has unsupported representation")
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "TIMESTAMP"}, dbschema.Time, "2026-01-01T00:00:00.123456789Z", "")
	require.ErrorContains(t, err, "timestamp must be RFC3339 and no more precise than microseconds")
	tsVal, err := encodeBigQueryValue(bqwriter.Field{Type: "TIMESTAMP"}, dbschema.Time, "2026-01-01T00:00:00.123456Z", "")
	require.NoError(t, err)
	require.Contains(t, tsVal, "2026-01-01")

	// DATE, TIME, DATETIME NormalizeScalar error
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "DATE"}, dbschema.Time, "invalid_date", "")
	require.ErrorContains(t, err, "invalid DATE value")

	// unsupported field.Type
	_, err = encodeBigQueryValue(bqwriter.Field{Type: "RECORD"}, dbschema.String, "val", "")
	require.ErrorContains(t, err, "BigQuery field type \"RECORD\" is unsupported")

	// Preflight validation errors
	sink := BigQueryCopySink{Writer: nil}
	_, err = sink.Preflight(context.Background(), nil)
	require.ErrorContains(t, err, "BigQuery writer is not configured")

	writer, err := bqwriter.NewLoadWriter(context.Background(), bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "offline_test", Location: "US",
		HTTPClient: &http.Client{Transport: bigQueryNoRequestTransport{}},
	})
	require.NoError(t, err)
	sink.Writer = writer

	// Database route error
	_, err = sink.Preflight(context.Background(), []CopyTable{
		{Ref: dal.NewDatabaseCollectionRef("db1", "", "tbl", ""), Definition: &dbschema.CollectionDef{Name: "tbl"}},
	})
	require.ErrorContains(t, err, "has a database route or parent")

	// Table name collision
	_, err = sink.Preflight(context.Background(), []CopyTable{
		{Ref: dal.NewRootCollectionRef("Tbl", ""), Definition: &dbschema.CollectionDef{Name: "Tbl", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}}},
		{Ref: dal.NewRootCollectionRef("tbl", ""), Definition: &dbschema.CollectionDef{Name: "tbl", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}}},
	})
	require.ErrorContains(t, err, "map to the same BigQuery table")

	// Invalid field name
	_, err = sink.Preflight(context.Background(), []CopyTable{
		{Ref: dal.NewRootCollectionRef("tbl", ""), Definition: &dbschema.CollectionDef{
			Name: "tbl", Fields: []dbschema.FieldDef{{Name: dal.FieldName([]byte{0xff}), Type: dbschema.Int}},
		}},
	})
	require.ErrorContains(t, err, "not valid UTF-8")

	// Field name collision
	_, err = sink.Preflight(context.Background(), []CopyTable{
		{Ref: dal.NewRootCollectionRef("tbl", ""), Definition: &dbschema.CollectionDef{
			Name: "tbl", Fields: []dbschema.FieldDef{
				{Name: "Col", Type: dbschema.Int},
				{Name: "col", Type: dbschema.Int},
			},
		}},
	})
	require.ErrorContains(t, err, "map to the same BigQuery field")

	// Field type mapping error
	_, err = sink.Preflight(context.Background(), []CopyTable{
		{Ref: dal.NewRootCollectionRef("tbl", ""), Definition: &dbschema.CollectionDef{
			Name: "tbl", Fields: []dbschema.FieldDef{{Name: "col", Type: dbschema.Type(120)}},
		}},
	})
	require.ErrorContains(t, err, "has no lossless BigQuery mapping")

	// No fields error
	_, err = sink.Preflight(context.Background(), []CopyTable{
		{Ref: dal.NewRootCollectionRef("tbl", ""), Definition: &dbschema.CollectionDef{Name: "tbl"}},
	})
	require.ErrorContains(t, err, "has no fields")

	// Schema with warnings: default/autoincrement, primary key, indexes, foreign keys
	validTable := CopyTable{
		Ref: dal.NewQualifiedRootCollectionRef("schema1", "items", ""),
		Definition: &dbschema.CollectionDef{
			Name: "items",
			Fields: []dbschema.FieldDef{
				{Name: "id", Type: dbschema.Int, AutoIncrement: true},
				{Name: "val", Type: dbschema.String, Default: dbschema.DefaultLiteral{Value: "default_val"}},
			},
			PrimaryKey:  []dal.FieldName{"id"},
			Indexes:     []dbschema.IndexDef{{Name: "idx_val"}},
			ForeignKeys: []dbschema.ForeignKeyDef{{Name: "fk_self", ReferencedCollection: "schema1/items"}},
		},
	}
	plan, err := sink.Preflight(context.Background(), []CopyTable{validTable})
	require.NoError(t, err)
	require.NotEmpty(t, plan.(copyPlanWarnings).Warnings())

	// TargetName
	targetName := plan.TargetName(validTable)
	require.Equal(t, "schema1__items", targetName)
	missingTable := CopyTable{Ref: dal.NewRootCollectionRef("missing", "")}
	require.Equal(t, "missing", plan.TargetName(missingTable))

	// EncodeRow
	_, err = plan.EncodeRow(missingTable, dbschema.SourceRow{})
	require.ErrorContains(t, err, "differs from the described fields")
	_, err = plan.EncodeRow(validTable, dbschema.SourceRow{Values: map[string]any{"id": 1}})
	require.ErrorContains(t, err, "differs from the described fields")
	_, err = plan.EncodeRow(validTable, dbschema.SourceRow{Values: map[string]any{"id": 1, "wrong": "bar"}})
	require.ErrorContains(t, err, "is missing field \"val\"")
	_, err = plan.EncodeRow(validTable, dbschema.SourceRow{Values: map[string]any{"id": struct{}{}, "val": "bar"}})
	require.ErrorContains(t, err, "integer value has unsupported representation")

	// bqJSONMarshal error in EncodeRow
	origBQMarshal := bqJSONMarshal
	defer func() { bqJSONMarshal = origBQMarshal }()
	bqJSONMarshal = func(v any) ([]byte, error) { return nil, errors.New("bq marshal fail") }
	_, err = plan.EncodeRow(validTable, dbschema.SourceRow{Values: map[string]any{"id": int64(1), "val": "bar"}})
	require.ErrorContains(t, err, "encode row JSON: bq marshal fail")
	bqJSONMarshal = origBQMarshal

	// LoadTable absent table
	_, _, err = plan.LoadTable(context.Background(), missingTable, nil)
	require.ErrorContains(t, err, "source table is absent from transfer plan")
}

type mockTransport struct {
	roundTrip func(req *http.Request) (*http.Response, error)
}

func (m mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTrip(req)
}

// Not parallel: mutates package seams.
func TestBigQueryPrepareAndLoadErrors(t *testing.T) {
	ctx := context.Background()
	// Prepare EnsureDataset error
	transport := mockTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("network down")
		},
	}
	writer, err := bqwriter.NewLoadWriter(ctx, bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "research", Location: "US",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)

	table := CopyTable{
		Ref: dal.NewRootCollectionRef("items", ""),
		Definition: &dbschema.CollectionDef{
			Name:   "items",
			Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
		},
	}
	plan, err := (BigQueryCopySink{Writer: writer}).Preflight(ctx, []CopyTable{table})
	require.NoError(t, err)
	err = plan.Prepare(ctx)
	require.ErrorContains(t, err, "BigQuery get dataset failed")

	// CheckTablesAbsent error
	transport = mockTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "datasets") && !strings.Contains(req.URL.Path, "tables") {
				return bigQueryPrepareResponse(req, http.StatusOK, `{"datasetReference":{"projectId":"demodb","datasetId":"research"},"location":"US"}`), nil
			}
			return nil, errors.New("check tables fail")
		},
	}
	writer, err = bqwriter.NewLoadWriter(ctx, bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "research", Location: "US",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)
	plan, err = (BigQueryCopySink{Writer: writer}).Preflight(ctx, []CopyTable{table})
	require.NoError(t, err)
	err = plan.Prepare(ctx)
	require.ErrorContains(t, err, "BigQuery check destination tables failed")

	// CreateTable error
	transport = mockTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "datasets") && !strings.Contains(req.URL.Path, "tables") && req.Method == http.MethodGet {
				return bigQueryPrepareResponse(req, http.StatusOK, `{"datasetReference":{"projectId":"demodb","datasetId":"research"},"location":"US"}`), nil
			}
			if req.Method == http.MethodGet && strings.Contains(req.URL.Path, "tables") {
				return bigQueryPrepareResponse(req, http.StatusNotFound, `{"error":{"code":404}}`), nil
			}
			return nil, errors.New("create table fail")
		},
	}
	writer, err = bqwriter.NewLoadWriter(ctx, bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "research", Location: "US",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)
	plan, err = (BigQueryCopySink{Writer: writer}).Preflight(ctx, []CopyTable{table})
	require.NoError(t, err)
	err = plan.Prepare(ctx)
	require.ErrorContains(t, err, "create table \"items\": BigQuery create destination table failed")

	// LoadTable call
	transport = mockTransport{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("load job failed")
		},
	}
	writer, err = bqwriter.NewLoadWriter(ctx, bqwriter.LoadConfig{
		ProjectID: "demodb", DatasetID: "research", Location: "US",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)
	plan, err = (BigQueryCopySink{Writer: writer}).Preflight(ctx, []CopyTable{table})
	require.NoError(t, err)
	_, _, err = plan.LoadTable(ctx, CopyTable{Ref: dal.NewRootCollectionRef("absent", "")}, strings.NewReader(""))
	require.ErrorContains(t, err, "source table is absent from transfer plan")
	_, _, err = plan.LoadTable(ctx, table, strings.NewReader("{}\n"))
	require.ErrorContains(t, err, "BigQuery table was not created by this load writer")
}

type mockBaseSchemaReader struct{}

func (mockBaseSchemaReader) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return nil, nil
}
func (mockBaseSchemaReader) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return nil, nil
}
func (mockBaseSchemaReader) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}
func (mockBaseSchemaReader) ListConstraints(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, nil
}
func (mockBaseSchemaReader) ListReferrers(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, nil
}

type mockListCollectionsFailDB struct {
	dal.DB
	mockBaseSchemaReader
}

func (m mockListCollectionsFailDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockListCollectionsFailDB) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return nil, errors.New("list failed")
}

type mockEmptyCollectionsDB struct {
	dal.DB
	mockBaseSchemaReader
}

func (m mockEmptyCollectionsDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockEmptyCollectionsDB) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{}, nil
}

type mockInconsistentSchemaDB struct {
	dal.DB
	mockBaseSchemaReader
}

func (m mockInconsistentSchemaDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockInconsistentSchemaDB) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef("tbl", "")}, nil
}

func (m mockInconsistentSchemaDB) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return &dbschema.CollectionDef{Name: "different_name"}, nil
}

type mockNonMapRecordDB struct {
	dal.DB
}

func (m mockNonMapRecordDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockNonMapRecordDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	k := record.NewKeyWithID("t", "1")
	rec := record.NewRecordWithData(k, "string_not_map")
	return &singleRecordReader{rec: rec}, nil
}

type mockNextFailDB struct {
	dal.DB
}

func (m mockNextFailDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockNextFailDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	return mockRecordsReader{err: errors.New("next fail")}, nil
}

type mockSourceRowsFailDB struct {
	dal.DB
	openErr error
	cursor  dbschema.SourceRowCursor
}

func (m mockSourceRowsFailDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockSourceRowsFailDB) OpenSourceRows(ctx context.Context, ref *dal.CollectionRef) (dbschema.SourceRowCursor, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	return m.cursor, nil
}

type mockCursor struct {
	rows  []dbschema.SourceRow
	index int
	err   error
}

func (m *mockCursor) Next() (dbschema.SourceRow, error) {
	if m.err != nil {
		return dbschema.SourceRow{}, m.err
	}
	if m.index >= len(m.rows) {
		return dbschema.SourceRow{}, io.EOF
	}
	r := m.rows[m.index]
	m.index++
	return r, nil
}

func (m *mockCursor) Close() error { return nil }

type mockCursorWithCloseErr struct {
	mockCursor
	closeErr error
}

func (m *mockCursorWithCloseErr) Close() error {
	return m.closeErr
}

// Not parallel: mutates package seams.
func TestTransfer_Coverage100(t *testing.T) {
	ctx := context.Background()

	// 1. CopyToSink source == nil || sink == nil
	_, err := CopyToSink(ctx, nil, mockSink{}, CopyOpts{})
	require.ErrorContains(t, err, "copy source and target are required")
	_, err = CopyToSink(ctx, mockSingleRecordDB{}, nil, CopyOpts{})
	require.ErrorContains(t, err, "copy source and target are required")

	// 2. ListCollections error
	_, err = CopyToSink(ctx, mockListCollectionsFailDB{}, mockSink{}, CopyOpts{})
	require.ErrorContains(t, err, "list source collections: list failed")

	// 3. Empty collections
	_, err = CopyToSink(ctx, mockEmptyCollectionsDB{}, mockSink{}, CopyOpts{})
	require.ErrorIs(t, err, ErrSourceHasNoTables)

	// 4. ApplyTableFilter error
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	makeSQLiteFile(t, dbPath, "CREATE TABLE t (id INT)")
	ref, _ := Parse("sqlite://" + dbPath)
	db, err := ref.Open(ctx)
	require.NoError(t, err)

	_, err = CopyToSink(ctx, db, mockSink{}, CopyOpts{
		Filters: &filter.Directives{IncludeTables: []string{"nonexistent"}},
	})
	require.Error(t, err)

	// 5. Filter excludes all tables
	summary, err := CopyToSink(ctx, db, mockSink{}, CopyOpts{
		Filters: &filter.Directives{ExcludeTables: []string{"t"}},
	})
	require.NoError(t, err)
	require.Equal(t, 0, summary.Tables)

	// 6. DescribeCollection error
	_, err = CopyToSink(ctx, mockDescribeFailDB{}, mockSink{}, CopyOpts{})
	require.ErrorContains(t, err, "describe source collection \"bad_tbl\": cannot describe")

	// 7. Inconsistent schema
	_, err = CopyToSink(ctx, mockInconsistentSchemaDB{}, mockSink{}, CopyOpts{})
	require.ErrorContains(t, err, "returned inconsistent schema")

	// 8. Preflight error
	_, err = CopyToSink(ctx, db, mockSink{err: errors.New("preflight failed")}, CopyOpts{})
	require.ErrorContains(t, err, "preflight target: preflight failed")

	// 9. Plan == nil
	_, err = CopyToSink(ctx, db, mockSink{plan: nil}, CopyOpts{})
	require.ErrorContains(t, err, "target returned an empty transfer plan")

	// 10. copyMkdirTemp error
	origMkdirTemp := copyMkdirTemp
	defer func() { copyMkdirTemp = origMkdirTemp }()
	copyMkdirTemp = func(dir, pattern string) (string, error) { return "", errors.New("mkdir fail") }
	_, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{}}, CopyOpts{})
	require.ErrorContains(t, err, "create transfer staging directory: mkdir fail")
	copyMkdirTemp = origMkdirTemp

	// 11. copyOpenFile error
	origOpenFile := copyOpenFile
	defer func() { copyOpenFile = origOpenFile }()
	copyOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, errors.New("open file fail")
	}
	_, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{}}, CopyOpts{})
	require.ErrorContains(t, err, "stage source collection \"t\": open file fail")
	copyOpenFile = origOpenFile

	// 12. SchemaOnly branch
	summary, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{targetName: "t_target"}}, CopyOpts{SchemaOnly: true})
	require.NoError(t, err)
	require.Equal(t, 1, summary.Created)
	require.Equal(t, []string{"t_target"}, summary.CreatedNames)

	// 13. Prepare error
	_, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{prepErr: errors.New("prep fail")}}, CopyOpts{SchemaOnly: true})
	require.ErrorContains(t, err, "prepare target: prep fail")

	// 14. copyOpen error
	origOpen := copyOpen
	defer func() { copyOpen = origOpen }()
	copyOpen = func(name string) (*os.File, error) { return nil, errors.New("copy open fail") }
	_, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{}}, CopyOpts{})
	require.ErrorContains(t, err, "open staged collection \"t\": copy open fail")
	copyOpen = origOpen

	// 15. LoadTable error
	_, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{loadErr: errors.New("load fail")}}, CopyOpts{})
	require.ErrorContains(t, err, "load target collection \"t\": load fail")

	// 16. Row count mismatch error
	_, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{loadRows: 99}}, CopyOpts{})
	require.ErrorContains(t, err, "target row count for \"t\" differs from staged source (99 != 0)")

	// 17. removeCopyStagingDir error -> ErrStagingCleanup
	origRemove := removeCopyStagingDir
	defer func() { removeCopyStagingDir = origRemove }()
	removeCopyStagingDir = func(path string) error { return errors.New("remove fail") }
	_, err = CopyToSink(ctx, db, mockSink{plan: mockSinkPlan{targetName: "t"}}, CopyOpts{SchemaOnly: true})
	require.ErrorIs(t, err, ErrStagingCleanup)
	removeCopyStagingDir = origRemove

	// stageCopyRows direct branches
	cTable := CopyTable{
		Ref: dal.NewRootCollectionRef("t", ""),
		Definition: &dbschema.CollectionDef{
			Name:   "t",
			Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
		},
	}
	tmpDir2 := t.TempDir()
	dbPath2 := filepath.Join(tmpDir2, "rows.db")
	makeSQLiteFile(t, dbPath2, "CREATE TABLE t (id INT); INSERT INTO t VALUES (1);")
	ref2, _ := Parse("sqlite://" + dbPath2)
	dbWithRows, err := ref2.Open(ctx)
	require.NoError(t, err)

	// newline missing
	_, err = stageCopyRows(ctx, dbWithRows, cTable, mockSinkPlan{encodeLine: []byte("no_newline")}, CopyOpts{}, io.Discard)
	require.ErrorContains(t, err, "target row encoder must return one newline-terminated JSON record")

	// writer error
	_, err = stageCopyRows(ctx, dbWithRows, cTable, mockSinkPlan{}, CopyOpts{}, failingWriter{})
	require.Error(t, err)

	// OpenSourceRows error
	rowsFailDB := mockSourceRowsFailDB{DB: db, openErr: errors.New("open rows fail")}
	_, err = stageCopyRows(ctx, rowsFailDB, cTable, mockSinkPlan{}, CopyOpts{}, io.Discard)
	require.ErrorContains(t, err, "open physical rows for \"t\": open rows fail")

	// cursor.Next error
	cursorNextErr := mockSourceRowsFailDB{DB: db, cursor: &mockCursor{err: errors.New("next fail")}}
	_, err = stageCopyRows(ctx, cursorNextErr, cTable, mockSinkPlan{}, CopyOpts{}, io.Discard)
	require.ErrorContains(t, err, "read physical row from \"t\": next fail")

	// cursor.Close error
	cursorCloseErr := mockSourceRowsFailDB{DB: db, cursor: &mockCursorWithCloseErr{closeErr: errors.New("cursor close fail")}}
	_, err = stageCopyRows(ctx, cursorCloseErr, cTable, mockSinkPlan{}, CopyOpts{}, io.Discard)
	require.ErrorContains(t, err, "close physical rows for \"t\": cursor close fail")

	// Query fallback branches (with HasRowFilters = true)
	// CompileWhereForTable error
	optsBadFilter := CopyOpts{
		Filters: &filter.Directives{
			Where: map[string]*filter.PredicateGroup{
				"t": {
					Operator: filter.And,
					Conditions: []filter.Predicate{
						{Field: "missing_col", Operator: filter.OpEqual, Value: "1"},
					},
				},
			},
		},
	}
	_, err = stageCopyRows(ctx, db, cTable, mockSinkPlan{}, optsBadFilter, io.Discard)
	require.ErrorContains(t, err, "compile where for \"t\"")

	// Non-map record data
	optsFilter := CopyOpts{Filters: &filter.Directives{LimitsByTable: map[string]int{"t": 10}}}
	_, err = stageCopyRows(ctx, mockNonMapRecordDB{DB: db}, cTable, mockSinkPlan{}, optsFilter, io.Discard)
	require.ErrorContains(t, err, "returned record data string, expected map[string]any")

	// reader.Next error
	_, err = stageCopyRows(ctx, mockNextFailDB{DB: db}, cTable, mockSinkPlan{}, optsFilter, io.Discard)
	require.ErrorContains(t, err, "read source collection \"t\": next fail")

	// normalizeSQLiteBooleanValues branches
	require.NoError(t, normalizeSQLiteBooleanValues(CopyTable{}, dbschema.SourceRow{}))
	require.NoError(t, normalizeSQLiteBooleanValues(CopyTable{Definition: &dbschema.CollectionDef{}}, dbschema.SourceRow{}))
	require.NoError(t, normalizeSQLiteBooleanValues(CopyTable{Definition: &dbschema.CollectionDef{
		SourceDefinition: &dbschema.SourceDefinition{Dialect: "postgres"},
	}}, dbschema.SourceRow{}))
	boolTable := CopyTable{Definition: &dbschema.CollectionDef{
		SourceDefinition: &dbschema.SourceDefinition{
			Dialect: "sqlite",
			Columns: []dbschema.SourceColumnDef{
				{Name: "not_bool", DeclaredType: "int"},
				{Name: "is_bool", DeclaredType: "bool"},
			},
		},
		Fields: []dbschema.FieldDef{
			{Name: "not_bool", Type: dbschema.Int},
			{Name: "is_bool", Type: dbschema.String}, // portable type String != Bool
		},
	}}
	err = normalizeSQLiteBooleanValues(boolTable, dbschema.SourceRow{Values: map[string]any{"not_bool": 1}})
	require.ErrorContains(t, err, "declares bool but its portable type is string")

	boolTable.Definition.Fields[1].Type = dbschema.Bool
	// missing from Values
	err = normalizeSQLiteBooleanValues(boolTable, dbschema.SourceRow{Values: map[string]any{"not_bool": 1}})
	require.ErrorContains(t, err, "is missing from the source row")

	// value == nil
	row := dbschema.SourceRow{Values: map[string]any{"is_bool": nil}}
	require.NoError(t, normalizeSQLiteBooleanValues(boolTable, row))
	require.Nil(t, row.Values["is_bool"])

	// sqliteBooleanValue error
	row = dbschema.SourceRow{Values: map[string]any{"is_bool": "invalid"}}
	err = normalizeSQLiteBooleanValues(boolTable, row)
	require.ErrorContains(t, err, "value has unsupported representation")

	// valid int64 1 -> true
	row = dbschema.SourceRow{Values: map[string]any{"is_bool": int64(1)}}
	require.NoError(t, normalizeSQLiteBooleanValues(boolTable, row))
	require.Equal(t, true, row.Values["is_bool"])
}

type mockViewReaderDB struct {
	dal.DB
	dbschema.SchemaReader
	views []dbschema.SourceViewDef
	err   error
}

func (m mockViewReaderDB) Adapter() dal.Adapter { return mockAdapter{name: "dalgo2sqlite"} }

func (m mockViewReaderDB) ListSourceViews(ctx context.Context) ([]dbschema.SourceViewDef, error) {
	return m.views, m.err
}

// Not parallel: mutates package seams.
func TestExportInGitDB_Coverage100(t *testing.T) {
	// validateExportRelationships
	err := validateExportRelationships([]exportTable{
		{def: dbschema.CollectionDef{
			Name: "t1",
			ForeignKeys: []dbschema.ForeignKeyDef{{
				Name:                "fk_ns",
				ReferencedNamespace: "other_ns",
			}},
		}},
	})
	require.ErrorContains(t, err, "targets unsupported namespace")

	err = validateExportRelationships([]exportTable{
		{def: dbschema.CollectionDef{
			Name: "t1",
			ForeignKeys: []dbschema.ForeignKeyDef{{
				Name:                 "fk_missing",
				ReferencedCollection: "missing_tbl",
			}},
		}},
	})
	require.ErrorContains(t, err, "targets missing collection")

	err = validateExportRelationships([]exportTable{
		{def: dbschema.CollectionDef{
			Name: "t1",
			ForeignKeys: []dbschema.ForeignKeyDef{{
				Name:                 "fk_no_pk",
				ReferencedCollection: "t2",
			}},
		}},
		{def: dbschema.CollectionDef{Name: "t2"}},
	})
	require.ErrorContains(t, err, "has no resolvable target key")

	err = validateExportRelationships([]exportTable{
		{def: dbschema.CollectionDef{
			Name: "t1",
			ForeignKeys: []dbschema.ForeignKeyDef{{
				Name:                 "fk_mismatch",
				ReferencedCollection: "t2",
				ReferencedFields:     []dal.FieldName{"id1", "id2"},
				Fields:               []dal.FieldName{"col1"},
			}},
		}},
		{def: dbschema.CollectionDef{
			Name:       "t2",
			PrimaryKey: []dal.FieldName{"id1", "id2"},
			Fields:     []dbschema.FieldDef{{Name: "id1"}, {Name: "id2"}},
		}},
	})
	require.ErrorContains(t, err, "has mismatched target field tuple")

	err = validateExportRelationships([]exportTable{
		{def: dbschema.CollectionDef{
			Name: "t1",
			ForeignKeys: []dbschema.ForeignKeyDef{{
				Name:                 "fk_missing_col",
				ReferencedCollection: "t2",
				ReferencedFields:     []dal.FieldName{"missing_field"},
				Fields:               []dal.FieldName{"col1"},
			}},
		}},
		{def: dbschema.CollectionDef{
			Name:   "t2",
			Fields: []dbschema.FieldDef{{Name: "id1"}},
		}},
	})
	require.ErrorContains(t, err, "targets missing field \"missing_field\"")

	// nativeCollectionID
	_, err = nativeCollectionID("")
	require.ErrorContains(t, err, "unsafe collection name")
	_, err = nativeCollectionID("a\x00b")
	require.ErrorContains(t, err, "unsafe collection name")
	_, err = nativeCollectionID("a\x01b")
	require.ErrorContains(t, err, "contains a control character")

	id, err := nativeCollectionID("dt_abc")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(id, "dt_"))

	id, err = nativeCollectionID("CON")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(id, "dt_"))

	_, err = nativeCollectionID(strings.Repeat("a", 300))
	require.ErrorContains(t, err, "exceeds 255 bytes")

	// safeExportName
	require.Error(t, safeExportName("a/b"))
	require.Error(t, safeExportName("a\x01b"))
	require.NoError(t, safeExportName("valid_name"))

	// safeSourceFieldName
	require.Error(t, safeSourceFieldName(""))
	require.Error(t, safeSourceFieldName("a\x01b"))
	require.NoError(t, safeSourceFieldName("valid_field"))

	// safeINGRHeaderName
	require.Error(t, safeINGRHeaderName(" leading"))
	require.Error(t, safeINGRHeaderName("a,b"))
	require.NoError(t, safeINGRHeaderName("valid_header"))

	// newExportIDIndex seams
	stage := t.TempDir()
	origCreateTemp := exportCreateTemp
	defer func() { exportCreateTemp = origCreateTemp }()
	exportCreateTemp = func(dir, pattern string) (*os.File, error) { return nil, errors.New("create temp fail") }
	_, err = newExportIDIndex(stage)
	require.ErrorContains(t, err, "create temp fail")
	exportCreateTemp = origCreateTemp

	origFileClose := exportFileClose
	defer func() { exportFileClose = origFileClose }()
	exportFileClose = func(f *os.File) error { return errors.New("file close fail") }
	_, err = newExportIDIndex(stage)
	require.ErrorContains(t, err, "file close fail")
	exportFileClose = origFileClose

	origSQLOpen := exportSQLOpen
	defer func() { exportSQLOpen = origSQLOpen }()
	exportSQLOpen = func(driver, dsn string) (*sql.DB, error) { return nil, errors.New("sql open fail") }
	_, err = newExportIDIndex(stage)
	require.ErrorContains(t, err, "sql open fail")
	exportSQLOpen = origSQLOpen

	origDBExec := exportDBExec
	defer func() { exportDBExec = origDBExec }()
	exportDBExec = func(db *sql.DB, query string) (sql.Result, error) { return nil, errors.New("db exec fail") }
	_, err = newExportIDIndex(stage)
	require.ErrorContains(t, err, "db exec fail")
	exportDBExec = origDBExec

	origDBBegin := exportDBBegin
	defer func() { exportDBBegin = origDBBegin }()
	exportDBBegin = func(db *sql.DB) (*sql.Tx, error) { return nil, errors.New("db begin fail") }
	_, err = newExportIDIndex(stage)
	require.ErrorContains(t, err, "db begin fail")
	exportDBBegin = origDBBegin

	origTxPrepare := exportTxPrepare
	defer func() { exportTxPrepare = origTxPrepare }()
	exportTxPrepare = func(tx *sql.Tx, query string) (*sql.Stmt, error) { return nil, errors.New("tx prepare fail") }
	_, err = newExportIDIndex(stage)
	require.ErrorContains(t, err, "tx prepare fail")
	exportTxPrepare = origTxPrepare

	// successful index operations
	idx, err := newExportIDIndex(stage)
	require.NoError(t, err)
	require.NoError(t, idx.Add("id1"))
	// duplicate id in SQLite primary key
	require.Error(t, idx.Add("id1"))
	idx.Close()

	// encodeExportRow branches
	defKeyed := dbschema.CollectionDef{
		Name:       "items",
		PrimaryKey: []dal.FieldName{"id"},
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}, {Name: "val", Type: dbschema.String}},
	}
	// missing field
	_, _, err = encodeExportRow(defKeyed, map[string]any{"id": 1}, 1)
	require.ErrorContains(t, err, "missing field \"val\"")

	// encodeExportValue error
	_, _, err = encodeExportRow(defKeyed, map[string]any{"id": struct{}{}, "val": "ok"}, 1)
	require.ErrorContains(t, err, "field \"id\": integer arrived as struct")

	// extra field in data
	_, _, err = encodeExportRow(defKeyed, map[string]any{"id": 1, "val": "ok", "extra": 2}, 1)
	require.ErrorContains(t, err, "row field count 3 differs from described schema 2")

	// primary key null
	_, _, err = encodeExportRow(defKeyed, map[string]any{"id": nil, "val": "ok"}, 1)
	require.ErrorContains(t, err, "primary key field \"id\" is null")

	// exportJSONMarshal error
	origJSON := exportJSONMarshal
	defer func() { exportJSONMarshal = origJSON }()
	exportJSONMarshal = func(v any) ([]byte, error) { return nil, errors.New("json fail") }
	_, _, err = encodeExportRow(defKeyed, map[string]any{"id": 1, "val": "ok"}, 1)
	require.ErrorContains(t, err, "json fail")
	exportJSONMarshal = origJSON

	// keyless table returns ordinal
	defKeyless := dbschema.CollectionDef{
		Name:   "keyless",
		Fields: []dbschema.FieldDef{{Name: "val", Type: dbschema.String}},
	}
	_, idRow, err := encodeExportRow(defKeyless, map[string]any{"val": "ok"}, 42)
	require.NoError(t, err)
	require.Equal(t, "row-000000000042", idRow)

	// keyed table returns pk-
	_, idRow, err = encodeExportRow(defKeyed, map[string]any{"id": 1, "val": "ok"}, 1)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(idRow, "pk-"))

	// encodeExportValue branches
	// Decimal
	v, err := encodeExportValue(dbschema.FieldDef{Type: dbschema.Decimal}, json.Number("12.34"))
	require.NoError(t, err)
	require.Equal(t, "12.34", v)
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Decimal}, json.Number("not_num"))
	require.ErrorContains(t, err, "is not numeric")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Decimal}, 123)
	require.ErrorContains(t, err, "exact text required")

	// Bytes
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bytes}, []byte{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, "AQID", v)
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bytes}, "not_bytes")
	require.ErrorContains(t, err, "bytes arrived as string")

	// String
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.String}, []byte("hello"))
	require.NoError(t, err)
	require.Equal(t, "hello", v)
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.String}, []byte{0xff})
	require.ErrorContains(t, err, "invalid UTF-8 string")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.String}, string([]byte{0xff}))
	require.ErrorContains(t, err, "invalid UTF-8 string")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.String}, 123)
	require.ErrorContains(t, err, "string arrived as int")

	// Time
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Time}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Contains(t, v, "2026-01-01")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Time}, string([]byte{0xff}))
	require.ErrorContains(t, err, "invalid UTF-8 time text")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Time}, 123)
	require.ErrorContains(t, err, "time arrived as int")

	// Int
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, int8(1))
	require.NoError(t, err)
	require.Equal(t, int64(1), v)
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, int16(2))
	require.NoError(t, err)
	require.Equal(t, int64(2), v)
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, int32(3))
	require.NoError(t, err)
	require.Equal(t, int64(3), v)
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, uint(4))
	require.NoError(t, err)
	require.Equal(t, int64(4), v)
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, uint64(math.MaxUint64))
	require.ErrorContains(t, err, "exceeds int64 range")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, json.Number("bad_int"))
	require.ErrorContains(t, err, "is invalid or out of range")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, math.NaN())
	require.ErrorContains(t, err, "cannot be represented as exact integer")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Int}, struct{}{})
	require.ErrorContains(t, err, "integer arrived as struct")

	// Bool
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, int(0))
	require.NoError(t, err)
	require.Equal(t, false, v)
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, int(1))
	require.NoError(t, err)
	require.Equal(t, true, v)
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, int(2))
	require.ErrorContains(t, err, "expected bool or 0/1")
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, int64(0))
	require.NoError(t, err)
	require.Equal(t, false, v)
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, int64(1))
	require.NoError(t, err)
	require.Equal(t, true, v)
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, int64(2))
	require.ErrorContains(t, err, "expected bool or 0/1")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Bool}, "string_bool")
	require.ErrorContains(t, err, "boolean arrived as string")

	// Float
	v, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Float}, float32(1.5))
	require.NoError(t, err)
	require.Equal(t, float64(float32(1.5)), v)
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Float}, float32(math.NaN()))
	require.ErrorContains(t, err, "non-finite float")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Float}, math.Inf(1))
	require.ErrorContains(t, err, "non-finite float")
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Float}, "not_float")
	require.ErrorContains(t, err, "float arrived as string")

	// default type
	_, err = encodeExportValue(dbschema.FieldDef{Type: dbschema.Type(120)}, "val")
	require.ErrorContains(t, err, "unsupported source type")

	// checkPhysicalClasses branches
	require.NoError(t, checkPhysicalClasses(nil, nil, nil))
	fieldsDec := []dbschema.FieldDef{
		{Name: "dec", Type: dbschema.Decimal},
		{Name: "flt", Type: dbschema.Float},
		{Name: "unknown", Type: dbschema.Type(120)},
	}
	err = checkPhysicalClasses(fieldsDec, map[string]any{"dec": "1.23"}, map[string]string{"dec": "invalid"})
	require.ErrorContains(t, err, "has invalid physical storage class \"invalid\"")

	err = checkPhysicalClasses(fieldsDec, map[string]any{"dec": "1.23"}, map[string]string{"dec": ""})
	require.ErrorContains(t, err, "is missing a physical storage class for a non-null decimal")

	err = checkPhysicalClasses(fieldsDec, map[string]any{"dec": nil}, map[string]string{"dec": "integer"})
	require.ErrorContains(t, err, "has physical storage class \"integer\" for null decimal")

	err = checkPhysicalClasses(fieldsDec, map[string]any{"dec": "1.23"}, map[string]string{"dec": "blob"})
	require.ErrorContains(t, err, "has blob decimal value that exact string transport cannot preserve")

	// incompatible physical storage class
	err = checkPhysicalClasses(fieldsDec, map[string]any{"flt": 1.23}, map[string]string{"flt": "integer"})
	require.ErrorContains(t, err, "incompatible with logical float transport")

	// valid physical float
	require.NoError(t, checkPhysicalClasses(fieldsDec, map[string]any{"flt": 1.23}, map[string]string{"flt": "real"}))

	// unsupported logical type in physical check
	err = checkPhysicalClasses(fieldsDec, map[string]any{"unknown": "val"}, map[string]string{"unknown": "text"})
	require.ErrorContains(t, err, "has unsupported logical type")

	// storageClassAvailability
	avail := storageClassAvailability{}
	require.NoError(t, avail.Check([]dbschema.FieldDef{{Type: dbschema.Int}}, nil))
	require.NoError(t, avail.Check([]dbschema.FieldDef{{Type: dbschema.Decimal}}, map[string]string{}))
	err = avail.Check([]dbschema.FieldDef{{Type: dbschema.Decimal}}, nil)
	require.ErrorContains(t, err, "physical storage-class metadata availability changed")

	// exportCollection: foreign key targets missing collection in sourceIDs
	defFK := dbschema.CollectionDef{
		Name: "items",
		ForeignKeys: []dbschema.ForeignKeyDef{{
			Name:                 "fk1",
			ReferencedCollection: "missing_target",
			Fields:               []dal.FieldName{"id"},
			ReferencedFields:     []dal.FieldName{"id"},
		}},
		Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}},
	}
	_, err = exportCollection(context.Background(), nil, stage, dal.NewRootCollectionRef("items", ""), defFK, "items", map[string]string{}, true, ingitdb.RecordFormatJSON)
	require.ErrorContains(t, err, "source foreign key targets missing collection")

	// exportCollection: dalgo2ingitdb.ExportCollectionDefinition error
	defBadType := dbschema.CollectionDef{
		Name:   "bad",
		Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Type(120)}},
	}
	_, err = exportCollection(context.Background(), nil, stage, dal.NewRootCollectionRef("bad", ""), defBadType, "bad", map[string]string{}, true, ingitdb.RecordFormatJSON)
	require.Error(t, err)

	// ExportInGitDB options > 1
	_, err = ExportInGitDB(context.Background(), nil, "/tmp/dest", ExportOptions{}, ExportOptions{})
	require.ErrorContains(t, err, "only one export options value is supported")

	// ExportInGitDB viewReader error & safeExportName error
	dbTmp := t.TempDir()
	dbFilePath := filepath.Join(dbTmp, "views.db")
	makeSQLiteFile(t, dbFilePath, "CREATE TABLE t (id INT)")
	ref, _ := Parse("sqlite://" + dbFilePath)
	rawDB, err := ref.Open(context.Background())
	require.NoError(t, err)

	viewFailDB := mockViewReaderDB{DB: rawDB, SchemaReader: rawDB.(dbschema.SchemaReader), err: errors.New("list views fail")}
	destDir := filepath.Join(t.TempDir(), "export_dest")
	_, err = ExportInGitDB(context.Background(), viewFailDB, destDir)
	require.ErrorContains(t, err, "list source views: list views fail")

	viewUnsafeDB := mockViewReaderDB{DB: rawDB, SchemaReader: rawDB.(dbschema.SchemaReader), views: []dbschema.SourceViewDef{{Name: "unsafe/view"}}}
	_, err = ExportInGitDB(context.Background(), viewUnsafeDB, destDir)
	require.ErrorContains(t, err, "unsafe collection name")

	// with valid views
	viewOKDB := mockViewReaderDB{DB: rawDB, SchemaReader: rawDB.(dbschema.SchemaReader), views: []dbschema.SourceViewDef{{Name: "good_view", CreateSQL: "SELECT 1"}}}
	counts, err := ExportInGitDB(context.Background(), viewOKDB, destDir)
	require.NoError(t, err)
	require.Equal(t, int64(0), counts["t"])

	// exportJSONMarshalIndent error in ExportInGitDB
	origIndent := exportJSONMarshalIndent
	defer func() { exportJSONMarshalIndent = origIndent }()
	exportJSONMarshalIndent = func(v any, prefix, indent string) ([]byte, error) { return nil, errors.New("indent fail") }
	destDir2 := filepath.Join(t.TempDir(), "export_dest2")
	_, err = ExportInGitDB(context.Background(), rawDB, destDir2)
	require.ErrorContains(t, err, "indent fail")
	exportJSONMarshalIndent = origIndent

	// exportYAMLMarshal error on registry
	origYAML := exportYAMLMarshal
	defer func() { exportYAMLMarshal = origYAML }()
	callNum := 0
	exportYAMLMarshal = func(v any) ([]byte, error) {
		callNum++
		return nil, errors.New("yaml fail")
	}
	destDir3 := filepath.Join(t.TempDir(), "export_dest3")
	_, err = ExportInGitDB(context.Background(), rawDB, destDir3)
	require.ErrorContains(t, err, "yaml fail")
	exportYAMLMarshal = origYAML

	// Context canceled
	ctxCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	destDir4 := filepath.Join(t.TempDir(), "export_dest4")
	_, err = ExportInGitDB(ctxCanceled, rawDB, destDir4)
	require.ErrorIs(t, err, context.Canceled)
}

// Not parallel: mutates package seams.
func TestExportRecordsWriter_Coverage100(t *testing.T) {
	origBuffered := exportRecordsWriterNewBuffered
	defer func() { exportRecordsWriterNewBuffered = origBuffered }()

	// Hook to return a buffer that is full and writes to failingWriter
	exportRecordsWriterNewBuffered = func(out io.Writer) *bufio.Writer {
		b := bufio.NewWriterSize(out, 16)
		_, _ = b.Write([]byte("1234567890123456"))
		return b
	}

	// newExportRecordsWriter write errors
	_, err := newExportRecordsWriter(failingWriter{}, ingitdb.RecordFormatJSON, "c", nil)
	require.Error(t, err)
	_, err = newExportRecordsWriter(failingWriter{}, ingitdb.RecordFormatINGR, "c", nil)
	require.Error(t, err)
	// For CSV, write header with large field name > 4096 bytes so csv.Writer flushes
	largeFields := []dbschema.FieldDef{{Name: dal.FieldName(strings.Repeat("x", 5000)), Type: dbschema.String}}
	_, err = newExportRecordsWriter(failingWriter{}, ingitdb.RecordFormatCSV, "c", largeFields)
	require.Error(t, err)

	// Reset seam for standard writes
	exportRecordsWriterNewBuffered = origBuffered

	_, err = newExportRecordsWriter(nil, ingitdb.RecordFormat("unsupported"), "c", nil)
	require.ErrorContains(t, err, "unsupported records format")

	// Write json.Marshal errors (math.NaN())
	wJSON, err := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSON, "c", nil)
	require.NoError(t, err)
	require.Error(t, wJSON.Write("id", map[string]any{"v": math.NaN()}))

	wJSONL, err := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSONL, "c", nil)
	require.NoError(t, err)
	require.Error(t, wJSONL.Write("id", map[string]any{"v": math.NaN()}))

	fields := []dbschema.FieldDef{{Name: "f", Type: dbschema.Float}}
	wCSV, err := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatCSV, "c", fields)
	require.NoError(t, err)
	require.Error(t, wCSV.Write("id", map[string]any{"f": math.NaN()}))

	// JSON count > 0 (WriteString(",\n") fails)
	wFailJSON1, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSON, "c", nil)
	wFailJSON1.buffered = bufio.NewWriterSize(failingWriter{}, 16)
	_, _ = wFailJSON1.buffered.Write([]byte("1234567890123456"))
	wFailJSON1.count = 1
	require.Error(t, wFailJSON1.Write("id", map[string]any{"v": 1}))

	// JSON count == 0 (Fprintf fails)
	wFailJSON0, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSON, "c", nil)
	wFailJSON0.buffered = bufio.NewWriterSize(failingWriter{}, 16)
	_, _ = wFailJSON0.buffered.Write([]byte("1234567890123456"))
	wFailJSON0.count = 0
	require.Error(t, wFailJSON0.Write("id", map[string]any{"v": 1}))

	// YAML Fprintf fails
	wFailYAML, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatYAML, "c", nil)
	wFailYAML.buffered = bufio.NewWriterSize(failingWriter{}, 16)
	_, _ = wFailYAML.buffered.Write([]byte("1234567890123456"))
	require.Error(t, wFailYAML.Write("id", map[string]any{"v": 1}))

	// JSONL Write fails
	wFailJSONL, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSONL, "c", nil)
	wFailJSONL.buffered = bufio.NewWriterSize(failingWriter{}, 16)
	_, _ = wFailJSONL.buffered.Write([]byte("1234567890123456"))
	require.Error(t, wFailJSONL.Write("id", map[string]any{"v": 1}))

	// INGR WriteRecords fails
	wFailINGR, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatINGR, "c", nil)
	wFailINGR.ingr = mockINGRWriter{writeErr: errors.New("ingr write fail")}
	require.Error(t, wFailINGR.Write("id", map[string]any{"v": 1}))

	// CSV Write fails with large row value
	wFailCSV, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatCSV, "c", []dbschema.FieldDef{{Name: "f", Type: dbschema.String}})
	wFailCSV.csv = csv.NewWriter(failingWriter{})
	require.Error(t, wFailCSV.Write("id", map[string]any{"f": strings.Repeat("x", 5000)}))

	// Default unsupported format in Write
	wFailFormat, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSON, "c", nil)
	wFailFormat.format = ingitdb.RecordFormat("unsupported")
	require.ErrorContains(t, wFailFormat.Write("id", map[string]any{"v": 1}), "unsupported records format")

	// Close errors
	// JSON Close
	wCloseJSON, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSON, "c", nil)
	wCloseJSON.buffered = bufio.NewWriterSize(failingWriter{}, 16)
	_, _ = wCloseJSON.buffered.Write([]byte("1234567890123456"))
	require.Error(t, wCloseJSON.Close())

	// INGR Close
	wCloseINGR, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatINGR, "c", nil)
	wCloseINGR.ingr = mockINGRWriter{closeErr: errors.New("ingr close fail")}
	require.Error(t, wCloseINGR.Close())

	// CSV Close
	wCloseCSV, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatCSV, "c", nil)
	wCloseCSV.csv = csv.NewWriter(failingWriter{})
	_ = wCloseCSV.csv.Write([]string{"item"})
	require.Error(t, wCloseCSV.Close())

	// Flush error on JSONL Close
	wCloseJSONL, _ := newExportRecordsWriter(io.Discard, ingitdb.RecordFormatJSONL, "c", nil)
	wCloseJSONL.buffered = bufio.NewWriterSize(failingWriter{}, 16)
	_, _ = wCloseJSONL.buffered.Write([]byte("1234567890123456"))
	require.Error(t, wCloseJSONL.Close())
}

// Not parallel: mutates package seams.
func TestExportStorageClasses_Coverage100(t *testing.T) {
	dir := t.TempDir()
	w := newStorageClassWriter(dir)

	// len(classes) == 0
	require.NoError(t, w.Append("1", nil, nil))

	// no decimal fields
	fields := []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}
	require.NoError(t, w.Append("1", fields, map[string]string{"id": "integer"}))

	// storageJSONMarshal error
	origMarshal := storageJSONMarshal
	defer func() { storageJSONMarshal = origMarshal }()
	storageJSONMarshal = func(v any) ([]byte, error) { return nil, errors.New("storage json fail") }
	fieldsDec := []dbschema.FieldDef{{Name: "dec", Type: dbschema.Decimal}}
	err := w.Append("1", fieldsDec, map[string]string{"dec": "text"})
	require.ErrorContains(t, err, "storage json fail")
	storageJSONMarshal = origMarshal

	// w.rotate error with invalid dir
	wInvalid := newStorageClassWriter("/nonexistent/invalid/dir")
	err = wInvalid.Append("1", fieldsDec, map[string]string{"dec": "text"})
	require.Error(t, err)

	// finishFile when file == nil
	require.NoError(t, w.finishFile())

	// successful Append and Close
	require.NoError(t, w.Append("1", fieldsDec, map[string]string{"dec": "text"}))
	files, err := w.Close()
	require.NoError(t, err)
	require.Len(t, files, 1)

	// already closed Close call
	files2, err := w.Close()
	require.NoError(t, err)
	require.Equal(t, files, files2)
}

// Not parallel: mutates package seams.
func TestMiscellaneous_Coverage100(t *testing.T) {
	// typesCompatibleForReload
	require.True(t, typesCompatibleForReload(dbschema.Decimal, dbschema.Float, BackendInGitDB))

	// sharedPostgres ListSourceViews !ok branch
	origAsViewReader := postgresAsSourceViewReader
	defer func() { postgresAsSourceViewReader = origAsViewReader }()
	postgresAsSourceViewReader = func(db *dalgo2postgres.Database) (dbschema.SourceViewReader, bool) { return nil, false }
	sp := &sharedPostgres{}
	_, err := sp.ListSourceViews(context.Background())
	require.ErrorIs(t, err, dal.ErrNotSupported)
	postgresAsSourceViewReader = origAsViewReader

	// parseBigQueryDestination invalid dataset
	_, err = Parse("bigquery://my-project/123invalid?location=US")
	require.ErrorContains(t, err, "invalid BigQuery destination dataset")

	// openForTransfer from-schema non-postgres error
	_, err = BackendRef{Scheme: "sqlite", Path: "test.db"}.openForTransfer(context.Background(), "myschema")
	require.ErrorContains(t, err, "--from-schema is supported only for PostgreSQL sources")

	// openForTransfer non-postgres success
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "transfer.db")
	makeSQLiteFile(t, dbPath, "CREATE TABLE t (id INT)")
	ref, err := Parse("sqlite://" + dbPath)
	require.NoError(t, err)
	db, err := ref.openForTransfer(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, db)
}
