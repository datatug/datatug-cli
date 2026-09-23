package comparecache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
	"github.com/stretchr/testify/require"
)

func TestComparisonCachePagesMatchingRowsAndRollsBackObserverFailure(t *testing.T) {
	store := openTestStore(t, Options{ByteCap: 1 << 20, TTL: time.Hour})
	left := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "left-1"}
	right := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "right-1"}
	result := compareDemo(t, store, left, right)
	require.Equal(t, 1, result.Summary.Unchanged)
	require.Equal(t, 1, result.Summary.Changed)
	require.Equal(t, 1, result.Summary.Added)
	require.Equal(t, 1, result.Summary.Removed)

	page, err := store.Page(context.Background(), PageRequest{
		ComparisonID: ID(left, right),
		State:        recordsetcompare.RecordMatched,
		Limit:        10,
	})
	require.NoError(t, err)
	require.Len(t, page.Rows, 1)
	require.Equal(t, "matched", page.State)
	require.Equal(t, []string{"id"}, page.Key)
	require.False(t, page.Truncated)
	var compact map[string]any
	require.NoError(t, json.Unmarshal([]byte(mustRawPayload(t, store, ID(left, right), page.Rows[0].SortKey)), &compact))

	failing, err := store.Begin(context.Background(), BeginRequest{QueryID: "orders", Left: left, Right: right})
	require.NoError(t, err)
	require.Error(t, failing.Observer()(recordsetcompare.ComparedRecord{
		SortKey: "bad",
		State:   "not-a-state",
		Key:     []apicontract.TypedValue{apicontract.NewStringValue("x")},
		Row:     []apicontract.TypedValue{apicontract.NewStringValue("x")},
	}))
	failing.Abort()
	matched, err := store.Page(context.Background(), PageRequest{
		ComparisonID: ID(left, right),
		State:        recordsetcompare.RecordMatched,
	})
	require.NoError(t, err)
	require.Len(t, matched.Rows, 1)
}

func TestComparisonCacheObserverErrorRollsBackUnpublishedRows(t *testing.T) {
	store := openTestStore(t, Options{})
	left := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "left-2"}
	right := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "right-2"}
	session, err := store.Begin(context.Background(), BeginRequest{QueryID: "orders", Left: left, Right: right})
	require.NoError(t, err)
	observer := session.Observer()
	require.NoError(t, observer(recordsetcompare.ComparedRecord{
		SortKey: "a", State: recordsetcompare.RecordMatched,
		Key: []apicontract.TypedValue{apicontract.NewStringValue("1")},
		Row: []apicontract.TypedValue{apicontract.NewStringValue("1")},
	}))
	session.Abort()
	_, err = store.Lookup(context.Background(), ID(left, right))
	require.ErrorIs(t, err, ErrNotFound)
}

func TestComparisonCacheExpiresByTTLAndEvictsLRU(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store, err := Open(Options{PrivateDir: dir, ByteCap: 1 << 20, TTL: time.Hour, Now: func() time.Time { return now }})
	require.NoError(t, err)
	firstLeft := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "left-old"}
	firstRight := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "right-old"}
	compareDemo(t, store, firstLeft, firstRight)
	now = now.Add(2 * time.Hour)
	_, err = store.Lookup(context.Background(), ID(firstLeft, firstRight))
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.Close())

	now = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	store, err = Open(Options{PrivateDir: dir, ByteCap: 1 << 20, TTL: time.Hour, Now: func() time.Time { return now }})
	require.NoError(t, err)
	dropLeft := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "left-drop"}
	dropRight := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "right-drop"}
	keepLeft := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "left-keep"}
	keepRight := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "right-keep"}
	compareDemo(t, store, dropLeft, dropRight)
	_, err = store.Page(context.Background(), PageRequest{
		ComparisonID: ID(dropLeft, dropRight),
		State:        recordsetcompare.RecordMatched,
	})
	require.NoError(t, err)
	now = now.Add(time.Minute)
	compareDemo(t, store, keepLeft, keepRight)
	var keepSize int
	require.NoError(t, store.db.QueryRow(`SELECT byte_size FROM comparison_meta WHERE comparison_id = ?`, ID(keepLeft, keepRight)).Scan(&keepSize))
	require.Greater(t, keepSize, 0)
	require.NoError(t, store.Close())

	store, err = Open(Options{PrivateDir: dir, ByteCap: keepSize, TTL: time.Hour, Now: func() time.Time { return now }})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, err = store.Lookup(context.Background(), ID(dropLeft, dropRight))
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.Lookup(context.Background(), ID(keepLeft, keepRight))
	require.NoError(t, err)
}

func TestComparisonCacheStoresMinifiedJSONText(t *testing.T) {
	store := openTestStore(t, Options{})
	left := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "left-json"}
	right := apicontract.ExecutionRef{StoreID: "ops", ProjectID: "billing", ExecutionID: "right-json"}
	compareDemo(t, store, left, right)
	raw := mustRawPayload(t, store, ID(left, right), "")
	require.NotEmpty(t, raw)
	require.False(t, strings.Contains(raw, "\n"))
	require.False(t, strings.Contains(raw, "  "))
}

func openTestStore(t *testing.T, options Options) *Store {
	t.Helper()
	options.PrivateDir = t.TempDir()
	store, err := Open(options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	_, err = os.Stat(filepath.Join(options.PrivateDir, fileName))
	require.NoError(t, err)
	return store
}

func compareDemo(t *testing.T, store *Store, left, right apicontract.ExecutionRef) apicontract.CompareResult {
	t.Helper()
	leftSet, rightSet := twoSidedRecordsets()
	return compareWithCache(t, store, left, right, leftSet, rightSet)
}

func compareWithCache(t *testing.T, store *Store, left, right apicontract.ExecutionRef, leftSet, rightSet apicontract.Recordset) apicontract.CompareResult {
	t.Helper()
	session, err := store.Begin(context.Background(), BeginRequest{QueryID: "orders", Left: left, Right: right})
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := recordsetcompare.Compare(leftSet, rightSet, apicontract.CompareSideReceipt{
		Execution: left, ExecutedAt: now, RowCount: len(leftSet.Rows),
	}, apicontract.CompareSideReceipt{
		Execution: right, ExecutedAt: now, RowCount: len(rightSet.Rows),
	}, recordsetcompare.Options{Key: []string{"id"}, Observer: session.Observer()})
	if err != nil {
		session.Abort()
		require.NoError(t, err)
	}
	require.NoError(t, session.Commit(result))
	return result
}

func twoSidedRecordsets() (apicontract.Recordset, apicontract.Recordset) {
	columns := []apicontract.Column{
		{Name: "id", Type: string(apicontract.ValueTypeString)},
		{Name: "status", Type: string(apicontract.ValueTypeString)},
	}
	left := apicontract.Recordset{Columns: columns, Rows: [][]apicontract.TypedValue{
		{apicontract.NewStringValue("1"), apicontract.NewStringValue("same")},
		{apicontract.NewStringValue("2"), apicontract.NewStringValue("old")},
		{apicontract.NewStringValue("3"), apicontract.NewStringValue("gone")},
	}}
	right := apicontract.Recordset{Columns: columns, Rows: [][]apicontract.TypedValue{
		{apicontract.NewStringValue("1"), apicontract.NewStringValue("same")},
		{apicontract.NewStringValue("2"), apicontract.NewStringValue("new")},
		{apicontract.NewStringValue("4"), apicontract.NewStringValue("fresh")},
	}}
	return left, right
}

func mustRawPayload(t *testing.T, store *Store, comparisonID, sortKey string) string {
	t.Helper()
	query := `SELECT payload_json FROM comparison_rows WHERE comparison_id = ?`
	args := []any{comparisonID}
	if sortKey != "" {
		query += ` AND sort_key = ?`
		args = append(args, sortKey)
	} else {
		query += ` LIMIT 1`
	}
	var raw string
	require.NoError(t, store.db.QueryRow(query, args...).Scan(&raw))
	return raw
}
