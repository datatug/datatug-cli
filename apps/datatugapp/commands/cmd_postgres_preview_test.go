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

// With the switch off, `query run` on a PostgreSQL source answers the preview sentence, with or without policies, and
// never calls the opener.
func TestQuery_APostgresSourceWithThePreviewOffAnswersThePreviewSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	opens := standInForPostgres(t, nil)
	dir := policiesDir(t, map[string]string{"p.yaml": permissivePolicy})

	for name, args := range map[string][]string{
		"no policies":    {"--no-policies"},
		"with a policy":  {"--policies-dir", dir, "--as", "alice"},
		"an env source":  {"--no-policies"},
		"a typed source": {"--no-policies", "-f", "-"},
		// No policy flag at all and no policies directory: the person is told about the preview first, not about
		// the policies, which turning the preview on would not change.
		"no policy flag": {},
	} {
		source := pgMarkedSource
		if name == "an env source" {
			t.Setenv("DATATUG_DT02_PG_URL", pgMarkedSource)
			source = "env:DATATUG_DT02_PG_URL"
		}
		stdout, stderr, code := runQuery(t, "from: {name: customers}\n", append([]string{"--db", source, "--from", "customers"}, args...)...)
		if name == "a typed source" {
			stdout, stderr, code = runQuery(t, "from: {name: customers}\n", append([]string{"--db", source}, args...)...)
		}
		assert.Equal(t, exitCodeDatabase, code, name)
		assert.Contains(t, stderr, dbcopy.ErrPostgresPreview.Error(), name)
		assert.Empty(t, stdout, name)
		assertNoPgMarkers(t, name, stdout, stderr)
	}
	assert.Zero(t, opens.calls.Load(), "the opener is never called")
}

// With the switch on, a read through one or more policies on a PostgreSQL source is refused with the fixed sentence before the
// source is opened: the opener is never called, and the exit code is the one of invalid use.
func TestQuery_APostgresSourceReadThroughPoliciesIsRefused(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	opens := standInForPostgres(t, nil)
	dir := policiesDir(t, map[string]string{"p.yaml": permissivePolicy})

	stdout, stderr, code := runQuery(t, "", "--db", pgMarkedSource, "--from", "customers", "--policies-dir", dir, "--as", "alice")
	assert.Equal(t, exitCodeUsage, code)
	assert.Equal(t, "policy-enforced reads on PostgreSQL sources are not available in this preview\n", stderr)
	assert.Empty(t, stdout)
	assert.Zero(t, opens.calls.Load(), "the opener is never called")
}

// With the switch on and no policy, the source is opened as a protected read: the connection string the adapter gets
// has the read-only session and the other defaults, and the failure of the open is the classified one, with the exit
// code of its class and nothing of the source but the display form. Every class of failure of an open exits with 4, as
// the specs of `query run` and `db copy` require (the database cannot be opened): the classes differ in the sentence,
// which is the adapter's own, chosen by kind and SQLSTATE and never by text.
func TestQuery_APostgresSourceIsOpenedReadOnlyAndItsFailuresAreClassified(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	for name, tc := range map[string]struct {
		cause    error
		wantCode int
		wantText string
	}{
		"a rejected password":    {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "28P01"}, exitCodeDatabase, "the server rejected the user or the password"},
		"a server that is down":  {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork}, exitCodeDatabase, "the server could not be reached"},
		"a database that is not": {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureServer, SQLState: "3D000"}, exitCodeDatabase, "the database does not exist"},
		"a timeout":              {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTimeout}, exitCodeDatabase, "the attempt timed out"},
		"a TLS failure":          {&dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureTLS}, exitCodeDatabase, "the TLS handshake with the server failed"},
		"anything else":          {errors.New("dial " + pgMarkedSource + ": boom"), exitCodeDatabase, "the driver could not open the source"},
	} {
		opens := standInForPostgres(t, tc.cause)
		stdout, stderr, code := runQuery(t, "", "--db", pgMarkedSource, "--from", "customers", "--no-policies")
		assert.Equal(t, tc.wantCode, code, name)
		assert.Contains(t, stderr, `open postgres source "postgres://db.example.com:5433/shop": `+tc.wantText, name)
		assert.Empty(t, stdout, name)
		assertNoPgMarkers(t, name, stdout, stderr)
		require.Len(t, opens.dsns, 1, name)
		assert.Contains(t, opens.dsns[0], "default_transaction_read_only=on", name)
		assert.Contains(t, opens.dsns[0], "statement_timeout=30000", name)
		assert.Contains(t, opens.dsns[0], "connect_timeout=10", name)
	}
}

// A source that opened and whose pool cannot make a connection again (the server was restarted, the password was changed,
// the connection limit was reached) fails a read with the text pgx writes, which names the user and holds the whole
// configuration. The command shows one fixed sentence, and nothing of the user, the password or the parameter.
func TestQuery_AReadThatLosesItsConnectionShowsOneFixedSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	// The stand-in is built here, on the goroutine of the test: the opener runs on another one, where a failure of
	// the setup (FailNow) would end that goroutine and not the test.
	standIn := pgstandin.Unreachable(t, pgMarkerUser, pgMarkerPassword)
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return standIn, nil
	}))
	stdout, stderr, code := runQuery(t, "", "--db", pgMarkedSource, "--from", "customers", "--no-policies")
	assert.Equal(t, exitCodeDatabase, code)
	assert.Contains(t, stderr, "the connection to the PostgreSQL server was lost and could not be made again\n")
	assert.NotContains(t, stderr, "failed to connect")
	assert.Empty(t, stdout)
	assertNoPgMarkers(t, "lost connection", stdout, stderr)
}

// A URL that turns the read-only session off is refused by the read, and the refusal names the parameter and the
// one place that writes, and nothing of the URL.
func TestQuery_AURLThatTurnsReadOnlyOffIsRefused(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
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

// With the switch off, `db copy` answers the preview sentence for a PostgreSQL --from and for a PostgreSQL --to, names the
// side, and never calls the opener.
func TestDBCopy_APostgresSideWithThePreviewOffAnswersThePreviewSentence(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	opens := standInForPostgres(t, nil)
	file := emptySQLiteFile(t)

	for side, argv := range map[string][]string{
		"--from": {"db", "copy", "--from", pgMarkedSource, "--to", file},
		"--to":   {"db", "copy", "--from", file, "--to", pgMarkedSource},
	} {
		stdout, stderr, err := runCopy(t, argv...)
		require.Error(t, err, side)
		assert.Equal(t, "open "+side+": "+dbcopy.ErrPostgresPreview.Error(), err.Error(), side)
		var coder ExitCoder
		require.ErrorAs(t, err, &coder, side)
		assert.Equal(t, 4, coder.ExitCode(), side)
		assertNoPgMarkers(t, side, stdout.String(), stderr.String(), err.Error())
	}
	assert.Zero(t, opens.calls.Load(), "the opener is never called")
}

// With the switch on, a PostgreSQL --from is read through a read-only session and a PostgreSQL --to is the one open that is not.
func TestDBCopy_OnlyTheTargetIsOpenedForWriting(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	missing := "sqlite://" + filepath.Join(t.TempDir(), "missing.db")

	// --from is read-only; --to (a file that is not there) stops the command before any copy.
	opens := standInForPostgres(t, nil)
	_, _, err := runCopy(t, "db", "copy", "--from", pgMarkedSource, "--to", missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open --to")
	require.Len(t, opens.dsns, 1)
	assert.Contains(t, opens.dsns[0], "default_transaction_read_only=on")

	// --to is opened without the read-only default, and a URL that turns it off is allowed there.
	opens = standInForPostgres(t, &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork})
	file := emptySQLiteFile(t)
	_, _, err = runCopy(t, "db", "copy", "--from", file, "--to", pgMarkedSource+"&default_transaction_read_only=off")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `open --to: open postgres source "postgres://db.example.com:5433/shop": the server could not be reached`)
	require.Len(t, opens.dsns, 1)
	assert.NotContains(t, opens.dsns[0], "default_transaction_read_only=on")
	assert.Contains(t, opens.dsns[0], "default_transaction_read_only=off")
	assertNoPgMarkers(t, "to", err.Error())

	// --from, though, refuses a URL that turns it off.
	opens = standInForPostgres(t, nil)
	_, _, err = runCopy(t, "db", "copy", "--from", pgMarkedSource+"&default_transaction_read_only=off", "--to", file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open --from")
	assert.Contains(t, err.Error(), "default_transaction_read_only")
	assert.Zero(t, opens.calls.Load())
	assertNoPgMarkers(t, "from", err.Error())
}

// The help of `db copy` says that --to is the only place a PostgreSQL database is opened for writing, and that the
// source is read-only and a preview.
func TestDBCopy_HelpSaysWhereAPostgresDatabaseIsWritten(t *testing.T) {
	t.Parallel()
	stdout, _, err := runCopy(t, "db", "copy", "--help")
	require.NoError(t, err)
	help := stdout.String()
	assert.Contains(t, help, "the only way DataTug opens PostgreSQL for writing")
	assert.Contains(t, help, "postgres:// read-only")
	assert.Contains(t, help, dbcopy.PostgresPreviewEnv+"=1")
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
