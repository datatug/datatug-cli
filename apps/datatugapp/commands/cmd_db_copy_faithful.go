package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/spf13/cobra"
)

// exitCodeWarnings is the exit code of a copy that finished with warnings under --fail-on-warning.
const exitCodeWarnings = 3

// faithfulEligible reports whether a copy is a SQLite <-> PostgreSQL or SQLite -> SQLite one that keeps keys,
// indexes, constraints and exact values (see pkg/dbcopy/faithful).
func faithfulEligible(src, tgt dbcopy.BackendRef) bool {
	s, t := src.SQLEngine(), tgt.SQLEngine()
	return s != "" && t != ""
}

// dbCopyFaithful runs `db copy` between SQLite and PostgreSQL.
func dbCopyFaithful(cmd *cobra.Command, srcRef, tgtRef dbcopy.BackendRef, fromSchema, overwrite string) error {
	ctx := cmd.Context()
	errWriter := cmd.ErrOrStderr()
	asJSON, _ := cmd.Flags().GetBool("json")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	failOnWarning, _ := cmd.Flags().GetBool("fail-on-warning")
	toSchema, _ := cmd.Flags().GetString("to-schema")
	if toSchema != "" && tgtRef.Scheme != "postgres" {
		return Exit("--to-schema is supported only for PostgreSQL targets", 2)
	}
	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return Exit(err.Error(), 2)
	}
	if directives != nil && (len(directives.Where) > 0 || len(directives.LimitsByTable) > 0 || len(directives.PerTableColumns) > 0) {
		return Exit("--where, --limit and column rules are not supported by a SQLite <-> PostgreSQL copy, which copies whole tables; use --include / --exclude", 2)
	}
	if srcRef.Scheme == "sqlite" && tgtRef.Scheme == "sqlite" {
		same, err := sameFile(srcRef.Path, tgtRef.Path)
		if err == nil && same {
			return Exit("--from and --to are the same SQLite file", 2)
		}
	}
	opts := faithful.Options{Overwrite: overwrite, DryRun: dryRun}
	if directives != nil {
		opts.Include, opts.Exclude = directives.IncludeTables, directives.ExcludeTables
	}
	if progress, _ := cmd.Flags().GetBool("progress"); progress {
		opts.Progress = errWriter
	}

	redact := func(err error) string {
		out := dbcopy.RedactErrorWithSecrets(dbcopy.RedactErrorWithSecrets(err, srcRef.Path), tgtRef.Path)
		return out.Error()
	}
	finish := func(rep *faithful.Report, code int, msg string) error {
		if rep != nil {
			if code != 0 {
				rep.Status, rep.ExitCode, rep.Error = "failed", code, msg
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				_ = enc.Encode(rep)
			} else if code == 0 {
				rep.WriteText(errWriter)
			}
		}
		if code != 0 {
			return Exit(msg, code)
		}
		return nil
	}

	src, err := srcRef.OpenSQL(ctx, false)
	if err != nil {
		return finish(nil, 4, fmt.Sprintf("open --from: %v", err))
	}
	defer func() { _ = src.Close() }()
	from := faithful.Endpoint{Engine: faithful.Engine(srcRef.SQLEngine()), DB: src, Display: srcRef.Display(), Schema: fromSchema}
	to := faithful.Endpoint{Engine: faithful.Engine(tgtRef.SQLEngine()), Display: tgtRef.Display(), Schema: toSchema}
	if !dryRun {
		tgt, err := tgtRef.OpenSQL(ctx, true)
		if err != nil {
			return finish(nil, 4, fmt.Sprintf("open --to: %v", err))
		}
		defer func() { _ = tgt.Close() }()
		to.DB = tgt
	}

	rep, err := faithful.Copy(ctx, from, to, opts)
	if err != nil {
		code := 1
		if rep != nil && rep.ExitCode != 0 {
			code = rep.ExitCode
		}
		if errors.Is(err, faithful.ErrNoTables) {
			if srcRef.Scheme == "postgres" {
				schema := fromSchema
				if schema == "" {
					schema = "public"
				}
				return finish(rep, 1, fmt.Sprintf("PostgreSQL source schema %q has no tables; specify another schema with --from-schema", schema))
			}
			return finish(rep, 1, "source has no tables; nothing to copy")
		}
		return finish(rep, code, redact(err))
	}
	if failOnWarning && len(rep.Warnings) > 0 {
		if asJSON {
			rep.ExitCode = exitCodeWarnings
		}
		_ = finish(rep, 0, "")
		if !asJSON {
			return Exit(fmt.Sprintf("%d warning(s) and --fail-on-warning is set", len(rep.Warnings)), exitCodeWarnings)
		}
		return Exit(fmt.Sprintf("%d warning(s) and --fail-on-warning is set", len(rep.Warnings)), exitCodeWarnings)
	}
	return finish(rep, 0, "")
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}
