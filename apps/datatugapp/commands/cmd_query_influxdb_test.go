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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dal-go/dalgo2influxdb"
)

func influxTestMapping(server *httptest.Server) influxNativeConfig {
	return influxNativeConfig{Endpoint: server.URL, Database: "owned-database", TokenEnv: "DATATUG_INFLUX_READ_FIXTURE",
		Tables: map[string]dalgo2influxdb.Table{"events": {Name: "existing_events", Key: dalgo2influxdb.Column{Name: "event_id", Kind: "string"}, Columns: map[string]dalgo2influxdb.Column{
			"amount": {Name: "amount_native", Kind: "number"}, "label": {Name: "label_native", Kind: "string", Nullable: true}, "active": {Name: "active_native", Kind: "boolean"},
		}}}}
}
func influxTestConfig(t *testing.T, server *httptest.Server) string {
	t.Helper()
	config := influxTestMapping(server)
	config.CAFile = dynamoTestFile(t, "influx-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	return dynamoTestJSON(t, "influx-mapping.json", config)
}
func influxTestCommand(t *testing.T, ctx context.Context, config, request, timeout string) (string, string, error) {
	t.Helper()
	cmd := queryCommand()
	var output, diagnostic bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&diagnostic)
	cmd.SetIn(strings.NewReader(request))
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"influxdb", "--config", config, "--request", "-", "--timeout", timeout})
	err := cmd.Execute()
	return output.String(), diagnostic.String(), err
}
func influxTestResponse(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, text); err != nil {
		t.Error("owned fixture response write failed")
	}
}

func TestQueryInfluxOwnedTLSNativeJourney(t *testing.T) {
	t.Setenv("DATATUG_INFLUX_READ_FIXTURE", "owned-read-token-private")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/query_sql" || r.URL.RawQuery != "" || r.TLS == nil || r.Header.Get("Authorization") != "Bearer owned-read-token-private" {
			t.Error("native TLS/bearer endpoint contract changed")
			w.WriteHeader(400)
			return
		}
		var request struct {
			DB     string         `json:"db"`
			Q      string         `json:"q"`
			Params map[string]any `json:"params"`
			Format string         `json:"format"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.UseNumber()
		if err := d.Decode(&request); err != nil {
			t.Error("invalid bounded native request")
			w.WriteHeader(400)
			return
		}
		if request.DB != "owned-database" || request.Format != "json" || !strings.Contains(request.Q, `FROM "existing_events"`) || strings.Contains(request.Q, "owned-read-token-private") || strings.Contains(request.Q, "9007199254740993.000") {
			t.Error("unmapped table or unbound value in SQL")
			w.WriteHeader(400)
			return
		}
		if strings.Contains(request.Q, `"event_id" = $p0`) {
			key, ok := request.Params["p0"].(string)
			if !ok {
				t.Error("point key was not a bound string")
				w.WriteHeader(400)
				return
			}
			if key == "missing" {
				influxTestResponse(t, w, "[]")
				return
			}
			data, err := json.Marshal(key)
			if err != nil {
				t.Error("fixture key encoding failed")
				return
			}
			influxTestResponse(t, w, `[{"__dalgo_key":`+string(data)+`,"amount":9007199254740993.000,"label":null,"active":false}]`)
			return
		}
		number, ok := request.Params["p0"].(json.Number)
		if !ok || string(number) != "9007199254740993.000" {
			t.Error("wide filter lexeme was rounded")
			w.WriteHeader(400)
			return
		}
		if len(request.Params) > 1 {
			if request.Params["p1"] != json.Number("9007199254740993.000") || request.Params["p2"] != json.Number("9007199254740993.000") || request.Params["p3"] != "a" {
				t.Error("typed cursor was not rebound exactly")
				w.WriteHeader(400)
				return
			}
			influxTestResponse(t, w, `[{"__dalgo_key":"b","amount":9007199254740994,"active":true}]`)
		} else {
			influxTestResponse(t, w, `[{"__dalgo_key":"a","amount":9007199254740993.000,"active":false},{"__dalgo_key":"b","amount":9007199254740994,"active":true}]`)
		}
	}))
	t.Cleanup(server.Close)
	config := influxTestConfig(t, server)
	// A regular request file follows the same strict admission path as stdin.
	requestFile := dynamoTestFile(t, "influx-request.json", []byte(`{"operation":"getMany","collection":"events","keys":[]}`))
	fileCmd := queryInfluxDBCommand()
	var fileOutput bytes.Buffer
	fileCmd.SetOut(&fileOutput)
	fileCmd.SetErr(io.Discard)
	fileCmd.SetArgs([]string{"--config", config, "--request", requestFile})
	if err := fileCmd.Execute(); err != nil || fileOutput.String() != "[]\n" {
		t.Fatal("regular request file changed")
	}
	for _, key := range []string{"a", "missing", "astral-😀"} {
		request, err := json.Marshal(map[string]any{"operation": "get", "collection": "events", "key": key})
		if err != nil {
			t.Fatal(err)
		}
		output, diagnostic, err := influxTestCommand(t, context.Background(), config, string(request), "2s")
		if err != nil || !strings.Contains(diagnostic, influxNativePolicyNote) {
			t.Fatalf("get failed: %v", err)
		}
		if key == "missing" {
			if output != "null\n" {
				t.Fatal("missing get changed")
			}
		} else {
			var result struct {
				Key  string         `json:"key"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.Key != key || !reflect.DeepEqual(result.Data["amount"], map[string]any{"kind": "number", "value": "9007199254740993.000"}) || result.Data["active"] != false || result.Data["label"] != nil {
				t.Fatal("lossless native scalar changed")
			}
		}
	}
	for _, test := range []struct{ key, want string }{{"a", "true\n"}, {"missing", "false\n"}} {
		output, _, err := influxTestCommand(t, context.Background(), config, `{"operation":"exists","collection":"events","key":"`+test.key+`"}`, "2s")
		if err != nil || output != test.want {
			t.Fatal("exists contract changed")
		}
	}
	output, _, err := influxTestCommand(t, context.Background(), config, `{"operation":"getMany","collection":"events","keys":["a","missing","a"]}`, "2s")
	if err != nil {
		t.Fatal(err)
	}
	var many []any
	if err := json.Unmarshal([]byte(output), &many); err != nil {
		t.Fatal(err)
	}
	if len(many) != 3 || many[1] != nil || !reflect.DeepEqual(many[0], many[2]) {
		t.Fatal("getMany order/missing/duplicates changed")
	}
	output, _, err = influxTestCommand(t, context.Background(), config, `{"operation":"getMany","collection":"events","keys":[]}`, "2s")
	if err != nil || output != "[]\n" {
		t.Fatal("empty getMany changed")
	}
	query := `{"operation":"query","collection":"events","projection":["amount","active"],"filters":[{"field":"amount","operator":">=","value":{"kind":"number","value":"9007199254740993.000"}}],"order":[{"Field":"amount"}],"limit":1`
	output, _, err = influxTestCommand(t, context.Background(), config, query+"}", "2s")
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		Rows       []any `json:"rows"`
		NextCursor []any `json:"nextCursor"`
	}
	if err := json.Unmarshal([]byte(output), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 1 || !reflect.DeepEqual(first.NextCursor, []any{map[string]any{"kind": "number", "value": "9007199254740993.000"}, "a"}) {
		t.Fatal("typed keyset cursor changed")
	}
	cursor, err := json.Marshal(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	output, _, err = influxTestCommand(t, context.Background(), config, query+`,"startAfter":`+string(cursor)+"}", "2s")
	if err != nil || strings.Contains(output, "nextCursor") || !strings.Contains(output, `"value":"9007199254740994"`) {
		t.Fatal("resumed query failed")
	}
	if calls.Load() != 10 {
		t.Fatalf("unexpected native request count: %d", calls.Load())
	}
}

func TestQueryInfluxOwnedTLSDispatchedCancelDeadlineAndReopen(t *testing.T) {
	t.Setenv("DATATUG_INFLUX_READ_FIXTURE", "owned-read-token-private")
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "deadline"}[deadline], func(t *testing.T) {
			dispatched, disconnected, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var slow atomic.Bool
			slow.Store(true)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if slow.Swap(false) {
					if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10)); err != nil {
						t.Error("owned request could not be read")
						return
					}
					close(dispatched)
					select {
					case <-r.Context().Done():
						close(disconnected)
					case <-cleanup:
					}
					return
				}
				influxTestResponse(t, w, `[{"__dalgo_key":"a","amount":1,"label":"ok","active":true}]`)
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(cleanup) })
			config := influxTestConfig(t, server)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := "2s"
			if deadline {
				timeout = "500ms"
			}
			type outcome struct {
				out, diag string
				err       error
			}
			done := make(chan outcome, 1)
			go func() {
				out, diag, err := influxTestCommand(t, ctx, config, `{"operation":"get","collection":"events","key":"a"}`, timeout)
				done <- outcome{out, diag, err}
			}()
			select {
			case <-dispatched:
			case <-time.After(2 * time.Second):
				t.Fatal("read did not dispatch")
			}
			start := time.Now()
			if !deadline {
				cancel()
			}
			select {
			case result := <-done:
				label := "cancelled"
				if deadline {
					label = "deadline exceeded"
				}
				if result.err == nil || result.out != "" || !strings.Contains(result.err.Error(), label) || strings.Contains(result.diag, "owned-read-token-private") {
					t.Fatal("cancellation output/redaction changed")
				}
				if time.Since(start) > 750*time.Millisecond {
					t.Fatal("dispatched cancellation was not prompt")
				}
			case <-time.After(750 * time.Millisecond):
				t.Fatal("dispatched read outlived cancellation bound")
			}
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("owned HTTP request remained connected")
			}
			output, _, err := influxTestCommand(t, context.Background(), config, `{"operation":"get","collection":"events","key":"a"}`, "2s")
			if err != nil || output == "" {
				t.Fatal("subsequent native read failed")
			}
		})
	}
}

func TestInfluxSignalContextCleanupAndCommandReuse(t *testing.T) {
	previous := interruptContext
	defer func() { interruptContext = previous }()
	parent := context.Background()
	stopped := false
	var child context.Context
	interruptContext = func(given context.Context) (context.Context, func() os.Signal, func()) {
		if given != parent {
			t.Error("stale caller context retained")
		}
		var cancel context.CancelFunc
		child, cancel = context.WithCancel(given)
		return child, func() os.Signal { return nil }, func() { stopped = true; cancel() }
	}
	cmd := queryInfluxDBCommand()
	cmd.SetContext(parent)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", "missing", "--request", "-"})
	for range 2 {
		stopped = false
		if err := cmd.Execute(); err == nil || !stopped || child.Err() == nil || cmd.Context() != parent {
			t.Fatal("signal scope was not cleaned/restored")
		}
	}
	bare := queryInfluxDBCommand()
	stopped = false
	if err := bare.RunE(bare, nil); err == nil || !stopped || child.Err() == nil || bare.Context() != nil {
		t.Fatal("nil caller context was not restored")
	}
}

func TestInfluxTokenProviderContextAndRedaction(t *testing.T) {
	for _, token := range []string{"", "bad token", "bad\nvalue", strings.Repeat("x", 8193)} {
		t.Setenv("DATATUG_INFLUX_READ_FIXTURE", token)
		if _, err := influxTokenProvider("DATATUG_INFLUX_READ_FIXTURE")(context.Background()); err == nil || strings.Contains(err.Error(), token) && token != "" {
			t.Fatal("unsafe token accepted or exposed")
		}
	}
	t.Setenv("DATATUG_INFLUX_READ_FIXTURE", "safe-token")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := influxTokenProvider("DATATUG_INFLUX_READ_FIXTURE")(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("preabort was lost")
	}
	ctx, cancel = context.WithCancel(context.Background())
	entered := false
	probe := &dynamoCancelOnDeadlineContext{Context: ctx, cancel: cancel, entered: &entered}
	if _, err := influxTokenProvider("DATATUG_INFLUX_READ_FIXTURE")(probe); !entered || !errors.Is(err, context.Canceled) {
		t.Fatal("final token guard was lost")
	}
}
