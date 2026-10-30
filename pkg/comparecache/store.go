// Package comparecache is a server-private SQLite convenience store for
// comparison rows. It is not evidence or authority: every page read must
// re-authorize the retained snapshots that produced the comparison.
package comparecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
	_ "modernc.org/sqlite"
)

const (
	DefaultByteCap = 32 << 20
	DefaultTTL     = 24 * time.Hour
	DefaultLimit   = 50
	fileName       = "comparisons.sqlite"
)

var (
	ErrNotConfigured = errors.New("comparison cache is not configured")
	ErrNotFound      = errors.New("cached comparison not found")
)

// Options are trusted server configuration, never request-controlled.
type Options struct {
	PrivateDir string
	ByteCap    int
	TTL        time.Duration
	Now        func() time.Time
}

// ID derives the cache key from the two execution receipts already present
// on CompareResult. Core does not mint a separate comparison identifier.
func ID(left, right apicontract.ExecutionRef) string {
	return left.StoreID + "/" + left.ProjectID + "/" + left.ExecutionID + "|" +
		right.StoreID + "/" + right.ProjectID + "/" + right.ExecutionID
}

type Store struct {
	db      *sql.DB
	byteCap int
	ttl     time.Duration
	now     func() time.Time
	mu      chan struct{}
}

var (
	resolvePrivateDir = executionstore.ResolvePrivateDir
	statFile          = os.Stat
	chmod             = os.Chmod
	sqlOpen           = sql.Open
	dbExec            = func(db *sql.DB, query string) (sql.Result, error) { return db.Exec(query) }
	jsonMarshal       = json.Marshal
	txCommit          = func(tx *sql.Tx) error { return tx.Commit() }
	dbBeginTx         = func(db *sql.DB, ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
		return db.BeginTx(ctx, opts)
	}
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		return tx.ExecContext(ctx, query, args...)
	}
	dbExecContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		return db.ExecContext(ctx, query, args...)
	}
	rowsErrHook        = func(rows *sql.Rows) error { return rows.Err() }
	rowsCloseHook      = func(rows *sql.Rows) error { return rows.Close() }
	pragmaTableInfoSQL = `PRAGMA table_info(comparison_meta)`
	pageRowsSQL        = `SELECT sort_key, payload_json FROM comparison_rows
 WHERE comparison_id = ? AND state = ? AND sort_key > ?
 ORDER BY sort_key LIMIT ?`
	sumBytesSQL     = `SELECT COALESCE(SUM(byte_size), 0) FROM comparison_meta`
	selectVictimSQL = `SELECT comparison_id, byte_size FROM comparison_meta ORDER BY last_access_at_ns ASC, comparison_id ASC LIMIT 1`
)

func Open(options Options) (*Store, error) {
	privateDir, err := resolvePrivateDir(options.PrivateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve comparison cache directory: %w", err)
	}
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create comparison cache directory: %w", err)
	}
	if err := chmod(privateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure comparison cache directory: %w", err)
	}
	path := filepath.Join(privateDir, fileName)
	if _, err := statFile(path); err == nil {
		if err := chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure comparison cache file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat comparison cache file: %w", err)
	}
	db, err := sqlOpen("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open comparison cache: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err = dbExec(db, `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure comparison cache: %w", err)
	}
	if _, err = dbExec(db, `CREATE TABLE IF NOT EXISTS comparison_meta (
		comparison_id TEXT PRIMARY KEY,
		query_id TEXT NOT NULL,
		policy_fingerprint TEXT NOT NULL DEFAULT '',
		left_json TEXT NOT NULL,
		right_json TEXT NOT NULL,
		columns_json TEXT NOT NULL,
		key_json TEXT NOT NULL,
		byte_size INTEGER NOT NULL,
		created_at_ns INTEGER NOT NULL,
		last_access_at_ns INTEGER NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create comparison_meta: %w", err)
	}
	if err := migratePolicyFingerprint(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = dbExec(db, `CREATE TABLE IF NOT EXISTS comparison_rows (
		comparison_id TEXT NOT NULL,
		sort_key TEXT NOT NULL,
		state TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		PRIMARY KEY (comparison_id, sort_key)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create comparison_rows: %w", err)
	}
	if _, err = dbExec(db, `CREATE INDEX IF NOT EXISTS comparison_rows_id_sort
		ON comparison_rows (comparison_id, sort_key)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create comparison_rows sort index: %w", err)
	}
	if _, err = dbExec(db, `CREATE INDEX IF NOT EXISTS comparison_rows_id_state_sort
		ON comparison_rows (comparison_id, state, sort_key)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create comparison_rows state index: %w", err)
	}
	if err := chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure comparison cache file: %w", err)
	}
	byteCap := options.ByteCap
	if byteCap == 0 {
		byteCap = DefaultByteCap
	}
	if byteCap < 0 {
		_ = db.Close()
		return nil, errors.New("comparison cache byte cap must not be negative")
	}
	ttl := options.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < 0 {
		_ = db.Close()
		return nil, errors.New("comparison cache TTL must not be negative")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, byteCap: byteCap, ttl: ttl, now: now, mu: make(chan struct{}, 1)}, nil
}

// Older optional caches predate policy binding; their empty fingerprint fails
// closed at the API boundary until the comparison is recomputed.
func migratePolicyFingerprint(db *sql.DB) error {
	rows, err := db.Query(pragmaTableInfoSQL)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		found = found || name == "policy_fingerprint"
	}
	if err := rowsErrHook(rows); err != nil {
		return err
	}
	if err := rowsCloseHook(rows); err != nil {
		return err
	}
	if !found {
		_, err = db.Exec(`ALTER TABLE comparison_meta ADD COLUMN policy_fingerprint TEXT NOT NULL DEFAULT ''`)
	}
	return err
}

func (s *Store) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.mu <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) unlock() { <-s.mu }

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

type BeginRequest struct {
	PolicyFingerprint string
	QueryID           string
	Left              apicontract.ExecutionRef
	Right             apicontract.ExecutionRef
}

type WriteSession struct {
	store             *Store
	tx                *sql.Tx
	comparisonID      string
	queryID           string
	left              apicontract.ExecutionRef
	right             apicontract.ExecutionRef
	bytes             int
	done              bool
	ctx               context.Context
	policyFingerprint string
}

func (s *Store) Begin(ctx context.Context, req BeginRequest) (*WriteSession, error) {
	if s == nil {
		return nil, ErrNotConfigured
	}
	if err := req.Left.Validate(); err != nil {
		return nil, fmt.Errorf("left execution: %w", err)
	}
	if err := req.Right.Validate(); err != nil {
		return nil, fmt.Errorf("right execution: %w", err)
	}
	if strings.TrimSpace(req.QueryID) == "" {
		return nil, errors.New("query id is required")
	}
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	tx, err := dbBeginTx(s.db, ctx, nil)
	if err != nil {
		s.unlock()
		return nil, fmt.Errorf("begin comparison cache write: %w", err)
	}
	id := ID(req.Left, req.Right)
	if _, err := txExecContext(tx, ctx, `DELETE FROM comparison_rows WHERE comparison_id = ?`, id); err != nil {
		_ = tx.Rollback()
		s.unlock()
		return nil, fmt.Errorf("clear comparison rows: %w", err)
	}
	if _, err := txExecContext(tx, ctx, `DELETE FROM comparison_meta WHERE comparison_id = ?`, id); err != nil {
		_ = tx.Rollback()
		s.unlock()
		return nil, fmt.Errorf("clear comparison meta: %w", err)
	}
	return &WriteSession{
		store: s, tx: tx, comparisonID: id, queryID: req.QueryID,
		left: req.Left, right: req.Right, ctx: ctx, policyFingerprint: req.PolicyFingerprint,
	}, nil
}

func (s *WriteSession) Observer() recordsetcompare.RecordObserver {
	return func(record recordsetcompare.ComparedRecord) error {
		if s == nil || s.done {
			return errors.New("comparison cache write session is closed")
		}
		if !validState(record.State) {
			return fmt.Errorf("unsupported comparison row state %q", record.State)
		}
		payload, err := marshalRowPayload(record)
		if err != nil {
			return err
		}
		// Include indexed keys and state in the bounded logical storage size.
		rowBytes := len(payload) + len(record.SortKey) + len(record.State)
		if rowBytes > s.store.byteCap-s.bytes {
			s.Abort()
			return fmt.Errorf("comparison cache exceeds byte cap %d", s.store.byteCap)
		}
		if _, err := txExecContext(s.tx, s.ctx,
			`INSERT INTO comparison_rows (comparison_id, sort_key, state, payload_json) VALUES (?, ?, ?, ?)`,
			s.comparisonID, record.SortKey, string(record.State), payload,
		); err != nil {
			return fmt.Errorf("insert comparison row: %w", err)
		}
		s.bytes += rowBytes
		return nil
	}
}

func (s *WriteSession) Commit(result apicontract.CompareResult) error {
	if s == nil || s.done {
		return errors.New("comparison cache write session is closed")
	}
	if s.bytes > s.store.byteCap {
		s.Abort()
		return fmt.Errorf("comparison cache row bytes %d exceed cap %d", s.bytes, s.store.byteCap)
	}
	columnsJSON, err := jsonMarshal(result.Columns)
	if err != nil {
		s.Abort()
		return fmt.Errorf("marshal comparison columns: %w", err)
	}
	keyJSON, err := jsonMarshal(result.Key)
	if err != nil {
		s.Abort()
		return fmt.Errorf("marshal comparison key: %w", err)
	}
	leftJSON, err := jsonMarshal(s.left)
	if err != nil {
		s.Abort()
		return fmt.Errorf("marshal left execution: %w", err)
	}
	rightJSON, err := jsonMarshal(s.right)
	if err != nil {
		s.Abort()
		return fmt.Errorf("marshal right execution: %w", err)
	}
	metadataBytes := len(columnsJSON) + len(keyJSON) + len(leftJSON) + len(rightJSON) + len(s.queryID) + len(s.comparisonID) + len(s.policyFingerprint)
	if metadataBytes > s.store.byteCap-s.bytes {
		s.Abort()
		return fmt.Errorf("comparison cache exceeds byte cap %d", s.store.byteCap)
	}
	s.bytes += metadataBytes
	now := s.store.now().UTC().UnixNano()
	if _, err := txExecContext(s.tx, s.ctx,
		`INSERT INTO comparison_meta (
			comparison_id, query_id, policy_fingerprint, left_json, right_json, columns_json, key_json,
			byte_size, created_at_ns, last_access_at_ns
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.comparisonID, s.queryID, s.policyFingerprint, string(leftJSON), string(rightJSON),
		string(columnsJSON), string(keyJSON), s.bytes, now, now,
	); err != nil {
		s.Abort()
		return fmt.Errorf("insert comparison meta: %w", err)
	}
	if err := txCommit(s.tx); err != nil {
		s.done = true
		s.store.unlock()
		return fmt.Errorf("commit comparison cache: %w", err)
	}
	s.done = true
	err = s.store.evictLocked(s.ctx)
	s.store.unlock()
	return err
}

func (s *WriteSession) Abort() {
	if s == nil || s.done {
		return
	}
	_ = s.tx.Rollback()
	s.done = true
	s.store.unlock()
}

type PageRequest struct {
	PolicyFingerprint string
	ComparisonID      string
	State             recordsetcompare.RecordState
	After             string
	Limit             int
}

type CachedDelta struct {
	Column string                 `json:"column"`
	Value  apicontract.TypedValue `json:"value,omitempty"`
	Absent bool                   `json:"absent,omitempty"`
}

type CachedRow struct {
	SortKey string                   `json:"sortKey"`
	Key     []apicontract.TypedValue `json:"key"`
	Row     []apicontract.TypedValue `json:"row"`
	Deltas  []CachedDelta            `json:"deltas,omitempty"`
}

type Page struct {
	ComparisonID string                   `json:"comparisonId"`
	State        string                   `json:"state"`
	QueryID      string                   `json:"queryId"`
	Left         apicontract.ExecutionRef `json:"left"`
	Right        apicontract.ExecutionRef `json:"right"`
	Columns      []apicontract.Column     `json:"columns"`
	Key          []string                 `json:"key"`
	Rows         []CachedRow              `json:"rows"`
	NextSortKey  string                   `json:"nextSortKey,omitempty"`
	Truncated    bool                     `json:"truncated"`
}

type Meta struct {
	PolicyFingerprint string
	ComparisonID      string
	QueryID           string
	Left              apicontract.ExecutionRef
	Right             apicontract.ExecutionRef
	Columns           []apicontract.Column
	Key               []string
}

func (s *Store) Lookup(ctx context.Context, comparisonID string) (Meta, error) {
	if s == nil {
		return Meta{}, ErrNotConfigured
	}
	if err := s.lock(ctx); err != nil {
		return Meta{}, err
	}
	defer s.unlock()
	if err := s.evictLocked(ctx); err != nil {
		return Meta{}, err
	}
	return s.lookupLocked(ctx, comparisonID)
}

func (s *Store) Page(ctx context.Context, req PageRequest) (Page, error) {
	if s == nil {
		return Page{}, ErrNotConfigured
	}
	if req.ComparisonID == "" {
		return Page{}, errors.New("comparison id is required")
	}
	if !validState(req.State) {
		return Page{}, fmt.Errorf("unsupported comparison row state %q", req.State)
	}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > apicontract.CompareMaximumLimit {
		return Page{}, fmt.Errorf("limit must be between 1 and %d", apicontract.CompareMaximumLimit)
	}
	if err := s.lock(ctx); err != nil {
		return Page{}, err
	}
	defer s.unlock()
	if err := s.evictLocked(ctx); err != nil {
		return Page{}, err
	}
	// Use one SQLite snapshot for metadata and payloads. A per-Store lock
	// cannot exclude another server process replacing the same comparison.
	tx, err := dbBeginTx(s.db, ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Page{}, fmt.Errorf("begin comparison page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	meta, err := lookupComparison(ctx, tx, req.ComparisonID)
	if err != nil {
		return Page{}, err
	}
	if meta.PolicyFingerprint != req.PolicyFingerprint {
		return Page{}, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, pageRowsSQL,
		req.ComparisonID, string(req.State), req.After, limit+1,
	)
	if err != nil {
		return Page{}, fmt.Errorf("page comparison rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := Page{
		ComparisonID: req.ComparisonID,
		State:        string(req.State),
		QueryID:      meta.QueryID,
		Left:         meta.Left,
		Right:        meta.Right,
		Columns:      meta.Columns,
		Key:          meta.Key,
		Rows:         []CachedRow{},
	}
	for rows.Next() {
		var sortKey, payloadJSON string
		if err := rows.Scan(&sortKey, &payloadJSON); err != nil {
			return Page{}, fmt.Errorf("scan comparison row: %w", err)
		}
		if len(page.Rows) == limit {
			page.NextSortKey = page.Rows[len(page.Rows)-1].SortKey
			page.Truncated = true
			break
		}
		payload, err := unmarshalRowPayload(payloadJSON)
		if err != nil {
			return Page{}, err
		}
		page.Rows = append(page.Rows, CachedRow{
			SortKey: sortKey, Key: payload.Key, Row: payload.Row, Deltas: payload.Deltas,
		})
	}
	if err := rowsErrHook(rows); err != nil {
		return Page{}, fmt.Errorf("iterate comparison rows: %w", err)
	}
	if err := rowsCloseHook(rows); err != nil {
		return Page{}, fmt.Errorf("close comparison rows: %w", err)
	}
	if err := txCommit(tx); err != nil {
		return Page{}, fmt.Errorf("finish comparison page: %w", err)
	}
	now := s.now().UTC().UnixNano()
	if _, err := dbExecContext(s.db, ctx, `UPDATE comparison_meta SET last_access_at_ns = ? WHERE comparison_id = ?`, now, req.ComparisonID); err != nil {
		return Page{}, fmt.Errorf("touch comparison cache: %w", err)
	}
	return page, nil
}

type metadataQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) lookupLocked(ctx context.Context, comparisonID string) (Meta, error) {
	return lookupComparison(ctx, s.db, comparisonID)
}

func lookupComparison(ctx context.Context, db metadataQuerier, comparisonID string) (Meta, error) {
	var queryID, policyFingerprint, leftJSON, rightJSON, columnsJSON, keyJSON string
	err := db.QueryRowContext(ctx,
		`SELECT query_id, policy_fingerprint, left_json, right_json, columns_json, key_json FROM comparison_meta WHERE comparison_id = ?`,
		comparisonID,
	).Scan(&queryID, &policyFingerprint, &leftJSON, &rightJSON, &columnsJSON, &keyJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Meta{}, ErrNotFound
	}
	if err != nil {
		return Meta{}, fmt.Errorf("lookup comparison cache: %w", err)
	}
	meta := Meta{ComparisonID: comparisonID, QueryID: queryID, PolicyFingerprint: policyFingerprint}
	if err := json.Unmarshal([]byte(leftJSON), &meta.Left); err != nil {
		return Meta{}, fmt.Errorf("decode left execution: %w", err)
	}
	if err := json.Unmarshal([]byte(rightJSON), &meta.Right); err != nil {
		return Meta{}, fmt.Errorf("decode right execution: %w", err)
	}
	if err := json.Unmarshal([]byte(columnsJSON), &meta.Columns); err != nil {
		return Meta{}, fmt.Errorf("decode columns: %w", err)
	}
	if err := json.Unmarshal([]byte(keyJSON), &meta.Key); err != nil {
		return Meta{}, fmt.Errorf("decode key: %w", err)
	}
	return meta, nil
}

func (s *Store) evictLocked(ctx context.Context) error {
	// Expiry and LRU delete payloads and metadata atomically, including
	// against other Store instances sharing the private SQLite file.
	tx, err := dbBeginTx(s.db, ctx, nil)
	if err != nil {
		return fmt.Errorf("begin comparison eviction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cutoff := s.now().UTC().Add(-s.ttl).UnixNano()
	if _, err := txExecContext(tx, ctx, `DELETE FROM comparison_rows WHERE comparison_id IN (
		SELECT comparison_id FROM comparison_meta WHERE created_at_ns < ?
	)`, cutoff); err != nil {
		return fmt.Errorf("expire comparison rows: %w", err)
	}
	if _, err := txExecContext(tx, ctx, `DELETE FROM comparison_meta WHERE created_at_ns < ?`, cutoff); err != nil {
		return fmt.Errorf("expire comparison meta: %w", err)
	}
	var total int
	if err := tx.QueryRowContext(ctx, sumBytesSQL).Scan(&total); err != nil {
		return fmt.Errorf("sum comparison cache bytes: %w", err)
	}
	for total > s.byteCap {
		var id string
		var size int
		err := tx.QueryRowContext(ctx, selectVictimSQL).Scan(&id, &size)
		if errors.Is(err, sql.ErrNoRows) {
			return txCommit(tx)
		}
		if err != nil {
			return fmt.Errorf("select comparison cache LRU victim: %w", err)
		}
		if _, err := txExecContext(tx, ctx, `DELETE FROM comparison_rows WHERE comparison_id = ?`, id); err != nil {
			return fmt.Errorf("evict comparison rows: %w", err)
		}
		if _, err := txExecContext(tx, ctx, `DELETE FROM comparison_meta WHERE comparison_id = ?`, id); err != nil {
			return fmt.Errorf("evict comparison meta: %w", err)
		}
		total -= size
	}
	return txCommit(tx)
}

type rowPayload struct {
	Key    []apicontract.TypedValue `json:"key"`
	Row    []apicontract.TypedValue `json:"row"`
	Deltas []CachedDelta            `json:"deltas,omitempty"`
}

func marshalRowPayload(record recordsetcompare.ComparedRecord) (string, error) {
	payload := rowPayload{Key: record.Key, Row: record.Row}
	if record.State == recordsetcompare.RecordChanged {
		payload.Deltas = make([]CachedDelta, len(record.Deltas))
		for i, delta := range record.Deltas {
			payload.Deltas[i] = CachedDelta{Column: delta.Column, Value: delta.Value, Absent: delta.Absent}
		}
	}
	body, err := jsonMarshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal comparison row: %w", err)
	}
	if strings.Contains(string(body), "\n") || strings.Contains(string(body), "\t") {
		return "", errors.New("comparison row JSON must be minified TEXT")
	}
	return string(body), nil
}

func unmarshalRowPayload(raw string) (rowPayload, error) {
	var payload rowPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return rowPayload{}, fmt.Errorf("decode comparison row: %w", err)
	}
	return payload, nil
}

func validState(state recordsetcompare.RecordState) bool {
	switch state {
	case recordsetcompare.RecordMatched, recordsetcompare.RecordAdded, recordsetcompare.RecordRemoved, recordsetcompare.RecordChanged:
		return true
	default:
		return false
	}
}
