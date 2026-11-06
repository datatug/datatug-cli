package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	snowsdk "github.com/dal-go/dalgo2snowflake"
)

const snowflakeTestConfigJSON = `{"endpoint":"https://owned.snowflakecomputing.com","database":"DB","schema":"PUBLIC","warehouse":"READ_WH","role":"READER","tables":{"users":{"name":"EXISTING_USERS","idColumn":"ID"}},"authKind":"oauth","tokenEnv":"DALGO_SNOWFLAKE_READ_TEST"}`

func snowflakeTestConfig(t *testing.T) snowflakeNativeConfig {
	t.Helper()
	var c snowflakeNativeConfig
	if err := decodeSnowflakeConfig([]byte(snowflakeTestConfigJSON), &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func snowflakeTestRead(t *testing.T, c snowflakeNativeConfig, raw string) admittedSnowflakeRead {
	t.Helper()
	r, err := admitSnowflakeRequest(context.Background(), c, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func snowflakeTestFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func snowflakeTestDocument(id string) *snowsdk.Document {
	value, nul, own := "9223372036854775807.000000001", "null", "own"
	precision, scale := int64(math.MaxInt64), int64(math.MinInt64)
	nullable := false
	return &snowsdk.Document{ID: id, Row: snowsdk.Row{"ID": &id, "VALUE": &value, "NIL": nil, "TEXT_NULL": &nul, "__proto__": &own, "constructor": &own, "prototype": &own}, Columns: []snowsdk.Column{{Name: "VALUE", Type: "fixed", Precision: &precision, Scale: &scale, Nullable: &nullable}, {Name: "ID", Type: "text"}, {Name: "NIL", Type: "text"}, {Name: "TEXT_NULL", Type: "text"}, {Name: "__proto__", Type: "text"}, {Name: "constructor", Type: "text"}, {Name: "prototype", Type: "text"}}}
}

type snowflakeTestClient struct {
	get      func(context.Context, string, string) (*snowsdk.Document, error)
	exists   func(context.Context, string, string) (bool, error)
	many     func(context.Context, string, []string) ([]*snowsdk.Document, error)
	query    func(context.Context, dal.Query) (*snowsdk.Result, error)
	shutdown func(context.Context) error
}

func (c snowflakeTestClient) Get(ctx context.Context, collection, id string) (*snowsdk.Document, error) {
	return c.get(ctx, collection, id)
}
func (c snowflakeTestClient) Exists(ctx context.Context, collection, id string) (bool, error) {
	return c.exists(ctx, collection, id)
}
func (c snowflakeTestClient) GetMany(ctx context.Context, collection string, ids []string) ([]*snowsdk.Document, error) {
	return c.many(ctx, collection, ids)
}
func (c snowflakeTestClient) Query(ctx context.Context, q dal.Query) (*snowsdk.Result, error) {
	return c.query(ctx, q)
}
func (c snowflakeTestClient) Shutdown(ctx context.Context) error { return c.shutdown(ctx) }
func snowflakeSuccessfulClient() snowflakeTestClient {
	return snowflakeTestClient{get: func(_ context.Context, _ string, id string) (*snowsdk.Document, error) {
		if id == "missing" {
			return nil, nil
		}
		return snowflakeTestDocument(id), nil
	}, exists: func(_ context.Context, _ string, id string) (bool, error) { return id != "missing", nil }, many: func(_ context.Context, _ string, ids []string) ([]*snowsdk.Document, error) {
		out := make([]*snowsdk.Document, len(ids))
		for i, id := range ids {
			if id != "missing" {
				out[i] = snowflakeTestDocument(id)
			}
		}
		return out, nil
	}, query: func(context.Context, dal.Query) (*snowsdk.Result, error) {
		a, b := snowflakeTestDocument("a"), snowflakeTestDocument("b")
		return &snowsdk.Result{Rows: []snowsdk.Row{a.Row, b.Row}, Columns: a.Columns}, nil
	}, shutdown: func(context.Context) error { return nil }}
}

func TestSnowflakeNativeOperationsAndLosslessOutput(t *testing.T) {
	config := snowflakeTestConfig(t)
	for _, raw := range []string{`{"operation":"get","collection":"users","id":"a%2Fb"}`, `{"operation":"get","collection":"users","id":"missing"}`, `{"operation":"exists","collection":"users","id":"missing"}`, `{"operation":"testConnection","collection":"users","id":"a"}`, `{"operation":"getMany","collection":"users","ids":["b","missing","a","a"]}`, `{"operation":"getMany","collection":"users","ids":[]}`, `{"operation":"query","collection":"users","limit":2}`} {
		t.Run(raw, func(t *testing.T) {
			r := snowflakeTestRead(t, config, raw)
			client := snowflakeSuccessfulClient()
			var shutdowns, transports int
			client.shutdown = func(ctx context.Context) error {
				shutdowns++
				if ctx.Err() != nil {
					t.Error("cleanup context not independent/live")
				}
				if d, ok := ctx.Deadline(); !ok || time.Until(d) > 2*time.Second {
					t.Error("cleanup scope not bounded")
				}
				return nil
			}
			var output bytes.Buffer
			if err := deliverSnowflakeRead(context.Background(), &output, client, func() { transports++ }, r); err != nil {
				t.Fatal(err)
			}
			if shutdowns != 1 || transports != 1 || !strings.HasSuffix(output.String(), "\n") {
				t.Fatal("resources not closed exactly once or newline changed")
			}
			var decoded map[string]any
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["format"] != snowflakeWireFormat || decoded["operation"] != r.operation {
				t.Fatal("output profile")
			}
			if r.operation == "get" && r.id != "missing" {
				d := decoded["document"].(map[string]any)
				data := d["data"].(map[string]any)
				if d["id"] != "a%2Fb" || data["VALUE"] != "9223372036854775807.000000001" || data["NIL"] != nil || data["TEXT_NULL"] != "null" || data["__proto__"] != "own" || data["constructor"] != "own" || data["prototype"] != "own" {
					t.Fatal("native text/null/own field changed")
				}
				column := d["columns"].([]any)[0].(map[string]any)
				if column["precision"].(map[string]any)["value"] != "9223372036854775807" || column["scale"].(map[string]any)["value"] != "-9223372036854775808" || column["nullable"] != false {
					t.Fatal("int64 descriptor lost precision")
				}
				unknown := d["columns"].([]any)[1].(map[string]any)
				if _, present := unknown["nullable"]; present {
					t.Fatal("unknown nullable became false/null")
				}
			}
			if r.operation == "get" && r.id == "missing" && decoded["document"] != nil {
				t.Fatal("missing document changed")
			}
			if r.operation == "getMany" {
				docs := decoded["documents"].([]any)
				if len(docs) != len(r.ids) {
					t.Fatal("batch cardinality changed")
				}
				for i, id := range r.ids {
					if id == "missing" {
						if docs[i] != nil {
							t.Fatal("missing slot changed")
						}
					} else if docs[i].(map[string]any)["id"] != id {
						t.Fatal("batch order/duplicates changed")
					}
				}
			}
			if r.operation == "testConnection" && decoded["requestCompleted"] != true {
				t.Fatal("point connection observation missing")
			}
			if r.operation == "query" {
				rows := decoded["rows"].([]any)
				if len(rows) != 2 || rows[0].([]any)[0] != "9223372036854775807.000000001" || rows[0].([]any)[2] != nil {
					t.Fatal("native column ordering/null changed")
				}
				if _, has := decoded["cursor"]; has {
					t.Fatal("portable cursor invented")
				}
			}
		})
	}
}

type snowflakeCommitWriter struct {
	buffer bytes.Buffer
	ready  func() bool
	writes int
}

func (w *snowflakeCommitWriter) Write(p []byte) (int, error) {
	if !w.ready() {
		return 0, errors.New("write before owned shutdown")
	}
	w.writes++
	return w.buffer.Write(p)
}
func TestSnowflakeShutdownPrecedesFinalWriteAndSnapshotsValues(t *testing.T) {
	config := snowflakeTestConfig(t)
	r := snowflakeTestRead(t, config, `{"operation":"get","collection":"users","id":"a"}`)
	document := snowflakeTestDocument("a")
	client := snowflakeSuccessfulClient()
	client.get = func(context.Context, string, string) (*snowsdk.Document, error) { return document, nil }
	var drained, transport bool
	client.shutdown = func(context.Context) error {
		drained = true
		*document.Row["VALUE"] = "mutated"
		*document.Columns[0].Precision = 1
		return nil
	}
	writer := &snowflakeCommitWriter{ready: func() bool { return drained && transport }}
	if err := deliverSnowflakeRead(context.Background(), writer, client, func() { transport = true }, r); err != nil {
		t.Fatal(err)
	}
	if writer.writes != 1 || strings.Contains(writer.buffer.String(), "mutated") || !strings.Contains(writer.buffer.String(), `"value":"9223372036854775807"`) {
		t.Fatal("wire data retained mutable SDK pointers or multiple writes")
	}
}
func TestSnowflakeReadAndCleanupFailuresNeverCommitPrefix(t *testing.T) {
	config := snowflakeTestConfig(t)
	r := snowflakeTestRead(t, config, `{"operation":"get","collection":"users","id":"a"}`)
	for _, mode := range []string{"provider", "cleanup", "caller-during-cleanup", "caller-during-transport-close", "deadline-original", "caller-original"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := snowflakeSuccessfulClient()
			var shutdowns, closes int
			if mode == "provider" {
				client.get = func(context.Context, string, string) (*snowsdk.Document, error) {
					return nil, errors.New("SECRET_SOURCE")
				}
			}
			if mode == "deadline-original" || mode == "caller-original" {
				client.get = func(context.Context, string, string) (*snowsdk.Document, error) {
					if mode == "caller-original" {
						return nil, context.Canceled
					}
					return nil, context.DeadlineExceeded
				}
			}
			client.shutdown = func(cleanup context.Context) error {
				shutdowns++
				if cleanup.Err() != nil {
					t.Error("original context leaked into cleanup")
				}
				if mode == "caller-during-cleanup" {
					cancel()
				}
				if mode == "cleanup" || mode == "provider" || mode == "deadline-original" || mode == "caller-original" {
					return errors.New("SECRET_CLEANUP")
				}
				return nil
			}
			var output bytes.Buffer
			err := deliverSnowflakeRead(ctx, &output, client, func() {
				closes++
				if mode == "caller-during-transport-close" {
					cancel()
				}
			}, r)
			if err == nil || output.Len() != 0 || shutdowns != 1 || closes != 1 || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("failure leaked/prefix committed or cleanup skipped", err)
			}
			if mode == "caller-original" || strings.HasPrefix(mode, "caller-during") {
				if !strings.Contains(err.Error(), "cancelled") {
					t.Fatal("caller priority lost", err)
				}
			}
			if mode == "deadline-original" && !strings.Contains(err.Error(), "deadline") {
				t.Fatal("original deadline lost", err)
			}
		})
	}
}
func TestSnowflakeMetadataCommandHasNoCredentialsOrProviderIO(t *testing.T) {
	config := strings.TrimSuffix(snowflakeTestConfigJSON, "}") + `,"caFile":"/not/read/by/local/metadata"}`
	cmd := querySnowflakeCommand()
	original := context.Background()
	cmd.SetContext(original)
	cmd.SetIn(strings.NewReader(`{"operation":"metadata"}`))
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--config", snowflakeTestFile(t, config), "--request", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if cmd.Context() != original || !strings.Contains(stderr.String(), "DataTug application policies are not applied") {
		t.Fatal("signal context/policy note changed")
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	metadata := result["metadata"].(map[string]any)
	if metadata["provenance"] != "trusted configuration" || metadata["dynamicDiscovery"] != false || metadata["database"] != "DB" || metadata["warehouse"] != "READ_WH" {
		t.Fatal("metadata scope changed")
	}
	for _, secret := range []string{"tokenEnv", "authKind", "caFile", "endpoint"} {
		if _, present := metadata[secret]; present {
			t.Fatal("connection metadata exposed secret infrastructure", secret)
		}
	}
}
func TestSnowflakeCommandUsageAndInputFailuresRedact(t *testing.T) {
	validConfig := snowflakeTestFile(t, snowflakeTestConfigJSON)
	validRequest := snowflakeTestFile(t, `{"operation":"get","collection":"users","id":"a"}`)
	for _, test := range []struct {
		config, request string
		timeout         time.Duration
	}{{"", validRequest, time.Second}, {"-", validRequest, time.Second}, {validConfig, "", time.Second}, {validConfig, validRequest, 0}, {validConfig, validRequest, 3 * time.Minute}, {"/SECRET_FILE_MISSING", validRequest, time.Second}, {snowflakeTestFile(t, `{"endpoint":"SECRET"}`), validRequest, time.Second}, {validConfig, "/SECRET_REQUEST_MISSING", time.Second}, {validConfig, snowflakeTestFile(t, `{"operation":"query","sql":"SECRET"}`), time.Second}} {
		cmd := querySnowflakeCommand()
		cmd.SetContext(context.Background())
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		err := runSnowflakeCommand(cmd, test.config, test.request, test.timeout)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("usage/input failure escaped", err)
		}
	}
	cmd := querySnowflakeCommand()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd.SetContext(ctx)
	if err := runSnowflakeCommand(cmd, validConfig, validRequest, time.Second); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal("pre-abort command", err)
	}
}
