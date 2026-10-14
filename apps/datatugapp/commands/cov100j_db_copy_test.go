package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	bqwriter "github.com/dal-go/dalgo2bigquery"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBCopyDecodeLoadJobRefAndRecoverBranches(t *testing.T) {
	t.Parallel()

	// 1. decodeBigQueryLoadJobRef with input > 4096 bytes
	hugeJSON := "{" + strings.Repeat(" ", 4100) + "}"
	_, err := decodeBigQueryLoadJobRef(hugeJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")

	// 2. decodeBigQueryLoadJobRef with wrong type for field
	_, err = decodeBigQueryLoadJobRef(`{"jobId": 123}`)
	require.Error(t, err)

	// 3. dbCopyRecoverBigQuery error branches
	targetBQ, err := dbcopy.Parse("bigquery://demodb/research?location=US")
	require.NoError(t, err)

	ref := bqwriter.LoadJobRef{JobID: "j1", ProjectID: "demodb", DatasetID: "research", Location: "US", TableID: "tbl"}

	// non-bigquery target
	targetSqlite, err := dbcopy.Parse("sqlite:///tmp/db.sqlite")
	require.NoError(t, err)
	err = dbCopyRecoverBigQuery(context.Background(), targetSqlite, ref, http.DefaultClient, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "BigQuery recovery requires a BigQuery target")

	// nil client
	err = dbCopyRecoverBigQuery(context.Background(), targetBQ, ref, nil, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "BigQuery recovery requires a BigQuery target")

	// invalid load config (e.g. empty location)
	targetBadLoc := dbcopy.BackendRef{Scheme: "bigquery", ProjectID: "p", DatasetID: "d", Location: ""}
	err = dbCopyRecoverBigQuery(context.Background(), targetBadLoc, bqwriter.LoadJobRef{ProjectID: "p", DatasetID: "d"}, http.DefaultClient, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "configure BigQuery destination")

	// server returning uncertain error
	mockUncertainClient := &http.Client{Transport: copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		return copyHTTPResponse(req, http.StatusInternalServerError, `{"error":{"message":"internal error"}}`), nil
	})}
	err = dbCopyRecoverBigQuery(context.Background(), targetBQ, ref, mockUncertainClient, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "BigQuery load is still unresolved")

	// server returning non-retriable 404 error
	mock404Client := &http.Client{Transport: copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		return copyHTTPResponse(req, http.StatusNotFound, `{"error":{"message":"job not found"}}`), nil
	})}
	err = dbCopyRecoverBigQuery(context.Background(), targetBQ, ref, mock404Client, io.Discard)
	require.Error(t, err)
}

func TestDBCopyFlagsAndRecoverAction(t *testing.T) {
	t.Parallel()

	// Test copyOnlyFlagChanged with each flag
	for _, flagName := range []string{"from-schema", "overwrite", "parallel-streams", "progress", "include", "exclude", "where", "limit", "filter-config"} {
		cmd := dbCopyCommand()
		switch flagName {
		case "parallel-streams":
			cmd.Flags().Set(flagName, "2")
		case "progress":
			cmd.Flags().Set(flagName, "true")
		default:
			cmd.Flags().Set(flagName, "val")
		}
		require.True(t, copyOnlyFlagChanged(cmd), "flag %s should be recognized as copy-only", flagName)
	}

	// recover-job-ref with non-bigquery --to
	_, _, err := runCopy(t, "db", "copy", "--to", "sqlite:///tmp/db.sqlite", "--recover-job-ref", `{"jobId":"j"}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--recover-job-ref requires --to bigquery://")
}

// Not parallel: mutates package-level bqGoogleDefaultClient.
func TestDBCopyActionWithMockGoogleClient(t *testing.T) {
	origClient := bqGoogleDefaultClient
	t.Cleanup(func() { bqGoogleDefaultClient = origClient })

	// 1. bqGoogleDefaultClient fails during recover-job-ref
	bqGoogleDefaultClient = func(ctx context.Context, scopes ...string) (*http.Client, error) {
		return nil, errors.New("cannot obtain credentials")
	}
	_, _, err := runCopy(t, "db", "copy",
		"--to", "bigquery://demodb/research?location=US",
		"--recover-job-ref", `{"jobId":"job-1","projectId":"demodb","datasetId":"research","tableId":"tbl","location":"US"}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "open Google credentials from the local application default credentials")

	// 2. bqGoogleDefaultClient succeeds during recover-job-ref and recovers successfully
	mockHTTP := &http.Client{Transport: copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		return copyHTTPResponse(req, http.StatusOK, `{"jobReference":{"projectId":"demodb","jobId":"job-1","location":"US"},"configuration":{"load":{"destinationTable":{"projectId":"demodb","datasetId":"research","tableId":"tbl"}}},"status":{"state":"DONE"},"statistics":{"load":{"outputRows":"10"}}}`), nil
	})}
	bqGoogleDefaultClient = func(ctx context.Context, scopes ...string) (*http.Client, error) {
		return mockHTTP, nil
	}
	_, stderr, err := runCopy(t, "db", "copy",
		"--to", "bigquery://demodb/research?location=US",
		"--recover-job-ref", `{"jobId":"job-1","projectId":"demodb","datasetId":"research","tableId":"tbl","location":"US"}`)
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "recovered BigQuery load job job-1")
	require.Contains(t, stderr.String(), "verified 10 rows")

	// 3. dbCopyToBigQuery with bqGoogleDefaultClient error
	bqGoogleDefaultClient = func(ctx context.Context, scopes ...string) (*http.Client, error) {
		return nil, errors.New("auth failure")
	}
	targetBQ, _ := dbcopy.Parse("bigquery://demodb/research?location=US")
	err = dbCopyToBigQuery(context.Background(), nil, dbcopy.BackendRef{}, "", targetBQ, dbcopy.CopyOpts{}, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "open Google credentials")

	// 4. dbCopyToBigQuery with invalid BigQuery config (empty location)
	bqGoogleDefaultClient = func(ctx context.Context, scopes ...string) (*http.Client, error) {
		return http.DefaultClient, nil
	}
	targetNoLoc := dbcopy.BackendRef{Scheme: "bigquery", ProjectID: "p", DatasetID: "d", Location: ""}
	err = dbCopyToBigQuery(context.Background(), nil, dbcopy.BackendRef{}, "", targetNoLoc, dbcopy.CopyOpts{}, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "configure BigQuery destination")
}

// Not parallel: mutates package-level bqGoogleDefaultClient.
func TestDBCopyToBigQuerySourceNoTables(t *testing.T) {
	origClient := bqGoogleDefaultClient
	t.Cleanup(func() { bqGoogleDefaultClient = origClient })

	mockHTTP := &http.Client{Transport: copyHTTPTransport(func(req *http.Request) (*http.Response, error) {
		return copyHTTPResponse(req, http.StatusOK, `{}`), nil
	})}
	bqGoogleDefaultClient = func(ctx context.Context, scopes ...string) (*http.Client, error) {
		return mockHTTP, nil
	}

	targetBQ, err := dbcopy.Parse("bigquery://demodb/research?location=US")
	require.NoError(t, err)

	srcPath := filepath.Join(t.TempDir(), "empty.sqlite")
	emptySrc, err := dalgo2sqlite.NewDatabase(srcPath)
	require.NoError(t, err)

	// Postgres source with no tables
	err = dbCopyToBigQuery(context.Background(), emptySrc, dbcopy.BackendRef{Scheme: "postgres"}, "", targetBQ, dbcopy.CopyOpts{}, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has no tables")

	// Non-postgres source with no tables: prints message and returns nil
	var stderr bytes.Buffer
	err = dbCopyToBigQuery(context.Background(), emptySrc, dbcopy.BackendRef{Scheme: "sqlite"}, "", targetBQ, dbcopy.CopyOpts{}, &stderr)
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "source has no tables; nothing to copy")
}

func TestDBCopyDirectivesAndValidation(t *testing.T) {
	t.Parallel()

	// --from-schema with non-postgres source
	_, _, err := runCopy(t, "db", "copy", "--from", "sqlite:///tmp/in.db", "--to", "sqlite:///tmp/out.db", "--from-schema", "other")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--from-schema is supported only for PostgreSQL sources")

	// buildDirectivesFromFlags: --filter-config mixed with other filter flag
	cmd := dbCopyCommand()
	cmd.Flags().Set("filter-config", "config.yaml")
	cmd.Flags().Set("include", "table1")
	_, err = buildDirectivesFromFlags(cmd)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mutually exclusive")

	// buildDirectivesFromFlags: --filter-config non-existent
	cmd = dbCopyCommand()
	cmd.Flags().Set("filter-config", "non_existent_config.yaml")
	_, err = buildDirectivesFromFlags(cmd)
	require.Error(t, err)

	// buildDirectivesFromFlags: valid where and limit
	cmd = dbCopyCommand()
	cmd.Flags().Set("where", "users:age:>:21")
	cmd.Flags().Set("limit", "users:10")
	d, err := buildDirectivesFromFlags(cmd)
	require.NoError(t, err)
	require.NotNil(t, d.Where["users"])
	require.Equal(t, 10, d.LimitsByTable["users"])

	// buildDirectivesFromFlags: invalid where
	cmd = dbCopyCommand()
	cmd.Flags().Set("where", "bad_where_format")
	_, err = buildDirectivesFromFlags(cmd)
	require.Error(t, err)

	// buildDirectivesFromFlags: invalid limit
	cmd = dbCopyCommand()
	cmd.Flags().Set("limit", "bad_limit_format")
	_, err = buildDirectivesFromFlags(cmd)
	require.Error(t, err)

	// buildDirectivesFromFlags: duplicate limit
	cmdDup := dbCopyCommand()
	require.NoError(t, cmdDup.ParseFlags([]string{"--limit", "users:10", "--limit", "users:20"}))
	_, err = buildDirectivesFromFlags(cmdDup)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate entry for table")

	// buildDirectivesFromFlags: include and exclude together -> PreValidate error
	cmdBoth := dbCopyCommand()
	require.NoError(t, cmdBoth.ParseFlags([]string{"--include", "t1", "--exclude", "t2"}))
	_, err = buildDirectivesFromFlags(cmdBoth)
	require.Error(t, err)

	// buildDirectivesFromFlags: valid filter-config
	validCfg := filepath.Join(t.TempDir(), "valid.yaml")
	require.NoError(t, os.WriteFile(validCfg, []byte("include:\n  - Customer\n"), 0600))
	cmdValidCfg := dbCopyCommand()
	require.NoError(t, cmdValidCfg.ParseFlags([]string{"--filter-config", validCfg}))
	dValid, err := buildDirectivesFromFlags(cmdValidCfg)
	require.NoError(t, err)
	require.Equal(t, []string{"Customer"}, dValid.IncludeTables)

	// buildDirectivesFromFlags: filter-config fails PreValidate
	invalidCfg := filepath.Join(t.TempDir(), "invalid.yaml")
	require.NoError(t, os.WriteFile(invalidCfg, []byte("include:\n  - Customer\nexclude:\n  - Invoice\n"), 0600))
	cmdInvalidCfg := dbCopyCommand()
	require.NoError(t, cmdInvalidCfg.ParseFlags([]string{"--filter-config", invalidCfg}))
	_, err = buildDirectivesFromFlags(cmdInvalidCfg)
	require.Error(t, err)

	// runCopy with empty sqlite source to trigger ErrSourceHasNoTables -> "source has no tables; nothing to copy"
	emptySrc := filepath.Join(t.TempDir(), "empty.db")
	_, err = dalgo2sqlite.NewDatabase(emptySrc)
	require.NoError(t, err)
	outTgt := filepath.Join(t.TempDir(), "out.db")
	_, err = dalgo2sqlite.NewDatabase(outTgt)
	require.NoError(t, err)
	_, stderr, err := runCopy(t, "db", "copy", "--from", "sqlite://"+emptySrc, "--to", "sqlite://"+outTgt, "--progress")
	require.NoError(t, err)
	require.Contains(t, stderr.String(), "source has no tables; nothing to copy")

	// runCopy where buildDirectivesFromFlags fails (line 128)
	_, _, err = runCopy(t, "db", "copy", "--from", "sqlite://"+emptySrc, "--to", "sqlite://"+outTgt, "--include", "t1", "--exclude", "t2")
	require.Error(t, err)
}

// Not parallel: modifies bqRecoveryMarshal.
func TestBigQueryRecoveryHintMarshalError(t *testing.T) {
	oldMarshal := bqRecoveryMarshal
	t.Cleanup(func() { bqRecoveryMarshal = oldMarshal })
	bqRecoveryMarshal = func(v any) ([]byte, error) {
		return nil, errors.New("marshal error")
	}
	hint := bigQueryRecoveryHint(dbcopy.BackendRef{}, bqwriter.LoadJobRef{})
	require.Contains(t, hint, "BigQuery load outcome is uncertain; do not rerun the copy")

	// Test bigQueryCopyRecoveryMessage with ErrStagingCleanup (line 298)
	msg := bigQueryCopyRecoveryMessage(dbcopy.BackendRef{ProjectID: "p", DatasetID: "d", Location: "US"}, bqwriter.LoadJobRef{JobID: "j"}, dbcopy.ErrStagingCleanup)
	require.Contains(t, msg, "Warning: local staging cleanup failed")
}

func TestDBCopyBigQueryRecoveryTargetMismatch(t *testing.T) {
	t.Parallel()
	ref := `{"jobId":"j1","projectId":"sourceproject","datasetId":"dataset1","location":"US","tableId":"t1"}`
	_, _, err := runCopy(t, "db", "copy", "--to", "bigquery://targetproject/dataset1?location=US", "--recover-job-ref", ref)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--recover-job-ref destination does not match --to")
}

func TestDBCopyRecoverLoadRegularError(t *testing.T) {
	t.Parallel()
	targetBQ, err := dbcopy.Parse("bigquery://demodb/research?location=US")
	require.NoError(t, err)
	ref := bqwriter.LoadJobRef{JobID: "j1", ProjectID: "demodb", DatasetID: "research", Location: "US", TableID: "bad table name with spaces!"}
	err = dbCopyRecoverBigQuery(context.Background(), targetBQ, ref, http.DefaultClient, io.Discard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_input")
}

// Not parallel: modifies dbCopyRunner, dbCopyToSinkRunner, bqGoogleDefaultClient, and env variables.
func TestDBCopySeamBranches(t *testing.T) {
	origClient := bqGoogleDefaultClient
	t.Cleanup(func() { bqGoogleDefaultClient = origClient })
	bqGoogleDefaultClient = func(ctx context.Context, scopes ...string) (*http.Client, error) {
		return http.DefaultClient, nil
	}

	origSink := dbCopyToSinkRunner
	t.Cleanup(func() { dbCopyToSinkRunner = origSink })
	origCopy := dbCopyRunner
	t.Cleanup(func() { dbCopyRunner = origCopy })

	srcFile := filepath.Join(t.TempDir(), "s.sqlite")
	require.NoError(t, os.WriteFile(srcFile, nil, 0600))
	dstFile := filepath.Join(t.TempDir(), "d.sqlite")
	require.NoError(t, os.WriteFile(dstFile, nil, 0600))

	// 1. Target scheme is bigquery in runDbCopyCommand (line 143)
	// and summary.Tables > 0 with warnings in dbCopyToBigQuery (lines 218-224)
	dbCopyToSinkRunner = func(ctx context.Context, source dal.DB, sink dbcopy.CopySink, opts dbcopy.CopyOpts) (dbcopy.SourceSummary, error) {
		return dbcopy.SourceSummary{Tables: 1, Created: 1, RowsCopied: 5, Warnings: []string{"test-sink-warning"}}, nil
	}
	var errBuf bytes.Buffer
	cmdBQ := dbCopyCommand()
	cmdBQ.SetContext(context.Background())
	cmdBQ.SetErr(&errBuf)
	require.NoError(t, cmdBQ.Flags().Set("from", "sqlite://"+srcFile))
	require.NoError(t, cmdBQ.Flags().Set("to", "bigquery://demodb/research?location=US"))
	err := dbCopyAction(cmdBQ, nil)
	require.NoError(t, err)
	require.Contains(t, errBuf.String(), "db copy warning: test-sink-warning")

	// 2. dbCopyToBigQuery with uncertain error (lines 211-214)
	dbCopyToSinkRunner = func(ctx context.Context, source dal.DB, sink dbcopy.CopySink, opts dbcopy.CopyOpts) (dbcopy.SourceSummary, error) {
		return dbcopy.SourceSummary{}, &bqwriter.LoadOutcomeUnknownError{Job: bqwriter.LoadJobRef{JobID: "j1", ProjectID: "demodb", DatasetID: "research", Location: "US", TableID: "tbl"}}
	}
	err = dbCopyToBigQuery(context.Background(), nil, dbcopy.BackendRef{}, "", dbcopy.BackendRef{Scheme: "bigquery", ProjectID: "demodb", DatasetID: "research", Location: "US"}, dbcopy.CopyOpts{}, &errBuf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "BigQuery load outcome is uncertain")

	// 3. dbCopyToBigQuery with regular error (line 215)
	dbCopyToSinkRunner = func(ctx context.Context, source dal.DB, sink dbcopy.CopySink, opts dbcopy.CopyOpts) (dbcopy.SourceSummary, error) {
		return dbcopy.SourceSummary{}, errors.New("sink failure")
	}
	err = dbCopyToBigQuery(context.Background(), nil, dbcopy.BackendRef{}, "", dbcopy.BackendRef{Scheme: "bigquery", ProjectID: "demodb", DatasetID: "research", Location: "US"}, dbcopy.CopyOpts{}, &errBuf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sink failure")

	// 4. dbCopyRunner with warnings in runDbCopyCommand (lines 181-182)
	dbCopyRunner = func(ctx context.Context, src, tgt dal.DB, opts dbcopy.CopyOpts) (dbcopy.SourceSummary, error) {
		return dbcopy.SourceSummary{Tables: 1, Created: 1, RowsCopied: 10, Warnings: []string{"copy-warning-msg"}}, nil
	}
	errBuf.Reset()
	cmdWarn := dbCopyCommand()
	cmdWarn.SetContext(context.Background())
	cmdWarn.SetErr(&errBuf)
	require.NoError(t, cmdWarn.Flags().Set("from", "sqlite://"+srcFile))
	require.NoError(t, cmdWarn.Flags().Set("to", "sqlite://"+dstFile))
	err = dbCopyAction(cmdWarn, nil)
	require.NoError(t, err)
	require.Contains(t, errBuf.String(), "db copy warning: copy-warning-msg")

	// 5. ErrSourceHasNoTables with postgres srcRef (lines 153-157)
	t.Setenv("DATATUG_PREVIEW_POSTGRES", "1")
	restoreOpener := dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return &dalgo2postgres.Database{}, nil
	})
	t.Cleanup(restoreOpener)

	dbCopyRunner = func(ctx context.Context, src, tgt dal.DB, opts dbcopy.CopyOpts) (dbcopy.SourceSummary, error) {
		return dbcopy.SourceSummary{}, dbcopy.ErrSourceHasNoTables
	}

	// 5a. with --from-schema "custom" (line 153)
	t.Setenv("SRC_PG", "postgres://user:pass@localhost:5432/db")
	cmdPGSchema := dbCopyCommand()
	cmdPGSchema.SetContext(context.Background())
	require.NoError(t, cmdPGSchema.Flags().Set("from", "env:SRC_PG"))
	require.NoError(t, cmdPGSchema.Flags().Set("to", "sqlite://"+dstFile))
	require.NoError(t, cmdPGSchema.Flags().Set("from-schema", "custom"))
	err = dbCopyAction(cmdPGSchema, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), `PostgreSQL source schema "custom" has no tables`)

	// 5b. without --from-schema (line 155)
	cmdPGNoSchema := dbCopyCommand()
	cmdPGNoSchema.SetContext(context.Background())
	require.NoError(t, cmdPGNoSchema.Flags().Set("from", "env:SRC_PG"))
	require.NoError(t, cmdPGNoSchema.Flags().Set("to", "sqlite://"+dstFile))
	err = dbCopyAction(cmdPGNoSchema, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), `PostgreSQL source schema "public" has no tables`)
}
