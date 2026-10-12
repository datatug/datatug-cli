package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dal-go/dalgo/dal"
	bqwriter "github.com/dal-go/dalgo2bigquery"
	"github.com/dal-go/dalgo2postgres"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
	"github.com/datatug/datatug-cli/pkg/openvaultdb"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2/google"
)

// dbCopyCommand wires `datatug db copy --from <url> --to <url>` per
// spec/features/cli/db/copy/README.md.
func dbCopyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copy",
		Short: "Copy a database from one DALgo URL to another.",
		Long: "Copies source collections and rows through DALgo adapters. A BigQuery destination uses the authenticated Google identity configured on this machine and requires an explicit dataset location. Existing BigQuery tables are never replaced. For a complete, atomic native inGitDB export, use `datatug db export`.\n\n" +
			"PostgreSQL (postgres://) is a preview: it opens only while " + dbcopy.PostgresPreviewEnv + "=1. A PostgreSQL --from is read through a read-only session, " +
			"and a --from URL that turns the session's default_transaction_read_only off is refused. --to is the only place a PostgreSQL database is opened for writing.",
		RunE: dbCopyAction,
	}
	flags := cmd.Flags()
	flags.String("from", "", "Source URL (sqlite://, ingitdb://, postgres:// read-only, http(s):// DataTug project, openvaultdb://). Required.")
	flags.String("from-schema", "", "PostgreSQL source schema to read; defaults to public.")
	flags.String("to", "", "Target DALgo URL (sqlite://, ingitdb://, postgres://: the only way DataTug opens PostgreSQL for writing; bigquery://PROJECT/DATASET?location=LOCATION). Required.")
	flags.String("recover-job-ref", "", "Recover the same uncertain BigQuery load job using the credential-free JSON reference printed by an interrupted `db copy`; requires --to and does not resubmit source rows.")
	flags.String("overwrite", "", "Conflict policy when target already contains source-named collections. One of: recreate, reload.")
	flags.Int("parallel-streams", 0, "Maximum number of source tables copied concurrently. Capped to 1 when either driver advertises no concurrency.")
	flags.Bool("progress", false, "Emit per-table progress lines on stderr.")
	flags.String("include", "", "Comma-separated list of source tables to copy. Mutually exclusive with --exclude.")
	flags.String("exclude", "", "Comma-separated list of source tables to skip. Mutually exclusive with --include.")
	// StringArray (not StringSlice): repeated --where/--limit flags accumulate
	// verbatim, with no comma-splitting — matching github.com/urfave/cli/v3's
	// StringSliceFlag, whose values may themselves legitimately contain commas.
	flags.StringArray("where", nil, "Row predicate: <table>:<field>:<op>:<value>. Repeatable; multiple on the same table AND-compose. Operators: =, <, <=, >, >=, in.")
	flags.StringArray("limit", nil, "Per-table row limit: <table>:<N> (positive integer). Repeatable; one per table.")
	flags.String("filter-config", "", "Path to a YAML filter config file. Mutually exclusive with any other filter flag.")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func dbCopyAction(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	fromURL, _ := cmd.Flags().GetString("from")
	fromSchema, _ := cmd.Flags().GetString("from-schema")
	toURL, _ := cmd.Flags().GetString("to")
	recoverRefJSON, _ := cmd.Flags().GetString("recover-job-ref")
	errWriter := cmd.ErrOrStderr()
	if recoverRefJSON != "" {
		if fromURL != "" || copyOnlyFlagChanged(cmd) {
			return Exit("--recover-job-ref cannot be combined with --from or copy, filter, overwrite, or progress options", 2)
		}
		target, err := dbcopy.Parse(toURL)
		if err != nil || target.Scheme != "bigquery" {
			return Exit("--recover-job-ref requires --to bigquery://PROJECT/DATASET?location=LOCATION", 2)
		}
		ref, err := decodeBigQueryLoadJobRef(recoverRefJSON)
		if err != nil {
			return Exit("invalid --recover-job-ref: expected one JSON object with known fields and unique keys", 2)
		}
		if ref.ProjectID != target.ProjectID || ref.DatasetID != target.DatasetID || ref.Location != target.Location {
			return Exit("--recover-job-ref destination does not match --to", 2)
		}
		client, err := google.DefaultClient(ctx, bqwriter.BigqueryScope)
		if err != nil {
			return Exit("open Google credentials from the local application default credentials", 4)
		}
		return dbCopyRecoverBigQuery(ctx, target, ref, client, errWriter)
	}
	if fromURL == "" {
		return Exit("required flag(s) \"from\" not set", 2)
	}

	// Validate --overwrite (REQ:overwrite-values).
	overwrite, _ := cmd.Flags().GetString("overwrite")
	switch overwrite {
	case "", "recreate", "reload":
		// ok
	default:
		return Exit(
			fmt.Sprintf("invalid --overwrite value %q: valid values are recreate, reload", overwrite),
			2,
		)
	}

	// Parse both URLs first (REQ:unknown-scheme-rejected, REQ:ingitdb-url-local-only,
	// REQ:required-flags) — fail with exit 2 before any connection attempt.
	srcRef, err := dbcopy.Parse(fromURL)
	if err != nil {
		return Exit(fmt.Sprintf("--from: %v", err), 2)
	}
	tgtRef, err := dbcopy.Parse(toURL)
	if err != nil {
		return Exit(fmt.Sprintf("--to: %v", err), 2)
	}
	// A failure of a PostgreSQL side says which flag its connection string is read from (or its variable).
	srcRef, tgtRef = srcRef.WithFlag("--from"), tgtRef.WithFlag("--to")
	if fromSchema != "" && srcRef.Scheme != "postgres" {
		return Exit("--from-schema is supported only for PostgreSQL sources", 2)
	}
	if tgtRef.Scheme == "bigquery" && overwrite != "" {
		return Exit("BigQuery destinations never replace existing tables; omit --overwrite and choose a new dataset or table names", 2)
	}

	// Open both backends (REQ:exit-codes — 4 for connection failures). The source is opened as a
	// read; the target is the one open that is written through, so a PostgreSQL target is not
	// opened read-only.
	src, err := srcRef.OpenForCopy(ctx, fromSchema)
	if err != nil {
		return Exit(fmt.Sprintf("open --from: %v", err), 4)
	}

	directives, err := buildDirectivesFromFlags(cmd)
	if err != nil {
		return Exit(err.Error(), 2)
	}

	// Run the copy.
	parallelStreams, _ := cmd.Flags().GetInt("parallel-streams")
	opts := dbcopy.CopyOpts{
		Overwrite:       overwrite,
		Stderr:          errWriter,
		ParallelStreams: parallelStreams,
		Filters:         directives,
	}
	if progress, _ := cmd.Flags().GetBool("progress"); progress {
		opts.Progress = dbcopy.NewProgressWriter(errWriter, true)
	}
	if tgtRef.Scheme == "bigquery" {
		return dbCopyToBigQuery(ctx, src, srcRef, fromSchema, tgtRef, opts, errWriter)
	}
	tgt, err := tgtRef.OpenForWrite(ctx)
	if err != nil {
		return Exit(fmt.Sprintf("open --to: %v", err), 4)
	}

	summary, err := dbcopy.Copy(ctx, src, tgt, opts)
	if errors.Is(err, dbcopy.ErrSourceHasNoTables) {
		if srcRef.Scheme == "postgres" {
			schema := fromSchema
			if schema == "" {
				schema = dalgo2postgres.DefaultSchema
			}
			return Exit(fmt.Sprintf("PostgreSQL source schema %q has no tables; specify another schema with --from-schema", schema), 1)
		}
		// REQ:source-introspection-failure: exit 0 with stderr note.
		_, _ = fmt.Fprintln(errWriter, "source has no tables; nothing to copy")
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
		return Exit(err.Error(), 1)
	}

	if summary.Tables > 0 {
		_, _ = fmt.Fprintf(errWriter,
			"db copy: replicated schema for %d/%d collections (%d skipped), copied %d rows\n",
			summary.Created, summary.Tables, len(summary.Skipped), summary.RowsCopied,
		)
		dbcopy.WriteSkippedIndexes(errWriter, summary)
		for _, warning := range summary.Warnings {
			_, _ = fmt.Fprintf(errWriter, "db copy warning: %s\n", warning)
		}
	}
	return nil
}

func dbCopyToBigQuery(ctx context.Context, source dal.DB, sourceRef dbcopy.BackendRef, sourceSchema string, target dbcopy.BackendRef, opts dbcopy.CopyOpts, errWriter io.Writer) error {
	client, err := google.DefaultClient(ctx, bqwriter.BigqueryScope)
	if err != nil {
		return Exit("open Google credentials from the local application default credentials", 4)
	}
	writer, err := bqwriter.NewLoadWriter(ctx, bqwriter.LoadConfig{
		ProjectID: target.ProjectID, DatasetID: target.DatasetID,
		Location: target.Location, HTTPClient: client,
	})
	if err != nil {
		return Exit("configure BigQuery destination", 2)
	}
	summary, err := dbcopy.CopyToSink(ctx, source, dbcopy.BigQueryCopySink{Writer: writer}, opts)
	if errors.Is(err, dbcopy.ErrSourceHasNoTables) {
		if sourceRef.Scheme == "postgres" {
			if sourceSchema == "" {
				sourceSchema = dalgo2postgres.DefaultSchema
			}
			return Exit(fmt.Sprintf("PostgreSQL source schema %q has no tables; specify another schema with --from-schema", sourceSchema), 1)
		}
		_, _ = fmt.Fprintln(errWriter, "source has no tables; nothing to copy")
		return nil
	}
	if err != nil {
		var uncertain *bqwriter.LoadOutcomeUnknownError
		if errors.As(err, &uncertain) {
			return Exit(bigQueryCopyRecoveryMessage(target, uncertain.Job, err), 1)
		}
		return Exit(err.Error(), 1)
	}
	if summary.Tables > 0 {
		_, _ = fmt.Fprintf(errWriter, "db copy: loaded %d/%d collections, copied %d rows to BigQuery %s/%s in %s\n",
			summary.Created, summary.Tables, summary.RowsCopied, target.ProjectID, target.DatasetID, target.Location)
		for _, warning := range summary.Warnings {
			_, _ = fmt.Fprintf(errWriter, "db copy warning: %s\n", warning)
		}
	}
	return nil
}

func copyOnlyFlagChanged(cmd *cobra.Command) bool {
	for _, name := range []string{"from-schema", "overwrite", "parallel-streams", "progress", "include", "exclude", "where", "limit", "filter-config"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func decodeBigQueryLoadJobRef(referenceJSON string) (bqwriter.LoadJobRef, error) {
	var ref bqwriter.LoadJobRef
	if len(referenceJSON) > 4096 {
		return ref, errors.New("BigQuery recovery reference is too large")
	}
	if err := openvaultdb.DecodeJSONObjectStrict([]byte(referenceJSON), nil); err != nil {
		return ref, err
	}
	decoder := json.NewDecoder(strings.NewReader(referenceJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return bqwriter.LoadJobRef{}, err
	}
	return ref, nil
}

func dbCopyRecoverBigQuery(ctx context.Context, target dbcopy.BackendRef, ref bqwriter.LoadJobRef, client *http.Client, errWriter io.Writer) error {
	if target.Scheme != "bigquery" || client == nil {
		return Exit("BigQuery recovery requires a BigQuery target and local credentials", 2)
	}
	if ref.ProjectID != target.ProjectID || ref.DatasetID != target.DatasetID || ref.Location != target.Location {
		return Exit("--recover-job-ref destination does not match --to", 2)
	}
	writer, err := bqwriter.NewLoadWriter(ctx, bqwriter.LoadConfig{
		ProjectID: target.ProjectID, DatasetID: target.DatasetID,
		Location: target.Location, HTTPClient: client,
	})
	if err != nil {
		return Exit("configure BigQuery destination", 2)
	}
	receipt, err := writer.RecoverLoad(ctx, ref)
	if err != nil {
		var uncertain *bqwriter.LoadOutcomeUnknownError
		if errors.As(err, &uncertain) {
			return Exit("BigQuery load is still unresolved; keep the same --recover-job-ref and retry recovery when service access is available", 1)
		}
		return Exit(err.Error(), 1)
	}
	_, _ = fmt.Fprintf(errWriter, "db copy: recovered BigQuery load job %s for %s/%s.%s in %s; verified %d rows\n",
		receipt.JobID, receipt.ProjectID, receipt.DatasetID, receipt.TableID, receipt.Location, receipt.Rows)
	return nil
}

func bigQueryRecoveryHint(target dbcopy.BackendRef, ref bqwriter.LoadJobRef) string {
	data, err := json.Marshal(ref)
	if err != nil {
		return "BigQuery load outcome is uncertain; do not rerun the copy until the existing load job has been checked."
	}
	destination := fmt.Sprintf("bigquery://%s/%s?location=%s", target.ProjectID, target.DatasetID, target.Location)
	return fmt.Sprintf("BigQuery load outcome is uncertain. Do not rerun this copy; recover the same job with:\n  datatug db copy --to '%s' --recover-job-ref '%s'",
		destination, string(data))
}

func bigQueryCopyRecoveryMessage(target dbcopy.BackendRef, ref bqwriter.LoadJobRef, copyErr error) string {
	message := bigQueryRecoveryHint(target, ref)
	if errors.Is(copyErr, dbcopy.ErrStagingCleanup) {
		message += "\nWarning: local staging cleanup failed; staged source data may remain in temporary storage."
	}
	return message
}

// buildDirectivesFromFlags constructs a *filter.Directives from the
// CLI flags. Returns an error (mapped to exit 2 by the caller) on:
//   - --filter-config mixed with any other filter flag
//   - --include + --exclude both supplied
//   - any parse error
//
// --filter-config support lands in Task 10 (YAML config parser); this
// helper rejects --filter-config with a "not yet wired" error until then.
func buildDirectivesFromFlags(cmd *cobra.Command) (*filter.Directives, error) {
	configPath, _ := cmd.Flags().GetString("filter-config")
	include, _ := cmd.Flags().GetString("include")
	exclude, _ := cmd.Flags().GetString("exclude")
	where, _ := cmd.Flags().GetStringArray("where")
	limit, _ := cmd.Flags().GetStringArray("limit")
	otherFilterFlagsPresent := include != "" ||
		exclude != "" ||
		len(where) > 0 ||
		len(limit) > 0

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
	d.IncludeTables = filter.ParseTableList(include)
	d.ExcludeTables = filter.ParseTableList(exclude)

	for _, raw := range where {
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

	for _, raw := range limit {
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
