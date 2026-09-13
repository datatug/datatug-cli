package secureread

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

func snapshotCustomers() apicontract.Recordset {
	return apicontract.Recordset{
		Columns: []apicontract.Column{
			{Name: "id", Type: "string"}, {Name: "name", Type: "string"},
			{Name: "email", Type: "string"}, {Name: "ownerID", Type: "string"}, {Name: "country", Type: "string"},
		},
		Rows: [][]apicontract.TypedValue{
			{apicontract.NewStringValue("c1"), apicontract.NewStringValue("Ann"), apicontract.NewStringValue("ann@example.com"), apicontract.NewStringValue("alice"), apicontract.NewStringValue("IE")},
			{apicontract.NewStringValue("c2"), apicontract.NewStringValue("Ben"), apicontract.NewStringValue("ben@example.com"), apicontract.NewStringValue("bob"), apicontract.NewStringValue("GB")},
		},
	}
}

func TestRunSnapshotReappliesRowAndColumnPolicy(t *testing.T) {
	executor := NewExecutor(aliceSession(t, fieldRestrictedPolicy))
	result, err := executor.RunSnapshot(context.Background(), "customers", snapshotCustomers())
	if err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Data["ownerID"] != "alice" {
		t.Fatalf("rows = %+v, want only alice", result.Rows)
	}
	for _, column := range result.Columns {
		if column == "email" {
			t.Fatal("email column survived current policy")
		}
	}
}

func TestRunSnapshotUnexpressibleIntegerFailsClosed(t *testing.T) {
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(session)
	_, err = executor.RunSnapshot(context.Background(), "numbers", apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "value", Type: "integer"}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewIntegerValue("999999999999999999999999999999999")}},
	})
	if !errors.Is(err, ErrSnapshotPolicyUnexpressible) {
		t.Fatalf("RunSnapshot = %v, want ErrSnapshotPolicyUnexpressible", err)
	}
}

func TestRunSnapshotAppliesNumericPredicatesWithTypedSemantics(t *testing.T) {
	const numericPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: numeric-filter
default: deny
scopes:
  - path: /numeric_values
    rules:
      - id: values-over-ten
        effect: allow
        operations: [query]
        where:
          op: ">"
          left: { field: amount }
          right: { value: 10 }
`
	tests := []struct {
		name      string
		valueType string
		below     apicontract.TypedValue
		above     apicontract.TypedValue
	}{
		{name: "decimal", valueType: "decimal", below: apicontract.NewDecimalValue("2.0"), above: apicontract.NewDecimalValue("11.0")},
		{name: "integer", valueType: "integer", below: apicontract.NewIntegerValue("2"), above: apicontract.NewIntegerValue("11")},
		{name: "number", valueType: "number", below: apicontract.NewNumberValue(2), above: apicontract.NewNumberValue(11)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recordset := apicontract.Recordset{
				Columns: []apicontract.Column{{Name: "amount", Type: test.valueType}},
				Rows: [][]apicontract.TypedValue{
					{test.below},
					{test.above},
				},
			}
			result, err := NewExecutor(aliceSession(t, numericPolicy)).RunSnapshot(context.Background(), "numeric_values", recordset)
			if err != nil {
				t.Fatal(err)
			}
			want := apicontract.Recordset{Columns: recordset.Columns, Rows: [][]apicontract.TypedValue{{test.above}}}
			if result.SnapshotRecordset == nil || !reflect.DeepEqual(*result.SnapshotRecordset, want) {
				t.Fatalf("filtered = %+v, want %+v", result.SnapshotRecordset, want)
			}
		})
	}
}

func TestRunSnapshotUnrepresentableDecimalFailsClosed(t *testing.T) {
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewExecutor(session).RunSnapshot(context.Background(), "numbers", apicontract.Recordset{
		Columns: []apicontract.Column{{Name: "value", Type: "decimal"}},
		Rows:    [][]apicontract.TypedValue{{apicontract.NewDecimalValue("0.1")}},
	})
	if !errors.Is(err, ErrSnapshotPolicyUnexpressible) {
		t.Fatalf("RunSnapshot = %v, want ErrSnapshotPolicyUnexpressible", err)
	}
}

func TestRunSnapshotRevokedCollectionAccessFailsClosed(t *testing.T) {
	executor := NewExecutor(aliceSession(t, productsOnlyPolicy))
	_, err := executor.RunSnapshot(context.Background(), "customers", snapshotCustomers())
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("RunSnapshot = %v, want ErrAccessDenied", err)
	}
}

func TestRunSnapshotPreservesEveryTypedValueShape(t *testing.T) {
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	original := apicontract.Recordset{
		Columns: []apicontract.Column{
			{Name: "string", Type: "string"}, {Name: "number", Type: "number"},
			{Name: "integer", Type: "integer"}, {Name: "decimal", Type: "decimal"},
			{Name: "boolean", Type: "boolean"}, {Name: "date", Type: "date"},
			{Name: "datetime", Type: "datetime"}, {Name: "null", Type: "string"},
		},
		Rows: [][]apicontract.TypedValue{{
			apicontract.NewStringValue("seven"), apicontract.NewNumberValue(7.25),
			apicontract.NewIntegerValue("7"), apicontract.NewDecimalValue("7.25"),
			apicontract.NewBooleanValue(true), apicontract.NewDateValue("2026-09-13"),
			apicontract.NewDatetimeValue("2026-09-13T10:00:00Z"), apicontract.NewNullValue(),
		}},
	}
	result, err := NewExecutor(session).RunSnapshot(context.Background(), "typed_values", original)
	if err != nil {
		t.Fatal(err)
	}
	if result.SnapshotRecordset == nil || !reflect.DeepEqual(*result.SnapshotRecordset, original) {
		t.Fatalf("restored = %+v, want %+v", result.SnapshotRecordset, original)
	}
}
