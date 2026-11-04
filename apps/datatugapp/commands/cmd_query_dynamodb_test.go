package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo2dynamodb"
)

func dynamoTestFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func dynamoTestJSON(t *testing.T, name string, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return dynamoTestFile(t, name, data)
}
func dynamoTestConfig(t *testing.T, server *httptest.Server) string {
	t.Helper()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caPath := dynamoTestFile(t, "ca.pem", ca)
	return dynamoTestJSON(t, "mapping.json", dynamoNativeConfig{Endpoint: server.URL, Region: "us-east-1", CAFile: caPath, Tables: map[string]dalgo2dynamodb.Table{
		"items":  {Name: "existing-items", PartitionKey: "account"},
		"events": {Name: "existing-events", PartitionKey: "account", SortKey: "sequence"},
	}})
}
func dynamoTestCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "owned-fixture-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "owned-fixture-secret")
	t.Setenv("AWS_SESSION_TOKEN", "owned-fixture-session")
}
func dynamoTestCommand(t *testing.T, ctx context.Context, config, request string, timeout string) (string, string, error) {
	t.Helper()
	cmd := queryCommand()
	var output, diagnostic bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&diagnostic)
	cmd.SetIn(strings.NewReader(request))
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"dynamodb", "--config", config, "--request", "-", "--timeout", timeout})
	err := cmd.Execute()
	return output.String(), diagnostic.String(), err
}
func dynamoNativeFixtureItem(key map[string]any) map[string]any {
	item := map[string]any{
		"wide":     map[string]any{"N": "9007199254740993.000"},
		"binary":   map[string]any{"B": "AP+A"},
		"strings":  map[string]any{"SS": []any{"a", "b"}},
		"numbers":  map[string]any{"NS": []any{"1.0", "9007199254740993"}},
		"binaries": map[string]any{"BS": []any{"AP8=", "AQ=="}},
		"bool":     map[string]any{"BOOL": false},
		"null":     map[string]any{"NULL": true},
	}
	item["nested"] = map[string]any{"M": map[string]any{
		"list": map[string]any{"L": []any{map[string]any{"S": "value"}, map[string]any{"NULL": true}}},
	}}
	for name, value := range key {
		item[name] = value
	}
	return item
}
func TestQueryDynamoOwnedTLSNativeJourney(t *testing.T) {
	dynamoTestCredentials(t)
	var gets, queries atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || r.TLS == nil || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=owned-fixture-access/") || r.Header.Get("X-Amz-Security-Token") != "owned-fixture-session" {
			t.Error("official signed TLS request contract changed")
			w.WriteHeader(400)
			return
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, dynamoDocumentBytes)).Decode(&body); err != nil {
			t.Error("invalid SDK request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		switch r.Header.Get("X-Amz-Target") {
		case "DynamoDB_20120810.GetItem":
			gets.Add(1)
			var key map[string]any
			if err := json.Unmarshal(body["Key"], &key); err != nil {
				t.Error(err)
				return
			}
			var table string
			if err := json.Unmarshal(body["TableName"], &table); err != nil || table != "existing-items" {
				t.Error("hidden-layout table selected")
			}
			if reflect.DeepEqual(key, map[string]any{"account": map[string]any{"S": "missing"}}) {
				_, _ = w.Write([]byte("{}"))
				return
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"Item": dynamoNativeFixtureItem(key)}); err != nil {
				t.Error(err)
			}
		case "DynamoDB_20120810.Query":
			queries.Add(1)
			var expression string
			if err := json.Unmarshal(body["KeyConditionExpression"], &expression); err != nil || expression != "#p = :p" {
				t.Error("unbound partition expression")
			}
			if body["FilterExpression"] != nil || body["IndexName"] != nil {
				t.Error("unrequested native scope")
			}
			if body["ExclusiveStartKey"] == nil {
				_, _ = w.Write([]byte(`{"Items":[],"Count":0,"ScannedCount":1,"LastEvaluatedKey":{"account":{"S":"tenant"},"sequence":{"N":"0"}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"Items":[{"account":{"S":"tenant"},"sequence":{"N":"1"}}],"Count":1,"ScannedCount":1,"LastEvaluatedKey":{"account":{"S":"tenant"},"sequence":{"N":"1"}}}`))
		default:
			t.Error("non-read operation dispatched")
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	config := dynamoTestConfig(t, server)
	for _, request := range []string{
		`{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"N":"9007199254740993"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"B":"AP8="}}}`,
	} {
		output, diagnostic, err := dynamoTestCommand(t, context.Background(), config, request, "2s")
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		var input struct{ Key map[string]any }
		if err := json.Unmarshal([]byte(request), &input); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result["item"], dynamoNativeFixtureItem(input.Key)) {
			t.Fatal("AWS union tag/value fidelity changed")
		}
		if !strings.Contains(diagnostic, dynamoNativePolicyNote) {
			t.Fatal("native IAM boundary omitted")
		}
	}
	output, _, err := dynamoTestCommand(t, context.Background(), config, `{"operation":"getMany","collection":"items","keys":[{"account":{"S":"one"}},{"account":{"S":"missing"}},{"account":{"S":"one"}}]}`, "2s")
	if err != nil {
		t.Fatal(err)
	}
	var many struct{ Items []map[string]any }
	if err := json.Unmarshal([]byte(output), &many); err != nil {
		t.Fatal(err)
	}
	if len(many.Items) != 3 || many.Items[1] != nil || !reflect.DeepEqual(many.Items[0], many.Items[2]) {
		t.Fatal("GetMany duplicate/missing/order changed")
	}
	for _, operation := range []string{"get", "exists"} {
		output, _, err = dynamoTestCommand(t, context.Background(), config, `{"operation":"`+operation+`","collection":"items","key":{"account":{"S":"missing"}}}`, "2s")
		if err != nil {
			t.Fatal(err)
		}
		var absent map[string]any
		if err := json.Unmarshal([]byte(output), &absent); err != nil {
			t.Fatal(err)
		}
		if operation == "get" && absent["item"] != nil || operation == "exists" && absent["exists"] != false {
			t.Fatal("missing item contract changed")
		}
	}
	output, _, err = dynamoTestCommand(t, context.Background(), config, `{"operation":"query","collection":"events","partition":{"S":"tenant"},"limit":1}`, "2s")
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(output), &page); err != nil {
		t.Fatal(err)
	}
	if page["hasMore"] != true || page["pages"] != float64(2) || page["evaluated"] != float64(2) || page["cursor"] != nil || page["next"] != nil || queries.Load() != 2 || gets.Load() != 8 {
		t.Fatal("empty-page/hasMore/no-portable-cursor journey changed")
	}
	requestFile := dynamoTestFile(t, "request.json", []byte(`{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`))
	fileCommand := queryDynamoDBCommand()
	var fileOutput bytes.Buffer
	fileCommand.SetOut(&fileOutput)
	fileCommand.SetErr(io.Discard)
	fileCommand.SetArgs([]string{"--config", config, "--request", requestFile})
	if err := fileCommand.Execute(); err != nil || fileOutput.Len() == 0 {
		t.Fatal("request-file journey failed")
	}
	if output, _, err := dynamoTestCommand(t, context.Background(), config, `{"operation":"getMany","collection":"items","keys":[]}`, "2s"); err != nil || !strings.Contains(output, `"items":[]`) {
		t.Fatal("empty GetMany contract changed")
	}
}

func TestQueryDynamoRefusalsBeforeDispatch(t *testing.T) {
	dynamoTestCredentials(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(server.Close)
	config := dynamoTestConfig(t, server)
	for _, request := range []string{
		`{"operation":"Scan","collection":"items"}`,
		`{"operation":"get","collection":"foreign-secret","key":{"account":{"S":"one"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"S":"one","N":"1"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"N":"NaN"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"B":"not-base64!"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"S":"\ud800"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"S":"one","S":"two"}}}`,
		`{"operation":"get","collection":"items","key":{"account":{"S":"one"}},"secret-field":"secret"}`,
		`{"operation":"query","collection":"events","partition":{"S":"tenant"},"limit":0}`,
		`{"operation":"query","collection":"events","partition":{"S":"tenant"},"limit":1,"cursor":{}}`,
		`{"operation":"query","collection":"events","partition":{"S":"tenant"},"limit":1,"sort":{"operator":"DROP secret","values":[{"N":"1"}]}}`,
		"null", "[]", "{} {}", `{"operation":`, strings.Repeat(" ", dynamoDocumentBytes+1),
	} {
		output, diagnostic, err := dynamoTestCommand(t, context.Background(), config, request, "2s")
		if err == nil || output != "" {
			t.Fatal("invalid request accepted or wrote stdout")
		}
		if strings.Contains(err.Error()+diagnostic, "secret") {
			t.Fatal("request content escaped error redaction")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("refused request reached service")
	}
}

func TestQueryDynamoDispatchedCancellationDeadlineAndReopen(t *testing.T) {
	dynamoTestCredentials(t)
	for _, deadline := range []bool{false, true} {
		name := "caller"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			dispatched := make(chan struct{}, 1)
			disconnected := make(chan struct{}, 1)
			cleanup := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Key map[string]map[string]string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.Key["account"]["S"] == "slow" {
					dispatched <- struct{}{}
					select {
					case <-r.Context().Done():
						disconnected <- struct{}{}
					case <-cleanup:
					}
					return
				}
				w.Header().Set("Content-Type", "application/x-amz-json-1.0")
				_, _ = w.Write([]byte(`{"Item":{"account":{"S":"one"}}}`))
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(cleanup) })
			config := dynamoTestConfig(t, server)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			type completion struct {
				output string
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				output, _, err := dynamoTestCommand(t, ctx, config, `{"operation":"get","collection":"items","key":{"account":{"S":"slow"}}}`, "500ms")
				done <- completion{output, err}
			}()
			select {
			case <-dispatched:
			case <-time.After(2 * time.Second):
				t.Fatal("command did not reach owned service")
			}
			if !deadline {
				cancel(errors.New("secret caller cause"))
			}
			select {
			case result := <-done:
				if result.err == nil || result.output != "" || strings.Contains(result.err.Error(), "secret") {
					t.Fatal("late success or raw cancellation cause")
				}
				want := "cancelled"
				if deadline {
					want = "deadline"
				}
				if !strings.Contains(result.err.Error(), want) {
					t.Fatal("cancellation classification changed")
				}
			case <-time.After(750 * time.Millisecond):
				t.Fatal("dispatched cancellation exceeded750ms")
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("command HTTP read did not disconnect")
			}
			if output, _, err := dynamoTestCommand(t, context.Background(), config, `{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`, "2s"); err != nil || output == "" {
				t.Fatal("command did not reopen")
			}
		})
	}
}

func TestQueryDynamoProviderFailureAndCredentialRedaction(t *testing.T) {
	dynamoTestCredentials(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"secret token URL cause"}`))
	}))
	t.Cleanup(server.Close)
	config := dynamoTestConfig(t, server)
	request := `{"operation":"get","collection":"items","key":{"account":{"S":"one"}}}`
	output, diagnostic, err := dynamoTestCommand(t, context.Background(), config, request, "2s")
	if err == nil || output != "" || strings.Contains(err.Error()+diagnostic, "secret") {
		t.Fatal("provider error was not redacted")
	}
	before := requests.Load()
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	output, diagnostic, err = dynamoTestCommand(t, context.Background(), config, request, "2s")
	if err == nil || output != "" || requests.Load() != before || strings.Contains(err.Error()+diagnostic, "secret") {
		t.Fatal("missing credentials dispatched or leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, _, err := dynamoTestCommand(t, ctx, config, request, "2s"); err == nil || output != "" || requests.Load() != before {
		t.Fatal("preaborted command dispatched")
	}
}

type dynamoCancelMarshal struct{ cancel context.CancelFunc }

func (v dynamoCancelMarshal) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`{"ok":true}`), nil
}

type dynamoDeadlineMarshal struct {
	ctx     context.Context
	entered *bool
}

func (v dynamoDeadlineMarshal) MarshalJSON() ([]byte, error) {
	*v.entered = true
	<-v.ctx.Done()
	return []byte("{}"), nil
}

type dynamoPartialWriter struct{ err error }

func (w dynamoPartialWriter) Write(p []byte) (int, error) { return len(p) - 1, w.err }
func TestDynamoOutputFinalGuardAndWriterSemantics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var output bytes.Buffer
	if err := writeDynamoResult(ctx, &output, dynamoCancelMarshal{cancel}); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatal("serialization cancellation wrote stdout")
	}
	deadline, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stop()
	entered := false
	if err := writeDynamoResult(deadline, &output, dynamoDeadlineMarshal{deadline, &entered}); !errors.Is(err, context.DeadlineExceeded) || !entered || output.Len() != 0 {
		t.Fatal("serialization callback deadline wrote stdout")
	}
	value := map[string]any{"S": "<line>\n"}
	if err := writeDynamoResult(context.Background(), &output, value); err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	if err := json.NewEncoder(&expected).Encode(value); err != nil {
		t.Fatal(err)
	}
	if output.String() != expected.String() {
		t.Fatal("standard JSON escaping/newline changed")
	}
	if err := writeDynamoResult(context.Background(), dynamoPartialWriter{}, value); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write swallowed")
	}
	want := errors.New("owned stdout failure")
	if err := writeDynamoResult(context.Background(), dynamoPartialWriter{want}, value); !errors.Is(err, want) {
		t.Fatal("partial writer error changed")
	}
	if err := writeDynamoResult(context.Background(), &output, make(chan int)); !errors.Is(err, errDynamoResponse) {
		t.Fatal("encoding error changed")
	}
	if err := writeDynamoResult(context.Background(), &output, strings.Repeat("x", 8<<20)); !errors.Is(err, errDynamoResponse) {
		t.Fatal("output byte cap missing")
	}
}
