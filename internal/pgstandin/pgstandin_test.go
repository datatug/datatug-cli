package pgstandin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/dal-go/dalgo2postgres"
	dalrecord "github.com/dal-go/record"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The database the package hands out is a real adapter whose server has gone away: a call that needs a connection
// fails with the adapter's own connection error, which says what failed by a fixed sentence and holds nothing of the
// configuration: not the user in its text and not pgx's error in its chain. That is what a read of a source sees once
// its pool cannot make a connection again, and the tests of the packages that print errors rest on it: they look for
// the user and the password in what they print, and the markers are not there to find unless the code under test
// puts them there.
// (Before dalgo2postgres v0.6.0 the failure held pgx's own error and its text named the user; the test then asserted
// that, and the stand-in's purpose was to hand code a text with a secret in it. The adapter now keeps it out itself.)
func TestUnreachable_AReadFailsWithTheAdaptersConnectionErrorAndNothingOfThePool(t *testing.T) {
	t.Parallel()
	db := Unreachable(t, "USERMARKER-standin", "PWMARKER-standin")

	_, err := db.ListCollections(context.Background(), nil)
	require.Error(t, err)
	var adapterErr *dalgo2postgres.ConnectionError
	require.ErrorAs(t, err, &adapterErr, "the failure is the adapter's own connection error")
	assert.Empty(t, adapterErr.Host+adapterErr.Port+adapterErr.Database, "a call after the open names no part of the connection")
	assert.NotContains(t, err.Error(), "USERMARKER-standin")
	assert.NotContains(t, err.Error(), "PWMARKER-standin")
	var connectErr *pgconn.ConnectError
	assert.False(t, errors.As(err, &connectErr), "pgx's own error, which holds the password, is not reachable")
	assert.NotErrorIs(t, err, errDialRefused)
}

// Two stand-ins are two databases, each against its own server and its own configuration.
func TestUnreachable_TwoStandInsAreIndependent(t *testing.T) {
	t.Parallel()
	first := Unreachable(t, "FIRSTUSER", "pw")
	second := Unreachable(t, "SECONDUSER", "pw")
	_, firstErr := first.ListCollections(context.Background(), nil)
	_, secondErr := second.ListCollections(context.Background(), nil)
	require.Error(t, firstErr)
	require.Error(t, secondErr)
	assert.NotContains(t, firstErr.Error()+secondErr.Error(), "FIRSTUSER")
	assert.NotContains(t, firstErr.Error()+secondErr.Error(), "SECONDUSER")
	assert.NotSame(t, first, second)
	assert.NotEqual(t, fmt.Sprintf("%p", first.DB), fmt.Sprintf("%p", second.DB), "each has its own pool")
}

// A table named as keyed is declared with a key column "id", so that the calls that read or write a record by its
// key reach the connection too.
func TestUnreachable_ANamedTableIsKeyedSoAKeyedCallReachesTheConnection(t *testing.T) {
	t.Parallel()
	db := Unreachable(t, "USERMARKER-standin", "PWMARKER-standin", "orders", "customers")
	record := dalrecord.NewRecordWithData(dalrecord.NewKeyWithID("orders", "1"), map[string]any{"status": "new"})
	err := db.Get(context.Background(), record)
	require.Error(t, err)
	var adapterErr *dalgo2postgres.ConnectionError
	assert.ErrorAs(t, err, &adapterErr, "a keyed call reaches the connection: without the declaration it would fail before it, with another error")
	assert.NotContains(t, err.Error(), "USERMARKER-standin")
}

// Only the variables of libpq are unset, whatever the case of their names: the rest of the environment is not touched.
func TestUnsetLibpqVariables_UnsetsEveryPGVariableAndNothingElse(t *testing.T) {
	t.Parallel()
	var unset []string
	unsetLibpqVariables([]string{"PGSERVICE=none", "PGHOST=h", "pgtargetsessionattrs=read-write", "HOME=/home/x", "PATH=/bin", "=C:=C:\\", "NOTPG=1", "PG"},
		func(name string) error { unset = append(unset, name); return nil })
	assert.Equal(t, []string{"PGSERVICE", "PGHOST", "pgtargetsessionattrs", "PG"}, unset)
}

// A shell that names a service that is not there, and asks for a server that accepts writes, does not reach a stand-in:
// the variables are cleared when the package is imported. The test runs the binary of the package again with such a
// shell (a variable of a test that is parallel cannot be set from within), and the stand-in must open and fail a read
// as it does with no variables at all. Without the clearing the configuration is not readable, which fails the setup.
func TestUnreachable_TheLibpqVariablesOfTheShellDoNotReachAStandIn(t *testing.T) {
	if os.Getenv(libpqChildEnv) == "1" {
		db := Unreachable(t, "USERMARKER-standin", "PWMARKER-standin")
		_, err := db.ListCollections(context.Background(), nil)
		require.Error(t, err)
		var adapterErr *dalgo2postgres.ConnectionError
		assert.ErrorAs(t, err, &adapterErr)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestUnreachable_TheLibpqVariablesOfTheShellDoNotReachAStandIn$", "-test.count=1")
	command.Env = append(os.Environ(), libpqChildEnv+"=1", "PGSERVICE=no-such-service-in-this-shell", "PGTARGETSESSIONATTRS=read-write", "PGHOST=host.invalid")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

// libpqChildEnv tells the run of the test above that it is the second one.
const libpqChildEnv = "DATATUG_TEST_STANDIN_CHILD"
