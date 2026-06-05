package sqlexecute

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
)

func TestNewExecutor(t *testing.T) {
	getDB := func(envID, dbID string) (*datatug.EnvDb, error) { return nil, nil }
	getCatalog := func(server datatug.ServerRef, catalogID string) (*datatug.DbCatalogSummary, error) {
		return nil, nil
	}
	e := NewExecutor(getDB, getCatalog)
	if e.getDbByID == nil {
		t.Fatal("expected getDbByID to be set")
	}
	if e.getCatalogSummary == nil {
		t.Fatal("expected getCatalogSummary to be set")
	}
}

// ─── fake SQL driver for UNIQUEIDENTIFIER / close-error tests ────────────────

// fakeRow describes one column in a fake result set.
type fakeRow struct {
	colName   string
	colDbType string // returned by ColumnTypeDatabaseTypeName
	value     driver.Value
}

type fakeDriverTx struct{}

func (tx *fakeDriverTx) Commit() error   { return nil }
func (tx *fakeDriverTx) Rollback() error { return nil }

// fakeTypedRows implements driver.Rows + driver.RowsColumnTypeDatabaseTypeName
// so that database/sql populates ColumnType.DatabaseTypeName from fakeRow.colDbType.
type fakeTypedRows struct {
	rows []fakeRow
	pos  int
}

func (r *fakeTypedRows) Columns() []string {
	cols := make([]string, len(r.rows))
	for i, row := range r.rows {
		cols[i] = row.colName
	}
	return cols
}

func (r *fakeTypedRows) Close() error { return nil }

func (r *fakeTypedRows) Next(dest []driver.Value) error {
	if r.pos >= 1 {
		return io.EOF
	}
	r.pos++
	for i, row := range r.rows {
		dest[i] = row.value
	}
	return nil
}

// ColumnTypeDatabaseTypeName implements driver.RowsColumnTypeDatabaseTypeName.
func (r *fakeTypedRows) ColumnTypeDatabaseTypeName(index int) string {
	return r.rows[index].colDbType
}

// fakeTypedDriver opens connections that return fakeTypedRows for any query.
type fakeTypedDriver struct{ rows []fakeRow }

type fakeTypedConn struct{ rows []fakeRow }
type fakeTypedStmt struct{ rows []fakeRow }

func (d *fakeTypedDriver) Open(_ string) (driver.Conn, error) {
	return &fakeTypedConn{rows: d.rows}, nil
}
func (c *fakeTypedConn) Prepare(_ string) (driver.Stmt, error) {
	return &fakeTypedStmt{rows: c.rows}, nil
}
func (c *fakeTypedConn) Close() error              { return nil }
func (c *fakeTypedConn) Begin() (driver.Tx, error) { return &fakeDriverTx{}, nil }
func (s *fakeTypedStmt) Close() error              { return nil }
func (s *fakeTypedStmt) NumInput() int             { return -1 }
func (s *fakeTypedStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return nil, errors.New("exec not supported")
}
func (s *fakeTypedStmt) Query(_ []driver.Value) (driver.Rows, error) {
	return &fakeTypedRows{rows: s.rows}, nil
}

// closeErrDriver returns an error when its rows are closed.
type closeErrDriver struct{}
type closeErrConn struct{}
type closeErrStmt struct{}
type closeErrRows struct{ pos int }

func (d *closeErrDriver) Open(_ string) (driver.Conn, error) {
	return &closeErrConn{}, nil
}
func (c *closeErrConn) Prepare(_ string) (driver.Stmt, error) {
	return &closeErrStmt{}, nil
}
func (c *closeErrConn) Close() error              { return nil }
func (c *closeErrConn) Begin() (driver.Tx, error) { return &fakeDriverTx{}, nil }
func (s *closeErrStmt) Close() error              { return nil }
func (s *closeErrStmt) NumInput() int             { return -1 }
func (s *closeErrStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return nil, errors.New("exec not supported")
}
func (s *closeErrStmt) Query(_ []driver.Value) (driver.Rows, error) {
	return &closeErrRows{}, nil
}
func (r *closeErrRows) Columns() []string { return []string{"n"} }
func (r *closeErrRows) Close() error      { return errors.New("close error from fake rows") }
func (r *closeErrRows) Next(dest []driver.Value) error {
	if r.pos >= 1 {
		return io.EOF
	}
	r.pos++
	dest[0] = int64(1)
	return nil
}

// dbCloseErrDriver is a fake driver whose Conn.Close() returns an error.
// This covers the db.Close() error branch in executeCommand's deferred close.
type dbCloseErrDriver struct{}
type dbCloseErrConn struct{}
type dbCloseErrStmt struct{}
type dbCloseErrRows struct{ pos int }

func (d *dbCloseErrDriver) Open(_ string) (driver.Conn, error) { return &dbCloseErrConn{}, nil }
func (c *dbCloseErrConn) Prepare(_ string) (driver.Stmt, error) {
	return &dbCloseErrStmt{}, nil
}
func (c *dbCloseErrConn) Close() error              { return errors.New("db conn close error") }
func (c *dbCloseErrConn) Begin() (driver.Tx, error) { return &fakeDriverTx{}, nil }
func (s *dbCloseErrStmt) Close() error              { return nil }
func (s *dbCloseErrStmt) NumInput() int             { return -1 }
func (s *dbCloseErrStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return nil, errors.New("exec not supported")
}
func (s *dbCloseErrStmt) Query(_ []driver.Value) (driver.Rows, error) {
	return &dbCloseErrRows{}, nil
}
func (r *dbCloseErrRows) Columns() []string { return []string{"n"} }
func (r *dbCloseErrRows) Close() error      { return nil }
func (r *dbCloseErrRows) Next(dest []driver.Value) error {
	if r.pos >= 1 {
		return io.EOF
	}
	r.pos++
	dest[0] = int64(42)
	return nil
}

// validUUIDbytes is a valid 16-byte UUID value.
var validUUIDbytes = []byte{
	0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

func init() {
	// fakeuid: UNIQUEIDENTIFIER column with a real UUID value (non-sqlserver driver)
	sql.Register("fakeuid", &fakeTypedDriver{rows: []fakeRow{
		{colName: "uid", colDbType: "UNIQUEIDENTIFIER", value: validUUIDbytes},
	}})
	// fakeuidnull: UNIQUEIDENTIFIER column with NULL value
	sql.Register("fakeuidnull", &fakeTypedDriver{rows: []fakeRow{
		{colName: "uid", colDbType: "UNIQUEIDENTIFIER", value: nil},
	}})
	// fakeuidss: UNIQUEIDENTIFIER column, driver name "sqlserver" triggers byte-swap
	sql.Register("sqlserver-fake", &fakeTypedDriver{rows: []fakeRow{
		{colName: "uid", colDbType: "UNIQUEIDENTIFIER", value: validUUIDbytes},
	}})
	// fakecloseerr: rows.Close() returns an error (logged, not returned)
	sql.Register("fakecloseerr", &closeErrDriver{})
	// fakebaduid: UNIQUEIDENTIFIER column with invalid (non-16-byte) value to trigger uuid.FromBytes error
	sql.Register("fakebaduid", &fakeTypedDriver{rows: []fakeRow{
		{colName: "uid", colDbType: "UNIQUEIDENTIFIER", value: []byte{0x01, 0x02, 0x03}},
	}})
	// dbclosetest: Conn.Close() returns an error, covering the db.Close() defer error branch
	sql.Register("dbclosetest", &dbCloseErrDriver{})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// openMemDB opens an in-memory SQLite3 DB and creates a test table.
func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: DB: %v", err)
	}
	if _, err = db.Exec(`CREATE TABLE items (id INTEGER, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO items VALUES (1, 'Alice'), (2, 'Bob')`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}
	return db
}

// sqliteExecutor builds an Executor that points at the given temp-file path via sqlite3.
func sqliteExecutor(path string) Executor {
	return NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return &datatug.EnvDb{
				Server: datatug.ServerRef{Driver: "sqlite3"},
			}, nil
		},
		func(server datatug.ServerRef, catalogID string) (*datatug.DbCatalogSummary, error) {
			return &datatug.DbCatalogSummary{
				DbCatalogBase: datatug.DbCatalogBase{
					Path: path,
				},
			}, nil
		},
	)
}

// ─── Request.Validate tests ───────────────────────────────────────────────────

// TestRequest_Validate covers models.go Request.Validate
func TestRequest_Validate(t *testing.T) {
	t.Run("missing project", func(t *testing.T) {
		r := Request{}
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for missing project")
		}
	})

	t.Run("valid project no commands", func(t *testing.T) {
		r := Request{Project: "proj"}
		if err := r.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid command propagates error", func(t *testing.T) {
		r := Request{
			Project: "proj",
			Commands: []RequestCommand{
				{}, // Env is empty => error
			},
		}
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for invalid command")
		}
	})
}

// ─── RequestCommand.Validate tests ───────────────────────────────────────────

// TestRequestCommand_Validate covers models.go RequestCommand.Validate
func TestRequestCommand_Validate(t *testing.T) {
	t.Run("missing env", func(t *testing.T) {
		cmd := RequestCommand{}
		if err := cmd.Validate(); err == nil {
			t.Fatal("expected error for missing env")
		}
	})

	t.Run("missing text", func(t *testing.T) {
		cmd := RequestCommand{Env: "dev"}
		if err := cmd.Validate(); err == nil {
			t.Fatal("expected error for missing text")
		}
	})

	t.Run("ServerRef.Validate error (invalid driver)", func(t *testing.T) {
		cmd := RequestCommand{
			Env:  "dev",
			Text: "SELECT 1",
			ServerRef: datatug.ServerRef{
				Driver: "unknowndriver",
				Host:   "localhost",
			},
		}
		if err := cmd.Validate(); err == nil {
			t.Fatal("expected error for invalid server ref driver")
		}
	})

	t.Run("db and host both provided", func(t *testing.T) {
		// ServerRef must pass Validate: use sqlite3 (no host/port required).
		// But sqlite3 disallows Host, so use sqlserver with Host set, and also set DB.
		// ServerRef.Validate passes (sqlserver + host + port>=0).
		// Then v.DB != "" and v.Host != "" triggers the error.
		cmd := RequestCommand{
			Env:  "dev",
			Text: "SELECT 1",
			DB:   "mydb",
			ServerRef: datatug.ServerRef{
				Driver: "sqlserver",
				Host:   "myhost",
			},
		}
		if err := cmd.Validate(); err == nil {
			t.Fatal("expected error for db+host conflict")
		}
	})

	t.Run("db and driver both provided", func(t *testing.T) {
		// To reach the "db+driver" check we need:
		// - ServerRef.Validate passes
		// - v.DB != "" and v.Host == ""
		// - v.Driver != ""
		// sqlite3 with no host/port passes ServerRef.Validate.
		// Then DB != "" and Driver != "" hits the driver conflict.
		cmd := RequestCommand{
			Env:  "dev",
			Text: "SELECT 1",
			DB:   "mydb",
			ServerRef: datatug.ServerRef{
				Driver: "sqlite3",
			},
		}
		if err := cmd.Validate(); err == nil {
			t.Fatal("expected error for db+driver conflict")
		}
	})

	// Note: the "db and port both provided" branch (line 77 in models.go) is
	// structurally unreachable: ServerRef.Validate() requires Driver != "" to pass,
	// which means v.Driver != "" and the "db+driver" check fires first.
	// This gap is documented in TEST-COVERAGE.md.

	t.Run("valid command with sqlite3 server", func(t *testing.T) {
		cmd := RequestCommand{
			Env:       "dev",
			Text:      "SELECT 1",
			ServerRef: datatug.ServerRef{Driver: "sqlite3"},
		}
		if err := cmd.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ─── Execute dispatch tests ───────────────────────────────────────────────────

// TestExecute_SingleDispatch covers Execute dispatching to ExecuteSingle (len==1)
func TestExecute_SingleDispatch(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return nil, errors.New("stub error")
		},
		nil,
	)
	cmd := RequestCommand{Env: "dev", Text: "SELECT 1"}
	req := Request{Commands: []RequestCommand{cmd}}
	_, err := e.Execute(req)
	if err == nil {
		t.Fatal("expected error from ExecuteSingle dispatch")
	}
}

// TestExecute_MultiDispatch covers Execute dispatching to executeMulti (len>1)
func TestExecute_MultiDispatch(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return nil, errors.New("stub multi error")
		},
		nil,
	)
	cmd := RequestCommand{Env: "dev", Text: "SELECT 1"}
	req := Request{Commands: []RequestCommand{cmd, cmd}}
	_, err := e.Execute(req)
	if err == nil {
		t.Fatal("expected error from executeMulti dispatch")
	}
}

// ─── ExecuteSingle tests ──────────────────────────────────────────────────────

// TestExecuteSingle_Error covers ExecuteSingle early error return from executeCommand.
func TestExecuteSingle_Error(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return nil, errors.New("db lookup error")
		},
		nil,
	)
	_, err := e.ExecuteSingle(RequestCommand{Env: "env1", Text: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestExecuteSingle_Success covers the happy path of ExecuteSingle using in-memory SQLite.
func TestExecuteSingle_Success(t *testing.T) {
	e := sqliteExecutor(":memory:")
	resp, err := e.ExecuteSingle(RequestCommand{
		Env:  "dev",
		Text: "SELECT 1 AS n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("expected 1 command response, got %d", len(resp.Commands))
	}
	if resp.Duration == 0 {
		t.Fatal("expected non-zero duration")
	}
}

// ─── executeMulti tests ───────────────────────────────────────────────────────

// TestExecuteMulti_Success covers executeMulti success branch with multiple commands.
func TestExecuteMulti_Success(t *testing.T) {
	e := sqliteExecutor(":memory:")
	cmd := RequestCommand{Env: "dev", Text: "SELECT 42 AS val"}
	req := Request{Commands: []RequestCommand{cmd, cmd}}
	resp, err := e.executeMulti(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Commands) != 2 {
		t.Fatalf("expected 2 command responses, got %d", len(resp.Commands))
	}
}

// TestExecuteMulti_Error covers executeMulti commandErr branch.
func TestExecuteMulti_Error(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return nil, errors.New("forced multi error")
		},
		nil,
	)
	cmd := RequestCommand{Env: "dev", Text: "SELECT 1"}
	req := Request{Commands: []RequestCommand{cmd, cmd}}
	_, err := e.executeMulti(req)
	if err == nil {
		t.Fatal("expected error from executeMulti")
	}
}

// ─── executeCommand tests ─────────────────────────────────────────────────────

// TestExecuteCommand_GetDbByIDNil_InvalidServer covers the else branch of getDbByID==nil
// with an invalid server reference, which triggers dbServer.Validate error.
func TestExecuteCommand_GetDbByIDNil_InvalidServer(t *testing.T) {
	e := NewExecutor(nil, nil)
	cmd := RequestCommand{
		ServerRef: datatug.ServerRef{Host: "somehost"},
		Text:      "SELECT 1",
		Env:       "dev",
	}
	_, err := e.executeCommand(cmd)
	if err == nil {
		t.Fatal("expected error for invalid server params")
	}
}

// TestExecuteCommand_SQLite3_Success covers the sqlite3 case end-to-end.
func TestExecuteCommand_SQLite3_Success(t *testing.T) {
	e := sqliteExecutor(":memory:")
	recordset, err := e.executeCommand(RequestCommand{
		Env:  "dev",
		Text: "SELECT 1 AS num, 'hello' AS greeting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(recordset.Columns))
	}
}

// TestExecuteCommand_SQLite3_HomedirExpandError covers the homedir.Expand error path.
// A path like ~nonexistentuser999/foo triggers "cannot expand user-specific home dir".
func TestExecuteCommand_SQLite3_HomedirExpandError(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return &datatug.EnvDb{
				Server: datatug.ServerRef{Driver: "sqlite3"},
			}, nil
		},
		func(server datatug.ServerRef, catalogID string) (*datatug.DbCatalogSummary, error) {
			return &datatug.DbCatalogSummary{
				DbCatalogBase: datatug.DbCatalogBase{
					Path: "~nonexistentuser999/db.sqlite",
				},
			}, nil
		},
	)
	_, err := e.executeCommand(RequestCommand{Env: "dev", DB: "mydb", Text: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error from homedir.Expand")
	}
}

// TestExecuteCommand_SQLite3_WithParameters covers parameter substitution path.
func TestExecuteCommand_SQLite3_WithParameters(t *testing.T) {
	e := sqliteExecutor(":memory:")
	recordset, err := e.executeCommand(RequestCommand{
		Env:  "dev",
		Text: "SELECT @val AS v",
		Parameters: []datatug.Parameter{
			{ID: "val", Value: 99},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recordset.Rows))
	}
}

// TestExecuteCommand_SQLite3_NotEnoughArgsRetry covers the "not enough args" retry path.
// @param1 stays unsubstituted (no Parameters), sqlite3 reports "not enough args",
// the retry fills in nil and re-runs successfully.
func TestExecuteCommand_SQLite3_NotEnoughArgsRetry(t *testing.T) {
	e := sqliteExecutor(":memory:")
	recordset, err := e.executeCommand(RequestCommand{
		Env:  "dev",
		Text: "SELECT @param1 AS v",
	})
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row after retry, got %d", len(recordset.Rows))
	}
}

// TestExecuteCommand_SQLite3_NotEnoughArgs_DuplicateParam covers the
// duplicate-parameter continue branch in the retry loop.
// Query has @p twice; the second @p is a duplicate and hits the continue.
func TestExecuteCommand_SQLite3_NotEnoughArgs_DuplicateParam(t *testing.T) {
	e := sqliteExecutor(":memory:")
	// @p appears twice; after first substitution it becomes :1 and :1.
	// Actually the retry path fires when there are NO command.Parameters,
	// so @p is never substituted by the first loop. Both @p occurrences
	// remain and the retry loop processes them: first @p → appends nil,
	// second @p → duplicate → continue.
	recordset, err := e.executeCommand(RequestCommand{
		Env:  "dev",
		Text: "SELECT @p AS a, @p AS b",
	})
	if err != nil {
		t.Fatalf("expected retry with duplicate param to succeed, got error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row after retry, got %d", len(recordset.Rows))
	}
}

// TestExecuteCommand_GetDbByIDError covers the getDbByID error return path.
func TestExecuteCommand_GetDbByIDError(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return nil, fmt.Errorf("catalog not found: %s", dbID)
		},
		nil,
	)
	_, err := e.executeCommand(RequestCommand{Env: "env", DB: "mydb", Text: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error from getDbByID")
	}
}

// TestExecuteCommand_GetCatalogSummaryError covers getCatalogSummary error.
func TestExecuteCommand_GetCatalogSummaryError(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return &datatug.EnvDb{
				Server: datatug.ServerRef{Driver: "sqlite3"},
			}, nil
		},
		func(server datatug.ServerRef, catalogID string) (*datatug.DbCatalogSummary, error) {
			return nil, errors.New("catalog summary not found")
		},
	)
	_, err := e.executeCommand(RequestCommand{Env: "env", DB: "mydb", Text: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error from getCatalogSummary")
	}
}

// TestExecuteCommand_DefaultDriver_InvalidMode covers the "invalid connection parameters"
// error path. Port != 0 causes options=["mode=<port>"] which is not a valid mode string,
// so NewConnectionString returns an error → "invalid connection parameters: ..." wrapping.
func TestExecuteCommand_DefaultDriver_InvalidMode(t *testing.T) {
	e := NewExecutor(nil, nil)
	cmd := RequestCommand{
		Env:  "dev",
		Text: "SELECT 1",
		ServerRef: datatug.ServerRef{
			Driver: "sqlserver",
			Host:   "myhost",
			Port:   9999,
		},
	}
	_, err := e.executeCommand(cmd)
	if err == nil {
		t.Fatal("expected error for invalid connection parameters (bad mode)")
	}
}

// TestExecuteCommand_UnknownDriver_SqlOpenError covers the sql.Open error path.
// When getDbByID returns a server with an unregistered driver name, sql.Open fails.
func TestExecuteCommand_UnknownDriver_SqlOpenError(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return &datatug.EnvDb{
				Server: datatug.ServerRef{Driver: "totally-unknown-driver-xyz"},
			}, nil
		},
		nil,
	)
	_, err := e.executeCommand(RequestCommand{Env: "dev", DB: "mydb", Text: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error from sql.Open for unknown driver")
	}
}

// TestExecuteCommand_NotEnoughArgsNoParams covers the "not enough args" error path
// when the retry cannot substitute because the query uses positional ? placeholders
// (no @param patterns), so len(parameters) == 0 and we fall through to bare return.
func TestExecuteCommand_NotEnoughArgsNoParams(t *testing.T) {
	e := sqliteExecutor(":memory:")
	// "?" is a positional placeholder in sqlite3. Passing no args triggers
	// "not enough args to execute query: want 1 got 0".
	// The query has no @param patterns so the retry cannot help → bare return with error.
	_, err := e.executeCommand(RequestCommand{
		Env:  "dev",
		Text: "SELECT ? AS v",
	})
	if err == nil {
		t.Fatal("expected error for positional placeholder with no args")
	}
}

// ─── executeQuery tests ───────────────────────────────────────────────────────

// TestExecuteQuery_Error covers db.Query error (bad SQL).
func TestExecuteQuery_Error(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := NewExecutor(nil, nil)
	_, err = e.executeQuery(db, "sqlite3", "THIS IS NOT VALID SQL", nil)
	if err == nil {
		t.Fatal("expected error for invalid SQL")
	}
}

// TestExecuteQuery_Success covers the happy path of executeQuery with multiple rows and columns.
func TestExecuteQuery_Success(t *testing.T) {
	db := openMemDB(t)
	t.Cleanup(func() { _ = db.Close() })

	e := NewExecutor(nil, nil)
	recordset, err := e.executeQuery(db, "sqlite3", "SELECT id, name FROM items ORDER BY id", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(recordset.Columns))
	}
	if len(recordset.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(recordset.Rows))
	}
	if recordset.Duration == 0 {
		t.Fatal("expected non-zero duration")
	}
}

// TestExecuteQuery_NullableColumns covers row scanning with nullable values.
func TestExecuteQuery_NullableColumns(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE nulltest (v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO nulltest VALUES (NULL)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	e := NewExecutor(nil, nil)
	recordset, err := e.executeQuery(db, "sqlite3", "SELECT v FROM nulltest", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recordset.Rows))
	}
}

// TestExecuteQuery_UNIQUEIDENTIFIER_NonSqlserver covers the UNIQUEIDENTIFIER column path
// (non-sqlserver driver — no byte swap) using the registered fake driver.
func TestExecuteQuery_UNIQUEIDENTIFIER_NonSqlserver(t *testing.T) {
	db, err := sql.Open("fakeuid", "ignored")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := NewExecutor(nil, nil)
	recordset, err := e.executeQuery(db, "fakeuid", "SELECT uid", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recordset.Rows))
	}
}

// TestExecuteQuery_UNIQUEIDENTIFIER_Sqlserver covers the sqlserver byte-swap path.
func TestExecuteQuery_UNIQUEIDENTIFIER_Sqlserver(t *testing.T) {
	db, err := sql.Open("sqlserver-fake", "ignored")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := NewExecutor(nil, nil)
	recordset, err := e.executeQuery(db, "sqlserver", "SELECT uid", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recordset.Rows))
	}
}

// TestExecuteQuery_UNIQUEIDENTIFIER_Null covers the nil UNIQUEIDENTIFIER path.
func TestExecuteQuery_UNIQUEIDENTIFIER_Null(t *testing.T) {
	db, err := sql.Open("fakeuidnull", "ignored")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := NewExecutor(nil, nil)
	recordset, err := e.executeQuery(db, "fakeuidnull", "SELECT uid", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recordset.Rows))
	}
}

// TestExecuteQuery_RowsCloseError exercises the fakecloseerr driver to confirm
// executeQuery completes successfully even when the fake driver's Close() returns
// an error. The rows.Close() error path in the defer (executor.go:208-210) is
// structurally unreachable because database/sql auto-closes rows on EOF before
// the defer fires (rs.closed is already true, so the driver Close() is a no-op).
// This test documents that executeQuery returns successfully despite the driver
// error being unreachable via the defer.
func TestExecuteQuery_RowsCloseError(t *testing.T) {
	db, err := sql.Open("fakecloseerr", "ignored")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := NewExecutor(nil, nil)
	// The query succeeds; the rows.Close() defer path at executor.go:208-210 is
	// structurally unreachable (see TEST-COVERAGE.md gap documentation).
	recordset, err := e.executeQuery(db, "fakecloseerr", "SELECT n", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recordset.Rows))
	}
}

// TestExecuteCommand_DBCloseError covers the db.Close() error branch in the deferred
// close of executeCommand (executor.go:159-161). The "dbclosetest" driver's Conn.Close()
// returns an error; after a successful query the connection is established, so db.Close()
// propagates the driver error into the log (but does not return it to the caller).
func TestExecuteCommand_DBCloseError(t *testing.T) {
	e := NewExecutor(
		func(envID, dbID string) (*datatug.EnvDb, error) {
			return &datatug.EnvDb{
				Server: datatug.ServerRef{Driver: "dbclosetest", Host: "localhost"},
			}, nil
		},
		nil,
	)
	// The query succeeds; db.Close() logs an error but does not return it.
	recordset, err := e.executeCommand(RequestCommand{
		Env:  "dev",
		DB:   "mydb",
		Text: "SELECT n",
	})
	if err != nil {
		t.Fatalf("unexpected error (db.Close error is logged, not returned): %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(recordset.Rows))
	}
}

// TestExecuteQuery_UNIQUEIDENTIFIER_BadBytes covers the uuid.FromBytes error path
// (executor.go:246-248) by using a fake driver that returns a non-16-byte []byte
// value for a UNIQUEIDENTIFIER column, causing uuid.FromBytes to fail.
func TestExecuteQuery_UNIQUEIDENTIFIER_BadBytes(t *testing.T) {
	db, err := sql.Open("fakebaduid", "ignored")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := NewExecutor(nil, nil)
	_, err = e.executeQuery(db, "fakebaduid", "SELECT uid", nil)
	if err == nil {
		t.Fatal("expected error from uuid.FromBytes with invalid byte slice")
	}
}
