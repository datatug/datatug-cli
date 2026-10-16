package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/spf13/cobra"
)

// The native inGitDB destination is the one copy engine behind both `db copy --to ingitdb://<new folder>`
// and `db export`: the source is read as for any copy, filtered by --include/--exclude/--where/--limit, and
// written as a complete native inGitDB project in a staging folder that becomes the destination only
// when every collection succeeded.

var openExportSource = func(ctx context.Context, ref dbcopy.BackendRef, schema string) (dal.DB, error) {
	return ref.OpenForCopy(ctx, schema)
}

var runNativeExport = dbcopy.ExportInGitDBReport

// inGitDBRecordsFormatKey is the query parameter of an ingitdb:// destination URL that picks the records
// file format of every collection: ingitdb:///backup/shop?records-format=jsonl.
const inGitDBRecordsFormatKey = "records-format"

// splitInGitDBOptions takes the ?records-format=... option off an ingitdb:// destination URL. Any other
// URL, and an ingitdb:// URL whose query is not that option (a folder may hold a "?"), is returned as it is.
func splitInGitDBOptions(raw string) (base, recordsFormat string, err error) {
	at := strings.Index(raw, "?")
	if at < 0 || !strings.HasPrefix(strings.ToLower(raw), "ingitdb:") {
		return raw, "", nil
	}
	query, parseErr := url.ParseQuery(raw[at+1:])
	if parseErr != nil || len(query[inGitDBRecordsFormatKey]) == 0 {
		return raw, "", nil
	}
	for key := range query {
		if key != inGitDBRecordsFormatKey {
			return "", "", fmt.Errorf("unknown ingitdb destination option %q (known: %s)", key, inGitDBRecordsFormatKey)
		}
	}
	values := query[inGitDBRecordsFormatKey]
	if len(values) != 1 {
		return "", "", fmt.Errorf("the ingitdb destination option %s is given more than once", inGitDBRecordsFormatKey)
	}
	return raw[:at], values[0], nil
}

// isNewInGitDBTarget reports whether ref is an ingitdb:// destination that does not exist yet: the copy
// then writes a new native project. An existing project is copied into as before.
func isNewInGitDBTarget(ref dbcopy.BackendRef) bool {
	if ref.Scheme != "ingitdb" {
		return false
	}
	_, err := os.Lstat(ref.Path)
	return errors.Is(err, os.ErrNotExist)
}

// nativeInGitDBRefusedFlags are the copy flags that mean nothing for a new native inGitDB project.
var nativeInGitDBRefusedFlags = []struct{ name, why string }{
	{"overwrite", "the destination is a new folder and must not exist"},
	{"to-schema", "an inGitDB project has no schema"},
	{"decimals", "it applies to SQLite to PostgreSQL copies"},
	{"parallel-streams", "a native project is written one collection after the other"},
	{"progress", "a native project is written in one pass"},
	{"recover-job-ref", "it applies to BigQuery destinations"},
}

// nativeInGitDBRequest is one run of the native inGitDB destination.
type nativeInGitDBRequest struct {
	command       string // "db copy" or "db export": how reports and messages name the command
	fromRaw       string // --from as typed: secrets are scrubbed from errors
	src, tgt      dbcopy.BackendRef
	fromSchema    string
	recordsFormat string
	directives    *filter.Directives
	// quietWhenEmpty says that a source with no collection to write is a note and exit 0 (copy), not exit 1 (export).
	quietWhenEmpty bool
	fail           func(code int, format string, args ...any) error
}

// nativeFailer is the failure helper of the native destination: it returns Exit(message, code), and with
// --json first prints a "failed" report on stdout, as the other destinations do.
func nativeFailer(cmd *cobra.Command, command string, endpoints func() (from, to faithful.EndpointInfo)) func(int, string, ...any) error {
	return func(code int, format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			from, to := endpoints()
			printReport(cmd, &faithful.Report{Command: command, DryRun: dryRun, Status: "failed", ExitCode: code, Error: msg, From: from, To: to,
				Tables: []faithful.TableReport{}, Warnings: []faithful.Warning{}})
		}
		return Exit(msg, code)
	}
}

func printReport(cmd *cobra.Command, rep *faithful.Report) {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
}

// refuseNativeInGitDBFlags is the exit 2 for a flag that does not apply to a new native inGitDB project.
func refuseNativeInGitDBFlags(cmd *cobra.Command, fail func(int, string, ...any) error) error {
	for _, flag := range nativeInGitDBRefusedFlags {
		if cmd.Flags().Changed(flag.name) {
			return fail(2, "--%s is not supported for a new inGitDB destination: %s", flag.name, flag.why)
		}
	}
	return nil
}

func inGitDBEndpoints(src, tgt dbcopy.BackendRef) func() (faithful.EndpointInfo, faithful.EndpointInfo) {
	return func() (faithful.EndpointInfo, faithful.EndpointInfo) {
		from := faithful.EndpointInfo{}
		if src.Scheme != "" {
			from = faithful.EndpointInfo{Engine: faithful.Engine(src.Scheme), Display: src.Display()}
		}
		return from, faithful.EndpointInfo{Engine: "ingitdb", Display: tgt.Display()}
	}
}

// runNativeInGitDB copies the source into a new native inGitDB project at req.tgt and reports it.
func runNativeInGitDB(cmd *cobra.Command, req nativeInGitDBRequest) error {
	ctx := cmd.Context()
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	asJSON, _ := cmd.Flags().GetBool("json")
	failOnWarning, _ := cmd.Flags().GetBool("fail-on-warning")
	fail := req.fail
	srcRef := req.src.WithFlag("--from")
	src, err := openExportSource(ctx, srcRef, req.fromSchema)
	if err != nil {
		return fail(4, "open --from: %v", srcRef.OpenFailure(err))
	}
	result, err := runNativeExport(ctx, src, req.tgt.Path, dbcopy.ExportOptions{RecordsFormat: req.recordsFormat, Filters: req.directives, DryRun: dryRun})
	if errors.Is(err, dbcopy.ErrSourceHasNoTables) || errors.Is(err, dbcopy.ErrNoCollectionsSelected) {
		if req.quietWhenEmpty {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "source has no tables; nothing to copy")
			return nil
		}
		if errors.Is(err, dbcopy.ErrNoCollectionsSelected) {
			return fail(1, "no collection is selected to export")
		}
		return fail(1, "source has no collections to export")
	}
	if err != nil {
		return fail(1, "%s", redactExportFailure(err, req.fromRaw, srcRef).Error())
	}
	rep := nativeReport(req, srcRef, dryRun, result)
	code := 0
	if failOnWarning && len(rep.Warnings) > 0 {
		code = exitCodeWarnings
		rep.ExitCode = code
	}
	if asJSON {
		printReport(cmd, rep)
	} else {
		writeNativeText(cmd, req, rep)
	}
	if code != 0 {
		return Exit(fmt.Sprintf("%d warning(s) and --fail-on-warning is set", len(rep.Warnings)), code)
	}
	return nil
}

func nativeReport(req nativeInGitDBRequest, srcRef dbcopy.BackendRef, dryRun bool, result *dbcopy.ExportResult) *faithful.Report {
	from, to := inGitDBEndpoints(srcRef, req.tgt)()
	rep := &faithful.Report{Command: req.command, Status: "ok", DryRun: dryRun, From: from, To: to,
		Tables: []faithful.TableReport{}, Warnings: []faithful.Warning{}}
	for _, table := range result.Tables {
		rep.Tables = append(rep.Tables, faithful.TableReport{Name: table.Name, Rows: table.Rows, Columns: table.Columns, PrimaryKey: table.PrimaryKey,
			ForeignKeys: table.ForeignKeys, Indexes: table.Indexes, UniqueConstraints: table.UniqueConstraints})
		rep.Totals.Tables++
		rep.Totals.Rows += table.Rows
		rep.Totals.Columns += table.Columns
		if len(table.PrimaryKey) > 0 {
			rep.Totals.PrimaryKeys++
		}
		rep.Totals.ForeignKeys += table.ForeignKeys
		rep.Totals.Indexes += table.Indexes
		rep.Totals.UniqueConstraints += table.UniqueConstraints
	}
	for _, warning := range result.Warnings {
		rep.Warnings = append(rep.Warnings, faithful.Warning{Kind: warning.Kind, Object: warning.Object, Message: warning.Message})
	}
	switch {
	case dryRun:
		rep.Status = "dry-run"
	case len(rep.Warnings) > 0:
		rep.Status = "ok-with-warnings"
	}
	return rep
}

// writeNativeText prints the one line `db export` has always printed, then every warning.
func writeNativeText(cmd *cobra.Command, req nativeInGitDBRequest, rep *faithful.Report) {
	errWriter := cmd.ErrOrStderr()
	verb := "exported"
	if rep.DryRun {
		verb = "would export"
	}
	_, _ = fmt.Fprintf(errWriter, "%s: %s %d collections and %d rows to %s\n", req.command, verb, rep.Totals.Tables, rep.Totals.Rows, req.tgt.Display())
	for _, warning := range rep.Warnings {
		_, _ = fmt.Fprintf(errWriter, "%s warning: %s\n", req.command, warning)
	}
}

// dbCopyToNewInGitDB runs `db copy --to ingitdb://<folder that does not exist>`.
func dbCopyToNewInGitDB(cmd *cobra.Command, fromURL string, srcRef, tgtRef dbcopy.BackendRef, fromSchema, recordsFormat string) error {
	fail := nativeFailer(cmd, "db copy", inGitDBEndpoints(srcRef, tgtRef))
	if err := refuseNativeInGitDBFlags(cmd, fail); err != nil {
		return err
	}
	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return fail(2, "%v", err)
	}
	return runNativeInGitDB(cmd, nativeInGitDBRequest{command: "db copy", fromRaw: fromURL, src: srcRef, tgt: tgtRef, fromSchema: fromSchema,
		recordsFormat: recordsFormat, directives: directives, quietWhenEmpty: true, fail: fail})
}
