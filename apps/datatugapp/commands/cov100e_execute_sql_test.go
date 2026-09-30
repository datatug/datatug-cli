package commands

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// cov100eResult is a canned result set served by the fake SQL driver.
type cov100eResult struct {
	cols  []string
	types []string
	data  [][]driver.Value
}

var (
	cov100eOnce       sync.Once
	cov100eResults    = map[string]cov100eResult{}
	cov100eCloseErr   bool
	cov100eRowsClosed bool
)

const cov100eDriverName = "cov100e_fake"

type cov100eDrv struct{}

func (cov100eDrv) Open(string) (driver.Conn, error) { return cov100eConn{}, nil }

type cov100eConn struct{}

func (cov100eConn) Prepare(q string) (driver.Stmt, error) {
	if q == "fail" {
		return nil, errors.New("prepare failed")
	}
	return cov100eStmt{q: q}, nil
}
func (cov100eConn) Close() error {
	if cov100eCloseErr {
		return errors.New("close failed")
	}
	return nil
}
func (cov100eConn) Begin() (driver.Tx, error) { return nil, errors.New("no tx") }

type cov100eStmt struct{ q string }

func (cov100eStmt) Close() error  { return nil }
func (cov100eStmt) NumInput() int { return -1 }
func (cov100eStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("no exec")
}
func (s cov100eStmt) Query([]driver.Value) (driver.Rows, error) {
	return &cov100eRows{res: cov100eResults[s.q]}, nil
}

type cov100eRows struct {
	res cov100eResult
	i   int
}

func (r *cov100eRows) Columns() []string { return r.res.cols }
func (r *cov100eRows) Close() error {
	if cov100eRowsClosed {
		return errors.New("rows close failed")
	}
	return nil
}
func (r *cov100eRows) Next(dest []driver.Value) error {
	if r.i >= len(r.res.data) {
		return io.EOF
	}
	copy(dest, r.res.data[r.i])
	r.i++
	return nil
}
func (r *cov100eRows) ColumnTypeDatabaseTypeName(i int) string { return r.res.types[i] }

func cov100eSetup(t *testing.T) {
	t.Helper()
	cov100eOnce.Do(func() { sql.Register(cov100eDriverName, cov100eDrv{}) })
	cov100eResults = map[string]cov100eResult{}
	cov100eCloseErr, cov100eRowsClosed = false, false
	t.Cleanup(func() {
		cov100eResults = map[string]cov100eResult{}
		cov100eCloseErr, cov100eRowsClosed = false, false
	})
}

func cov100eCmd() *executeSQLCommand {
	return &executeSQLCommand{Driver: cov100eDriverName, Host: "h", User: "u", Password: "secret", Schema: "db"}
}

func TestCov100eValidate(t *testing.T) {
	v := &executeSQLCommand{Query: "a", CommandText: "b"}
	if err := v.Validate(); err == nil {
		t.Fatal("want error when both query and command text are set")
	}
	if err := (&executeSQLCommand{Query: "a"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCov100eExecute_ConnStringError(t *testing.T) {
	cov100eSetup(t)
	v := cov100eCmd()
	v.Port = "notanumber"
	if err := v.Execute(); err == nil {
		t.Fatal("want error for bad port")
	}
	v = cov100eCmd()
	v.Mode = "bogus"
	if err := v.Execute(); err == nil {
		t.Fatal("want error for bad mode")
	}
}

func TestCov100eExecute_OpenFatal(t *testing.T) {
	cov100eSetup(t)
	orig := executeSQLFatal
	t.Cleanup(func() { executeSQLFatal = orig })
	type sentinel struct{}
	executeSQLFatal = func(...any) { panic(sentinel{}) }
	v := cov100eCmd()
	v.Driver = "cov100e_unregistered"
	defer func() {
		if _, ok := recover().(sentinel); !ok {
			t.Fatal("want fatal seam to be invoked")
		}
	}()
	_ = v.Execute()
}

func TestCov100eExecute_QueryError(t *testing.T) {
	cov100eSetup(t)
	v := cov100eCmd()
	v.CommandText = "fail"
	if err := v.Execute(); err == nil {
		t.Fatal("want query error")
	}
}

func TestCov100eExecute_ColumnTypesError(t *testing.T) {
	cov100eSetup(t)
	orig := executeSQLColumnTypes
	t.Cleanup(func() { executeSQLColumnTypes = orig })
	executeSQLColumnTypes = func(*sql.Rows) ([]*sql.ColumnType, error) { return nil, errors.New("ct failed") }
	cov100eResults["SELECT * FROM t"] = cov100eResult{}
	v := cov100eCmd()
	v.CommandText = "*=t"
	if err := v.Execute(); err == nil || !strings.Contains(err.Error(), "ct failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestCov100eExecute_StdoutHandlerAndCloseErrors(t *testing.T) {
	cov100eSetup(t)
	cov100eCloseErr, cov100eRowsClosed = true, true
	cov100eResults["q"] = cov100eResult{
		cols:  []string{"id", "name"},
		types: []string{"INT", "TEXT"},
		data:  [][]driver.Value{{int64(1), "a"}},
	}
	v := cov100eCmd()
	v.Port = "1433"
	v.Mode = "ro"
	v.CommandText = "q"
	if err := v.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestCov100eExecute_CSVHandler(t *testing.T) {
	cov100eSetup(t)
	cov100eResults["q"] = cov100eResult{
		cols:  []string{"id"},
		types: []string{"INT"},
		data:  [][]driver.Value{{int64(1)}},
	}
	v := cov100eCmd()
	v.CommandText = "q"
	v.OutputPath = filepath.Join(t.TempDir(), "out.csv")
	if err := v.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.OutputPath); err != nil {
		t.Fatal(err)
	}
}

func TestCov100eCSVHandler_BadPath(t *testing.T) {
	cov100eSetup(t)
	cov100eResults["q"] = cov100eResult{cols: []string{"id"}, types: []string{"INT"}, data: [][]driver.Value{{int64(1)}}}
	db, err := sql.Open(cov100eDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	h := csvHandler{path: filepath.Join(t.TempDir(), "missing-dir", "out.csv")}
	if err := h.Process(nil, rows); err == nil {
		t.Fatal("want error writing to a missing directory")
	}
}

type cov100eFailWriter struct{}

func (cov100eFailWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func cov100eQuery(t *testing.T, q string, res cov100eResult) (*sql.Rows, []*sql.ColumnType) {
	t.Helper()
	cov100eSetup(t)
	cov100eResults[q] = res
	db, err := sql.Open(cov100eDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.QueryContext(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rows.Close() })
	cts, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	return rows, cts
}

func TestCov100eWriterHandler(t *testing.T) {
	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	rows, cts := cov100eQuery(t, "q", cov100eResult{
		cols:  []string{"id", "n"},
		types: []string{"UNIQUEIDENTIFIER", "INT"},
		data:  [][]driver.Value{{uid, int64(5)}},
	})
	var buf bytes.Buffer
	if err := (writerHandler{&buf}).Process(cts, rows); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "01020304-0506-0708-090a-0b0c0d0e0f10") || !strings.Contains(buf.String(), "3 records") {
		t.Fatalf("output: %s", buf.String())
	}
}

func TestCov100eWriterHandler_BadUUID(t *testing.T) {
	rows, cts := cov100eQuery(t, "q", cov100eResult{
		cols:  []string{"id"},
		types: []string{"UNIQUEIDENTIFIER"},
		data:  [][]driver.Value{{[]byte{1, 2}}},
	})
	if err := (writerHandler{io.Discard}).Process(cts, rows); err == nil {
		t.Fatal("want uuid error")
	}
}

func TestCov100eWriterHandler_ScanError(t *testing.T) {
	rows, cts := cov100eQuery(t, "q", cov100eResult{
		cols:  []string{"a", "b"},
		types: []string{"INT", "INT"},
		data:  [][]driver.Value{{int64(1), int64(2)}},
	})
	// One column type for a two-column result: Scan destination count mismatch.
	if err := (writerHandler{io.Discard}).Process(cts[:1], rows); err == nil {
		t.Fatal("want scan error")
	}
}

func TestCov100eWriterHandler_WriteError(t *testing.T) {
	rows, cts := cov100eQuery(t, "q", cov100eResult{
		cols:  []string{"a"},
		types: []string{"INT"},
		data:  nil,
	})
	if err := (writerHandler{cov100eFailWriter{}}).Process(cts, rows); err == nil {
		t.Fatal("want write error")
	}
}

func TestCov100eUpdateUrlConfigCommandAction(t *testing.T) {
	cov100eSetup(t)
	cov100eResults["q"] = cov100eResult{cols: []string{"a"}, types: []string{"INT"}, data: [][]driver.Value{{int64(1)}}}

	cmd := updateUrlConfigCommandArgs()
	for k, val := range map[string]string{"driver": cov100eDriverName, "consoleCommand-text": "q"} {
		if err := cmd.Flags().Set(k, val); err != nil {
			t.Fatal(err)
		}
	}
	if err := updateUrlConfigCommandAction(cmd, nil); err != nil {
		t.Fatal(err)
	}

	cmd = updateUrlConfigCommandArgs()
	_ = cmd.Flags().Set("query", "a")
	_ = cmd.Flags().Set("consoleCommand-text", "b")
	if err := updateUrlConfigCommandAction(cmd, nil); err == nil {
		t.Fatal("want validation error")
	}
}
