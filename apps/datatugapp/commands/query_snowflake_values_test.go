package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	snowsdk "github.com/dal-go/dalgo2snowflake"
)

func TestSnowflakeMalformedNativeResultsFailClosedAndStillDrain(t *testing.T) {
	config := snowflakeTestConfig(t)
	for _, kind := range []string{"id", "missing-id", "nil-id", "columns-empty", "columns-many", "duplicate-column", "column-name", "column-type", "missing-cell", "extra-cell", "invalid-text", "oversize-text", "batch-cardinality", "query-nil", "query-overreturn"} {
		t.Run(kind, func(t *testing.T) {
			raw := `{"operation":"get","collection":"users","id":"a"}`
			if kind == "batch-cardinality" {
				raw = `{"operation":"getMany","collection":"users","ids":["a","b"]}`
			}
			if strings.HasPrefix(kind, "query-") {
				raw = `{"operation":"query","collection":"users","limit":1}`
			}
			r := snowflakeTestRead(t, config, raw)
			document := snowflakeTestDocument("a")
			switch kind {
			case "id":
				document.ID = "foreign"
			case "missing-id":
				delete(document.Row, "ID")
			case "nil-id":
				document.Row["ID"] = nil
			case "columns-empty":
				document.Columns = nil
			case "columns-many":
				document.Columns = make([]snowsdk.Column, 129)
			case "duplicate-column":
				document.Columns[1].Name = document.Columns[0].Name
			case "column-name":
				document.Columns[0].Name = ""
			case "column-type":
				document.Columns[0].Type = ""
			case "missing-cell":
				delete(document.Row, "NIL")
				document.Row["OTHER"] = nil
			case "extra-cell":
				document.Row["OTHER"] = nil
			case "oversize-text":
				v := strings.Repeat("x", (4<<20)+1)
				document.Row["VALUE"] = &v
			case "invalid-text":
				v := string([]byte{0xff})
				document.Row["VALUE"] = &v
			}
			client := snowflakeSuccessfulClient()
			client.get = func(context.Context, string, string) (*snowsdk.Document, error) { return document, nil }
			if kind == "batch-cardinality" {
				client.many = func(context.Context, string, []string) ([]*snowsdk.Document, error) {
					return []*snowsdk.Document{document}, nil
				}
			}
			if kind == "query-nil" {
				client.query = func(context.Context, dal.Query) (*snowsdk.Result, error) { return nil, nil }
			}
			var drained, closed int
			client.shutdown = func(context.Context) error { drained++; return nil }
			var out bytes.Buffer
			if err := deliverSnowflakeRead(context.Background(), &out, client, func() { closed++ }, r); err == nil || out.Len() != 0 || drained != 1 || closed != 1 {
				t.Fatal("malformed native output delivered or cleanup skipped", err)
			}
		})
	}
}
func TestSnowflakeWholeOutputBudgetCountsRepeatedEscapedCells(t *testing.T) {
	config := snowflakeTestConfig(t)
	r := snowflakeTestRead(t, config, `{"operation":"getMany","collection":"users","ids":["a","a","a"]}`)
	document := snowflakeTestDocument("a")
	large := strings.Repeat("\x00", 600000)
	document.Row["VALUE"] = &large
	client := snowflakeSuccessfulClient()
	client.many = func(context.Context, string, []string) ([]*snowsdk.Document, error) {
		return []*snowsdk.Document{document, document, document}, nil
	}
	var out bytes.Buffer
	if err := deliverSnowflakeRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
		t.Fatal("repeated shared leaf bypassed whole escaped output budget", err)
	}
}
func TestSnowflakeQueryHeaderAndRowsShareNodeAndByteBudget(t *testing.T) {
	config := snowflakeTestConfig(t)
	r := snowflakeTestRead(t, config, `{"operation":"query","collection":"users","limit":1000}`)
	columns := make([]snowsdk.Column, 128)
	for i := range columns {
		columns[i] = snowsdk.Column{Name: strings.Repeat("N", 128) + string(rune(0x100+i)), Type: "text"}
	}
	row := make(snowsdk.Row, len(columns))
	for _, c := range columns {
		row[c.Name] = nil
	}
	rows := make([]snowsdk.Row, 1000)
	for i := range rows {
		rows[i] = row
	}
	client := snowflakeSuccessfulClient()
	client.query = func(context.Context, dal.Query) (*snowsdk.Result, error) {
		return &snowsdk.Result{Rows: rows, Columns: columns}, nil
	}
	var out bytes.Buffer
	if err := deliverSnowflakeRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
		t.Fatal("aggregate row/key/node budget bypass", err)
	}
}
func TestSnowflakeOptionalDescriptorsAndMetadataBudget(t *testing.T) {
	ctx := context.Background()
	budget := snowflakeWireBudget{}
	unknown := budget.descriptor(ctx, nil)
	if budget.err != nil || unknown != nil {
		t.Fatal("unknown descriptor changed", budget.err)
	}
	length, byteLength := int64(9007199254740993), int64(123)
	columns, err := budget.columns(ctx, []snowsdk.Column{{Name: "a", Type: "text", Length: &length, ByteLength: &byteLength}})
	if err != nil || columns[0].Length.Value != "9007199254740993" || columns[0].ByteLength.Value != "123" {
		t.Fatal("descriptor exactness", err)
	}
	config := snowflakeTestConfig(t)
	config.Tables = make(map[string]snowsdk.Table, 129)
	for i := 0; i < 129; i++ {
		config.Tables[string(rune(i+33))] = snowsdk.Table{Name: "T", IDColumn: "ID"}
	}
	if _, err := snowflakeMetadataResult(ctx, config); err == nil {
		t.Fatal("metadata table cap skipped")
	}
	config = snowflakeTestConfig(t)
	config.Warehouse = ""
	config.Role = ""
	result, err := snowflakeMetadataResult(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	meta := result["metadata"].(map[string]any)
	if _, present := meta["warehouse"]; present {
		t.Fatal("absent warehouse invented")
	}
	if _, present := meta["role"]; present {
		t.Fatal("absent role invented")
	}
}

type snowflakePartialWriter struct{ failure error }

func (w snowflakePartialWriter) Write(p []byte) (int, error) { return len(p) / 2, w.failure }

type snowflakeCancelJSON struct{ cancel context.CancelFunc }

func (v snowflakeCancelJSON) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`{"native":"staged"}`), nil
}
func TestSnowflakeFinalEncoderCancellationRefusalAndWriterContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	if err := finishSnowflakeOutput(ctx, &out, snowflakeCancelJSON{cancel: cancel}); err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal("serialized late cancellation committed output", err)
	}
	if err := finishSnowflakeOutput(context.Background(), &out, make(chan int)); err == nil || out.Len() != 0 {
		t.Fatal("unsupported output encoding leaked/prefixed", err)
	}
	if err := finishSnowflakeOutput(context.Background(), snowflakePartialWriter{}, map[string]any{"a": "b"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write swallowed", err)
	}
	failure := errors.New("owned writer failed")
	if err := finishSnowflakeOutput(context.Background(), snowflakePartialWriter{failure: failure}, map[string]any{"a": "b"}); !errors.Is(err, failure) {
		t.Fatal("partial writer error replaced", err)
	}
}
func TestSnowflakePreAbortedWireConversionDoesNotCallProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := snowflakeTestClient{}
	request := admittedSnowflakeRead{operation: "get", collection: "users", id: "a"}
	if _, err := executeSnowflakeRead(ctx, client, request); !errors.Is(err, context.Canceled) {
		t.Fatal("preabort reached provider", err)
	}
	if _, err := executeSnowflakeRead(context.Background(), client, admittedSnowflakeRead{operation: "unknown"}); err == nil {
		t.Fatal("unknown operation accepted")
	}
}
