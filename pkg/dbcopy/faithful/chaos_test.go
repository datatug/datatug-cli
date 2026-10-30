package faithful

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"modernc.org/sqlite"
)

// This file is the test machinery that lets the PostgreSQL side of the copy run without a server and
// lets every statement of both sides fail on purpose:
//
//   - a recording connection wraps a real PostgreSQL connection and writes every call and its result to
//     a JSON file (DATATUG_RECORD_SCENARIOS=1 against a local server);
//   - a replay connection answers the same calls, in order, from that file;
//   - a chaos connection wraps either of them or a SQLite connection and fails the n-th call in one of
//     three ways, so that every error branch of the copy runs.

var errInjected = errors.New("injected failure")

// faultPlan numbers the calls of every connection that shares it and fails the failAt-th.
type faultPlan struct {
	mu     sync.Mutex
	calls  int
	failAt int // 0: never
	mode   faultMode
	// queries holds the numbers of the calls that return rows: the only ones the row faults touch.
	queries map[int]bool
}

type faultMode int

const (
	faultError faultMode = iota // the call returns an error
	faultRows                   // the rows of the call fail on the first Next
	faultShort                  // the rows of the call have one column too few: Scan fails
	faultModes
)

// next counts one call and says whether it is the one that fails.
func (p *faultPlan) next() (faultMode, bool) {
	if p == nil {
		return 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.mode, p.failAt != 0 && p.calls == p.failAt
}

// markQuery notes that the call just counted returns rows.
func (p *faultPlan) markQuery() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queries == nil {
		p.queries = map[int]bool{}
	}
	p.queries[p.calls] = true
}

func (p *faultPlan) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// ---- chaos wrapper ----

type chaosConnector struct {
	open func(ctx context.Context) (driver.Conn, error)
	plan *faultPlan
}

func (c chaosConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if _, fail := c.plan.next(); fail {
		return nil, errInjected
	}
	inner, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	return &chaosConn{inner: inner, plan: c.plan}, nil
}

func (c chaosConnector) Driver() driver.Driver { return nil }

type chaosConn struct {
	inner driver.Conn
	plan  *faultPlan
}

// CheckNamedValue lets the inner connection decide what an argument may be.
func (c *chaosConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := c.inner.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	v, err := driver.DefaultParameterConverter.ConvertValue(nv.Value)
	nv.Value = v
	return err
}

func (c *chaosConn) Prepare(q string) (driver.Stmt, error) { return c.inner.Prepare(q) }
func (c *chaosConn) Close() error                          { return c.inner.Close() }
func (c *chaosConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *chaosConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if _, fail := c.plan.next(); fail {
		return nil, errInjected
	}
	var tx driver.Tx
	var err error
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = c.inner.Begin() //nolint:staticcheck // the inner connection has no BeginTx
	}
	if err != nil {
		return nil, err
	}
	return &chaosTx{inner: tx, plan: c.plan}, nil
}

func (c *chaosConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if _, fail := c.plan.next(); fail {
		return nil, errInjected
	}
	if e, ok := c.inner.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *chaosConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	mode, fail := c.plan.next()
	c.plan.markQuery()
	if fail && mode == faultError {
		return nil, errInjected
	}
	qc, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := qc.QueryContext(ctx, q, args)
	if err != nil || !fail {
		return rows, err
	}
	return &chaosRows{Rows: rows, mode: mode}, nil
}

type chaosTx struct {
	inner driver.Tx
	plan  *faultPlan
}

func (t *chaosTx) Commit() error {
	if _, fail := t.plan.next(); fail {
		_ = t.inner.Rollback()
		return errInjected
	}
	return t.inner.Commit()
}

func (t *chaosTx) Rollback() error { return t.inner.Rollback() }

type chaosRows struct {
	driver.Rows
	mode faultMode
}

func (r *chaosRows) Columns() []string {
	cols := r.Rows.Columns()
	if r.mode == faultShort && len(cols) > 0 {
		return cols[:len(cols)-1]
	}
	return cols
}

func (r *chaosRows) Next(dest []driver.Value) error {
	if r.mode == faultRows {
		return errInjected
	}
	full := make([]driver.Value, len(r.Rows.Columns()))
	if err := r.Rows.Next(full); err != nil {
		return err
	}
	copy(dest, full)
	return nil
}

// ---- recording ----

type cell struct {
	T string          `json:"t"` // n i f s b y
	V json.RawMessage `json:"v,omitempty"`
}

type callRec struct {
	Kind string   `json:"kind"` // exec query begin commit rollback
	SQL  string   `json:"sql,omitempty"`
	Cols []string `json:"cols,omitempty"`
	Rows [][]cell `json:"rows,omitempty"`
	Err  string   `json:"err,omitempty"`
	// Code is the SQLSTATE of a failure the server reported.
	Code     string `json:"code,omitempty"`
	Affected int64  `json:"affected,omitempty"`
}

// errCode is the SQLSTATE of a PostgreSQL error, "" for any other.
func errCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// replayError is the error a recorded failure comes back as: it reads as it did and carries its SQLSTATE.
type replayError struct{ rec callRec }

func (e *replayError) Error() string { return e.rec.Err }
func (e *replayError) Unwrap() error {
	if e.rec.Code == "" {
		return nil
	}
	return &pgconn.PgError{Code: e.rec.Code, Message: e.rec.Err}
}

func normSQL(s string) string { return strings.Join(strings.Fields(s), " ") }

func toCell(v driver.Value) cell {
	switch x := v.(type) {
	case nil:
		return cell{T: "n"}
	case int64:
		b, _ := json.Marshal(x)
		return cell{T: "i", V: b}
	case float64:
		b, _ := json.Marshal(x)
		return cell{T: "f", V: b}
	case bool:
		b, _ := json.Marshal(x)
		return cell{T: "b", V: b}
	case []byte:
		b, _ := json.Marshal(base64.StdEncoding.EncodeToString(x))
		return cell{T: "y", V: b}
	default:
		b, _ := json.Marshal(fmt.Sprint(x))
		return cell{T: "s", V: b}
	}
}

func fromCell(c cell) driver.Value {
	switch c.T {
	case "i":
		var v int64
		_ = json.Unmarshal(c.V, &v)
		return v
	case "f":
		var v float64
		_ = json.Unmarshal(c.V, &v)
		return v
	case "b":
		var v bool
		_ = json.Unmarshal(c.V, &v)
		return v
	case "y":
		var s string
		_ = json.Unmarshal(c.V, &s)
		b, _ := base64.StdEncoding.DecodeString(s)
		return b
	case "s":
		var s string
		_ = json.Unmarshal(c.V, &s)
		return s
	}
	return nil
}

type recorder struct {
	mu    sync.Mutex
	calls []callRec
}

func (r *recorder) add(c callRec) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
}

func (r *recorder) save(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := json.MarshalIndent(r.calls, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

type recordingConn struct {
	inner driver.Conn
	rec   *recorder
}

func (c *recordingConn) CheckNamedValue(nv *driver.NamedValue) error {
	return c.inner.(driver.NamedValueChecker).CheckNamedValue(nv)
}

func (c *recordingConn) Prepare(q string) (driver.Stmt, error) { return c.inner.Prepare(q) }
func (c *recordingConn) Close() error                          { return c.inner.Close() }
func (c *recordingConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *recordingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.inner.(driver.ConnBeginTx).BeginTx(ctx, opts)
	rec := callRec{Kind: "begin"}
	if err != nil {
		rec.Err, rec.Code = err.Error(), errCode(err)
	}
	c.rec.add(rec)
	if err != nil {
		return nil, err
	}
	return &recordingTx{inner: tx, rec: c.rec}, nil
}

func (c *recordingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	res, err := c.inner.(driver.ExecerContext).ExecContext(ctx, q, args)
	rec := callRec{Kind: "exec", SQL: normSQL(q)}
	if err != nil {
		rec.Err, rec.Code = err.Error(), errCode(err)
	} else if n, nerr := res.RowsAffected(); nerr == nil {
		rec.Affected = n
	}
	c.rec.add(rec)
	return res, err
}

func (c *recordingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.inner.(driver.QueryerContext).QueryContext(ctx, q, args)
	rec := callRec{Kind: "query", SQL: normSQL(q)}
	if err != nil {
		rec.Err, rec.Code = err.Error(), errCode(err)
		c.rec.add(rec)
		return nil, err
	}
	rec.Cols = rows.Columns()
	for {
		dest := make([]driver.Value, len(rec.Cols))
		if nerr := rows.Next(dest); nerr != nil {
			if !errors.Is(nerr, io.EOF) {
				rec.Err, rec.Code = nerr.Error(), errCode(nerr)
			}
			break
		}
		row := make([]cell, len(dest))
		for i, v := range dest {
			row[i] = toCell(v)
		}
		rec.Rows = append(rec.Rows, row)
	}
	_ = rows.Close()
	c.rec.add(rec)
	return &replayRows{rec: rec}, nil
}

type recordingTx struct {
	inner driver.Tx
	rec   *recorder
}

func (t *recordingTx) Commit() error {
	err := t.inner.Commit()
	rec := callRec{Kind: "commit"}
	if err != nil {
		rec.Err, rec.Code = err.Error(), errCode(err)
	}
	t.rec.add(rec)
	return err
}

func (t *recordingTx) Rollback() error {
	err := t.inner.Rollback()
	t.rec.add(callRec{Kind: "rollback"})
	return err
}

// ---- replay ----

type replayState struct {
	mu       sync.Mutex
	calls    []callRec
	i        int
	mismatch []string
}

func (s *replayState) take(kind, q string) (callRec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.calls) {
		s.mismatch = append(s.mismatch, fmt.Sprintf("call %d (%s %s) is beyond the recording", s.i, kind, q))
		return callRec{}, fmt.Errorf("replay: call %d is beyond the recording", s.i)
	}
	rec := s.calls[s.i]
	s.i++
	if rec.Kind != kind || (kind != "begin" && rec.SQL != normSQL(q)) {
		msg := fmt.Sprintf("call %d: recorded %s %q, got %s %q", s.i-1, rec.Kind, rec.SQL, kind, normSQL(q))
		s.mismatch = append(s.mismatch, msg)
		return callRec{}, errors.New("replay: " + msg)
	}
	if rec.Err != "" && kind != "query" {
		return rec, &replayError{rec}
	}
	return rec, nil
}

type replayConn struct{ state *replayState }

func (c *replayConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *replayConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("replay: no prepare")
}
func (c *replayConn) Close() error { return nil }
func (c *replayConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *replayConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if _, err := c.state.take("begin", ""); err != nil {
		return nil, err
	}
	return &replayTx{state: c.state}, nil
}

func (c *replayConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	rec, err := c.state.take("exec", q)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(rec.Affected), nil
}

func (c *replayConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	rec, err := c.state.take("query", q)
	if err != nil {
		return nil, err
	}
	if rec.Err != "" && len(rec.Cols) == 0 {
		return nil, &replayError{rec}
	}
	return &replayRows{rec: rec}, nil
}

type replayTx struct{ state *replayState }

func (t *replayTx) Commit() error {
	_, err := t.state.take("commit", "")
	return err
}

func (t *replayTx) Rollback() error {
	_, err := t.state.take("rollback", "")
	return err
}

type replayRows struct {
	rec callRec
	i   int
}

func (r *replayRows) Columns() []string { return r.rec.Cols }
func (r *replayRows) Close() error      { return nil }

func (r *replayRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rec.Rows) {
		if r.rec.Err != "" {
			return &replayError{r.rec}
		}
		return io.EOF
	}
	for j, c := range r.rec.Rows[r.i] {
		dest[j] = fromCell(c)
	}
	r.i++
	return nil
}

// ---- endpoints ----

func scenarioFile(name string, n int) string {
	return filepath.Join("testdata", "scenarios", fmt.Sprintf("%s.%d.json", name, n))
}

// replayPG returns a PostgreSQL endpoint that answers from the recording of scenario name, number n.
func replayPG(t testing.TB, name string, n int, plan *faultPlan) (Endpoint, *replayState) {
	t.Helper()
	b, err := os.ReadFile(scenarioFile(name, n))
	if err != nil {
		t.Fatalf("no recording (record it with DATATUG_RECORD_SCENARIOS=1): %v", err)
	}
	state := &replayState{}
	if err := json.Unmarshal(b, &state.calls); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(chaosConnector{open: func(context.Context) (driver.Conn, error) { return &replayConn{state: state}, nil }, plan: plan})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return Endpoint{Engine: Postgres, DB: db, Display: "postgres://replay/" + name, Schema: "public"}, state
}

// recordPG creates a database on the local server and returns an endpoint whose calls are recorded to
// the file of scenario name, number n, when the test ends.
func recordPG(t testing.TB, name string, n int) Endpoint {
	t.Helper()
	real := newPGDatabase(t)
	var dbName string
	if err := real.DB.QueryRow("SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv(pgAdminEnv))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	connector := stdlib.GetConnector(*cfg)
	db := sql.OpenDB(chaosConnector{open: func(ctx context.Context) (driver.Conn, error) {
		inner, err := connector.Connect(ctx)
		if err != nil {
			return nil, err
		}
		return &recordingConn{inner: inner, rec: rec}, nil
	}})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = db.Close()
		if err := os.MkdirAll(filepath.Dir(scenarioFile(name, n)), 0o755); err != nil {
			t.Error(err)
		}
		if err := rec.save(scenarioFile(name, n)); err != nil {
			t.Error(err)
		}
	})
	return Endpoint{Engine: Postgres, DB: db, Display: "postgres://record/" + name, Schema: "public"}
}

// chaosSQLite returns a SQLite endpoint (a fresh file) whose calls share the plan.
func chaosSQLite(t testing.TB, plan *faultPlan) Endpoint {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.sqlite")
	return chaosSQLiteAt(t, path, plan)
}

func chaosSQLiteAt(t testing.TB, path string, plan *faultPlan) Endpoint {
	t.Helper()
	db := sql.OpenDB(chaosConnector{open: func(context.Context) (driver.Conn, error) {
		return (&sqlite.Driver{}).Open("file:" + filepath.ToSlash(path) + "?mode=rwc&_pragma=synchronous(OFF)")
	}, plan: plan})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return Endpoint{Engine: SQLite, DB: db, Display: path}
}
