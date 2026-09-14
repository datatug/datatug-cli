package api

import (
	"context"
	"log"
	"sync"

	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

var (
	compareCacheMu sync.RWMutex
	compareCache   *comparecache.Store
)

// ConfigureCompareCache opens the optional private comparison-row sidecar.
// Cache data is never evidence: callers must re-authorize both snapshots
// before paging rows.
func ConfigureCompareCache(options comparecache.Options) error {
	store, err := comparecache.Open(options)
	if err != nil {
		return err
	}
	compareCacheMu.Lock()
	previous := compareCache
	compareCache = store
	compareCacheMu.Unlock()
	if previous != nil {
		return previous.Close()
	}
	return nil
}

func CloseCompareCache() error {
	compareCacheMu.Lock()
	store := compareCache
	compareCache = nil
	compareCacheMu.Unlock()
	if store == nil {
		return nil
	}
	return store.Close()
}

func BeginCompareCache(ctx context.Context, req comparecache.BeginRequest) (*comparecache.WriteSession, error) {
	compareCacheMu.RLock()
	store := compareCache
	compareCacheMu.RUnlock()
	if store == nil {
		return nil, nil
	}
	return store.Begin(ctx, req)
}

func LookupCompareCache(ctx context.Context, comparisonID string) (comparecache.Meta, error) {
	compareCacheMu.RLock()
	store := compareCache
	compareCacheMu.RUnlock()
	if store == nil {
		return comparecache.Meta{}, comparecache.ErrNotConfigured
	}
	return store.Lookup(ctx, comparisonID)
}

func PageCompareCache(ctx context.Context, req comparecache.PageRequest) (comparecache.Page, error) {
	compareCacheMu.RLock()
	store := compareCache
	compareCacheMu.RUnlock()
	if store == nil {
		return comparecache.Page{}, comparecache.ErrNotConfigured
	}
	return store.Page(ctx, req)
}

func LogCompareCacheSkip(op string, err error) {
	if err == nil {
		return
	}
	log.Printf("compare cache %s skipped: %v", op, err)
}

func ComparisonCacheID(left, right apicontract.ExecutionRef) string {
	return comparecache.ID(left, right)
}
