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
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/dal-go/dalgo2dynamodb"
)

type dynamoErrorReader struct{}

func (dynamoErrorReader) Read([]byte) (int, error) { return 0, errors.New("secret reader failure") }

type dynamoObservedReader struct {
	reader  io.Reader
	started chan struct{}
	once    bool
}

func (r *dynamoObservedReader) Read(p []byte) (int, error) {
	if !r.once {
		r.once = true
		close(r.started)
	}
	return r.reader.Read(p)
}

func TestDynamoInputDeadlineAndBorrowedReaderOwnership(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := readDynamoDocument(ctx, &dynamoObservedReader{reader: reader, started: started})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("input read was not entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("borrowed input cancellation delayed")
	}
	// It remains the owner's pipe: cancellation must not close shared stdin.
	written := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("{}")); _ = writer.Close(); written <- err }()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal("borrowed stdin was closed")
		}
	case <-time.After(time.Second):
		t.Fatal("owned reader did not finish")
	}
	if _, err := readDynamoDocument(context.Background(), nil); !errors.Is(err, errDynamoReadInput) {
		t.Fatal(err)
	}
	if _, err := readDynamoDocument(context.Background(), dynamoErrorReader{}); !errors.Is(err, errDynamoReadInput) {
		t.Fatal("reader cause escaped")
	}
	if _, err := readDynamoDocument(context.Background(), strings.NewReader(strings.Repeat(" ", dynamoDocumentBytes+1))); !errors.Is(err, errDynamoInput) {
		t.Fatal("input cap missing")
	}
	if _, err := readDynamoDocument(ctx, strings.NewReader("{}")); !errors.Is(err, context.Canceled) {
		t.Fatal("preabort not observed")
	}
}

func TestDynamoInputAdmissionRemainsBoundedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make([]chan error, 2)
	writers := make([]*io.PipeWriter, 2)
	for i := 0; i < 2; i++ {
		reader, writer := io.Pipe()
		writers[i] = writer
		t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
		started := make(chan struct{})
		done[i] = make(chan error, 1)
		go func(result chan error) {
			_, err := readDynamoDocument(ctx, &dynamoObservedReader{reader: reader, started: started})
			result <- err
		}(done[i])
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("owned input did not occupy admission")
		}
	}
	cancel()
	for _, result := range done {
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(750 * time.Millisecond):
			t.Fatal("cancelled input caller did not return")
		}
	}
	// A full queue must also honor an elapsed deadline before its owner has
	// delivered a cancellation notification, without waiting on unfinished I/O.
	elapsedParent, cancelElapsed := context.WithCancel(context.Background())
	defer cancelElapsed()
	elapsed := dynamoPendingDeadlineContext{Context: elapsedParent, deadline: time.Now().Add(-time.Second)}
	elapsedDone := make(chan error, 1)
	go func() { _, err := readDynamoDocument(elapsed, dynamoErrorReader{}); elapsedDone <- err }()
	select {
	case err := <-elapsedDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("full queue lost elapsed deadline")
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("elapsed deadline waited on full input queue")
	}
	wait, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stop()
	if _, err := readDynamoDocument(wait, dynamoErrorReader{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unfinished borrowed reads freed admission too early")
	}
	for _, writer := range writers {
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	reopen, finish := context.WithTimeout(context.Background(), time.Second)
	defer finish()
	if data, err := readDynamoDocument(reopen, strings.NewReader("{}")); err != nil || string(data) != "{}" {
		t.Fatal("input admission did not reopen")
	}
}

func TestDynamoStrictUnicodeAndDuplicateDocuments(t *testing.T) {
	for _, input := range []string{
		`{"operation":"unfinished\`,
		`{"operation":"\u`,
		`{"operation":"get",1:"invalid object key"}`,
		`{"operation":"get","operation":"query"}`,
		`{"key":{"account":{"S":"one","S":"two"}}}`,
		`{"operation":"\ud800"}`, `{"operation":"\udc00"}`,
		`{"operation":"\ud800\u0000"}`, `{"operation":"\ud800X"}`,
		`{"operation":"\uZZZZ"}`, `{"operation":"\u123"}`,
		"{\"operation\":\"" + string([]byte{0xff}) + "\"}",
		`{"operation":"\x00"}`, `{"operation":} `, `{"operation":true}`,
		`{"operation":"get"} true`, "", `null`,
		`{"operation":"get","unknownSecret":"secret"}`,
		`{"operation":"get","key":{"x":{"S":` + strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40) + "}}}",
	} {
		var request dynamoNativeRequest
		if err := decodeDynamoDocument([]byte(input), &request); !errors.Is(err, errDynamoInput) {
			t.Fatal("malformed/ambiguous document accepted")
		}
	}
	for _, input := range []string{`{"operation":"\u0041"}`, `{"operation":"\ud83d\ude00"}`, `{"operation":"\\ud800"}`, `{"operation":"escaped\"quote"}`, `{"operation":"🌍"}`} {
		var request dynamoNativeRequest
		if err := decodeDynamoDocument([]byte(input), &request); err != nil {
			t.Fatal("well-formed Unicode was rejected")
		}
	}
}

func TestDynamoNativeScalarKeysAndWireRefusals(t *testing.T) {
	table := dalgo2dynamodb.Table{Name: "events", PartitionKey: "p", SortKey: "s"}
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	for _, key := range []dynamoKeyJSON{
		{"p": {"S": raw(`"tenant"`)}},
		{"p": {"S": raw(`"tenant"`)}, "foreign": {"N": raw(`"1"`)}},
		{"p": {"S": raw(`"tenant"`)}, "s": {"BOOL": raw("true")}},
		{"p": {"S": raw(`"tenant"`)}, "s": {"N": raw(`"1e999999"`)}},
		{"p": {"S": raw(`"tenant"`)}, "s": {"B": raw(`""`)}},
		{"p": {"S": raw(`"tenant"`)}, "s": {"N": raw("123")}},
	} {
		if _, err := decodeDynamoKey(key, table); !errors.Is(err, errDynamoInput) {
			t.Fatal("invalid native key accepted")
		}
	}
	valid := dynamoKeyJSON{"p": {"S": raw(`"tenant"`)}, "s": {"N": raw(`"9007199254740993.000"`)}}
	decoded, err := decodeDynamoKey(valid, table)
	if err != nil || decoded["s"].(*types.AttributeValueMemberN).Value != "9007199254740993.000" {
		t.Fatal("native number spelling changed")
	}
	for _, text := range []string{"NaN", "1e999999", "1e-131", strings.Repeat("1", 129), ""} {
		if dynamoNumber(text) {
			t.Fatal("invalid number accepted")
		}
	}
	for _, text := range []string{"-1.25e+3", "+1.000", ".5", "9007199254740993"} {
		if !dynamoNumber(text) {
			t.Fatal("valid decimal rejected")
		}
	}
	var typedNil *types.AttributeValueMemberS
	invalid := []types.AttributeValue{nil, typedNil, &types.AttributeValueMemberS{Value: string([]byte{0xff})}, &types.AttributeValueMemberN{Value: "NaN"}, &types.AttributeValueMemberNULL{Value: false}, (*types.AttributeValueMemberB)(nil), (*types.AttributeValueMemberBOOL)(nil), (*types.AttributeValueMemberM)(nil), (*types.AttributeValueMemberL)(nil), &types.AttributeValueMemberSS{}, &types.AttributeValueMemberSS{Value: []string{string([]byte{0xff})}}, &types.AttributeValueMemberNS{}, &types.AttributeValueMemberNS{Value: []string{"NaN"}}, &types.AttributeValueMemberBS{}}
	invalid = append(invalid, &types.AttributeValueMemberL{Value: []types.AttributeValue{nil}})
	for _, value := range invalid {
		if _, err := dynamoWireItem(dalgo2dynamodb.Item{"a": value}); !errors.Is(err, errDynamoResponse) {
			t.Fatal("unknown/malformed wire value accepted")
		}
	}
	deep := types.AttributeValue(&types.AttributeValueMemberNULL{Value: true})
	for i := 0; i < 35; i++ {
		deep = &types.AttributeValueMemberM{Value: dalgo2dynamodb.Item{"nested": deep}}
	}
	if _, err := dynamoWireItem(dalgo2dynamodb.Item{"a": deep}); !errors.Is(err, errDynamoResponse) {
		t.Fatal("wire depth cap missing")
	}
	if _, err := dynamoWireItem(dalgo2dynamodb.Item{"": &types.AttributeValueMemberBOOL{Value: true}}); !errors.Is(err, errDynamoResponse) {
		t.Fatal("invalid native name accepted")
	}
	wide := make(dalgo2dynamodb.Item)
	for i := range 10_001 {
		wide[strconv.Itoa(i)] = &types.AttributeValueMemberBOOL{Value: true}
	}
	if _, err := dynamoWireItem(wide); !errors.Is(err, errDynamoResponse) {
		t.Fatal("oversize native object accepted")
	}
	if item, err := dynamoWireItem(nil); err != nil || item != nil {
		t.Fatal("missing item changed")
	}
	if _, err := dynamoWireItems([]dalgo2dynamodb.Item{{"a": nil}}); !errors.Is(err, errDynamoResponse) {
		t.Fatal("wire batch ignored invalid item")
	}
	var output bytes.Buffer
	if err := writeDynamoResult(context.Background(), &output, map[string]any{"B": ""}); err != nil {
		t.Fatal(err)
	}
}

func TestDynamoAdmissionRefusesMixedModesAndUnboundedRequests(t *testing.T) {
	config := dynamoNativeConfig{Tables: map[string]dalgo2dynamodb.Table{"items": {Name: "items", PartitionKey: "account"}, "events": {Name: "events", PartitionKey: "account", SortKey: "sequence"}}}
	for _, document := range []string{
		`{"operation":"query","collection":"events","partition":{},"limit":1}`,
		`{"operation":"query","collection":"events","partition":{"SS":"not a scalar"},"limit":1}`,
		`{"operation":"get","collection":"items","key":{"account":{"S":"x"}},"limit":1}`,
		`{"operation":"get","collection":"items","key":{"account":{"S":"x"}},"keys":[]}`,
		`{"operation":"getMany","collection":"items"}`,
		`{"operation":"getMany","collection":"items","keys":[{"wrong":{"S":"x"}}]}`,
		`{"operation":"query","collection":"events","partition":{"BOOL":true},"limit":1}`,
		`{"operation":"query","collection":"items","partition":{"S":"x"},"limit":1,"sort":{"operator":"=","values":[{"N":"1"}]}}`,
		`{"operation":"query","collection":"events","partition":{"S":"x"},"limit":1,"sort":{"operator":"between","values":[{"N":"1"}]}}`,
		`{"operation":"query","collection":"events","partition":{"S":"x"},"limit":1,"sort":{"operator":"=","values":[{"BOOL":true}]}}`,
		`{"operation":"query","collection":"events","partition":{"S":"x"},"limit":1,"sort":{"operator":"begins_with","values":[{"N":"1"}]}}`,
		`{"operation":"query","collection":"events","partition":{"S":"x"},"limit":1,"sort":{"operator":"between","values":[{"N":"2"},{"N":"1"}]}}`,
		`{"operation":"query","collection":"events","partition":{"S":"x"},"limit":1,"sort":{"operator":"between","values":[{"N":"1"},{"S":"2"}]}}`,
	} {
		var request dynamoNativeRequest
		if err := decodeDynamoDocument([]byte(document), &request); err != nil {
			t.Fatal(err)
		}
		if _, err := admitDynamoRead(config, request); !errors.Is(err, errDynamoInput) {
			t.Fatal("mixed or unsafe request admitted")
		}
	}
	for _, operator := range []string{"=", "<", "<=", ">", ">=", "begins_with", "between"} {
		document := `{"operation":"query","collection":"events","partition":{"S":"x"},"limit":1,"sort":{"operator":"` + operator + `","values":[{"N":"1"}]}}`
		if operator == "between" {
			document = strings.Replace(document, `[{"N":"1"}]`, `[{"N":"1"},{"N":"2"}]`, 1)
		}
		if operator == "begins_with" {
			document = strings.Replace(document, `[{"N":"1"}]`, `[{"S":"prefix"}]`, 1)
		}
		var request dynamoNativeRequest
		if err := decodeDynamoDocument([]byte(document), &request); err != nil {
			t.Fatal(err)
		}
		if _, err := admitDynamoRead(config, request); err != nil {
			t.Fatal("native sort mapping rejected")
		}
	}
	for _, values := range []string{`[{"S":"a"},{"S":"z"}]`, `[{"B":"AQ=="},{"B":"Ag=="}]`} {
		document := `{"operation":"query","collection":"events","partition":{"S":"x"},"limit":1,"sort":{"operator":"between","values":` + values + `}}`
		var request dynamoNativeRequest
		if err := decodeDynamoDocument([]byte(document), &request); err != nil {
			t.Fatal(err)
		}
		if _, err := admitDynamoRead(config, request); err != nil {
			t.Fatal("native string/binary order changed")
		}
	}
}
