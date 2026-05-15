package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
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
			&cli.StringFlag{
				Name:  "include",
				Usage: "Comma-separated list of source tables to copy. Mutually exclusive with --exclude.",
			},
			&cli.StringFlag{
				Name:  "exclude",
				Usage: "Comma-separated list of source tables to skip. Mutually exclusive with --include.",
			},
			&cli.StringSliceFlag{
				Name:  "where",
				Usage: "Row predicate: <table>:<field>:<op>:<value>. Repeatable; multiple on the same table AND-compose. Operators: =, <, <=, >, >=, in.",
			},
			&cli.StringSliceFlag{
				Name:  "limit",
				Usage: "Per-table row limit: <table>:<N> (positive integer). Repeatable; one per table.",
			},
			&cli.StringFlag{
				Name:  "filter-config",
				Usage: "Path to a YAML filter config file. Mutually exclusive with any other filter flag.",
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

	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return cli.Exit(err.Error(), 2)
	}

	// Run the copy.
	opts := dbcopy.CopyOpts{
		Overwrite:       overwrite,
		Stderr:          cmd.Root().ErrWriter,
		ParallelStreams: cmd.Int("parallel-streams"),
		Filters:         directives,
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
		// REQ:backend-coverage — runtime capability gap (exit 1) is
		// covered by this branch: engine_rows.go wraps "not supported"
		// driver errors with the "lacks push-down support" sentinel
		// string before returning, so the descriptive message reaches
		// stderr. Exit 1 is shared with other runtime failures (per
		// REQ:exit-codes); parse-time rejection (exit 2) is handled
		// earlier in dbCopyAction.
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

// buildDirectivesFromFlags constructs a *filter.Directives from the
// CLI flags. Returns an error (mapped to exit 2 by the caller) on:
//   - --filter-config mixed with any other filter flag
//   - --include + --exclude both supplied
//   - any parse error
//
// --filter-config support lands in Task 10 (YAML config parser); this
// helper rejects --filter-config with a "not yet wired" error until then.
func buildDirectivesFromFlags(cmd *cli.Command) (*filter.Directives, error) {
	configPath := cmd.String("filter-config")
	otherFilterFlagsPresent := cmd.String("include") != "" ||
		cmd.String("exclude") != "" ||
		len(cmd.StringSlice("where")) > 0 ||
		len(cmd.StringSlice("limit")) > 0

	if configPath != "" && otherFilterFlagsPresent {
		return nil, fmt.Errorf("--filter-config and individual filter flags are mutually exclusive; supply at most one")
	}

	if configPath != "" {
		d, err := filter.ParseConfigFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("--filter-config %q: %w", configPath, err)
		}
		if err := d.PreValidate(); err != nil {
			return nil, err
		}
		return d, nil
	}

	d := &filter.Directives{}
	d.IncludeTables = filter.ParseTableList(cmd.String("include"))
	d.ExcludeTables = filter.ParseTableList(cmd.String("exclude"))

	for _, raw := range cmd.StringSlice("where") {
		table, pred, err := filter.ParseWhereFlag(raw)
		if err != nil {
			return nil, err
		}
		if d.Where == nil {
			d.Where = map[string]*filter.PredicateGroup{}
		}
		grp := d.Where[table]
		if grp == nil {
			grp = &filter.PredicateGroup{Operator: filter.And}
			d.Where[table] = grp
		}
		grp.Conditions = append(grp.Conditions, pred)
	}

	for _, raw := range cmd.StringSlice("limit") {
		table, n, err := filter.ParseLimitFlag(raw)
		if err != nil {
			return nil, err
		}
		if d.LimitsByTable == nil {
			d.LimitsByTable = map[string]int{}
		}
		if _, dup := d.LimitsByTable[table]; dup {
			return nil, fmt.Errorf("--limit: duplicate entry for table %q", table)
		}
		d.LimitsByTable[table] = n
	}

	if err := d.PreValidate(); err != nil {
		return nil, err
	}
	return d, nil
}

