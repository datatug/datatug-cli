package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInterrupt replaces interruptContext with a context that the test cancels, as a signal does, and records whether the
// stop function was called.
func fakeInterrupt(t *testing.T, sig os.Signal) (cancel func(), stopped *bool) {
	t.Helper()
	orig := interruptContext
	t.Cleanup(func() { interruptContext = orig })
	var cancelled bool
	stopped = new(bool)
	var cancelCtx context.CancelFunc
	interruptContext = func(parent context.Context) (context.Context, func() os.Signal, func()) {
		ctx, c := context.WithCancel(parent)
		cancelCtx = c
		return ctx, func() os.Signal {
				if cancelled {
					return sig
				}
				return nil
			}, func() {
				*stopped = true
				c()
			}
	}
	return func() { cancelled = true; cancelCtx() }, stopped
}

// A signal during the export stops it through its own return path (where the staging folder is removed), exits 1 and says
// that it was interrupted; with --json the report says failed.
func TestExportInterruptedDuringTheExport(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `INSERT INTO p VALUES (1)`)
	origRun := runNativeExport
	defer func() { runNativeExport = origRun }()
	for _, dry := range []bool{false, true} {
		cancel, stopped := fakeInterrupt(t, os.Interrupt)
		var gotCtx context.Context
		runNativeExport = func(ctx context.Context, _ dal.DB, _ string, _ ...dbcopy.ExportOptions) (*dbcopy.ExportResult, error) {
			gotCtx = ctx
			cancel() // the signal arrives while rows are written
			<-ctx.Done()
			return nil, ctx.Err()
		}
		args := []string{"db", "export", "--from", src, "--to", filepath.Join(t.TempDir(), "out"), "--json"}
		if dry {
			args = append(args, "--dry-run")
		}
		stdout, _, err := runCopy(t, args...)
		assert.Equal(t, 1, exportExitCode(t, err))
		assert.ErrorContains(t, err, "interrupted (interrupt)")
		assert.ErrorContains(t, err, "nothing published")
		var rep map[string]any
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &rep))
		assert.Equal(t, "failed", rep["status"])
		assert.Equal(t, dry, rep["dryRun"] == true)
		assert.NotNil(t, gotCtx)
		assert.True(t, *stopped, "the signals are given back")
	}
	// The same through db copy to a new folder.
	cancel, _ := fakeInterrupt(t, os.Interrupt)
	runNativeExport = func(ctx context.Context, _ dal.DB, _ string, _ ...dbcopy.ExportOptions) (*dbcopy.ExportResult, error) {
		cancel()
		return nil, ctx.Err()
	}
	_, _, err := runCopy(t, "db", "copy", "--from", src, "--to", "ingitdb://"+filepath.Join(t.TempDir(), "n"))
	assert.Equal(t, 1, exportExitCode(t, err))
	assert.ErrorContains(t, err, "interrupted (interrupt): the db copy was stopped")
}

// A signal while the source is opened is the same interruption, not a connection failure (exit 4).
func TestExportInterruptedWhileOpeningTheSource(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	origOpen := openExportSource
	defer func() { openExportSource = origOpen }()
	cancel, _ := fakeInterrupt(t, os.Interrupt)
	openExportSource = func(ctx context.Context, _ dbcopy.BackendRef, _ string) (dal.DB, error) {
		cancel()
		return nil, ctx.Err()
	}
	_, _, err := runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(t.TempDir(), "out"))
	assert.Equal(t, 1, exportExitCode(t, err))
	assert.ErrorContains(t, err, "interrupted (interrupt)")
	// Without a signal the same failure stays a connection failure.
	fakeInterrupt(t, os.Interrupt)
	openExportSource = func(context.Context, dbcopy.BackendRef, string) (dal.DB, error) { return nil, errors.New("refused") }
	_, _, err = runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(t.TempDir(), "out"))
	assert.Equal(t, 4, exportExitCode(t, err))
}

// A failure that no signal caused is reported as before, and a signal that comes after the export finished changes nothing.
func TestExportFailureWithoutASignalIsUnchanged(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `INSERT INTO p VALUES (1)`)
	origRun := runNativeExport
	defer func() { runNativeExport = origRun }()
	fakeInterrupt(t, os.Interrupt)
	runNativeExport = func(context.Context, dal.DB, string, ...dbcopy.ExportOptions) (*dbcopy.ExportResult, error) {
		return nil, errors.New("disk full")
	}
	_, _, err := runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(t.TempDir(), "out"))
	assert.Equal(t, 1, exportExitCode(t, err))
	assert.ErrorContains(t, err, "disk full")
	assert.NotContains(t, err.Error(), "interrupted")
}
