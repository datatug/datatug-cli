package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	convexsdk "github.com/dal-go/dalgo2convex"
)

type convexBoundaryClient struct {
	row    *convexsdk.Row
	rows   []*convexsdk.Row
	exists bool
	page   convexsdk.Page
	err    error
	query  func(context.Context, convexsdk.PageOptions) (convexsdk.Page, error)
	close  func() error
}

func (c convexBoundaryClient) Get(context.Context, string, string) (*convexsdk.Row, error) {
	return c.row, c.err
}
func (c convexBoundaryClient) Exists(context.Context, string, string) (bool, error) {
	return c.exists, c.err
}
func (c convexBoundaryClient) GetMany(context.Context, string, []string) ([]*convexsdk.Row, error) {
	return c.rows, c.err
}
func (c convexBoundaryClient) QueryFunction(ctx context.Context, _ string, _ convexsdk.Object, p convexsdk.PageOptions) (convexsdk.Page, error) {
	if c.query != nil {
		return c.query(ctx, p)
	}
	return c.page, c.err
}
func (c convexBoundaryClient) Close() error { return c.close() }
func convexBoundaryRow(id string) *convexsdk.Row {
	return &convexsdk.Row{ID: id, Document: convexsdk.Object{"_id": convexsdk.String(id), "wide": convexsdk.Int64(math.MaxInt64)}}
}

func TestConvexNativeCodecAndOwnDataFieldsRemainLossless(t *testing.T) {
	for _, raw := range []string{`{}`, `{"wide":{"$integer":"/////////38="},"binary":{"$bytes":"AP+A"}}`, `{"nested":{"constructor":"own","prototype":true,"__proto__":{},"":null,"_data":"wire"}}`, `{"values":[true,null,"😀",{"$float":"AAAAAAAAAIA="}]}`} {
		object, err := convexInputObject(context.Background(), json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		data, err := convexEncodeObject(object)
		if err != nil {
			t.Fatal(err)
		}
		first, err := convexsdk.DecodeValue([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := convexsdk.EncodeValue(first)
		if err != nil || !bytes.Equal(data, expected) {
			t.Fatal("native scalar tag or ownfield changed")
		}
	}
	for _, raw := range []string{`[]`, `null`, `1`, `{"x":1,"x":2}`, `{"x":{"$integer":"AA=="}}`, `{"x":"\ud800"}`} {
		if _, err := convexInputObject(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatal("malformed/nonnative args admitted")
		}
	}
	object, err := convexInputObject(context.Background(), nil)
	if err != nil || len(object) != 0 {
		t.Fatal("absent args must be emptyobject")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := convexInputObject(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("input preabort lost")
	}
	if _, err := convexClientOptions(ctx, convexNativeConfig{Anonymous: true}, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal("constructor-options preabort lost")
	}
	var entered bool
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	metadata := dynamoCancelOnDeadlineContext{Context: parent, cancel: stop, entered: &entered}
	if _, err := convexInputObject(metadata, json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) || !entered {
		t.Fatal("post-codec caller cancellation lost")
	}
}

func TestConvexClosedOutputAdmissionBoundsAndPostCodecCancellation(t *testing.T) {
	for _, rows := range [][]*convexsdk.Row{make([]*convexsdk.Row, 1001), {{ID: ""}}, {{ID: strings.Repeat("a", 1025), Document: convexsdk.Object{}}}, {{ID: string([]byte{0xff}), Document: convexsdk.Object{}}}, {{ID: "a", Document: convexsdk.Object{"$bad": convexsdk.Null()}}}, {{ID: "a", Document: convexsdk.Object{"invalid": convexsdk.String(string([]byte{0xff}))}}}, {{ID: "a", Document: convexsdk.Object{"large": convexsdk.String(strings.Repeat("x", 17<<20))}}}} {
		if _, err := convexWireDocuments(context.Background(), rows); !errors.Is(err, errDynamoResponse) {
			t.Fatal("unbounded/invalid native output admitted")
		}
	}
	// Each native object is valid, but their aggregate encoded output is too large.
	data := convexsdk.Object{"large": convexsdk.String(strings.Repeat("<", 700000))}
	if _, err := convexWireDocuments(context.Background(), []*convexsdk.Row{{ID: "a", Document: data}, {ID: "b", Document: data}}); !errors.Is(err, errDynamoResponse) {
		t.Fatal("aggregate escaping budget bypass")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := convexWireDocuments(ctx, []*convexsdk.Row{nil}); !errors.Is(err, context.Canceled) {
		t.Fatal("output preabort lost")
	}
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	entered := false
	metadata := dynamoCancelOnDeadlineContext{Context: parent, cancel: stop, entered: &entered}
	if _, err := convexWireDocuments(metadata, []*convexsdk.Row{convexBoundaryRow("a")}); !errors.Is(err, context.Canceled) || !entered {
		t.Fatal("post-codec fresh delivery check lost")
	}
}

func TestConvexReadBoundaryClosesBeforeBufferedDeliveryAndRedacts(t *testing.T) {
	for _, operation := range []string{"get", "exists", "testConnection", "getMany", "query"} {
		t.Run(operation, func(t *testing.T) {
			closed := 0
			c := convexBoundaryClient{err: errors.New("PRIVATE_NATIVE_CAUSE"), close: func() error { closed++; return nil }}
			a := admittedConvexRead{request: convexNativeRequest{Operation: operation, Collection: "tasks", ID: "a", IDs: []string{"a"}, Query: "list", PageSize: 1, Pages: 1}}
			var out bytes.Buffer
			if err := deliverConvexRead(context.Background(), &out, c, a); err == nil || strings.Contains(err.Error(), "PRIVATE") || out.Len() != 0 || closed != 1 {
				t.Fatal("native failure leaked result/cause or ownedclient")
			}
		})
	}
	point := admittedConvexRead{request: convexNativeRequest{Operation: "get", Collection: "tasks", ID: "a"}}
	for _, cancelClose := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		closed := 0
		c := convexBoundaryClient{row: convexBoundaryRow("a"), close: func() error {
			closed++
			if cancelClose {
				cancel()
			}
			return errors.New("PRIVATE_CLOSE_CAUSE")
		}}
		var out bytes.Buffer
		err := deliverConvexRead(ctx, &out, c, point)
		cancel()
		if err == nil || closed != 1 || out.Len() != 0 || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("cleanup error leaked data/cause")
		}
		if cancelClose && !strings.Contains(err.Error(), "cancelled") {
			t.Fatal("cleanup cancellation priority lost")
		}
	}
	c := convexBoundaryClient{row: convexBoundaryRow("foreign"), close: func() error { return nil }}
	var out bytes.Buffer
	if err := deliverConvexRead(context.Background(), &out, c, point); err == nil || out.Len() != 0 {
		t.Fatal("foreign point identity admitted")
	}
	c.row = &convexsdk.Row{ID: "a"}
	if err := deliverConvexRead(context.Background(), &out, c, point); err == nil || out.Len() != 0 {
		t.Fatal("malformed native document admitted")
	}
	c.row = nil
	if err := deliverConvexRead(context.Background(), dynamoPartialWriter{}, c, point); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("shortwrite semantics lost")
	}
	writerErr := errors.New("owned writer failed")
	if err := deliverConvexRead(context.Background(), dynamoPartialWriter{err: writerErr}, c, point); !errors.Is(err, writerErr) {
		t.Fatal("writer error lost")
	}
}

func TestConvexBatchCardinalityAndWholePageBoundariesFailClosed(t *testing.T) {
	batch := admittedConvexRead{request: convexNativeRequest{Operation: "getMany", Collection: "tasks", IDs: []string{"a"}}}
	for _, rows := range [][]*convexsdk.Row{nil, {convexBoundaryRow("b")}} {
		c := convexBoundaryClient{rows: rows, close: func() error { return nil }}
		var out bytes.Buffer
		if err := deliverConvexRead(context.Background(), &out, c, batch); err == nil || out.Len() != 0 {
			t.Fatal("batch shape/identity admitted")
		}
	}
	query := admittedConvexRead{request: convexNativeRequest{Operation: "query", Query: "list", PageSize: 1, Pages: 2}}
	for _, p := range []convexsdk.Page{{Documents: []convexsdk.Row{*convexBoundaryRow("a"), *convexBoundaryRow("b")}, IsDone: true}, {IsDone: true, NextCursor: "private"}, {IsDone: false}} {
		c := convexBoundaryClient{page: p, close: func() error { return nil }}
		var out bytes.Buffer
		if err := deliverConvexRead(context.Background(), &out, c, query); err == nil || out.Len() != 0 {
			t.Fatal("malformed wholepage admitted")
		}
	}
	for _, repeat := range []bool{false, true} {
		calls := 0
		c := convexBoundaryClient{close: func() error { return nil }, query: func(_ context.Context, o convexsdk.PageOptions) (convexsdk.Page, error) {
			calls++
			cursor := "first"
			if calls == 2 && !repeat {
				cursor = "second"
			}
			return convexsdk.Page{Documents: []convexsdk.Row{*convexBoundaryRow("a")}, NextCursor: cursor}, nil
		}}
		var out bytes.Buffer
		if err := deliverConvexRead(context.Background(), &out, c, query); err == nil || out.Len() != 0 || calls != 2 {
			t.Fatal("repeatedhandle/duplicateID was acknowledged")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := convexBoundaryClient{close: func() error { return nil }, query: func(context.Context, convexsdk.PageOptions) (convexsdk.Page, error) {
		calls++
		cancel()
		return convexsdk.Page{NextCursor: "first"}, nil
	}}
	var out bytes.Buffer
	if err := deliverConvexRead(ctx, &out, c, query); err == nil || out.Len() != 0 || calls != 1 {
		t.Fatal("cancelled first page dispatched later work")
	}
}

func TestConvexAdmittedPointFinalCallerAndElapsedDeadline(t *testing.T) {
	config := convexNativeConfig{Collections: map[string]convexCollectionConfig{"tasks": {GetFunction: "dalgo:getTask"}}}
	request := convexNativeRequest{Operation: "get", Collection: "tasks", ID: "a"}
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		ctx      context.Context
		expected error
	}{{caller, context.Canceled}, {expired, context.DeadlineExceeded}} {
		if _, err := admitConvexRead(tc.ctx, config, request); !errors.Is(err, tc.expected) {
			t.Fatal("valid point admission lost caller/deadline priority")
		}
	}
}

func TestConvexFinalRendererObservesCancellationDuringCallerMetadata(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, closed := false, 0
	ctx := dynamoCancelOnDeadlineContext{Context: parent, cancel: cancel, entered: &entered}
	client := convexBoundaryClient{exists: true, close: func() error { closed++; return nil }}
	admitted := admittedConvexRead{request: convexNativeRequest{Operation: "exists", Collection: "tasks", ID: "a"}}
	var output bytes.Buffer
	err := deliverConvexRead(ctx, &output, client, admitted)
	if err == nil || !strings.Contains(err.Error(), "cancelled") || !entered || closed != 1 || output.Len() != 0 || !errors.Is(parent.Err(), context.Canceled) {
		t.Fatal("final output admitted caller cancellation or lost owned cleanup")
	}
}
