package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/internal/pgstandin"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parts of a PostgreSQL source that must never be shown: a marker in the password, one in the user name and
// one in a query parameter.
const (
	pgMarkerPassword = "PWMARKER-dt02-cmd"
	pgMarkerUser     = "USERMARKER-dt02-cmd"
	pgMarkerQuery    = "QUERYMARKER-dt02-cmd"
	pgMarkedSource   = "postgres://" + pgMarkerUser + ":" + pgMarkerPassword + "@db.example.com:5433/shop?application_name=" + pgMarkerQuery
)

// pgOpens is what the stand-in for the PostgreSQL constructor saw.
type pgOpens struct {
	calls atomic.Int32
	dsns  []string
}

// standInForPostgres replaces the constructor of a PostgreSQL database for the length of a test with one that records
// the connection string it was given and answers err (nil: a database that holds nothing).
func standInForPostgres(t *testing.T, err error) *pgOpens {
	t.Helper()
	opens := &pgOpens{}
	restore := dbcopy.SetPostgresOpenerForTest(func(dsn string, _ dal.Schema, _ dalgo2sql.DbOptions, _ ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		opens.calls.Add(1)
		opens.dsns = append(opens.dsns, dsn)
		if err != nil {
			return nil, err
		}
		return &dalgo2postgres.Database{}, nil
	})
	t.Cleanup(restore)
	return opens
}

// assertNoPgMarkers fails when what the command wrote holds the password, the user name or the query parameter.
func assertNoPgMarkers(t *testing.T, name string, outputs ...string) {
	t.Helper()
	for _, output := range outputs {
		for _, marker := range []string{pgMarkerPassword, pgMarkerUser, pgMarkerQuery} {
			assert.NotContains(t, output, marker, name)
		}
	}
}

// A read through one or more policies on a PostgreSQL source is refused with the fixed sentence before the
// source is opened: the opener is never called, and the exit code is the one of invalid use.
func TestQuery_APostgresSourceReadThroughPoliciesIsRefused(t *testing.T) {
	opens := standInForPostgres(t, nil)
	dir := policiesDir(t, map[string]string{"p.yaml": permissivePolicy})

	stdout, stderr, code := runQuery(t, "", "--db", pgMarkedSource, "--from", "customers", "--policies-dir", dir, "--as", "alice")
	assert.Equal(t, exitCodeUsage, code)
	assert.Equal(t, "policy-enforced reads on PostgreSQL sources are not available yet: run with --no-policies\n", stderr)
	assert.Empty(t, stdout)
	assert.Zero(t, opens.calls.Load(), "the opener is never called")
}

// With no policy, the source is opened as a protected read: the connection string the adapter gets
// has the read-only session and the other defaults, and the failure of the open is the classified one, with the exit
// code of its class and nothing of the source but the display form. Every class of failure of an open exits with 4, as
// the specs of `query run` and `db copy` require (the database cannot be opened): the classes differ in the sentence,
// which is the adapter's own, chosen by kind and SQLSTATE and never by text.
func TestQuery_APostgresSourceIsOpenedReadOnlyAndItsFailuresAreClassified(t *testing.T) {
	for name, tc := range map[string]struct {
		cause    error
		wantCode int
		wantText string
	}{
		"a rejected password":    {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01"}, exitCodeDatabase, "the server refused the connection: password authentication failed (SQLSTATE 28P01)"},
		"a server that is down":  {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "db.example.com", Port: "5433", Database: "shop"}, exitCodeDatabase, "the server could not be reached"},
		"a database that is not": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "3D000"}, exitCodeDatabase, "the server refused the connection: the database does not exist (SQLSTATE 3D000)"},
		"a timeout":              {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTimeout}, exitCodeDatabase, "the connection timed out or was canceled"},
		"a TLS failure":          {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTLS}, exitCodeDatabase, "the TLS handshake with the server failed"},
		"anything else":          {errors.New("dial " + pgMarkedSource + ": boom"), exitCodeDatabase, "the driver could not open the source (its own message is not shown: a driver can quote the connection string)"},
	} {
		opens := standInForPostgres(t, tc.cause)
		stdout, stderr, code := runQuery(t, "", "--db", pgMarkedSource, "--from", "customers", "--no-policies")
		assert.Equal(t, tc.wantCode, code, name)
		assert.Contains(t, stderr, tc.wantText+"; "+queryHint, name)
		assert.NotContains(t, stderr, "db.example.com", name)
		assert.NotContains(t, stderr, "5433", name)
		assert.Empty(t, stdout, name)
		assertNoPgMarkers(t, name, stdout, stderr)
		require.Len(t, opens.dsns, 1, name)
		assert.Contains(t, opens.dsns[0], "default_transaction_read_only=on", name)
		assert.Contains(t, opens.dsns[0], "statement_timeout=30000", name)
		assert.Contains(t, opens.dsns[0], "connect_timeout=10", name)
	}
}

// queryHint is what a failure of the PostgreSQL source of --db says about where its connection string is read from.
const queryHint = "the PostgreSQL connection string is read from the --db flag"

// A source that opened and whose pool cannot make a connection again (the server was restarted, the password was changed,
// the connection limit was reached) fails a read with the adapter's connection error. The command shows the same text as
// an open that fails gives (the adapter's sentence and the hint of the flag), and nothing of the user, the password or
// the parameter. (Before the adapter classified such a failure, the read failed with the text pgx writes, which names the
// user, and the command showed a sentence of its own.)
func TestQuery_AReadThatLosesItsConnectionShowsOneFixedSentence(t *testing.T) {
	// The stand-in is built here, on the goroutine of the test: the opener runs on another one, where a failure of
	// the setup (FailNow) would end that goroutine and not the test.
	standIn := pgstandin.Unreachable(t, pgMarkerUser, pgMarkerPassword)
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return standIn, nil
	}))
	stdout, stderr, code := runQuery(t, "", "--db", pgMarkedSource, "--from", "customers", "--no-policies")
	assert.Equal(t, exitCodeDatabase, code)
	assert.Contains(t, stderr, "the connection failed; "+queryHint+"\n")
	assert.NotContains(t, stderr, "failed to connect")
	assert.NotContains(t, stderr, "db.example.com")
	assert.Empty(t, stdout)
	assertNoPgMarkers(t, "lost connection", stdout, stderr)
}

// A URL that turns the read-only session off is refused by the read, and the refusal names the parameter and the
// one place that writes, and nothing of the URL.
func TestQuery_AURLThatTurnsReadOnlyOffIsRefused(t *testing.T) {
	opens := standInForPostgres(t, nil)
	source := pgMarkedSource + "&default_transaction_read_only=off"
	stdout, stderr, code := runQuery(t, "", "--db", source, "--from", "customers", "--no-policies")
	assert.Equal(t, exitCodeDatabase, code)
	assert.Contains(t, stderr, "default_transaction_read_only")
	assert.Contains(t, stderr, "datatug db copy --to")
	assert.Empty(t, stdout)
	assertNoPgMarkers(t, "off", stdout, stderr)
	assert.Zero(t, opens.calls.Load())
}

// A PostgreSQL --from that turns the read-only session off is refused before anything is dialled,
// and a --to that is not reachable exits 4 without showing the connection string. (That a --to is opened without the
// read-only default is the test of postgresConnectionString in pkg/dbcopy.)
func TestDBCopy_OnlyTheTargetIsOpenedForWriting(t *testing.T) {
	standInForPostgres(t, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "127.0.0.1", Port: "1", Database: "db"})
	file := emptySQLiteFile(t)

	// --to is not reachable (nothing listens on port 1): exit 4, the secret stays out of the text.
	_, _, err := runCopy(t, "db", "copy", "--from", file, "--to", "postgres://"+pgMarkerUser+":"+pgMarkerPassword+"@127.0.0.1:1/db?sslmode=disable&connect_timeout=2&default_transaction_read_only=off")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open --to")
	var coder ExitCoder
	if assert.ErrorAs(t, err, &coder) {
		assert.Equal(t, 4, coder.ExitCode(), "a target that cannot be opened exits 4")
	}
	assertNoPgMarkers(t, "to", err.Error())

	// --from, though, refuses a URL that turns it off.
	_, _, err = runCopy(t, "db", "copy", "--from", pgMarkedSource+"&default_transaction_read_only=off", "--to", file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open --from")
	assert.Contains(t, err.Error(), "default_transaction_read_only")
	assertNoPgMarkers(t, "from", err.Error())
}

// A PostgreSQL --from that cannot be reached names the flag it was typed in, exits 4, and shows nothing of the
// connection string.
func TestDBCopy_AnUnreachableFromSideNamesTheFromFlag(t *testing.T) {
	standInForPostgres(t, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: "127.0.0.1", Port: "1", Database: "shop"})

	stdout, stderr, err := runCopy(t, "db", "copy", "--from", "postgres://"+pgMarkerUser+":"+pgMarkerPassword+"@127.0.0.1:1/shop?sslmode=disable&connect_timeout=2", "--to", emptySQLiteFile(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "open --from")
	assert.Contains(t, err.Error(), "the PostgreSQL connection string is read from the --from flag")
	var coder ExitCoder
	if assert.ErrorAs(t, err, &coder) {
		assert.Equal(t, 4, coder.ExitCode())
	}
	assertNoPgMarkers(t, "from", stdout.String(), stderr.String(), err.Error())
}

// The help of `db copy` says that --to is the only place a PostgreSQL database is opened for writing, and that the
// source is read-only.
func TestDBCopy_HelpSaysWhereAPostgresDatabaseIsWritten(t *testing.T) {
	t.Parallel()
	stdout, _, err := runCopy(t, "db", "copy", "--help")
	require.NoError(t, err)
	help := stdout.String()
	assert.Contains(t, help, "the only way DataTug opens PostgreSQL for writing")
	assert.Contains(t, help, "postgres:// read-only")
	assert.Contains(t, help, "default_transaction_read_only")
	for _, line := range strings.Split(help, "\n") {
		assert.NotContains(t, line, "PWMARKER", fmt.Sprint(line))
	}
}

// emptySQLiteFile makes the empty file that a sqlite:// side of `db copy` needs, and returns its URL.
func emptySQLiteFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.db")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	return "sqlite://" + path
}
