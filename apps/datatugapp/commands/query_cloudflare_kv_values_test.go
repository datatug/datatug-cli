package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	kv "github.com/dal-go/dalgo2cloudflarekv"
)

type cloudflareKVBoundaryClient struct {
	entry   kv.Entry
	entries []kv.Entry
	exists  bool
	page    kv.ListPage
	err     error
	list    func(context.Context, kv.ListOptions) (kv.ListPage, error)
	close   func() error
}

func (c cloudflareKVBoundaryClient) Get(context.Context, string) (kv.Entry, error) {
	return c.entry, c.err
}
func (c cloudflareKVBoundaryClient) Exists(context.Context, string) (bool, error) {
	return c.exists, c.err
}
func (c cloudflareKVBoundaryClient) GetMany(context.Context, []string) ([]kv.Entry, error) {
	return c.entries, c.err
}
func (c cloudflareKVBoundaryClient) ListKeys(ctx context.Context, request kv.ListOptions) (kv.ListPage, error) {
	if c.list != nil {
		return c.list(ctx, request)
	}
	return c.page, c.err
}
func (c cloudflareKVBoundaryClient) Close() error {
	if c.close != nil {
		return c.close()
	}
	return nil
}

type cloudflareKVOutputFunc func([]byte) (int, error)

func (f cloudflareKVOutputFunc) Write(b []byte) (int, error) { return f(b) }
func cloudflareKVBoundaryConfig() cloudflareKVConfig {
	return cloudflareKVConfig{CollectionAlias: "cache", AllowedPrefixes: []string{"tenant/"}}
}
func cloudflareKVBoundaryRequest(operation string) cloudflareKVRequest {
	prefix := "tenant/"
	r := cloudflareKVRequest{Operation: operation, Collection: "cache"}
	switch operation {
	case "get", "exists", "testConnection":
		r.Key = "tenant/a"
	case "getMany":
		r.Keys = []string{"tenant/a"}
	case "list":
		r.Prefix = &prefix
		r.PageSize = 10
		r.Pages = 2
	}
	return r
}

func TestCloudflareKVLosslessWireTextTagsAndClosedNativeValues(t *testing.T) {
	config := cloudflareKVBoundaryConfig()
	zero := uint64(0)
	maximum := ^uint64(0)
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[true,null,9007199254740993.000]`), json.RawMessage(cloudflareKVFixtureMetadata), json.RawMessage(`"<native>&😀"`)} {
		budget := cloudflareKVWireBudget{}
		value, err := budget.key(context.Background(), config, kv.KeyInfo{Name: "tenant/a", Metadata: raw, Expiration: &maximum})
		if err != nil || value.Expiration == nil || value.Expiration.Value != "18446744073709551615" {
			t.Fatal("native metadata/expiry refused", err)
		}
		if raw == nil {
			if value.Metadata != nil {
				t.Fatal("absent metadata changed")
			}
		} else if value.Metadata == nil || value.Metadata.Kind != "json" || value.Metadata.Value != string(raw) {
			t.Fatal("JSON number lexemes/null/own fields normalized")
		}
	}
	budget := cloudflareKVWireBudget{}
	entry, err := budget.entry(context.Background(), config, kv.Entry{Key: "tenant/a", Value: []byte{}, Expiration: &zero})
	if err != nil || entry.Value.Kind != "bytes" || entry.Value.Base64 != "" || entry.Expiration.Value != "0" {
		t.Fatal("present empty bytes/zeroexpiry", err)
	}
	if cloudflareKVExpiry(nil) != nil {
		t.Fatal("absent expiry changed")
	}
	for _, raw := range []json.RawMessage{json.RawMessage{}, json.RawMessage(`{"x":1,"x":2}`), json.RawMessage(`"\ud800"`), json.RawMessage(`{"missing":}`), json.RawMessage(strings.Repeat(" ", 1025))} {
		budget := cloudflareKVWireBudget{}
		if _, err := budget.key(context.Background(), config, kv.KeyInfo{Name: "tenant/a", Metadata: raw}); !errors.Is(err, errDynamoResponse) {
			t.Fatal("malformed metadata accepted")
		}
	}
	for _, entry := range []kv.Entry{{Key: "foreign", Value: []byte{}}, {Key: string([]byte{0xff}), Value: []byte{}}, {Key: "tenant/a", Value: nil}, {Key: "tenant/a", Value: make([]byte, (1<<20)+1)}} {
		budget := cloudflareKVWireBudget{}
		if _, err := budget.entry(context.Background(), config, entry); !errors.Is(err, errDynamoResponse) {
			t.Fatal("malformed raw entry accepted")
		}
	}
	budget = cloudflareKVWireBudget{}
	if _, err := budget.key(context.Background(), config, kv.KeyInfo{Name: "foreign"}); !errors.Is(err, errDynamoResponse) {
		t.Fatal("foreign metadata name accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := budget.entry(ctx, config, kv.Entry{Key: "tenant/a", Value: []byte{}}); !errors.Is(err, context.Canceled) {
		t.Fatal("entry preabort")
	}
	if _, err := budget.key(ctx, config, kv.KeyInfo{Name: "tenant/a"}); !errors.Is(err, context.Canceled) {
		t.Fatal("metadata preabort")
	}
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	entered := false
	callback := dynamoCancelOnDeadlineContext{Context: parent, cancel: stop, entered: &entered}
	if _, err := budget.entry(callback, config, kv.Entry{Key: "tenant/a", Value: []byte{}}); !errors.Is(err, context.Canceled) || !entered {
		t.Fatal("entry final caller metadata freshness")
	}
	parent2, stop2 := context.WithCancel(context.Background())
	defer stop2()
	entered2 := false
	callback2 := dynamoCancelOnDeadlineContext{Context: parent2, cancel: stop2, entered: &entered2}
	if _, err := budget.key(callback2, config, kv.KeyInfo{Name: "tenant/a"}); !errors.Is(err, context.Canceled) || !entered2 {
		t.Fatal("metadata final caller freshness")
	}
}

func TestCloudflareKVReadBoundaryAlwaysClosesBothOwnedResourcesBeforeOutput(t *testing.T) {
	config := cloudflareKVBoundaryConfig()
	for _, operation := range []string{"get", "exists", "testConnection", "getMany", "list"} {
		t.Run(operation, func(t *testing.T) {
			closed, transportClosed := 0, 0
			c := cloudflareKVBoundaryClient{err: errors.New("PRIVATE_NATIVE_CAUSE"), close: func() error { closed++; return nil }}
			var out bytes.Buffer
			err := deliverCloudflareKVRead(context.Background(), &out, c, func() { transportClosed++ }, config, cloudflareKVBoundaryRequest(operation))
			if err == nil || strings.Contains(err.Error(), "PRIVATE") || out.Len() != 0 || closed != 1 || transportClosed != 1 {
				t.Fatal("error leaked/skipped owned cleanup")
			}
		})
	}
	request := cloudflareKVBoundaryRequest("get")
	for _, cancelAt := range []string{"none", "client", "transport"} {
		ctx, cancel := context.WithCancel(context.Background())
		closed, transportClosed := 0, 0
		c := cloudflareKVBoundaryClient{entry: kv.Entry{Key: "tenant/a", Value: []byte{}}, close: func() error {
			closed++
			if cancelAt == "client" {
				cancel()
			}
			return errors.New("PRIVATE_CLOSE_CAUSE")
		}}
		var out bytes.Buffer
		err := deliverCloudflareKVRead(ctx, &out, c, func() {
			transportClosed++
			if cancelAt == "transport" {
				cancel()
			}
		}, config, request)
		cancel()
		if err == nil || out.Len() != 0 || closed != 1 || transportClosed != 1 || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("close failure cleanup/classification")
		}
		if cancelAt != "none" && !strings.Contains(err.Error(), "cancelled") {
			t.Fatal("cleanup cancellation priority lost")
		}
	}
	closed, transportClosed := false, false
	c := cloudflareKVBoundaryClient{entry: kv.Entry{Key: "tenant/a", Value: []byte{0, 255}}, close: func() error { closed = true; return nil }}
	var out bytes.Buffer
	writer := cloudflareKVOutputFunc(func(p []byte) (int, error) {
		if !closed || !transportClosed {
			t.Error("stdout preceded resource close")
		}
		return out.Write(p)
	})
	if err := deliverCloudflareKVRead(context.Background(), writer, c, func() { transportClosed = true }, config, request); err != nil || out.Len() == 0 {
		t.Fatal("successful output cleanup order", err)
	}
	if err := deliverCloudflareKVRead(context.Background(), dynamoPartialWriter{}, c, func() {}, config, request); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("partial write lost", err)
	}
	writeErr := errors.New("owned output failed")
	if err := deliverCloudflareKVRead(context.Background(), dynamoPartialWriter{err: writeErr}, c, func() {}, config, request); !errors.Is(err, writeErr) {
		t.Fatal("ordinary writer error lost", err)
	}
}

func TestCloudflareKVFalseExistsCardinalityAndWholePageFaultsFailClosed(t *testing.T) {
	config := cloudflareKVBoundaryConfig()
	cases := []struct {
		request cloudflareKVRequest
		client  cloudflareKVBoundaryClient
	}{
		{cloudflareKVBoundaryRequest("exists"), cloudflareKVBoundaryClient{}}, {cloudflareKVBoundaryRequest("testConnection"), cloudflareKVBoundaryClient{}},
		{cloudflareKVBoundaryRequest("get"), cloudflareKVBoundaryClient{entry: kv.Entry{Key: "foreign", Value: []byte{}}}},
		{cloudflareKVBoundaryRequest("getMany"), cloudflareKVBoundaryClient{}}, {cloudflareKVBoundaryRequest("getMany"), cloudflareKVBoundaryClient{entries: []kv.Entry{{Key: "foreign", Value: []byte{}}}}},
		{cloudflareKVBoundaryRequest("list"), cloudflareKVBoundaryClient{page: kv.ListPage{Complete: true, NextCursor: "PRIVATE"}}},
		{cloudflareKVBoundaryRequest("list"), cloudflareKVBoundaryClient{}}, {cloudflareKVBoundaryRequest("list"), cloudflareKVBoundaryClient{page: kv.ListPage{Keys: make([]kv.KeyInfo, 11), Complete: true}}},
		{cloudflareKVBoundaryRequest("list"), cloudflareKVBoundaryClient{page: kv.ListPage{Keys: []kv.KeyInfo{{Name: "foreign"}}, Complete: true}}},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		if err := deliverCloudflareKVRead(context.Background(), &out, tc.client, func() {}, config, tc.request); err == nil || out.Len() != 0 || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("malformed native result or false existence delivered")
		}
	}
	for _, repeatedCursor := range []bool{false, true} {
		calls := 0
		c := cloudflareKVBoundaryClient{list: func(context.Context, kv.ListOptions) (kv.ListPage, error) {
			calls++
			cursor := "one"
			if calls == 2 && !repeatedCursor {
				cursor = "two"
			}
			return kv.ListPage{Keys: []kv.KeyInfo{{Name: "tenant/a"}}, NextCursor: cursor}, nil
		}}
		var out bytes.Buffer
		if err := deliverCloudflareKVRead(context.Background(), &out, c, func() {}, config, cloudflareKVBoundaryRequest("list")); err == nil || out.Len() != 0 || calls != 2 {
			t.Fatal("duplicate crosspage key/repeated cursor ACKed")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c := cloudflareKVBoundaryClient{list: func(context.Context, kv.ListOptions) (kv.ListPage, error) {
		calls++
		cancel()
		return kv.ListPage{NextCursor: "one"}, nil
	}}
	var out bytes.Buffer
	if err := deliverCloudflareKVRead(ctx, &out, c, func() {}, config, cloudflareKVBoundaryRequest("list")); err == nil || out.Len() != 0 || calls != 1 {
		t.Fatal("later page after caller revocation")
	}
}

func TestCloudflareKVWholeRawAndEscapedOutputBudgetsRefuseWithoutPrefix(t *testing.T) {
	config := cloudflareKVBoundaryConfig()
	request := cloudflareKVBoundaryRequest("getMany")
	request.Keys = []string{}
	entries := []kv.Entry{}
	shared := make([]byte, 1<<20)
	for i := range 5 {
		key := fmt.Sprintf("tenant/%d", i)
		request.Keys = append(request.Keys, key)
		entries = append(entries, kv.Entry{Key: key, Value: shared})
	}
	var out bytes.Buffer
	if err := deliverCloudflareKVRead(context.Background(), &out, cloudflareKVBoundaryClient{entries: entries}, func() {}, config, request); err == nil || out.Len() != 0 {
		t.Fatal("shared large value occurrences bypassed aggregate raw cap")
	}
	request = cloudflareKVBoundaryRequest("list")
	request.PageSize = 1000
	request.Pages = 1
	keys := make([]kv.KeyInfo, 1000)
	sharedMetadata := json.RawMessage(`"` + strings.Repeat("<", 1000) + `"`)
	for i := range keys {
		keys[i] = kv.KeyInfo{Name: fmt.Sprintf("tenant/%04d", i) + strings.Repeat("<", 490), Metadata: sharedMetadata}
	}
	if err := deliverCloudflareKVRead(context.Background(), &out, cloudflareKVBoundaryClient{page: kv.ListPage{Keys: keys, Complete: true}}, func() {}, config, request); err == nil || out.Len() != 0 {
		t.Fatal("escaped output budget emitted partial listing")
	}
}

func TestCloudflareKVValidAdmissionAndFinalRendererObserveRealCallerRevocation(t *testing.T) {
	config := cloudflareKVBoundaryConfig()
	request := cloudflareKVBoundaryRequest("get")
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	if err := admitCloudflareKVRead(caller, config, &request); !errors.Is(err, context.Canceled) {
		t.Fatal("point final admission cancellation")
	}
	ctx, cancelBatch := context.WithCancel(context.Background())
	cancelBatch()
	batch := cloudflareKVBoundaryRequest("getMany")
	if err := admitCloudflareKVRead(ctx, config, &batch); !errors.Is(err, context.Canceled) {
		t.Fatal("batch per-key freshness")
	}
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	entered := false
	metadata := dynamoCancelOnDeadlineContext{Context: parent, cancel: stop, entered: &entered}
	closed, transportClosed := false, false
	c := cloudflareKVBoundaryClient{exists: true, close: func() error { closed = true; return nil }}
	var out bytes.Buffer
	err := deliverCloudflareKVRead(metadata, &out, c, func() { transportClosed = true }, config, cloudflareKVBoundaryRequest("exists"))
	if err == nil || !strings.Contains(err.Error(), "cancelled") || !entered || !closed || !transportClosed || out.Len() != 0 || !errors.Is(parent.Err(), context.Canceled) {
		t.Fatal("post-serialization stdout guard missed actual caller metadata cancellation")
	}
	expired, stopExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stopExpired()
	request = cloudflareKVBoundaryRequest("get")
	if err := admitCloudflareKVRead(expired, config, &request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("elapsed deadline admission")
	}
}

func TestCloudflareKVUnknownOperationAndRevokedBatchNeverDeliver(t *testing.T) {
	config := cloudflareKVBoundaryConfig()
	request := cloudflareKVBoundaryRequest("get")
	request.Operation = "delete"
	if err := admitCloudflareKVRead(context.Background(), config, &request); !errors.Is(err, errDynamoInput) {
		t.Fatal("unknown operation admitted")
	}

	for _, operation := range []string{"get", "getMany"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			closed, transportClosed := 0, 0
			client := cloudflareKVBoundaryClient{
				entry:   kv.Entry{Key: "tenant/a", Value: []byte{0, 255}},
				entries: []kv.Entry{{Key: "tenant/a", Value: []byte{0, 255}}},
				close:   func() error { closed++; return nil },
			}
			var output bytes.Buffer
			err := deliverCloudflareKVRead(ctx, &output, client, func() { transportClosed++ }, config, cloudflareKVBoundaryRequest(operation))
			if err == nil || !strings.Contains(err.Error(), "cancelled") || output.Len() != 0 || closed != 1 || transportClosed != 1 {
				t.Fatal("revoked read delivered native bytes or skipped owned cleanup")
			}
		})
	}
}
