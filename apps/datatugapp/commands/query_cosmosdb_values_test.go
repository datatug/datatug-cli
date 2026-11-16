package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/dal-go/dalgo2cosmosdb"
)

type cosmosBoundaryClient struct {
	document  *dalgo2cosmosdb.Document
	documents []*dalgo2cosmosdb.Document
	page      dalgo2cosmosdb.Page
	exists    bool
	err       error
	close     func() error
}

func (c cosmosBoundaryClient) Get(context.Context, dalgo2cosmosdb.NativeKey, ...dalgo2cosmosdb.ReadOptions) (*dalgo2cosmosdb.Document, error) {
	return c.document, c.err
}
func (c cosmosBoundaryClient) Exists(context.Context, dalgo2cosmosdb.NativeKey, ...dalgo2cosmosdb.ReadOptions) (bool, error) {
	return c.exists, c.err
}
func (c cosmosBoundaryClient) GetMany(context.Context, []dalgo2cosmosdb.NativeKey) ([]*dalgo2cosmosdb.Document, error) {
	return c.documents, c.err
}
func (c cosmosBoundaryClient) Query(context.Context, dalgo2cosmosdb.Query) (dalgo2cosmosdb.Page, error) {
	return c.page, c.err
}
func (c cosmosBoundaryClient) Close() error { return c.close() }

func TestCosmosParameterNumbersAndNativeJSONOutputContract(t *testing.T) {
	for _, raw := range []string{`null`, `true`, `"native"`, `9007199254740993.000`, `-1.2500e+17`, `[1,null,{"value":2}]`, `{"nested":{"value":false}}`} {
		value, err := cosmosInputValue(json.RawMessage(raw))
		if err != nil {
			t.Fatal("valid native JSON parameter refused")
		}
		encoded, err := json.Marshal(value)
		if err != nil || !bytes.Equal(encoded, []byte(raw)) {
			t.Fatal("parameter lexeme/shape changed")
		}
	}
	for _, raw := range []string{"", `NaN`, `{"x":1,"x":2}`, `"\ud800"`, `null null`, `{"unknown":`} {
		if _, err := cosmosInputValue(json.RawMessage(raw)); err == nil {
			t.Fatal("malformed parameter accepted")
		}
	}
	data := map[string]any{"id": "a", "tenant": "p", "amount": json.Number("9007199254740993.000"), "nested": []any{nil, true, false, json.Number("-1"), "<native>&", map[string]any{"n": json.Number("1e99999")}}}
	rows, err := cosmosWireDocuments(context.Background(), []*dalgo2cosmosdb.Document{{Key: dalgo2cosmosdb.NativeKey{ID: "a", Partition: "p"}, Data: data}, nil})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeDynamoResult(context.Background(), &output, rows); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"key":{"id":"a","partition":"p"}`, `"value":"9007199254740993.000"`, `"value":"-1"`, `"value":"1e99999"`, `[null,true,false,`, `\u003cnative\u003e\u0026`, `},null]`} {
		if !strings.Contains(output.String(), required) {
			t.Fatal("lossless native scalar/nested contract changed")
		}
	}
}

func TestCosmosOutputRefusesUnknownCyclicDeepAndOversizeWhole(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	deep := any(nil)
	for range 34 {
		deep = []any{deep}
	}
	for _, value := range []any{
		float64(1), json.RawMessage(`1`), []byte("opaque"), json.Number("NaN"), json.Number(strings.Repeat("1", 129)), string([]byte{0xff}), strings.Repeat("x", (1<<20)+1),
		map[string]any{string([]byte{0xff}): nil}, map[string]any{strings.Repeat("k", (1<<20)+1): nil},
		make([]any, 65536), cycle, deep,
		[]any{strings.Repeat("<", 700000), strings.Repeat("<", 700000)},
		map[string]any{"nested": []any{float64(1)}},
	} {
		_, err := cosmosWireDocuments(context.Background(), []*dalgo2cosmosdb.Document{{Key: dalgo2cosmosdb.NativeKey{ID: "a", Partition: "p"}, Data: map[string]any{"value": value}}})
		if !errors.Is(err, errDynamoResponse) {
			t.Fatal("unsupported or over-budget document escaped closed output admission")
		}
	}
	for _, documents := range [][]*dalgo2cosmosdb.Document{
		make([]*dalgo2cosmosdb.Document, 1001),
		{{Key: dalgo2cosmosdb.NativeKey{ID: string([]byte{0xff})}}},
		{{Key: dalgo2cosmosdb.NativeKey{Partition: string([]byte{0xff})}}},
	} {
		if _, err := cosmosWireDocuments(context.Background(), documents); !errors.Is(err, errDynamoResponse) {
			t.Fatal("malformed document boundary accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cosmosWireDocuments(ctx, []*dalgo2cosmosdb.Document{{Data: map[string]any{}}}); !errors.Is(err, context.Canceled) {
		t.Fatal("conversion preabort lost")
	}
	// Aggregate escaping counts repeated references per occurrence, not once
	// per string identity; a hostile decoded map cannot bypass the wire budget.
	largeKey := strings.Repeat("<", 700000)
	if _, err := cosmosWireDocuments(context.Background(), []*dalgo2cosmosdb.Document{{Data: map[string]any{largeKey: map[string]any{largeKey: nil}}}}); !errors.Is(err, errDynamoResponse) {
		t.Fatal("aggregate map-key expansion bypassed budget")
	}
}

func TestCosmosOutputClosesOnceBeforeDeliveryAndPreservesErrors(t *testing.T) {
	key := &cosmosNativeKey{ID: "a", Partition: "p"}
	for _, operation := range []string{"get", "exists", "testConnection", "getMany", "query"} {
		t.Run(operation, func(t *testing.T) {
			a := admittedCosmosRead{request: cosmosNativeRequest{Operation: operation, Key: key}, keys: []dalgo2cosmosdb.NativeKey{{ID: "a", Partition: "p"}}}
			closed := 0
			client := cosmosBoundaryClient{err: errors.New("private SQL/token/URL cause"), close: func() error { closed++; return nil }}
			var output bytes.Buffer
			if err := deliverCosmosRead(context.Background(), &output, client, a); err == nil || output.Len() != 0 || strings.Contains(err.Error(), "private") || closed != 1 {
				t.Fatal("provider failure exposed partial output/cause or leaked client")
			}
		})
	}
	a := admittedCosmosRead{request: cosmosNativeRequest{Operation: "get", Key: key}}
	for _, cancelOnClose := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		closed := 0
		client := cosmosBoundaryClient{close: func() error {
			closed++
			if cancelOnClose {
				cancel()
			}
			return errors.New("private close cause")
		}}
		var output bytes.Buffer
		err := deliverCosmosRead(ctx, &output, client, a)
		cancel()
		if err == nil || closed != 1 || output.Len() != 0 || strings.Contains(err.Error(), "private") {
			t.Fatal("close failure leaked result/cause")
		}
		if cancelOnClose && !strings.Contains(err.Error(), "cancelled") {
			t.Fatal("caller cancellation during cleanup lost priority")
		}
	}
	client := cosmosBoundaryClient{close: func() error { return nil }}
	var output bytes.Buffer
	batch := admittedCosmosRead{request: cosmosNativeRequest{Operation: "getMany"}, keys: []dalgo2cosmosdb.NativeKey{{ID: "a", Partition: "p"}}}
	if err := deliverCosmosRead(context.Background(), &output, client, batch); err == nil || output.Len() != 0 {
		t.Fatal("batch cardinality mismatch delivered partial result")
	}
	client.document = &dalgo2cosmosdb.Document{Data: map[string]any{"unsupported": float64(1)}}
	if err := deliverCosmosRead(context.Background(), &output, client, a); err == nil || output.Len() != 0 {
		t.Fatal("unsafe point result delivered")
	}
	client.documents = []*dalgo2cosmosdb.Document{client.document}
	if err := deliverCosmosRead(context.Background(), &output, client, batch); err == nil || output.Len() != 0 {
		t.Fatal("unsafe batch result delivered")
	}
	client.page = dalgo2cosmosdb.Page{Documents: client.documents}
	if err := deliverCosmosRead(context.Background(), &output, client, admittedCosmosRead{request: cosmosNativeRequest{Operation: "query"}}); err == nil || output.Len() != 0 {
		t.Fatal("unsafe query result delivered")
	}
	client.document = nil
	if err := deliverCosmosRead(context.Background(), dynamoPartialWriter{}, client, a); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write contract changed")
	}
	writerErr := errors.New("owned writer failed")
	if err := deliverCosmosRead(context.Background(), dynamoPartialWriter{err: writerErr}, client, a); !errors.Is(err, writerErr) {
		t.Fatal("writer error contract changed")
	}
}

// A native JSON object has a different resource shape from an array. Both
// hostile field cardinality and aggregate escaped-string/number-tag expansion
// must fail before any partial result reaches stdout, retaining caller input.
func TestCosmosAggregateNativeObjectsAndNumberTagsRefuseWholeOutput(t *testing.T) {
	fields := make(map[string]any, 65536)
	for i := range 65536 {
		fields[strconv.Itoa(i)] = nil
	}
	mixed := []any{strings.Repeat("<", 700000), strings.Repeat("<", 690000)}
	wide := json.Number("1" + strings.Repeat("0", 127))
	for range 512 {
		mixed = append(mixed, wide)
	}
	for _, data := range []map[string]any{fields, {"values": mixed}} {
		closed := 0
		client := cosmosBoundaryClient{document: &dalgo2cosmosdb.Document{Key: dalgo2cosmosdb.NativeKey{ID: "a", Partition: "p"}, Data: data}, close: func() error { closed++; return nil }}
		var output bytes.Buffer
		admitted := admittedCosmosRead{request: cosmosNativeRequest{Operation: "get", Key: &cosmosNativeKey{ID: "a", Partition: "p"}}}
		if err := deliverCosmosRead(context.Background(), &output, client, admitted); err == nil || output.Len() != 0 || closed != 1 {
			t.Fatal("aggregate native output bypassed admission or leaked owned client")
		}
	}
	if len(fields) != 65536 || len(mixed) != 514 || mixed[2] != wide {
		t.Fatal("failed conversion mutated borrowed native values")
	}
}

func TestCosmosFinalRendererObservesCallerMetadataCancellationAfterClose(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := false
	// Deadline is caller-owned metadata. It performs real cancellation; Err
	// remains the ordinary parent context's Err, without synthetic call counts.
	ctx := dynamoCancelOnDeadlineContext{Context: parent, cancel: cancel, entered: &entered}
	closed := 0
	client := cosmosBoundaryClient{close: func() error { closed++; return nil }}
	admitted := admittedCosmosRead{request: cosmosNativeRequest{Operation: "get", Key: &cosmosNativeKey{ID: "missing", Partition: "p"}}}
	var output bytes.Buffer
	err := deliverCosmosRead(ctx, &output, client, admitted)
	if err == nil || !strings.Contains(err.Error(), "cancelled") || !errors.Is(parent.Err(), context.Canceled) || !entered || closed != 1 || output.Len() != 0 {
		t.Fatal("final renderer delivered after caller cancellation or repeated cleanup")
	}
}
