package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	dbrsdk "github.com/dal-go/dalgo2databricks"
)

func TestDatabricksMalformedNativeResultsFailClosedAndStillDrain(t *testing.T) {
	config := databricksTestConfig(t)
	for _, kind := range []string{
		"id",
		"missing-id",
		"nil-id",
		"columns-empty",
		"columns-many",
		"duplicate-column",
		"column-name",
		"column-type",
		"missing-cell",
		"extra-cell",
		"invalid-text",
		"oversize-text",
		"batch-cardinality",
		"query-nil",
		"query-overreturn",
	} {
		t.Run(kind, func(t *testing.T) {
			raw := `{"operation":"get","collection":"users","id":"a"}`
			if kind == "batch-cardinality" {
				raw = `{"operation":"getMany","collection":"users","ids":["a","b"]}`
			}
			if strings.HasPrefix(kind, "query-") {
				raw = `{"operation":"query","collection":"users","limit":1}`
			}
			r := databricksTestRead(t, config, raw)
			document := databricksTestDocument("a")
			switch kind {
			case "id":
				document.ID = "foreign"
			case "missing-id":
				delete(document.Row, "id")
			case "nil-id":
				document.Row["id"] = nil
			case "columns-empty":
				document.Columns = nil
			case "columns-many":
				document.Columns = make([]dbrsdk.Column, 129)
			case "duplicate-column":
				document.Columns[1].Name = document.Columns[0].Name
			case "column-name":
				document.Columns[0].Name = ""
			case "column-type":
				document.Columns[0].Type = ""
			case "missing-cell":
				delete(document.Row, "nil")
				document.Row["other"] = nil
			case "extra-cell":
				document.Row["other"] = nil
			case "oversize-text":
				v := strings.Repeat("x", (4<<20)+1)
				document.Row["value"] = &v
			case "invalid-text":
				v := string([]byte{0xff})
				document.Row["value"] = &v
			}
			client := databricksSuccessfulClient()
			client.get = func(context.Context, string, string) (*dbrsdk.Document, error) { return document, nil }
			if kind == "batch-cardinality" {
				client.many = func(context.Context, string, []string) ([]*dbrsdk.Document, error) {
					return []*dbrsdk.Document{document}, nil
				}
			}
			if kind == "query-nil" {
				client.query = func(context.Context, dal.Query) (*dbrsdk.Result, error) { return nil, nil }
			}
			var drained, closed int
			client.shutdown = func(context.Context) error { drained++; return nil }
			var out bytes.Buffer
			if err := deliverDatabricksRead(context.Background(), &out, client, func() { closed++ }, r); err == nil || out.Len() != 0 || drained != 1 || closed != 1 {
				t.Fatal("malformed native output delivered or cleanup skipped", err)
			}
		})
	}
}

func TestDatabricksWholeOutputBudgetCountsRepeatedEscapedCells(t *testing.T) {
	config := databricksTestConfig(t)
	r := databricksTestRead(t, config, `{"operation":"getMany","collection":"users","ids":["a","a","a"]}`)
	document := databricksTestDocument("a")
	large := strings.Repeat("\x00", 600000)
	document.Row["value"] = &large
	client := databricksSuccessfulClient()
	client.many = func(context.Context, string, []string) ([]*dbrsdk.Document, error) {
		return []*dbrsdk.Document{document, document, document}, nil
	}
	var out bytes.Buffer
	if err := deliverDatabricksRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
		t.Fatal("repeated shared leaf bypassed whole escaped output budget", err)
	}
}

func TestDatabricksQueryHeaderAndRowsShareNodeAndByteBudget(t *testing.T) {
	config := databricksTestConfig(t)
	r := databricksTestRead(t, config, `{"operation":"query","collection":"users","limit":1000}`)
	columns := make([]dbrsdk.Column, 128)
	for i := range columns {
		columns[i] = dbrsdk.Column{Name: strings.Repeat("N", 128) + string(rune(0x100+i)), Type: "STRING"}
	}
	row := make(dbrsdk.Row, len(columns))
	for _, c := range columns {
		row[c.Name] = nil
	}
	rows := make([]dbrsdk.Row, 1000)
	for i := range rows {
		rows[i] = row
	}
	client := databricksSuccessfulClient()
	client.query = func(context.Context, dal.Query) (*dbrsdk.Result, error) {
		return &dbrsdk.Result{Rows: rows, Columns: columns}, nil
	}
	var out bytes.Buffer
	if err := deliverDatabricksRead(context.Background(), &out, client, func() {}, r); err == nil || out.Len() != 0 {
		t.Fatal("aggregate row/key/node budget bypass", err)
	}
}

func TestDatabricksOptionalDescriptorsAndMetadataBudget(t *testing.T) {
	ctx := context.Background()
	budget := databricksWireBudget{}
	unknown := budget.descriptor(ctx, nil)
	if budget.err != nil || unknown != nil {
		t.Fatal("unknown descriptor changed", budget.err)
	}
	prec, scale := int64(38), int64(10)
	columns, err := budget.columns(ctx, []dbrsdk.Column{{Name: "a", Type: "DECIMAL", Precision: &prec, Scale: &scale}})
	if err != nil || columns[0].Precision.Value != "38" || columns[0].Scale.Value != "10" {
		t.Fatal("descriptor exactness", err)
	}
	config := databricksTestConfig(t)
	config.Tables = make(map[string]dbrsdk.Table, 129)
	for i := 0; i < 129; i++ {
		config.Tables[string(rune(i+33))] = dbrsdk.Table{Name: "t", IDColumn: "id"}
	}
	if _, err := databricksMetadataResult(ctx, config); err == nil {
		t.Fatal("metadata table cap skipped")
	}
	config = databricksTestConfig(t)
	result, err := databricksMetadataResult(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	meta := result["metadata"].(map[string]any)
	if meta["warehouseId"] != "wh-read" || meta["catalog"] != "main" || meta["schema"] != "default" {
		t.Fatal("metadata fields incorrect")
	}
}

type databricksPartialWriter struct{ failure error }

func (w databricksPartialWriter) Write(p []byte) (int, error) { return len(p) / 2, w.failure }

type databricksCancelJSON struct{ cancel context.CancelFunc }

func (v databricksCancelJSON) MarshalJSON() ([]byte, error) {
	v.cancel()
	return []byte(`{"native":"staged"}`), nil
}

func TestDatabricksFinalEncoderCancellationRefusalAndWriterContract(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	if err := finishDatabricksOutput(ctx, &out, databricksCancelJSON{cancel: cancel}); err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal("serialized late cancellation committed output", err)
	}
	if err := finishDatabricksOutput(context.Background(), &out, make(chan int)); err == nil || out.Len() != 0 {
		t.Fatal("unsupported output encoding leaked/prefixed", err)
	}
	if err := finishDatabricksOutput(context.Background(), databricksPartialWriter{}, map[string]any{"a": "b"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write swallowed", err)
	}
	failure := errors.New("owned writer failed")
	if err := finishDatabricksOutput(context.Background(), databricksPartialWriter{failure: failure}, map[string]any{"a": "b"}); !errors.Is(err, failure) {
		t.Fatal("partial writer error replaced", err)
	}
}

func TestDatabricksPreAbortedWireConversionDoesNotCallProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := databricksTestClient{}
	request := admittedDatabricksRead{operation: "get", collection: "users", id: "a"}
	if _, err := executeDatabricksRead(ctx, client, request); !errors.Is(err, context.Canceled) {
		t.Fatal("preabort reached provider", err)
	}
	if _, err := executeDatabricksRead(context.Background(), client, admittedDatabricksRead{operation: "unknown"}); err == nil {
		t.Fatal("unknown operation accepted")
	}
}
