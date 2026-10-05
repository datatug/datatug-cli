// Package pgstandin gives the tests of this module a PostgreSQL database that has no server: a real
// *dalgo2postgres.Database whose connection pool lost its server, so that every call that needs a
// connection fails the way a read fails when the server was restarted, the password was changed or
// the connection limit was reached: with the adapter's own connection error, a fixed sentence that holds
// nothing of the configuration (dalgo2postgres v0.6.0 and later; before it the call failed with the text pgx
// writes, which names the user). A test of a command, a route or a chat puts it behind the opener of PostgreSQL
// databases and looks for a marker in the user name and the password in everything the code under test
// printed, answered or logged.
//
// Nothing is dialled: the one connection the database is opened with runs over an in-memory pipe against
// a few lines of the protocol, and every later dial fails. Test code only: nothing in this module's binaries
// imports it.
package pgstandin

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// errDialRefused is what every dial after the first fails with.
var errDialRefused = errors.New("the dial is refused: no server")

// The libpq variables of the developer's shell (PGSERVICE, PGTARGETSESSIONATTRS, PGHOST, ...) are read by pgx
// whenever it builds a configuration, which this package and the tests that build pgx's own errors do; a stand-in
// built from a shell that names a service that is not there, or asks for a server that accepts writes, would fail to
// set up, or behave unlike the one of a machine with no such variables. They are cleared when the package is
// imported, before any test runs and so before any test can be parallel (a test cannot use t.Setenv there). Nothing is
// dialled in any case.
func init() { unsetLibpqVariables(os.Environ(), os.Unsetenv) }

// unsetLibpqVariables unsets, through unset, every variable of environ (the "NAME=value" entries of os.Environ) whose
// name starts with PG, in any case (names are case-insensitive on Windows).
func unsetLibpqVariables(environ []string, unset func(name string) error) {
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PG") {
			_ = unset(name)
		}
	}
}

// Unreachable opens a *dalgo2postgres.Database as the user, with the password, against an in-memory server that
// answers the one connection the adapter verifies the source with, and then goes away for good. It uses the exact
// identifier mode, as BackendRef does, and declares no recordset, as BackendRef does; a test of a call that reads
// or writes a record by its key names the tables it needs declared (each keyed by a column "id"), because without a
// declaration those calls fail before they reach a connection. The database is closed when the test ends.
func Unreachable(tb testing.TB, user, password string, keyedTables ...string) *dalgo2postgres.Database {
	tb.Helper()
	config, err := pgx.ParseConfig("postgres://" + user + ":" + password + "@127.0.0.1:5432/shop?sslmode=disable")
	require.NoError(tb, err, "pgstandin: the configuration is readable")
	var dials atomic.Int32
	var server atomic.Pointer[net.Conn]
	config.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		if dials.Add(1) > 1 {
			return nil, errDialRefused
		}
		client, peer := net.Pipe()
		server.Store(&peer)
		go serve(peer)
		return client, nil
	}
	name := stdlib.RegisterConnConfig(config)
	tb.Cleanup(func() { stdlib.UnregisterConnConfig(name) })

	db, err := dalgo2postgres.NewDatabaseWithOptions(name, dal.NewSchema(nil, nil), keyedOptions(keyedTables),
		dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact))
	require.NoError(tb, err, "pgstandin: the database opens against the in-memory server")
	tb.Cleanup(func() { _ = db.Close() })
	// The server goes away: the one connection of the pool is dead, and a new one is refused.
	_ = (*server.Load()).Close()
	return db
}

// serve is the whole server: it reads the startup message, accepts it, and answers whatever the client sends next
// (the verification of the connection is one empty query) with an empty answer, until the client or the test closes
// the pipe.
func serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	backend := pgproto3.NewBackend(conn, conn)
	_, err := backend.ReceiveStartupMessage()
	for greeting := true; err == nil; greeting = false {
		if greeting {
			backend.Send(&pgproto3.AuthenticationOk{})
		} else {
			backend.Send(&pgproto3.EmptyQueryResponse{})
		}
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if err = backend.Flush(); err == nil {
			_, err = backend.Receive()
		}
	}
}

// keyedOptions declares each of keyedTables with a key column "id".
func keyedOptions(keyedTables []string) dalgo2sql.DbOptions {
	options := dalgo2sql.DbOptions{}
	for _, table := range keyedTables {
		if options.Recordsets == nil {
			options.Recordsets = map[string]*dalgo2sql.Recordset{}
		}
		options.Recordsets[table] = dalgo2sql.NewRecordset(table, dalgo2sql.Table, []dal.FieldRef{dal.Field("id")})
	}
	return options
}

// Recorder keeps the text of every statement that the in-memory servers of one [Recording] were sent, in the order
// they arrived. The statement the driver verifies a connection with is not one of them.
type Recorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *Recorder) record(statement string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, statement)
}

// Statements returns the statements recorded so far, a copy.
func (r *Recorder) Statements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

// connectionCheck is the statement pgx's driver sends to verify a connection (a comment, which the server answers as an
// empty query).
const connectionCheck = "-- ping"

// refusedStatement is the error every statement of a stand-in that accepts none is answered with.
const refusedStatement = "the stand-in server refuses every statement"

// Recording opens a *dalgo2postgres.Database as BackendRef does (the exact identifier mode), against in-memory servers
// that record each statement they are sent. A server answers a statement with success when accept is true, and with an
// error of the server's own kind (SQLSTATE XX000, so not a connection failure) when it is false: the second is a
// handle that fails on any statement, and a call that returns another error did not reach one. Only the simple
// protocol is spoken; a statement sent in the extended protocol, which a call with parameters does, is answered with an
// error and not recorded. The database is closed when the test ends. keyedTables are declared as in [Unreachable].
//
// Nothing is dialled: every connection of the pool is an in-memory pipe, and the pool makes as many as it needs.
func Recording(tb testing.TB, accept bool, keyedTables ...string) (*dalgo2postgres.Database, *Recorder) {
	tb.Helper()
	config, err := pgx.ParseConfig("postgres://standin:standin@127.0.0.1:5432/shop?sslmode=disable")
	require.NoError(tb, err, "pgstandin: the configuration is readable")
	recorder := &Recorder{}
	config.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		go serveRecording(peer, recorder, accept)
		return client, nil
	}
	name := stdlib.RegisterConnConfig(config)
	tb.Cleanup(func() { stdlib.UnregisterConnConfig(name) })
	db, err := dalgo2postgres.NewDatabaseWithOptions(name, dal.NewSchema(nil, nil), keyedOptions(keyedTables),
		dalgo2postgres.WithIdentifierMode(dalgo2postgres.IdentifierExact))
	require.NoError(tb, err, "pgstandin: the database opens against the in-memory server")
	tb.Cleanup(func() { _ = db.Close() })
	return db, recorder
}

// serveRecording is the server of [Recording]: it accepts the startup message and then answers what the client sends
// until the client ends the connection.
func serveRecording(conn net.Conn, recorder *Recorder, accept bool) {
	defer func() { _ = conn.Close() }()
	backend := pgproto3.NewBackend(conn, conn)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	failed := false // a message of the extended protocol was answered with an error: the rest is skipped up to the Sync
	for backend.Flush() == nil {
		message, err := backend.Receive()
		if err != nil {
			return
		}
		switch m := message.(type) {
		case *pgproto3.Terminate:
			return
		case *pgproto3.Query:
			answerQuery(backend, recorder, m.String, accept)
		case *pgproto3.Sync:
			failed = false
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		default:
			if !failed {
				failed = true
				backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: refusedStatement})
			}
		}
	}
}

// answerQuery records statement, unless it is the empty one, and answers it.
func answerQuery(backend *pgproto3.Backend, recorder *Recorder, statement string, accept bool) {
	switch {
	case statement == connectionCheck:
		backend.Send(&pgproto3.EmptyQueryResponse{})
	case accept:
		recorder.record(statement)
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("OK")})
	default:
		recorder.record(statement)
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: refusedStatement})
	}
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
}
