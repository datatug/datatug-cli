package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/urfave/cli/v3"
)

// dbCopyCommand wires `datatug db copy --from <url> --to <url>` per
// spec/features/cli/db/copy/README.md. The first slice is schema-only;
// see the engine package doc and the upstream issues for the row-CRUD
// follow-up.
func dbCopyCommand() *cli.Command {
	return &cli.Command{
		Name:        "copy",
		Usage:       "Copy a database from one DALgo URL to another (schema-only first slice).",
		ArgsUsage:   "",
		Description: "Replicates the source schema (collections, primary keys, indexes) to the target. Row data is not yet copied; see docs/upstream-issues/.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "from",
				Usage:    "Source DALgo URL (sqlite://, ingitdb://). Required.",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "to",
				Usage:    "Target DALgo URL (sqlite://, ingitdb://). Required.",
				Required: true,
			},
			&cli.StringFlag{
				Name:  "overwrite",
				Usage: "Conflict policy when target already contains source-named collections. One of: recreate, reload.",
			},
			&cli.IntFlag{
				Name:  "parallel-streams",
				Usage: "Maximum number of source tables copied concurrently. Capped to 1 when either driver advertises no concurrency.",
			},
			&cli.BoolFlag{
				Name:  "progress",
				Usage: "Emit per-table progress lines on stderr.",
			},
		},
		Action: dbCopyAction,
	}
}

func dbCopyAction(ctx context.Context, cmd *cli.Command) error {
	fromURL := cmd.String("from")
	toURL := cmd.String("to")

	// Validate --overwrite (REQ:overwrite-values).
	overwrite := cmd.String("overwrite")
	switch overwrite {
	case "", "recreate", "reload":
		// ok
	default:
		return cli.Exit(
			fmt.Sprintf("invalid --overwrite value %q: valid values are recreate, reload", overwrite),
			2,
		)
	}

	// Parse both URLs first (REQ:unknown-scheme-rejected, REQ:ingitdb-url-local-only,
	// REQ:required-flags) — fail with exit 2 before any connection attempt.
	srcRef, err := dbcopy.Parse(fromURL)
	if err != nil {
		return cli.Exit(fmt.Sprintf("--from: %v", err), 2)
	}
	tgtRef, err := dbcopy.Parse(toURL)
	if err != nil {
		return cli.Exit(fmt.Sprintf("--to: %v", err), 2)
	}

	// Open both backends (REQ:exit-codes — 4 for connection failures).
	src, err := srcRef.Open(ctx)
	if err != nil {
		return cli.Exit(fmt.Sprintf("open --from: %v", err), 4)
	}
	tgt, err := tgtRef.Open(ctx)
	if err != nil {
		return cli.Exit(fmt.Sprintf("open --to: %v", err), 4)
	}

	// Run the copy.
	opts := dbcopy.CopyOpts{
		Overwrite:       overwrite,
		Stderr:          cmd.Root().ErrWriter,
		ParallelStreams: cmd.Int("parallel-streams"),
	}
	if cmd.Bool("progress") {
		opts.Progress = dbcopy.NewProgressWriter(cmd.Root().ErrWriter, true)
	}

	summary, err := dbcopy.Copy(ctx, src, tgt, opts)
	if errors.Is(err, dbcopy.ErrSourceHasNoTables) {
		// REQ:source-introspection-failure: exit 0 with stderr note.
		_, _ = fmt.Fprintln(cmd.Root().ErrWriter, "source has no tables; nothing to copy")
		return nil
	}
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}

	if summary.Tables > 0 {
		_, _ = fmt.Fprintf(cmd.Root().ErrWriter,
			"db copy: replicated schema for %d/%d collections (%d skipped), copied %d rows\n",
			summary.Created, summary.Tables, len(summary.Skipped), summary.RowsCopied,
		)
	}
	return nil
}

