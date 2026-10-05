package sqlexecute

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// The user, the password and the database of a command are not written into the connection
// string of a server: no caller passes them (the databases route connects with the server it was
// given and nothing else), and a value that holds a ";" or a "=" would start another key of the
// string, which the driver reads over an earlier one. A test of the string that a caller's value
// reaches fails when one of the three is written again unchecked.
func TestServerConnectionParams_NeverWritesTheUserThePasswordOrTheDatabase(t *testing.T) {
	server := datatug.ServerRef{Driver: "sqlserver", Host: "db1.example.com", Port: 1433}
	const want = "server=db1.example.com;port=1433;trusted_connection=yes"
	for _, tc := range []struct {
		name    string
		command RequestCommand
	}{
		{"plain values", RequestCommand{Credentials: datatug.Credentials{Username: "alice", Password: "secret"}, DB: "shop"}},
		{"a user that names another server", RequestCommand{Credentials: datatug.Credentials{Username: "alice;server=evil.example.com"}}},
		{"a database that names another server", RequestCommand{DB: "shop;server=evil.example.com"}},
		{"a password with a separator", RequestCommand{Credentials: datatug.Credentials{Username: "alice", Password: "pa;ss=word;server=evil.example.com"}}},
		{"braces and a control character", RequestCommand{Credentials: datatug.Credentials{Username: "a{b}\n", Password: "{x}"}, DB: "d\x00b"}},
	} {
		params, err := serverConnectionParams(server, tc.command)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := params.ConnectionString(); got != want {
			t.Errorf("%s: connection string = %q, want %q", tc.name, got, want)
		}
		if shown := params.String(); strings.Contains(shown, "evil") || strings.Contains(shown, "password=") || strings.Contains(shown, "user id=") || strings.Contains(shown, "database=") {
			t.Errorf("%s: the string that is shown holds a value of the caller: %q", tc.name, shown)
		}
	}
}

// blockingDriver opens connections whose queries wait for the context of the call to end, as the
// connection to a server that accepts and never answers does.
type blockingDriver struct{}

func (blockingDriver) Open(string) (driver.Conn, error) { return blockingConn{}, nil }

type blockingConn struct{}

func (blockingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (blockingConn) Close() error                        { return nil }
func (blockingConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }

// QueryContext waits for the context to end: a query that was made with no context never does.
func (blockingConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func init() { sql.Register("blockingdb", blockingDriver{}) }

// A query runs with the context of its request: a server that never answers ends with the
// context's own error when the deadline passes, and a request that was cancelled before it began
// runs no query.
func TestExecuteSingleContext_AQueryEndsWhenItsContextEnds(t *testing.T) {
	executor := executorOfServer(datatug.ServerRef{Driver: "blockingdb", Host: "db1.example.com", Port: 1433})
	command := RequestCommand{Env: "dev", DB: "shop", Text: "SELECT n"}

	type outcome struct {
		err error
	}
	run := func(ctx context.Context) outcome {
		done := make(chan outcome, 1)
		go func() {
			_, err := executor.ExecuteSingleContext(ctx, command)
			done <- outcome{err}
		}()
		select {
		case result := <-done:
			return result
		case <-time.After(5 * time.Second):
			t.Fatal("the query did not end when its context did")
			return outcome{}
		}
	}

	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if result := run(deadline); !errors.Is(result.err, context.DeadlineExceeded) {
		t.Errorf("a server that never answers: err = %v, want the deadline", result.err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if result := run(cancelled); !errors.Is(result.err, context.Canceled) {
		t.Errorf("a request that was cancelled: err = %v, want the cancellation", result.err)
	}
}

// The calls that take no context have none to end: they behave as they did.
func TestExecuteSingle_NeedsNoContext(t *testing.T) {
	capturedDSNs = nil
	response, err := executorOfServer(datatug.ServerRef{Driver: "dsncapture", Host: "db1.example.com"}).
		ExecuteSingle(RequestCommand{Env: "dev", DB: "shop", Text: "SELECT n"})
	if err != nil || len(response.Commands) != 1 {
		t.Fatalf("response = %+v, err = %v", response, err)
	}
}
