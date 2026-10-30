package endpoints

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

func TestDetectiveTransientRowsAreBoundedPrincipalScopedAndExpire(t *testing.T) {
	cache := &transientRowsCache{items: make(map[transientRowsKey]transientRowsEntry)}
	ref := apicontract.ExecutionRef{StoreID: "store", ProjectID: "project", ExecutionID: "execution"}
	rows := apicontract.Recordset{Columns: []apicontract.Column{{Name: "id", Type: string(apicontract.ValueTypeInteger)}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("7")}}}
	now := time.Now()
	cache.put(ref, "alice", rows, now)
	cache.put(ref, "alice", rows, now.Add(time.Second))
	if len(cache.items) != 1 || cache.bytes != len(cache.items[transientRowsKey{ref: ref, actor: "alice"}].encoded) {
		t.Fatalf("replacing a receipt corrupted cache accounting: items=%d bytes=%d", len(cache.items), cache.bytes)
	}
	if _, ok := cache.get(ref, "bob", now); ok {
		t.Fatal("transient rows crossed the principal boundary")
	}
	rows.Rows[0][0] = apicontract.NewIntegerValue("99")
	got, ok := cache.get(ref, "alice", now)
	if !ok || got.Rows[0][0].Str != "7" {
		t.Fatalf("transient rows aliased mutable caller bytes: %+v %v", got, ok)
	}
	if _, ok := cache.get(ref, "alice", now.Add(transientRowsTTL+time.Second)); ok {
		t.Fatal("expired transient rows remained readable")
	}
	if len(cache.items) != 0 || cache.bytes != 0 {
		t.Fatalf("expired transient rows still consume capacity: items=%d bytes=%d", len(cache.items), cache.bytes)
	}
	for i := 0; i <= transientRowsMaxEntries; i++ {
		current := ref
		current.ExecutionID = "execution-" + strconv.Itoa(i)
		cache.put(current, "alice", rows, now.Add(time.Duration(i)*time.Second))
	}
	if len(cache.items) > transientRowsMaxEntries || cache.bytes > transientRowsTotalBytes {
		t.Fatalf("transient cache exceeded its bounds: items=%d bytes=%d", len(cache.items), cache.bytes)
	}
	aboveDurableCap := apicontract.Recordset{Columns: []apicontract.Column{{Name: "text", Type: string(apicontract.ValueTypeString)}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue(strings.Repeat("x", 2<<20))}}}
	largeRef := ref
	largeRef.ExecutionID = "above-durable-cap"
	cache.put(largeRef, "alice", aboveDurableCap, now)
	if _, ok := cache.get(largeRef, "alice", now); !ok {
		t.Fatal("fresh exact rows above the durable snapshot cap were unavailable")
	}
	oversized := apicontract.Recordset{Columns: []apicontract.Column{{Name: "text", Type: string(apicontract.ValueTypeString)}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue(strings.Repeat("x", transientRowsEntryBytes))}}}
	before := len(cache.items)
	cache.put(ref, "alice", oversized, now)
	if len(cache.items) != before {
		t.Fatal("oversized result entered the transient row cache")
	}
}
