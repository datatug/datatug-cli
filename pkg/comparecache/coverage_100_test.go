package comparecache

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/recordsetcompare"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Not parallel: mutates global seams (resolvePrivateDir, statFile, chmod, sqlOpen, dbExec)
func TestOpen_Errors_And_Seams(t *testing.T) {
	// resolvePrivateDir error
	origResolve := resolvePrivateDir
	resolvePrivateDir = func(string) (string, error) { return "", errors.New("resolve fail") }
	_, err := Open(Options{})
	resolvePrivateDir = origResolve
	assert.ErrorContains(t, err, "resolve comparison cache directory")

	// MkdirAll error (file exists in place of directory)
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "isfile")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))
	_, err = Open(Options{PrivateDir: filepath.Join(filePath, "sub")})
	assert.ErrorContains(t, err, "create comparison cache directory")

	// chmod dir error
	origChmod := chmod
	chmod = func(name string, mode os.FileMode) error {
		if mode == 0o700 {
			return errors.New("chmod dir fail")
		}
		return os.Chmod(name, mode)
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	chmod = origChmod
	assert.ErrorContains(t, err, "secure comparison cache directory")

	// statFile error (not ErrNotExist)
	origStat := statFile
	statFile = func(name string) (os.FileInfo, error) {
		return nil, errors.New("stat error")
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	statFile = origStat
	assert.ErrorContains(t, err, "stat comparison cache file")

	// sqlOpen error
	origSqlOpen := sqlOpen
	sqlOpen = func(driverName, dataSourceName string) (*sql.DB, error) {
		return nil, errors.New("sql open fail")
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	sqlOpen = origSqlOpen
	assert.ErrorContains(t, err, "open comparison cache")

	// dbExec failure on PRAGMA
	origDbExec := dbExec
	dbExec = func(db *sql.DB, query string) (sql.Result, error) {
		if query[:6] == "PRAGMA" {
			return nil, errors.New("pragma fail")
		}
		return db.Exec(query)
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	dbExec = origDbExec
	assert.ErrorContains(t, err, "configure comparison cache")

	// dbExec failure on CREATE TABLE comparison_meta
	dbExec = func(db *sql.DB, query string) (sql.Result, error) {
		if strings.Contains(query, "CREATE TABLE IF NOT EXISTS comparison_meta") {
			return nil, errors.New("meta table fail")
		}
		return db.Exec(query)
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	dbExec = origDbExec
	assert.ErrorContains(t, err, "create comparison_meta")

	// dbExec failure on CREATE TABLE comparison_rows
	dbExec = func(db *sql.DB, query string) (sql.Result, error) {
		if strings.Contains(query, "CREATE TABLE IF NOT EXISTS comparison_rows") {
			return nil, errors.New("rows table fail")
		}
		return db.Exec(query)
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	dbExec = origDbExec
	assert.ErrorContains(t, err, "create comparison_rows")

	// dbExec failure on index sort
	dbExec = func(db *sql.DB, query string) (sql.Result, error) {
		if strings.Contains(query, "comparison_rows_id_sort") {
			return nil, errors.New("sort index fail")
		}
		return db.Exec(query)
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	dbExec = origDbExec
	assert.ErrorContains(t, err, "create comparison_rows sort index")

	// dbExec failure on index state sort
	dbExec = func(db *sql.DB, query string) (sql.Result, error) {
		if strings.Contains(query, "comparison_rows_id_state_sort") {
			return nil, errors.New("state index fail")
		}
		return db.Exec(query)
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	dbExec = origDbExec
	assert.ErrorContains(t, err, "create comparison_rows state index")

	// chmod final failure
	chmod = func(name string, mode os.FileMode) error {
		if mode == 0o600 {
			return errors.New("chmod 0600 fail")
		}
		return os.Chmod(name, mode)
	}
	_, err = Open(Options{PrivateDir: t.TempDir()})
	chmod = origChmod
	assert.ErrorContains(t, err, "secure comparison cache file")

	// ByteCap < 0 & TTL < 0
	_, err = Open(Options{PrivateDir: t.TempDir(), ByteCap: -1})
	assert.ErrorContains(t, err, "byte cap must not be negative")
	_, err = Open(Options{PrivateDir: t.TempDir(), TTL: -1})
	assert.ErrorContains(t, err, "TTL must not be negative")

	// Existing file chmod failure
	dir := t.TempDir()
	store, err := Open(Options{PrivateDir: dir})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	chmod = func(name string, mode os.FileMode) error {
		if filepath.Base(name) == fileName && mode == 0o600 {
			return errors.New("existing file chmod fail")
		}
		return os.Chmod(name, mode)
	}
	_, err = Open(Options{PrivateDir: dir})
	chmod = origChmod
	assert.ErrorContains(t, err, "secure comparison cache file")

	// migratePolicyFingerprint error during Open
	origPragma := pragmaTableInfoSQL
	pragmaTableInfoSQL = "SELECT 1"
	_, err = Open(Options{PrivateDir: t.TempDir()})
	pragmaTableInfoSQL = origPragma
	assert.Error(t, err)

	// Reopen existing file successfully
	store, err = Open(Options{PrivateDir: dir})
	require.NoError(t, err)
	assert.NoError(t, store.Close())
}

// Not parallel: mutates pragmaTableInfoSQL, rowsErrHook, rowsCloseHook
func TestMigratePolicyFingerprint_EdgeCases(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	defer db.Close()

	// Closed DB
	closedDB, _ := sql.Open("sqlite", filepath.Join(dir, "closed.db"))
	_ = closedDB.Close()
	assert.Error(t, migratePolicyFingerprint(closedDB))

	// Missing column: table created without policy_fingerprint
	_, err = db.Exec(`CREATE TABLE comparison_meta (comparison_id TEXT PRIMARY KEY, query_id TEXT NOT NULL)`)
	require.NoError(t, err)
	assert.NoError(t, migratePolicyFingerprint(db))

	// pragmaTableInfoSQL returns scan error
	origSQL := pragmaTableInfoSQL
	pragmaTableInfoSQL = "SELECT 1"
	err = migratePolicyFingerprint(db)
	pragmaTableInfoSQL = origSQL
	assert.Error(t, err)

	// rowsErrHook failure
	origErr := rowsErrHook
	rowsErrHook = func(*sql.Rows) error { return errors.New("simulated rows err") }
	err = migratePolicyFingerprint(db)
	rowsErrHook = origErr
	assert.ErrorContains(t, err, "simulated rows err")

	// rowsCloseHook failure
	origClose := rowsCloseHook
	rowsCloseHook = func(*sql.Rows) error { return errors.New("simulated rows close") }
	err = migratePolicyFingerprint(db)
	rowsCloseHook = origClose
	assert.ErrorContains(t, err, "simulated rows close")
}

func TestNilStore_And_Lock_EdgeCases(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (*Store)(nil).Close())
	assert.NoError(t, (&Store{}).Close())

	_, err := (*Store)(nil).Begin(context.Background(), BeginRequest{})
	assert.ErrorIs(t, err, ErrNotConfigured)

	_, err = (*Store)(nil).Lookup(context.Background(), "id")
	assert.ErrorIs(t, err, ErrNotConfigured)

	_, err = (*Store)(nil).Page(context.Background(), PageRequest{})
	assert.ErrorIs(t, err, ErrNotConfigured)

	// s.lock with canceled context
	store := openTestStore(t, Options{})
	ctxCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, store.lock(ctxCanceled), context.Canceled)

	// s.lock when mutex already taken and context times out
	store.mu <- struct{}{}
	ctxTimeout, cancelTimeout := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelTimeout()
	assert.ErrorIs(t, store.lock(ctxTimeout), context.DeadlineExceeded)
	<-store.mu
}

// Not parallel: mutates dbBeginTx, txExecContext
func TestBegin_Validation_And_Errors(t *testing.T) {
	store := openTestStore(t, Options{})
	validLeft := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e1"}
	validRight := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e2"}

	// Left invalid
	_, err := store.Begin(context.Background(), BeginRequest{Left: apicontract.ExecutionRef{}})
	assert.ErrorContains(t, err, "left execution:")

	// Right invalid
	_, err = store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: apicontract.ExecutionRef{}})
	assert.ErrorContains(t, err, "right execution:")

	// QueryID empty
	_, err = store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "  "})
	assert.ErrorContains(t, err, "query id is required")

	// dbBeginTx error
	origBeginTx := dbBeginTx
	dbBeginTx = func(db *sql.DB, ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
		return nil, errors.New("simulated begin error")
	}
	_, err = store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	dbBeginTx = origBeginTx
	assert.ErrorContains(t, err, "begin comparison cache write")

	// txExecContext error clearing rows
	origExec := txExecContext
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM comparison_rows") {
			return nil, errors.New("simulated delete rows error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	_, err = store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	txExecContext = origExec
	assert.ErrorContains(t, err, "clear comparison rows")

	// txExecContext error clearing meta
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM comparison_meta") {
			return nil, errors.New("simulated delete meta error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	_, err = store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	txExecContext = origExec
	assert.ErrorContains(t, err, "clear comparison meta")
}

// Not parallel: mutates jsonMarshal, txCommit, txExecContext
func TestObserver_And_Commit_EdgeCases(t *testing.T) {
	store := openTestStore(t, Options{ByteCap: 1000})
	validLeft := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e1"}
	validRight := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e2"}

	sess, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)

	obs := sess.Observer()

	// Nil write session observer
	var nilSess *WriteSession
	assert.ErrorContains(t, nilSess.Observer()(recordsetcompare.ComparedRecord{}), "closed")

	// Invalid record state
	assert.ErrorContains(t, obs(recordsetcompare.ComparedRecord{State: "BAD"}), "unsupported comparison row state")

	// TypedValue with invalid type fails marshalRowPayload
	assert.Error(t, obs(recordsetcompare.ComparedRecord{
		State: recordsetcompare.RecordMatched,
		Key:   []apicontract.TypedValue{{Type: "INVALID"}},
	}))

	// Row bytes exceeds byte cap
	assert.ErrorContains(t, obs(recordsetcompare.ComparedRecord{
		SortKey: string(make([]byte, 1001)),
		State:   recordsetcompare.RecordMatched,
	}), "exceeds byte cap")

	// txExecContext failure on INSERT comparison_rows
	sess2, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	origExec := txExecContext
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO comparison_rows") {
			return nil, errors.New("simulated insert row error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	assert.ErrorContains(t, sess2.Observer()(recordsetcompare.ComparedRecord{
		SortKey: "k", State: recordsetcompare.RecordMatched,
	}), "insert comparison row")
	txExecContext = origExec
	sess2.Abort()

	// Commit on nil / closed session
	assert.ErrorContains(t, nilSess.Commit(apicontract.CompareResult{}), "closed")
	assert.ErrorContains(t, sess2.Commit(apicontract.CompareResult{}), "closed")

	// Commit s.bytes > s.store.byteCap
	sess3, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	sess3.bytes = 1001
	assert.ErrorContains(t, sess3.Commit(apicontract.CompareResult{}), "exceed cap")

	// Commit jsonMarshal failure on columns
	sess4, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	origMarshal := jsonMarshal
	jsonMarshal = func(v any) ([]byte, error) {
		if _, ok := v.([]apicontract.Column); ok {
			return nil, errors.New("simulated marshal columns error")
		}
		return origMarshal(v)
	}
	assert.ErrorContains(t, sess4.Commit(apicontract.CompareResult{}), "marshal comparison columns")

	// Commit jsonMarshal failure on key
	sess4Key, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	jsonMarshal = func(v any) ([]byte, error) {
		if _, ok := v.([]string); ok {
			return nil, errors.New("simulated marshal key error")
		}
		return origMarshal(v)
	}
	assert.ErrorContains(t, sess4Key.Commit(apicontract.CompareResult{}), "marshal comparison key")

	// Commit jsonMarshal failure on left
	sess4Left, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	leftFailed := false
	jsonMarshal = func(v any) ([]byte, error) {
		if ref, ok := v.(apicontract.ExecutionRef); ok && ref == validLeft && !leftFailed {
			leftFailed = true
			return nil, errors.New("simulated marshal left error")
		}
		return origMarshal(v)
	}
	assert.ErrorContains(t, sess4Left.Commit(apicontract.CompareResult{}), "marshal left execution")

	// Commit jsonMarshal failure on right
	sess4Right, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	jsonMarshal = func(v any) ([]byte, error) {
		if ref, ok := v.(apicontract.ExecutionRef); ok && ref == validRight {
			return nil, errors.New("simulated marshal right error")
		}
		return origMarshal(v)
	}
	assert.ErrorContains(t, sess4Right.Commit(apicontract.CompareResult{}), "marshal right execution")
	jsonMarshal = origMarshal

	// Commit metadataBytes exceeds byteCap
	sess5, err := store.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	sess5.store.byteCap = 5 // smaller than metadata JSON
	assert.ErrorContains(t, sess5.Commit(apicontract.CompareResult{}), "exceeds byte cap")

	// Commit txExecContext failure on INSERT comparison_meta
	store2 := openTestStore(t, Options{ByteCap: 100000})
	sess6, err := store2.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO comparison_meta") {
			return nil, errors.New("simulated insert meta error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	assert.ErrorContains(t, sess6.Commit(apicontract.CompareResult{}), "insert comparison meta")
	txExecContext = origExec

	// Commit txCommit error
	sess7, err := store2.Begin(context.Background(), BeginRequest{Left: validLeft, Right: validRight, QueryID: "q"})
	require.NoError(t, err)
	origCommit := txCommit
	txCommit = func(tx *sql.Tx) error { return errors.New("simulated tx commit error") }
	err = sess7.Commit(apicontract.CompareResult{})
	txCommit = origCommit
	assert.ErrorContains(t, err, "commit comparison cache")
}

// Not parallel: mutates pageRowsSQL, rowsErrHook, rowsCloseHook, txCommit, dbExecContext, dbBeginTx
func TestPage_Validation_And_Errors(t *testing.T) {
	store := openTestStore(t, Options{})
	validLeft := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e1"}
	validRight := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e2"}
	compareDemo(t, store, validLeft, validRight)
	compID := ID(validLeft, validRight)

	// Empty comparisonID
	_, err := store.Page(context.Background(), PageRequest{})
	assert.ErrorContains(t, err, "comparison id is required")

	// Invalid state
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: "BAD"})
	assert.ErrorContains(t, err, "unsupported comparison row state")

	// Limit < 1 and limit > max
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched, Limit: -1})
	assert.ErrorContains(t, err, "limit must be between 1 and")
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched, Limit: apicontract.CompareMaximumLimit + 1})
	assert.ErrorContains(t, err, "limit must be between 1 and")

	// evictLocked failure in Page and Lookup
	origBeginTx := dbBeginTx
	dbBeginTx = func(db *sql.DB, ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
		if opts == nil {
			return nil, errors.New("simulated evict begin error")
		}
		return db.BeginTx(ctx, opts)
	}
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	assert.ErrorContains(t, err, "begin comparison eviction")
	_, err = store.Lookup(context.Background(), compID)
	assert.ErrorContains(t, err, "begin comparison eviction")
	dbBeginTx = origBeginTx

	// dbBeginTx failure in Page
	dbBeginTx = func(db *sql.DB, ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
		if opts != nil && opts.ReadOnly {
			return nil, errors.New("simulated page read-only begin error")
		}
		return db.BeginTx(ctx, opts)
	}
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	dbBeginTx = origBeginTx
	assert.ErrorContains(t, err, "begin comparison page")

	// Policy fingerprint mismatch -> ErrNotFound
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched, PolicyFingerprint: "mismatch"})
	assert.ErrorIs(t, err, ErrNotFound)

	// pageRowsSQL query error
	origPageSQL := pageRowsSQL
	pageRowsSQL = "SELECT * FROM invalid_table"
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	pageRowsSQL = origPageSQL
	assert.ErrorContains(t, err, "page comparison rows")

	// pageRowsSQL scan error
	pageRowsSQL = "SELECT 1 FROM comparison_rows WHERE comparison_id = ? AND state = ? AND sort_key > ? ORDER BY sort_key LIMIT ?"
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	pageRowsSQL = origPageSQL
	assert.ErrorContains(t, err, "scan comparison row")

	// Corrupt payload in comparison_rows -> unmarshalRowPayload error
	_, err = store.db.Exec(`INSERT INTO comparison_rows (comparison_id, sort_key, state, payload_json) VALUES (?, ?, ?, ?)`, compID, "z_corrupt", "matched", "invalid json")
	require.NoError(t, err)
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	assert.ErrorContains(t, err, "decode comparison row")
	_, err = store.db.Exec(`DELETE FROM comparison_rows WHERE sort_key = 'z_corrupt'`)
	require.NoError(t, err)

	// rowsErrHook failure
	origErr := rowsErrHook
	rowsErrHook = func(*sql.Rows) error { return errors.New("simulated page rows err") }
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	rowsErrHook = origErr
	assert.ErrorContains(t, err, "iterate comparison rows")

	// rowsCloseHook failure
	origClose := rowsCloseHook
	rowsCloseHook = func(*sql.Rows) error { return errors.New("simulated page rows close") }
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	rowsCloseHook = origClose
	assert.ErrorContains(t, err, "close comparison rows")

	// Non-existent comparisonID -> ErrNotFound
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: "not-found", State: recordsetcompare.RecordMatched})
	assert.ErrorIs(t, err, ErrNotFound)

	// txCommit failure in Page
	commitCount := 0
	origCommit := txCommit
	txCommit = func(tx *sql.Tx) error {
		commitCount++
		if commitCount == 2 {
			return errors.New("simulated page commit fail")
		}
		return tx.Commit()
	}
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	txCommit = origCommit
	assert.ErrorContains(t, err, "finish comparison page")

	// dbExecContext failure -> touch comparison cache fails
	origExec := dbExecContext
	dbExecContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE comparison_meta") {
			return nil, errors.New("simulated touch error")
		}
		return db.ExecContext(ctx, query, args...)
	}
	_, err = store.Page(context.Background(), PageRequest{ComparisonID: compID, State: recordsetcompare.RecordMatched})
	dbExecContext = origExec
	assert.ErrorContains(t, err, "touch comparison cache")
}

func TestLookup_CorruptData_And_QueryError(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, Options{})

	// Closed DB query error
	closedDB, _ := sql.Open("sqlite", filepath.Join(t.TempDir(), "c.db"))
	_ = closedDB.Close()
	_, err := lookupComparison(context.Background(), closedDB, "id")
	assert.ErrorContains(t, err, "lookup comparison cache")

	now := time.Now().UnixNano()
	// Corrupt left_json
	_, err = store.db.Exec(`INSERT INTO comparison_meta VALUES ('c1', 'q', '', 'bad', '{}', '[]', '[]', 10, ?, ?)`, now, now)
	require.NoError(t, err)
	_, err = store.Lookup(context.Background(), "c1")
	assert.ErrorContains(t, err, "decode left execution")

	// Corrupt right_json
	_, err = store.db.Exec(`INSERT INTO comparison_meta VALUES ('c2', 'q', '', '{}', 'bad', '[]', '[]', 10, ?, ?)`, now, now)
	require.NoError(t, err)
	_, err = store.Lookup(context.Background(), "c2")
	assert.ErrorContains(t, err, "decode right execution")

	// Corrupt columns_json
	_, err = store.db.Exec(`INSERT INTO comparison_meta VALUES ('c3', 'q', '', '{}', '{}', 'bad', '[]', 10, ?, ?)`, now, now)
	require.NoError(t, err)
	_, err = store.Lookup(context.Background(), "c3")
	assert.ErrorContains(t, err, "decode columns")

	// Corrupt key_json
	_, err = store.db.Exec(`INSERT INTO comparison_meta VALUES ('c4', 'q', '', '{}', '{}', '[]', 'bad', 10, ?, ?)`, now, now)
	require.NoError(t, err)
	_, err = store.Lookup(context.Background(), "c4")
	assert.ErrorContains(t, err, "decode key")
}

// Not parallel: mutates sumBytesSQL, selectVictimSQL, txExecContext, dbBeginTx, txCommit
func TestEvictLocked_EdgeCases(t *testing.T) {
	store := openTestStore(t, Options{})
	validLeft := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e1"}
	validRight := apicontract.ExecutionRef{StoreID: "s", ProjectID: "p", ExecutionID: "e2"}
	compareDemo(t, store, validLeft, validRight)

	// BeginTx fails
	origBeginTx := dbBeginTx
	dbBeginTx = func(db *sql.DB, ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
		return nil, errors.New("simulated begin eviction error")
	}
	assert.ErrorContains(t, store.evictLocked(context.Background()), "begin comparison eviction")
	dbBeginTx = origBeginTx

	origExec := txExecContext
	// fail expire comparison_rows
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "WHERE created_at_ns < ?") && strings.Contains(query, "comparison_rows") {
			return nil, errors.New("simulated expire rows error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	assert.ErrorContains(t, store.evictLocked(context.Background()), "expire comparison rows")

	// fail expire comparison_meta
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM comparison_meta WHERE created_at_ns < ?") {
			return nil, errors.New("simulated expire meta error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	assert.ErrorContains(t, store.evictLocked(context.Background()), "expire comparison meta")
	txExecContext = origExec

	// sumBytesSQL invalid query
	origSumSQL := sumBytesSQL
	sumBytesSQL = "SELECT * FROM invalid_table"
	assert.ErrorContains(t, store.evictLocked(context.Background()), "sum comparison cache bytes")
	sumBytesSQL = origSumSQL

	// selectVictimSQL ErrNoRows branch: store.byteCap = -1 on empty database
	storeEmpty := openTestStore(t, Options{})
	storeEmpty.byteCap = -1
	assert.NoError(t, storeEmpty.evictLocked(context.Background()))

	// selectVictimSQL invalid query
	origVictimSQL := selectVictimSQL
	selectVictimSQL = "SELECT * FROM invalid_table"
	store.byteCap = 1 // force victim loop
	assert.ErrorContains(t, store.evictLocked(context.Background()), "select comparison cache LRU victim")
	selectVictimSQL = origVictimSQL

	// fail evict comparison_rows
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM comparison_rows WHERE comparison_id = ?") {
			return nil, errors.New("simulated evict rows error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	assert.ErrorContains(t, store.evictLocked(context.Background()), "evict comparison rows")

	// fail evict comparison_meta
	txExecContext = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM comparison_meta WHERE comparison_id = ?") {
			return nil, errors.New("simulated evict meta error")
		}
		return tx.ExecContext(ctx, query, args...)
	}
	assert.ErrorContains(t, store.evictLocked(context.Background()), "evict comparison meta")
	txExecContext = origExec
}

// Not parallel: mutates jsonMarshal
func TestPayload_Marshaling_EdgeCases(t *testing.T) {
	// unmarshalRowPayload error
	_, err := unmarshalRowPayload("not-json")
	assert.ErrorContains(t, err, "decode comparison row")

	// marshalRowPayload with newline
	origMarshal := jsonMarshal
	jsonMarshal = func(v any) ([]byte, error) {
		return []byte("{\n}"), nil
	}
	_, err = marshalRowPayload(recordsetcompare.ComparedRecord{})
	jsonMarshal = origMarshal
	assert.ErrorContains(t, err, "must be minified TEXT")

	// validState check
	assert.True(t, validState(recordsetcompare.RecordMatched))
	assert.True(t, validState(recordsetcompare.RecordAdded))
	assert.True(t, validState(recordsetcompare.RecordRemoved))
	assert.True(t, validState(recordsetcompare.RecordChanged))
	assert.False(t, validState("unknown"))
}

func TestStore_Abort_EdgeCases(t *testing.T) {
	t.Parallel()

	var nilSess *WriteSession
	nilSess.Abort() // should not panic

	doneSess := &WriteSession{done: true}
	doneSess.Abort() // should no-op
}
