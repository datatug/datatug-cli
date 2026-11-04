package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const (
	athenaConditionContractSQL     = `SELECT "id", "amount" FROM "AwsDataCatalog"."native"."actual" WHERE ("id" IN (?, ?) OR ("amount" >= ? AND "amount" IS NOT NULL)) ORDER BY "id" ASC NULLS LAST OFFSET 0 ROWS LIMIT 1`
	athenaConditionContractWide    = "1234567890123456789012345678.0123456789"
	athenaConditionContractQueryID = "owned-condition-contract-1"
)

func athenaConditionContractConfig(t *testing.T) athenaNativeConfig {
	t.Helper()
	cfg := athenaOptionsTestConfig(t)
	table := cfg.Tables["items"]
	table.IDColumn = "id"
	cfg.Tables["items"] = table
	cfg.Identity.SessionTokenEnv = "ATHENA_TEST_SESSION"
	t.Setenv("ATHENA_TEST_ACCESS", athenaSessionCredential.AccessKeyID)
	t.Setenv("ATHENA_TEST_SECRET", athenaSessionCredential.SecretAccessKey)
	t.Setenv("ATHENA_TEST_SESSION", athenaSessionCredential.SessionToken)
	return cfg
}

func TestAthenaConditionContractActualQuery(t *testing.T) {
	cfg := athenaConditionContractConfig(t)
	raw := []byte(`{
		"operation":"query",
		"collection":"items",
		"projection":["id","amount"],
		"order":[{"field":"id","direction":"asc"}],
		"limit":1,
		"offset":0,
		"pageSize":2,
		"where":{"or":[
			{"documentId":true,"operator":"in","value":["a'b","other"]},
			{"and":[
				{"field":"amount","operator":">=","value":{"kind":"number","value":"` + athenaConditionContractWide + `"}},
				{"field":"amount","operator":"!=","value":null}
			]}
		]}
	}`)
	f := athenaSessionNewFixture(t, func(_ *athenaSessionFixture, w http.ResponseWriter, _ *http.Request, event athenaSessionEvent) bool {
		switch event.op {
		case "StartQueryExecution":
			if !athenaSessionStartRequestExpect(t, event.body, athenaConditionContractSQL,
				[]string{"'a''b'", "'other'", athenaConditionContractWide}) {
				w.WriteHeader(400)
				return true
			}
			athenaSessionWrite(t, w, 200, map[string]any{"QueryExecutionId": athenaConditionContractQueryID})
			return true
		case "GetQueryExecution":
			if len(event.body) != 1 || event.body["QueryExecutionId"] != athenaConditionContractQueryID {
				t.Error("condition execution request changed")
				w.WriteHeader(400)
				return true
			}
			athenaSessionWrite(t, w, 200, map[string]any{"QueryExecution": map[string]any{
				"QueryExecutionId": athenaConditionContractQueryID, "Query": athenaConditionContractSQL,
				"ExecutionParameters": []any{"'a''b'", "'other'", athenaConditionContractWide},
				"WorkGroup":           athenaSessionWorkGroup,
				"QueryExecutionContext": map[string]any{
					"Catalog": "AwsDataCatalog", "Database": "native",
				},
				"EngineVersion": map[string]any{"EffectiveEngineVersion": "Athena engine version 3"},
				"ResultConfiguration": map[string]any{
					"OutputLocation":          athenaSessionOutput + athenaConditionContractQueryID + ".csv",
					"ExpectedBucketOwner":     athenaSessionAccount,
					"EncryptionConfiguration": map[string]any{"EncryptionOption": "SSE_S3"},
				},
				"Status":     map[string]any{"State": "SUCCEEDED"},
				"Statistics": map[string]any{"DataScannedInBytes": json.Number("9007199254740993")},
			}})
			return true
		case "GetQueryResults":
			if _, hasToken := event.body["NextToken"]; len(event.body) != 3 || hasToken {
				t.Error("condition result request must have exactly three fields and no NextToken")
				w.WriteHeader(400)
				return true
			}
			if !athenaSessionResultRequestExpect(t, event.body, athenaConditionContractQueryID, 2) {
				w.WriteHeader(400)
				return true
			}
			athenaSessionWrite(t, w, 200, map[string]any{"ResultSet": map[string]any{
				"ResultSetMetadata": map[string]any{"ColumnInfo": athenaSessionColumns()},
				"Rows": []any{
					athenaSessionRow("id", athenaSessionPointer("amount")),
					athenaSessionRow("a'b", athenaSessionPointer(athenaConditionContractWide)),
				},
			}})
			return true
		}
		return false
	})
	client, closeTransport := athenaSessionClient(t, f, cfg)
	closes := 0
	closed := func() {
		closes++
		closeTransport()
	}
	request, err := admitAthenaRequest(context.Background(), cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := executeAthenaRead(context.Background(), client, closed, request)
	if err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("transport callback ran %d times, want 1", closes)
	}
	if f.count("StartQueryExecution") != 1 || f.count("GetQueryExecution") != 1 || f.count("GetQueryResults") != 1 {
		t.Fatalf("unexpected RPC counts: start=%d execution=%d results=%d",
			f.count("StartQueryExecution"), f.count("GetQueryExecution"), f.count("GetQueryResults"))
	}
	observer := &athenaExecutorOutputWriter{t: t, closes: func() int { return closes }}
	if err = finishAthenaOutput(context.Background(), observer, candidate); err != nil {
		t.Fatal(err)
	}
	if observer.writes != 1 || !bytes.Equal(observer.buffer.Bytes(), candidate) {
		t.Fatal("condition output was not written exactly once unchanged")
	}
	frames := athenaSessionFrames(t, bytes.NewBuffer(candidate))
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(frames))
	}
	frame := frames[0]
	if frame.Operation != "query" || frame.Format != "athena_string_null_v1" ||
		frame.Receipt.QueryID != athenaConditionContractQueryID || frame.HasMore {
		t.Fatal("condition frame identity, original query ID or continuation changed")
	}
	if len(frame.Columns) != 2 || frame.Columns[0].Name != "id" || frame.Columns[0].Type != "varchar" ||
		frame.Columns[1].Name != "amount" || frame.Columns[1].Type != "decimal(38,10)" {
		t.Fatal("condition frame did not retain ordered native columns")
	}
	if len(frame.Cells) != 1 || len(frame.Cells[0]) != 2 || frame.Cells[0][0] == nil ||
		*frame.Cells[0][0] != "a'b" || frame.Cells[0][1] == nil ||
		*frame.Cells[0][1] != athenaConditionContractWide {
		t.Fatal("condition frame native STRING values changed")
	}
	rendered := string(candidate)
	for _, private := range []string{"cursor", "NextToken",
		athenaSessionCredential.AccessKeyID, athenaSessionCredential.SecretAccessKey,
		athenaSessionCredential.SessionToken} {
		if strings.Contains(rendered, private) {
			t.Fatalf("condition output leaked private %q", private)
		}
	}
}

func TestAthenaConditionContractStrictRefusalMatrix(t *testing.T) {
	cfg := athenaInputKeyedConfig(t)
	refused := func(t *testing.T, document string) {
		t.Helper()
		admitted, err := admitAthenaRequest(context.Background(), cfg, []byte(document))
		if err != errAthenaInput {
			t.Fatal("invalid condition request did not return the input error")
		}
		if !reflect.DeepEqual(admitted, admittedAthenaRead{}) {
			t.Fatal("invalid condition request returned a non-zero admission")
		}
	}
	for _, testCase := range []struct {
		name  string
		where string
	}{
		{"mixed_group_and_leaf", `{"or":[{"field":"id","operator":"=","value":"a"}],"field":"id","operator":"=","value":"b"}`},
		{"empty_group", `{"and":[]}`},
		{"null_group", `{"or":null}`},
		{"bad_child_unknown_field", `{"and":[{"unknown":true}]}`},
		{"ambiguous_field_and_document_id", `{"field":"id","documentId":true,"operator":"="}`},
		{"document_id_false", `{"documentId":false,"operator":"=","value":"a"}`},
		{"operator_not_string", `{"field":"id","operator":1,"value":"a"}`},
		{"unknown_operator", `{"field":"id","operator":"like","value":"a"}`},
		{"numeric_wrong_kind", `{"field":"amount","operator":"=","value":{"kind":"currency","value":"1"}}`},
		{"numeric_value_not_string", `{"field":"amount","operator":"=","value":{"kind":"number","value":1}}`},
		{"numeric_extra_field", `{"field":"amount","operator":"=","value":{"kind":"number","value":"1","extra":true}}`},
		{"numeric_leading_zero", `{"field":"amount","operator":"=","value":{"kind":"number","value":"01"}}`},
		{"bare_array_scalar", `{"field":"id","operator":"=","value":["a"]}`},
		{"invalid_in_member", `{"field":"id","operator":"in","value":[{"nested":true}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			refused(t, `{"operation":"query","collection":"items","limit":1,"where":`+testCase.where+`}`)
		})
	}
}
