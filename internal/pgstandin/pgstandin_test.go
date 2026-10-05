package pgstandin

import (
	"context"
	"errors"
	"testing"

	dalrecord "github.com/dal-go/record"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The database the package hands out is a real adapter whose server has gone away: a call that needs a connection
// fails with the text pgx writes for it, which names the user, and the failure holds pgx's own *pgconn.ConnectError,
// which holds the whole configuration. That is what a read of a source sees once its pool cannot make a connection
// again, and it is what the tests of the packages that print errors must keep out of their outputs.
func TestUnreachable_AReadFailsWithTheTextAndTheTypesPgxWrites(t *testing.T) {
	t.Parallel()
	db := Unreachable(t, "USERMARKER-standin", "PWMARKER-standin")

	_, err := db.ListCollections(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "USERMARKER-standin", "pgx names the user in the text")
	var connectErr *pgconn.ConnectError
	require.True(t, errors.As(err, &connectErr), "and the failure holds pgx's own error")
	assert.Equal(t, "PWMARKER-standin", connectErr.Config.Password, "which holds the password too")
	assert.ErrorIs(t, err, errDialRefused)
}

// Two stand-ins are two databases, each against its own server and its own configuration.
func TestUnreachable_TwoStandInsAreIndependent(t *testing.T) {
	t.Parallel()
	first := Unreachable(t, "FIRSTUSER", "pw")
	second := Unreachable(t, "SECONDUSER", "pw")
	_, firstErr := first.ListCollections(context.Background(), nil)
	_, secondErr := second.ListCollections(context.Background(), nil)
	assert.Contains(t, firstErr.Error(), "FIRSTUSER")
	assert.NotContains(t, firstErr.Error(), "SECONDUSER")
	assert.Contains(t, secondErr.Error(), "SECONDUSER")
}

// A table named as keyed is declared with a key column "id", so that the calls that read or write a record by its
// key reach the connection too.
func TestUnreachable_ANamedTableIsKeyedSoAKeyedCallReachesTheConnection(t *testing.T) {
	t.Parallel()
	db := Unreachable(t, "USERMARKER-standin", "PWMARKER-standin", "orders", "customers")
	record := dalrecord.NewRecordWithData(dalrecord.NewKeyWithID("orders", "1"), map[string]any{"status": "new"})
	err := db.Get(context.Background(), record)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "USERMARKER-standin")
}
