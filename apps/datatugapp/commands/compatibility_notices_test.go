package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

func TestCopyCompatibilityNoticeKeepsLocalTargetsAndRedactsRemoteTargets(t *testing.T) {
	for _, scheme := range []string{"sqlite", "duckdb", "ingitdb", "http", "https"} {
		var output bytes.Buffer
		warnRemoteCopyTarget(&output, dbcopy.BackendRef{Scheme: scheme, Path: "/private/owned/file"})
		require.Empty(t, output.String(), scheme)
	}
	for _, target := range []dbcopy.BackendRef{
		{Scheme: "postgres", Path: "postgres://user:secret@localhost/owned"},
		{Scheme: "postgres", Path: "postgres://user:secret@127.0.0.2/owned"},
		{Scheme: "postgres", Path: "postgres://user:secret@[::1]/owned"},
		{Scheme: "couchdb", CouchDBHost: "LOCALHOST:5984"},
		{Scheme: "postgrest", PostgRESTHost: "127.0.0.1:3000"},
		{Scheme: "clickhouse", ClickHouseAddress: "[::1]:9000"},
		{Scheme: "libsql", LibSQLHost: "localhost:8080"},
		{Scheme: "typesense", TypesenseHost: "127.0.0.1:8108"},
	} {
		var output bytes.Buffer
		warnRemoteCopyTarget(&output, target)
		require.Empty(t, output.String())
	}
	for _, target := range []dbcopy.BackendRef{
		{Scheme: "postgres", Path: "postgres://secret-user:secret-password@db.example/private?sslmode=require"},
		{Scheme: "postgres", Path: "postgres://localhost.example/private"},
		{Scheme: "postgres", Path: "postgres://10.0.0.1/private"},
		{Scheme: "postgres", Path: "%invalid"},
		{Scheme: "bigquery", ProjectID: "private-project", DatasetID: "private_dataset"},
		{Scheme: "openvaultdb", Path: "/private/connection.json"},
		{Scheme: "unknown", Path: "secret"},
	} {
		var output bytes.Buffer
		warnRemoteCopyTarget(&output, target)
		require.Equal(t, remoteCopyCompatibilityNotice+"\n", output.String())
	}
}

func TestCopyActionEmitsWarningBeforeAnyConnectionAttempt(t *testing.T) {
	// The independent schema error exits before either opener; the target URL
	// contains credential sentinels which must never appear in the notice.
	stdout, stderr, err := runCopy(t, "db", "copy", "--from", "sqlite:///unused.db",
		"--to", "postgres://private-user:private-password@db.example/private-database",
		"--from-schema", "unsupported-for-sqlite")
	require.Error(t, err)
	require.Equal(t, remoteCopyCompatibilityNotice+"\n", stderr.String())
	require.Empty(t, stdout.String())
}

func TestCopyRecoveryWarnsBeforeCredentialOrNetworkAccess(t *testing.T) {
	prior := bqGoogleDefaultClient
	t.Cleanup(func() { bqGoogleDefaultClient = prior })
	var stderr bytes.Buffer
	bqGoogleDefaultClient = func(context.Context, ...string) (*http.Client, error) {
		require.Equal(t, remoteCopyCompatibilityNotice+"\n", stderr.String())
		return nil, errors.New("credential fixture unavailable")
	}
	command := dbCopyCommand()
	command.SetErr(&stderr)
	require.NoError(t, command.Flags().Set("to", "bigquery://owned-project/owned_dataset?location=US"))
	require.NoError(t, command.Flags().Set("recover-job-ref", `{"projectId":"owned-project","datasetId":"owned_dataset","tableId":"owned_table","location":"US","jobId":"owned-job"}`))
	require.Error(t, dbCopyAction(command, nil))
	require.Equal(t, remoteCopyCompatibilityNotice+"\n", stderr.String())
}

func TestLegacySQLActionWarnsWithoutEchoingInputs(t *testing.T) {
	command := updateUrlConfigCommandArgs()
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	require.NoError(t, command.Flags().Set("query", "private-query-text"))
	require.NoError(t, command.Flags().Set("consoleCommand-text", "private-command-text"))
	require.NoError(t, command.Flags().Set("password", "private-password"))
	// The mutually exclusive flags are rejected before any native opener.
	require.Error(t, updateUrlConfigCommandAction(command, nil))
	require.Equal(t, legacySQLRetirementNotice+"\n", stderr.String())
}
