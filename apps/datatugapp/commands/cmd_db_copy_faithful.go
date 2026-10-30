package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
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

// rowFilters turns the --where / --limit directives into the filters of the faithful engine.
func rowFilters(d *filter.Directives) map[string]faithful.RowFilter {
	out := map[string]faithful.RowFilter{}
	for table, grp := range d.Where {
		f := out[table]
		for _, p := range grp.Conditions {
			f.Where = append(f.Where, faithful.Cond{Field: p.Field, Op: string(p.Operator), Value: p.Value})
		}
		out[table] = f
	}
	for table, n := range d.LimitsByTable {
		f := out[table]
		f.Limit = n
		out[table] = f
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstMissingDir is the outermost folder of dir that does not exist yet ("" when dir exists): the
// folder a copy would have to create first, so that a failed copy can remove what it created.
func firstMissingDir(dir string) string {
	missing := ""
	for dir != "" && dir != "." && dir != string(filepath.Separator) {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		missing = dir
		dir = filepath.Dir(dir)
	}
	return missing
}

// removeCreatedDirs removes the folders a failed copy created, from the innermost up to top, as far as
// they are empty.
func removeCreatedDirs(file, top string) {
	if top == "" {
		return
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil || dir == top {
			return
		}
	}
}

// dbCopyFaithful runs `db copy` between SQLite and PostgreSQL.
func dbCopyFaithful(cmd *cobra.Command, srcRef, tgtRef dbcopy.BackendRef, fromSchema, overwrite string) error {
	ctx := cmd.Context()
	errWriter := cmd.ErrOrStderr()
	asJSON, _ := cmd.Flags().GetBool("json")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	failOnWarning, _ := cmd.Flags().GetBool("fail-on-warning")
	toSchema, _ := cmd.Flags().GetString("to-schema")
	decimals, _ := cmd.Flags().GetString("decimals")

	failed := false
	// A SQLite file that this command created (and the folders made for it) is removed again when the
	// copy fails: nothing is left behind.
	createdFile, createdDir := "", ""
	defer func() {
		if createdFile != "" && failed {
			for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
				_ = os.Remove(createdFile + suffix)
			}
			removeCreatedDirs(createdFile, createdDir)
		}
	}()
	engineOf := func(r dbcopy.BackendRef) faithful.EndpointInfo {
		return faithful.EndpointInfo{Engine: faithful.Engine(r.SQLEngine()), Display: r.Display()}
	}
	// finish prints the report (--json: on stdout, also for a failure that has no report yet) and returns the exit.
	finish := func(rep *faithful.Report, code int, msg string) error {
		failed = code != 0 && code != exitCodeWarnings
		if rep == nil && asJSON && code != 0 {
			rep = &faithful.Report{Command: "db copy", DryRun: dryRun, From: engineOf(srcRef), To: engineOf(tgtRef), Tables: []faithful.TableReport{}, Warnings: []faithful.Warning{}}
		}
		if rep != nil {
			switch code {
			case 0:
			case exitCodeWarnings:
				rep.ExitCode = code // the copy is made: the status stays "ok-with-warnings"
			default:
				rep.Status, rep.ExitCode, rep.Error = "failed", code, msg
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				_ = enc.Encode(rep)
			} else if code == 0 || code == exitCodeWarnings {
				rep.WriteText(errWriter)
			}
		}
		if code != 0 {
			return Exit(msg, code)
		}
		return nil
	}

	if decimals != "numeric" && decimals != "text" {
		return finish(nil, 2, fmt.Sprintf("invalid --decimals value %q: valid values are numeric, text", decimals))
	}
	if decimals == "text" && (srcRef.Scheme != "sqlite" || tgtRef.Scheme != "postgres") {
		return finish(nil, 2, "--decimals=text applies to SQLite to PostgreSQL copies only: no other copy turns a DECIMAL_TEXT into a PostgreSQL type")
	}
	if toSchema != "" && tgtRef.Scheme != "postgres" {
		return finish(nil, 2, "--to-schema is supported only for PostgreSQL targets")
	}
	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return finish(nil, 2, err.Error())
	}
	if cmd.Flags().Changed("parallel-streams") {
		n, _ := cmd.Flags().GetInt("parallel-streams")
		if n < 1 {
			return finish(nil, 2, "--parallel-streams must be a positive integer")
		}
		if n > 1 {
			_, _ = fmt.Fprintf(errWriter, "warning: a %s to %s copy writes the target in one transaction, one table after the other; ignoring --parallel-streams=%d, using 1\n", srcRef.SQLEngine(), tgtRef.SQLEngine(), n)
		}
	}
	if srcRef.Scheme == "sqlite" && tgtRef.Scheme == "sqlite" {
		same, err := sameFile(srcRef.Path, tgtRef.Path)
		if err == nil && same {
			return finish(nil, 2, "--from and --to are the same SQLite file")
		}
	}
	opts := faithful.Options{Overwrite: overwrite, DryRun: dryRun, Filters: rowFilters(directives), DecimalsText: decimals == "text"}
	opts.Include, opts.Exclude = directives.IncludeTables, directives.ExcludeTables
	if progress, _ := cmd.Flags().GetBool("progress"); progress {
		opts.Progress = errWriter
	}
	redact := func(err error) string {
		out := dbcopy.RedactErrorWithSecrets(dbcopy.RedactErrorWithSecrets(err, srcRef.Path), tgtRef.Path)
		return out.Error()
	}

	src, err := srcRef.OpenSQL(ctx, false)
	if err != nil {
		return finish(nil, 4, fmt.Sprintf("open --from: %v", err))
	}
	defer func() { _ = src.Close() }()
	from := faithful.Endpoint{Engine: faithful.Engine(srcRef.SQLEngine()), DB: src, Display: srcRef.Display(), Schema: fromSchema}
	to := faithful.Endpoint{Engine: faithful.Engine(tgtRef.SQLEngine()), Display: tgtRef.Display(), Schema: toSchema}
	if !dryRun {
		if tgtRef.Scheme == "sqlite" {
			if _, statErr := os.Stat(tgtRef.Path); statErr != nil {
				createdFile = tgtRef.Path
				createdDir = firstMissingDir(filepath.Dir(tgtRef.Path))
			}
		}
		tgt, err := tgtRef.OpenSQL(ctx, true)
		if err != nil {
			failed = true
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
			// REQ:source-introspection-failure: exit 0 with a note on stderr.
			_, _ = fmt.Fprintln(errWriter, "source has no tables; nothing to copy")
			failed = true // nothing was copied: a target file made for it is removed
			return nil
		}
		return finish(rep, code, redact(err))
	}
	if failOnWarning && len(rep.Warnings) > 0 {
		return finish(rep, exitCodeWarnings, fmt.Sprintf("%d warning(s) and --fail-on-warning is set", len(rep.Warnings)))
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
