package endpoints

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// A successful recorded query can be checked against its exact rows during
// the current agent session without opting into long-lived snapshot storage.
// These bytes never leave process memory; the execution record remains the
// durable authority for identity, completeness, and the row fingerprint.
const (
	transientRowsTTL        = 5 * time.Minute
	transientRowsEntryBytes = 8 << 20
	transientRowsTotalBytes = 32 << 20
	transientRowsMaxEntries = 32
)

type transientRowsKey struct {
	ref   apicontract.ExecutionRef
	actor string
}

type transientRowsEntry struct {
	encoded []byte
	at      time.Time
}

type transientRowsCache struct {
	mu    sync.Mutex
	items map[transientRowsKey]transientRowsEntry
	bytes int
}

var detectiveTransientRows = &transientRowsCache{items: make(map[transientRowsKey]transientRowsEntry)}

func (c *transientRowsCache) put(ref apicontract.ExecutionRef, actor string, rows apicontract.Recordset, now time.Time) {
	if ref.Validate() != nil || actor == "" || rows.Validate() != nil {
		return
	}
	encoded, err := json.Marshal(rows)
	if err != nil || len(encoded) > transientRowsEntryBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	key := transientRowsKey{ref: ref, actor: actor}
	if prior, ok := c.items[key]; ok {
		c.bytes -= len(prior.encoded)
		delete(c.items, key)
	}
	for len(c.items) >= transientRowsMaxEntries || c.bytes+len(encoded) > transientRowsTotalBytes {
		var oldest transientRowsKey
		var oldestAt time.Time
		for candidate, item := range c.items {
			if oldestAt.IsZero() || item.at.Before(oldestAt) {
				oldest, oldestAt = candidate, item.at
			}
		}
		if oldestAt.IsZero() {
			break
		}
		c.bytes -= len(c.items[oldest].encoded)
		delete(c.items, oldest)
	}
	c.items[key] = transientRowsEntry{encoded: encoded, at: now}
	c.bytes += len(encoded)
}

func (c *transientRowsCache) get(ref apicontract.ExecutionRef, actor string, now time.Time) (apicontract.Recordset, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	item, ok := c.items[transientRowsKey{ref: ref, actor: actor}]
	if !ok {
		return apicontract.Recordset{}, false
	}
	var rows apicontract.Recordset
	if json.Unmarshal(item.encoded, &rows) != nil || rows.Validate() != nil {
		return apicontract.Recordset{}, false
	}
	return rows, true
}

func (c *transientRowsCache) prune(now time.Time) {
	for key, item := range c.items {
		if !now.Before(item.at.Add(transientRowsTTL)) {
			c.bytes -= len(item.encoded)
			delete(c.items, key)
		}
	}
}
