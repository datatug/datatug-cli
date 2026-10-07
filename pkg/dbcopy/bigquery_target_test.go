package dbcopy

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/dbschema"
	bqwriter "github.com/dal-go/dalgo2bigquery"
)

func TestBigQueryValueEncodingRetainsExactWideValuesAndRejectsLoss(t *testing.T) {
	intField := bqwriter.Field{Name: "id", Type: "INT64", Mode: "REQUIRED"}
	value, err := encodeBigQueryValue(intField, dbschema.Int, int64(math.MaxInt64), "INTEGER")
	if err != nil || value != "9223372036854775807" {
		t.Fatalf("wide INT64 = %#v, %v", value, err)
	}
	if _, err := encodeBigQueryValue(intField, dbschema.Int, float64(1<<54), "REAL"); err == nil {
		t.Fatal("lossy floating representation was accepted as INT64")
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
