package sqlexecute

import (
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// The connection string of a server is built from its driver, host and port, which are
// checked first: a host that is not a host name or an address (a ";" in it starts another
// key of the string) is refused before anything is opened, and the port is a port.

// capturedDSNs are the connection strings that the "dsncapture" driver was opened with.
var capturedDSNs []string

type dsnCaptureDriver struct{}

func (dsnCaptureDriver) Open(name string) (driver.Conn, error) {
	capturedDSNs = append(capturedDSNs, name)
	return (&fakeTypedDriver{rows: []fakeRow{{colName: "n", colDbType: "INT", value: int64(1)}}}).Open(name)
}

func init() { sql.Register("dsncapture", dsnCaptureDriver{}) }

// executorOfServer is an executor whose environment has the server.
func executorOfServer(server datatug.ServerRef) Executor {
	return NewExecutor(func(envID, dbID string) (*datatug.EnvDb, error) {
		return &datatug.EnvDb{Server: server}, nil
	}, nil)
}

func TestServerConnectionParams(t *testing.T) {
	for _, tc := range []struct {
		name    string
		server  datatug.ServerRef
		command RequestCommand
		want    string
	}{
		{"a host and a port", datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com", Port: 1433}, RequestCommand{}, "server=db1.example.com;port=1433;trusted_connection=yes"},
		{"a host and no port", datatug.ServerRef{Driver: "sqlserver", Host: "localhost"}, RequestCommand{}, "server=localhost;trusted_connection=yes"},
		{"an IPv6 host", datatug.ServerRef{Driver: "sqlserver", Host: "::1", Port: 1}, RequestCommand{}, "server=::1;port=1;trusted_connection=yes"},
		{"no host (a driver that has a default)", datatug.ServerRef{Driver: "postgres"}, RequestCommand{}, "server=;trusted_connection=yes"},
		{"the highest port", datatug.ServerRef{Driver: "sqlserver", Host: "h", Port: 65535}, RequestCommand{}, "server=h;port=65535;trusted_connection=yes"},
		{"a user and a database", datatug.ServerRef{Driver: "sqlserver", Host: "h"}, RequestCommand{Credentials: datatug.Credentials{Username: "u", Password: "p"}, DB: "shop"}, "server=h;user id=u;password=p;database=shop"},
	} {
		params, err := serverConnectionParams(tc.server, tc.command)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if params.ConnectionString() != tc.want {
			t.Errorf("%s: connection string = %q, want %q", tc.name, params.ConnectionString(), tc.want)
		}
	}
}

func TestServerConnectionParams_RefusesWhatIsNotAHostOrAPort(t *testing.T) {
	var refused []sourcecases.UnsafeIdentifier
	for _, c := range sourcecases.UnsafeHosts() {
		if c.ID != "" { // an empty host is the host of a driver that has a default
			refused = append(refused, c)
		}
	}
	refused = append(refused,
		sourcecases.UnsafeIdentifier{Name: "a key of the connection string", ID: "db1.example.com;key=value"},
		sourcecases.UnsafeIdentifier{Name: "another key", ID: "db1.example.com;other=value"},
		sourcecases.UnsafeIdentifier{Name: "an equals sign", ID: "key=value"},
	)
	for _, c := range refused {
		_, err := serverConnectionParams(datatug.ServerRef{Driver: "sqlserver", Host: c.ID}, RequestCommand{})
		if err == nil {
			t.Errorf("%s: the host %q was accepted", c.Name, c.ID)
		} else if len(c.ID) >= 3 && strings.Contains(err.Error(), c.ID) {
			t.Errorf("%s: the error echoes the host: %v", c.Name, err)
		}
	}
	for _, port := range []int{-1, 65536, 99999999999} {
		if _, err := serverConnectionParams(datatug.ServerRef{Driver: "sqlserver", Host: "h", Port: port}, RequestCommand{}); err == nil {
			t.Errorf("the port %d was accepted", port)
		}
	}
}

// A server's host and port reach the driver as the connection string says, and a host that
// is not a host reaches no driver at all.
func TestExecuteCommand_TheConnectionIsBuiltFromTheServer(t *testing.T) {
	capturedDSNs = nil
	recordset, err := executorOfServer(datatug.ServerRef{Driver: "dsncapture", Host: "db1.example.com", Port: 1433}).
		executeCommand(RequestCommand{Env: "dev", DB: "shop", Text: "SELECT n"})
	if err != nil {
		t.Fatalf("a server with a port: %v", err)
	}
	if len(recordset.Rows) != 1 {
		t.Errorf("rows = %v, want one", recordset.Rows)
	}
	if want := []string{"server=db1.example.com;port=1433;trusted_connection=yes;database=shop"}; strings.Join(capturedDSNs, "|") != strings.Join(want, "|") {
		t.Errorf("the driver was opened with %q, want %q", capturedDSNs, want)
	}

	capturedDSNs = nil
	_, err = executorOfServer(datatug.ServerRef{Driver: "dsncapture", Host: "db1.example.com;key=value"}).
		executeCommand(RequestCommand{Env: "dev", DB: "shop", Text: "SELECT n"})
	if err == nil || !strings.Contains(err.Error(), "invalid connection parameters") || strings.Contains(err.Error(), "key=value") {
		t.Errorf("a host with a ';' gave %v, want the refusal of the parameters, which does not echo the host", err)
	}
	if len(capturedDSNs) != 0 {
		t.Errorf("a host that is not a host reached the driver: %q", capturedDSNs)
	}
}

// The file of a SQLite server is opened read-only as the file its name is, whatever the name
// holds: a "?", a "#" or a "%" is a character of the name, and is not the start of the
// driver's own parameters, which would open (and create) a shorter name.
func TestExecuteCommand_SQLite3_OpensTheFileItsNameIsReadOnlyAndNothingElse(t *testing.T) {
	for _, name := range []string{"what?mode=rw.db", "a#b.db", "100%.db", "a%23b.db", "with space.db"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			plain := filepath.Join(dir, "plain.db")
			raw, err := sql.Open("sqlite3", plain)
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{"CREATE TABLE marker (name TEXT)", "INSERT INTO marker VALUES ('" + strings.ReplaceAll(name, "'", "''") + "')"} {
				if _, err = raw.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			_ = raw.Close()
			path := filepath.Join(dir, name)
			if err = os.Rename(plain, path); err != nil {
				t.Fatal(err)
			}

			recordset, err := sqliteExecutor(path).executeCommand(RequestCommand{Env: "dev", Text: "SELECT name FROM marker"})
			if err != nil {
				t.Fatalf("the file was not opened as the file its name is: %v", err)
			}
			if len(recordset.Rows) != 1 || recordset.Rows[0][0] != name {
				t.Errorf("rows = %v, want the marker %q of the file that was named", recordset.Rows, name)
			}
			// The executor reads the rows of a statement, and a statement that cannot write is
			// not reported by the rows it has: the file is read again to see it was not written.
			_, _ = sqliteExecutor(path).executeCommand(RequestCommand{Env: "dev", Text: "INSERT INTO marker VALUES ('x')"})
			recordset, err = sqliteExecutor(path).executeCommand(RequestCommand{Env: "dev", Text: "SELECT name FROM marker"})
			if err != nil || len(recordset.Rows) != 1 {
				t.Errorf("the file was written through a connection that is read-only: rows %v, err %v", recordset.Rows, err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 || entries[0].Name() != name {
				t.Errorf("the folder holds %d entries after the opens, want only the file that was named", len(entries))
			}
		})
	}
}
