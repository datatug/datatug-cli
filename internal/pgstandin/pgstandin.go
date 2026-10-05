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

	options := dalgo2sql.DbOptions{}
	for _, table := range keyedTables {
		if options.Recordsets == nil {
			options.Recordsets = map[string]*dalgo2sql.Recordset{}
		}
		options.Recordsets[table] = dalgo2sql.NewRecordset(table, dalgo2sql.Table, []dal.FieldRef{dal.Field("id")})
	}
	db, err := dalgo2postgres.NewDatabaseWithOptions(name, dal.NewSchema(nil, nil), options,
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
