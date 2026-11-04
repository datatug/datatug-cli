package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const athenaPointExecutorSQL = `SELECT "id", "amount" FROM "AwsDataCatalog"."native"."actual" WHERE "id" = ? ORDER BY "id" ASC NULLS LAST OFFSET 0 ROWS LIMIT 2`

type athenaPointRouteCase struct {
	name      string
	operation string
	id        string
	parameter string
	queryID   string
	found     bool
}

func TestAthenaExecutorActualPointReadRoutes(t *testing.T) {
	wide := "1234567890123456789012345678.0123456789"
	for _, testCase := range []athenaPointRouteCase{
		{name: "get/found/quoted-id/wide-decimal", operation: "get", id: "a'b", parameter: "'a''b'", queryID: "owned-point-get-found", found: true},
		{name: "exists/missing", operation: "exists", id: "missing", parameter: "'missing'", queryID: "owned-point-exists-missing"},
		{name: "testConnection/missing", operation: "testConnection", id: "missing", parameter: "'missing'", queryID: "owned-point-test-connection-missing"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := athenaOptionsTestConfig(t)
			table := cfg.Tables["items"]
			table.IDColumn = "id"
			cfg.Tables["items"] = table
			cfg.Identity.SessionTokenEnv = "ATHENA_TEST_SESSION"
			t.Setenv("ATHENA_TEST_ACCESS", athenaSessionCredential.AccessKeyID)
			t.Setenv("ATHENA_TEST_SECRET", athenaSessionCredential.SecretAccessKey)
			t.Setenv("ATHENA_TEST_SESSION", athenaSessionCredential.SessionToken)
			f := athenaSessionNewFixture(t, func(_ *athenaSessionFixture, w http.ResponseWriter, _ *http.Request, event athenaSessionEvent) bool {
				switch event.op {
				case "StartQueryExecution":
					if !athenaSessionStartRequestExpect(t, event.body, athenaPointExecutorSQL, []string{testCase.parameter}) {
						w.WriteHeader(400)
						return true
					}
					athenaSessionWrite(t, w, 200, map[string]any{"QueryExecutionId": testCase.queryID})
					return true
				case "GetQueryExecution":
					if len(event.body) != 1 || event.body["QueryExecutionId"] != testCase.queryID {
						t.Error("point execution request changed")
						w.WriteHeader(400)
						return true
					}
					athenaSessionWrite(t, w, 200, map[string]any{"QueryExecution": map[string]any{
						"QueryExecutionId": testCase.queryID, "Query": athenaPointExecutorSQL,
						"ExecutionParameters": []any{testCase.parameter},
						"WorkGroup":           athenaSessionWorkGroup,
						"QueryExecutionContext": map[string]any{
							"Catalog": "AwsDataCatalog", "Database": "native",
						},
						"EngineVersion": map[string]any{"EffectiveEngineVersion": "Athena engine version 3"},
						"ResultConfiguration": map[string]any{
							"OutputLocation":          athenaSessionOutput + testCase.queryID + ".csv",
							"ExpectedBucketOwner":     athenaSessionAccount,
							"EncryptionConfiguration": map[string]any{"EncryptionOption": "SSE_S3"},
						},
						"Status":     map[string]any{"State": "SUCCEEDED"},
						"Statistics": map[string]any{"DataScannedInBytes": json.Number("9007199254740993")},
					}})
					return true
				case "GetQueryResults":
					if _, hasToken := event.body["NextToken"]; len(event.body) != 3 || hasToken {
						t.Error("initial point result request must have exactly three fields and no NextToken")
						w.WriteHeader(400)
						return true
					}
					if !athenaSessionResultRequestExpect(t, event.body, testCase.queryID, 1000) {
						w.WriteHeader(400)
						return true
					}
					rows := []any{athenaSessionRow("id", athenaSessionPointer("amount"))}
					if testCase.found {
						rows = append(rows, athenaSessionRow(testCase.id, athenaSessionPointer(wide)))
					}
					athenaSessionWrite(t, w, 200, map[string]any{"ResultSet": map[string]any{
						"ResultSetMetadata": map[string]any{"ColumnInfo": athenaSessionColumns()},
						"Rows":              rows,
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
			request, err := admitAthenaRequest(context.Background(), cfg,
				[]byte(`{"operation":"`+testCase.operation+`","collection":"items","id":"`+testCase.id+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			frame, err := executeAthenaRead(context.Background(), client, closed, request)
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
			if err = finishAthenaOutput(context.Background(), observer, frame); err != nil {
				t.Fatal(err)
			}
			if observer.writes != 1 || !bytes.Equal(observer.buffer.Bytes(), frame) {
				t.Fatal("point output was not written exactly once unchanged")
			}
			if testCase.operation == "get" {
				envelope := athenaValuesObject(t, frame, "operation", "format", "collection", "document")
				if athenaValuesText(t, envelope["operation"]) != "get" ||
					athenaValuesText(t, envelope["format"]) != athenaPointFormat ||
					athenaValuesText(t, envelope["collection"]) != "items" {
					t.Fatal("get envelope identity changed")
				}
				document := athenaValuesObject(t, envelope["document"], "id", "data", "columns")
				if athenaValuesText(t, document["id"]) != testCase.id {
					t.Fatal("returned document ID changed")
				}
				data := athenaValuesObject(t, document["data"], "id", "amount")
				if athenaValuesText(t, data["id"]) != testCase.id || athenaValuesText(t, data["amount"]) != wide {
					t.Fatal("returned native ID or wide decimal text changed")
				}
				var columns []json.RawMessage
				if json.Unmarshal(document["columns"], &columns) != nil || len(columns) != 2 {
					t.Fatal("returned ordered native columns changed")
				}
				wantNames := []string{"id", "amount"}
				wantTypes := []string{"varchar", "decimal(38,10)"}
				for i, raw := range columns {
					var column map[string]json.RawMessage
					if json.Unmarshal(raw, &column) != nil {
						t.Fatalf("returned native column %d is not an object", i)
					}
					if athenaValuesText(t, column["name"]) != wantNames[i] || athenaValuesText(t, column["type"]) != wantTypes[i] {
						t.Fatalf("returned native column %d changed", i)
					}
				}
			} else {
				names := []string{"operation", "format", "collection", "id", "exists"}
				if testCase.operation == "testConnection" {
					names = append(names, "requestCompleted")
				}
				envelope := athenaValuesObject(t, frame, names...)
				if athenaValuesText(t, envelope["operation"]) != testCase.operation ||
					athenaValuesText(t, envelope["format"]) != athenaPointFormat ||
					athenaValuesText(t, envelope["collection"]) != "items" ||
					athenaValuesText(t, envelope["id"]) != testCase.id {
					t.Fatal("point existence envelope identity changed")
				}
				athenaValuesFalse(t, envelope["exists"])
				if testCase.operation == "testConnection" && !athenaValuesJSONBool(t, envelope["requestCompleted"]) {
					t.Fatal("testConnection did not report a completed request")
				}
			}
			rendered := string(frame)
			for _, private := range []string{"receipt", "cursor", "NextToken",
				athenaSessionCredential.AccessKeyID, athenaSessionCredential.SecretAccessKey,
				athenaSessionCredential.SessionToken} {
				if strings.Contains(rendered, private) {
					t.Fatalf("point output leaked private %q", private)
				}
			}
		})
	}
}

func athenaPointExecutorGoldenExecution(t *testing.T, w http.ResponseWriter, queryID string, parameters []any) {
	t.Helper()
	athenaSessionWrite(t, w, 200, map[string]any{"QueryExecution": map[string]any{
		"QueryExecutionId": queryID, "Query": athenaPointExecutorSQL,
		"ExecutionParameters": parameters,
		"WorkGroup":           athenaSessionWorkGroup,
		"QueryExecutionContext": map[string]any{
			"Catalog": "AwsDataCatalog", "Database": "native",
		},
		"EngineVersion": map[string]any{"EffectiveEngineVersion": "Athena engine version 3"},
		"ResultConfiguration": map[string]any{
			"OutputLocation":          athenaSessionOutput + queryID + ".csv",
			"ExpectedBucketOwner":     athenaSessionAccount,
			"EncryptionConfiguration": map[string]any{"EncryptionOption": "SSE_S3"},
		},
		"Status":     map[string]any{"State": "SUCCEEDED"},
		"Statistics": map[string]any{"DataScannedInBytes": json.Number("9007199254740993")},
	}})
}

func athenaPointExecutorGetManyHook(t *testing.T, failLast bool) athenaSessionHook {
	ids := []string{"b", "missing", "a", "a"}
	parameters := []string{"'b'", "'missing'", "'a'", "'a'"}
	amounts := map[string]string{"b": "1.25", "a": "2.50"}
	queryIDs := []string{"owned-getmany-b", "owned-getmany-missing", "owned-getmany-a1", "owned-getmany-a2"}
	var mu sync.Mutex
	starts := 0
	tokens := map[string]bool{}
	queries := map[string]int{}
	served := map[string]bool{}
	return func(_ *athenaSessionFixture, w http.ResponseWriter, _ *http.Request, event athenaSessionEvent) bool {
		mu.Lock()
		defer mu.Unlock()
		refuse := func(message string) bool {
			t.Error(message)
			w.WriteHeader(400)
			return true
		}
		switch event.op {
		case "StartQueryExecution":
			if starts >= len(ids) {
				return refuse("unexpected extra point Start")
			}
			if !athenaSessionStartRequestExpect(t, event.body, athenaPointExecutorSQL, []string{parameters[starts]}) {
				w.WriteHeader(400)
				return true
			}
			token, _ := event.body["ClientRequestToken"].(string)
			if token == "" || tokens[token] {
				return refuse("point Start token missing or reused")
			}
			tokens[token] = true
			queries[queryIDs[starts]] = starts
			athenaSessionWrite(t, w, 200, map[string]any{"QueryExecutionId": queryIDs[starts]})
			starts++
		case "GetQueryExecution":
			queryID, _ := event.body["QueryExecutionId"].(string)
			index, ok := queries[queryID]
			if len(event.body) != 1 || !ok {
				return refuse("unexpected point execution request")
			}
			athenaPointExecutorGoldenExecution(t, w, queryID, []any{parameters[index]})
		case "GetQueryResults":
			queryID, _ := event.body["QueryExecutionId"].(string)
			index, ok := queries[queryID]
			if !ok || served[queryID] {
				return refuse("unexpected or replayed point results request")
			}
			if _, hasToken := event.body["NextToken"]; len(event.body) != 3 || hasToken {
				return refuse("point results request must have exactly three fields and no NextToken")
			}
			if !athenaSessionResultRequestExpect(t, event.body, queryID, 1000) {
				w.WriteHeader(400)
				return true
			}
			served[queryID] = true
			rows := []any{athenaSessionRow("id", athenaSessionPointer("amount"))}
			id := ids[index]
			if failLast && index == len(ids)-1 {
				rows = append(rows, athenaSessionRow(id, athenaSessionPointer(amounts[id])),
					athenaSessionRow(id+"-extra", athenaSessionPointer(amounts[id])))
			} else if id != "missing" {
				rows = append(rows, athenaSessionRow(id, athenaSessionPointer(amounts[id])))
			}
			athenaSessionWrite(t, w, 200, map[string]any{"ResultSet": map[string]any{
				"ResultSetMetadata": map[string]any{"ColumnInfo": athenaSessionColumns()},
				"Rows":              rows,
			}})
		default:
			return false
		}
		return true
	}
}

func athenaPointExecutorRunGetMany(t *testing.T, failLast bool) (*athenaSessionFixture, []byte, int, error) {
	t.Helper()
	cfg := athenaOptionsTestConfig(t)
	table := cfg.Tables["items"]
	table.IDColumn = "id"
	cfg.Tables["items"] = table
	cfg.Identity.SessionTokenEnv = "ATHENA_TEST_SESSION"
	t.Setenv("ATHENA_TEST_ACCESS", athenaSessionCredential.AccessKeyID)
	t.Setenv("ATHENA_TEST_SECRET", athenaSessionCredential.SecretAccessKey)
	t.Setenv("ATHENA_TEST_SESSION", athenaSessionCredential.SessionToken)
	f := athenaSessionNewFixture(t, athenaPointExecutorGetManyHook(t, failLast))
	client, closeTransport := athenaSessionClient(t, f, cfg)
	closes := 0
	closed := func() {
		closes++
		closeTransport()
	}
	request, err := admitAthenaRequest(context.Background(), cfg,
		[]byte(`{"operation":"getMany","collection":"items","ids":["b","missing","a","a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := executeAthenaRead(context.Background(), client, closed, request)
	return f, candidate, closes, err
}

func TestAthenaExecutorActualGetManyPointBatch(t *testing.T) {
	f, candidate, closes, err := athenaPointExecutorRunGetMany(t, false)
	if err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("transport callback ran %d times, want 1", closes)
	}
	if f.count("StartQueryExecution") != 4 || f.count("GetQueryExecution") != 4 || f.count("GetQueryResults") != 4 {
		t.Fatalf("unexpected RPC counts: start=%d execution=%d results=%d",
			f.count("StartQueryExecution"), f.count("GetQueryExecution"), f.count("GetQueryResults"))
	}
	observer := &athenaExecutorOutputWriter{t: t, closes: func() int { return closes }}
	if err = finishAthenaOutput(context.Background(), observer, candidate); err != nil {
		t.Fatal(err)
	}
	if observer.writes != 1 || !bytes.Equal(observer.buffer.Bytes(), candidate) {
		t.Fatal("getMany output was not written exactly once unchanged")
	}
	envelope := athenaValuesObject(t, candidate, "operation", "format", "collection", "documents")
	if athenaValuesText(t, envelope["operation"]) != "getMany" ||
		athenaValuesText(t, envelope["format"]) != athenaPointFormat ||
		athenaValuesText(t, envelope["collection"]) != "items" {
		t.Fatal("getMany envelope identity changed")
	}
	var documents []json.RawMessage
	if json.Unmarshal(envelope["documents"], &documents) != nil || len(documents) != 4 {
		t.Fatal("getMany did not retain one ordered slot per requested ID")
	}
	ids, amounts := []string{"b", "missing", "a", "a"}, map[string]string{"b": "1.25", "a": "2.50"}
	for i, raw := range documents {
		if i == 1 {
			if string(bytes.TrimSpace(raw)) != "null" {
				t.Fatal("missing point document was not JSON null")
			}
			continue
		}
		document := athenaValuesObject(t, raw, "id", "data", "columns")
		id := athenaValuesText(t, document["id"])
		data := athenaValuesObject(t, document["data"], "id", "amount")
		if id != ids[i] || athenaValuesText(t, data["id"]) != id || athenaValuesText(t, data["amount"]) != amounts[id] {
			t.Fatalf("document %d changed ordered native identity or data", i)
		}
	}
	rendered := string(candidate)
	for _, private := range []string{"receipt", "cursor", "NextToken",
		athenaSessionCredential.AccessKeyID, athenaSessionCredential.SecretAccessKey,
		athenaSessionCredential.SessionToken} {
		if strings.Contains(rendered, private) {
			t.Fatalf("getMany output leaked private %q", private)
		}
	}
}

func TestAthenaExecutorActualGetManyPointBatchFailure(t *testing.T) {
	f, candidate, closes, err := athenaPointExecutorRunGetMany(t, true)
	if err == nil || candidate != nil {
		t.Fatalf("whole-batch identity failure returned candidate=%d bytes and error=%v", len(candidate), err)
	}
	if closes != 1 {
		t.Fatalf("transport callback ran %d times, want 1", closes)
	}
	if f.count("StartQueryExecution") != 4 || f.count("GetQueryExecution") != 4 || f.count("GetQueryResults") != 4 {
		t.Fatalf("failure refetched or replayed: start=%d execution=%d results=%d",
			f.count("StartQueryExecution"), f.count("GetQueryExecution"), f.count("GetQueryResults"))
	}
	observer := &athenaExecutorOutputWriter{t: t, closes: func() int { return closes }}
	if candidate != nil {
		if err = finishAthenaOutput(context.Background(), observer, candidate); err != nil {
			t.Fatal(err)
		}
	}
	if observer.writes != 0 {
		t.Fatalf("failure wrote %d external outputs, want 0", observer.writes)
	}
}
