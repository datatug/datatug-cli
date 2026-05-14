package commands

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

// runCopy invokes the copy command with the given argv slice (no "datatug"
// prefix; pass starting from "db"). Captures stderr and stdout. Returns
// the cli.Command's exit-coder error (or nil) for the caller to inspect.
func runCopy(t *testing.T, argv ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	root := &cli.Command{
		Name:      "datatug",
		Commands:  []*cli.Command{dbCommand()},
		Writer:    stdout,
		ErrWriter: stderr,
		// urfave/cli's default ExitErrHandler calls os.Exit on ExitCoder
		// errors, which would terminate the test process. Override to a
		// no-op so the test can observe the returned error directly.
		ExitErrHandler: func(_ context.Context, _ *cli.Command, _ error) {},
	}
	err = root.Run(context.Background(), append([]string{"datatug"}, argv...))
	return
}

// REQ:required-flags — missing --from must exit 2 naming the missing flag.
func TestDBCopy_MissingFrom_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy", "--to", "sqlite:///tmp/out.db")
	assert.Error(t, err)
	if ec, ok := err.(cli.ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	assert.Contains(t, err.Error(), "from")
}

// REQ:required-flags — missing --to must exit 2 naming the missing flag.
func TestDBCopy_MissingTo_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy", "--from", "sqlite:///tmp/in.db")
	assert.Error(t, err)
	if ec, ok := err.(cli.ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	assert.Contains(t, err.Error(), "to")
}

// REQ:overwrite-values — bogus --overwrite=foo must exit 2.
func TestDBCopy_OverwriteBogus_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy",
		"--from", "sqlite:///tmp/in.db",
		"--to", "sqlite:///tmp/out.db",
		"--overwrite", "merge",
	)
	assert.Error(t, err)
	if ec, ok := err.(cli.ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	msg := err.Error()
	assert.Contains(t, msg, "merge")
	assert.Contains(t, msg, "recreate")
	assert.Contains(t, msg, "reload")
}

// REQ:unknown-scheme-rejected — exit 2 with substrings naming the bad
// scheme and the supported list.
func TestDBCopy_UnknownScheme_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy",
		"--from", "mongodb://host/db",
		"--to", "sqlite:///tmp/out.db",
	)
	assert.Error(t, err)
	if ec, ok := err.(cli.ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	msg := err.Error()
	assert.Contains(t, msg, "mongodb")
	assert.Contains(t, msg, "sqlite")
	assert.Contains(t, msg, "ingitdb")
}

// REQ:ingitdb-url-local-only — remote ingitdb URLs exit 2.
func TestDBCopy_RemoteInGitDB_Exit2(t *testing.T) {
	t.Parallel()
	_, _, err := runCopy(t, "db", "copy",
		"--from", "sqlite:///tmp/in.db",
		"--to", "ingitdb://github.com/owner/repo",
	)
	assert.Error(t, err)
	if ec, ok := err.(cli.ExitCoder); ok {
		assert.Equal(t, 2, ec.ExitCode())
	}
	assert.Contains(t, err.Error(), "local paths only")
}

// End-to-end happy path against the checked-in Chinook fixture going to
// an empty inGitDB target. Succeeds with exit 0, the row-copy summary on
// stderr, schemas+rows for the 7 describe-able tables (including
// PlaylistTrack with its composite PK, copied via `__`-joined keys);
// 4 tables are describe-skipped because dalgo2sqlite can't describe
// DATETIME / NUMERIC; tracked upstream).
func TestDBCopy_Chinook_SQLiteToInGitDB_HappyPath(t *testing.T) {
	t.Parallel()
	chinook, err := filepath.Abs("../../../pkg/dbcopy/testdata/chinook.db")
	assert.NoError(t, err)

	tgtDir := t.TempDir()
	_, stderr, runErr := runCopy(t, "db", "copy",
		"--from", "sqlite://"+chinook,
		"--to", "ingitdb://"+tgtDir,
	)
	assert.NoError(t, runErr)
	assert.Contains(t, stderr.String(), "db copy: replicated schema for 7/11 collections")
	// 729 single-PK rows + 8715 PlaylistTrack composite-PK rows = 9444.
	assert.Contains(t, stderr.String(), "copied 9444 rows")
	assert.NotContains(t, stderr.String(), "row copy skipped for \"PlaylistTrack\"",
		"PlaylistTrack must no longer appear in the skip list")
}
