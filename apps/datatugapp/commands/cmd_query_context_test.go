package commands

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingInterrupt replaces interruptContext and records what the action gave
// it, so a test can see the per-invocation scope it derived and released.
type capturingInterrupt struct {
	parents []context.Context
	scopes  []context.Context
	stops   int
}

func captureInterrupt(t *testing.T) *capturingInterrupt {
	t.Helper()
	orig := interruptContext
	t.Cleanup(func() { interruptContext = orig })
	c := &capturingInterrupt{}
	interruptContext = func(parent context.Context) (context.Context, func() os.Signal, func()) {
		ctx, cancel := context.WithCancel(parent)
		c.parents = append(c.parents, parent)
		c.scopes = append(c.scopes, ctx)
		return ctx, func() os.Signal { return nil }, func() {
			c.stops++
			cancel()
		}
	}
	return c
}

// The same Cobra command object is reused after a usage error: the action gives
// the caller's own context back (instead of leaving the cancelled
// per-invocation context on the command) and releases that scope, so the next
// invocation reads through a live parent.
func TestQueryRunRestoresTheCallersContextAfterUsageError(t *testing.T) {
	path := covCSQLiteFile(t, "reuse.sqlite", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO t VALUES (1,'a')`)
	cmd, out, _ := covCRunCmd(t, map[string]string{
		queryDBFlag:         "sqlite://" + path,
		queryFromFlag:       "t",
		querySQLFlag:        "select 1", // a second input: a usage error before any read
		queryNoPoliciesFlag: "true",
		queryFormatFlag:     "json",
	})
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	cmd.SetContext(parent)
	capture := captureInterrupt(t)

	require.Error(t, queryRunCommandAction(cmd, nil), "two query inputs are a usage error")
	require.Equal(t, parent, cmd.Context(), "the caller's context is restored, not the invocation's")
	require.NoError(t, parent.Err(), "the caller's parent is still live")
	require.Equal(t, []context.Context{parent}, capture.parents)
	require.Len(t, capture.scopes, 1)
	require.ErrorIs(t, capture.scopes[0].Err(), context.Canceled, "the invocation scope is released")
	require.Equal(t, 1, capture.stops)

	// The same command, now with one input: the restored live parent lets it read.
	require.NoError(t, cmd.Flags().Set(querySQLFlag, ""))
	out.Reset()
	require.NoError(t, queryRunCommandAction(cmd, nil))
	for _, want := range []string{`"name"`, `"a"`} {
		assert.Contains(t, out.String(), want)
	}
	require.Equal(t, parent, cmd.Context())
	require.NoError(t, parent.Err())
	require.Equal(t, []context.Context{parent, parent}, capture.parents)
	require.Len(t, capture.scopes, 2)
	require.ErrorIs(t, capture.scopes[1].Err(), context.Canceled)
	require.Equal(t, 2, capture.stops)
}

// A caller that already cancelled its own context is still refused: restoring
// the original must not hide that cancellation.
func TestQueryRunKeepsExternallyCancelledParentRefused(t *testing.T) {
	path := covCSQLiteFile(t, "cancelled.sqlite", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO t VALUES (1,'a')`)
	cmd, out, _ := covCRunCmd(t, map[string]string{
		queryDBFlag:         "sqlite://" + path,
		queryFromFlag:       "t",
		queryNoPoliciesFlag: "true",
		queryFormatFlag:     "json",
	})
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	cmd.SetContext(parent)
	capture := captureInterrupt(t)

	require.Error(t, queryRunCommandAction(cmd, nil))
	assert.Empty(t, strings.TrimSpace(out.String()), "a refused read prints no rows")
	require.Equal(t, parent, cmd.Context(), "the caller's cancelled context is restored")
	require.ErrorIs(t, parent.Err(), context.Canceled)
	require.Equal(t, []context.Context{parent}, capture.parents)
	require.Len(t, capture.scopes, 1)
	require.ErrorIs(t, capture.scopes[0].Err(), context.Canceled)
	require.Equal(t, 1, capture.stops)
}

// runQueryCommand is also callable on its own: with no command context it still
// reads through a background context.
func TestRunQueryCommandUsesBackgroundForANilCommandContext(t *testing.T) {
	path := covCSQLiteFile(t, "direct.sqlite", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO t VALUES (1,'a')`)
	cmd, out, _ := covCRunCmd(t, map[string]string{
		queryDBFlag:         "sqlite://" + path,
		queryFromFlag:       "t",
		queryNoPoliciesFlag: "true",
		queryFormatFlag:     "json",
	})
	require.Nil(t, cmd.Context())
	o, err := readQueryOptions(cmd)
	require.NoError(t, err)
	require.NoError(t, runQueryCommand(cmd, o))
	assert.Contains(t, out.String(), `"name"`)
}

// Called directly (never through Execute) the command has no context: the
// helper still gets one, and nil is what the command holds again afterwards.
func TestQueryRunRestoresANilContext(t *testing.T) {
	path := covCSQLiteFile(t, "nil.sqlite", `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO t VALUES (1,'a')`)
	cmd, out, _ := covCRunCmd(t, map[string]string{
		queryDBFlag:         "sqlite://" + path,
		queryFromFlag:       "t",
		queryNoPoliciesFlag: "true",
		queryFormatFlag:     "json",
	})
	require.Nil(t, cmd.Context())
	capture := captureInterrupt(t)

	require.NoError(t, queryRunCommandAction(cmd, nil))
	assert.Contains(t, out.String(), `"name"`)
	assert.Nil(t, cmd.Context(), "a nil caller context is restored as nil")
	require.Len(t, capture.parents, 1)
	require.NotNil(t, capture.parents[0])
	require.NoError(t, capture.parents[0].Err())
	require.ErrorIs(t, capture.scopes[0].Err(), context.Canceled)
	require.Equal(t, 1, capture.stops)

	out.Reset()
	require.NoError(t, queryRunCommandAction(cmd, nil), "the same command reads again")
	assert.Contains(t, out.String(), `"name"`)
	assert.Nil(t, cmd.Context())
	require.Equal(t, 2, capture.stops)
}
