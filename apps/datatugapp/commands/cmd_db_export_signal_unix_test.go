//go:build unix

package commands

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real thing: a SIGINT or a SIGTERM sent to the process cancels the context of the export, which stops, and the command
// exits 1 naming the signal. After the first signal the process takes signals as usual again.
func TestExportRealSignalsStopTheExport(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `INSERT INTO p VALUES (1)`)
	origRun := runNativeExport
	defer func() { runNativeExport = origRun }()
	for sig, name := range map[syscall.Signal]string{syscall.SIGINT: "interrupt", syscall.SIGTERM: "terminated"} {
		runNativeExport = func(ctx context.Context, _ dal.DB, _ string, _ ...dbcopy.ExportOptions) (*dbcopy.ExportResult, error) {
			require.NoError(t, syscall.Kill(os.Getpid(), sig))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				t.Error("the signal did not cancel the export")
				return nil, nil
			}
		}
		_, _, err := runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(t.TempDir(), "out"))
		assert.Equal(t, 1, exportExitCode(t, err), name)
		assert.ErrorContains(t, err, "interrupted ("+name+")")
	}
}

// Without a signal the real interruptContext runs the export as before and gives the signals back when it ends.
func TestInterruptContextStopIsIdempotent(t *testing.T) {
	ctx, which, stop := interruptContext(context.Background())
	assert.Nil(t, which())
	assert.NoError(t, ctx.Err())
	stop()
	stop()
	assert.Error(t, ctx.Err())
	assert.Nil(t, which())
}
