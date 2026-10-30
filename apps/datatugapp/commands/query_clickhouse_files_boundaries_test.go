package commands

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/stretchr/testify/require"
)

func TestClickHouseAnonymousSinkValidatesBeforeWritingAndKeepsPrivacy(t *testing.T) {
	root := resultsHome(t)
	sink, err := openResultSink(queryOptions{resultID: "anonymous-boundary", resultRows: 1})
	require.NoError(t, err)
	defer sink.discardUnlessWritten()
	require.ErrorContains(t, sink.writeAnonymous(io.Discard, secureread.Result{}), "unavailable")
	invalid := clickHouseCommandResult()
	invalid.OrdinalColumns = nil
	require.Error(t, sink.writeAnonymous(io.Discard, invalid))
	require.False(t, sink.written)
	var out bytes.Buffer
	require.NoError(t, sink.writeAnonymous(&out, clickHouseCommandResult()))
	require.JSONEq(t, `{"resultFiles":{"version":1},"rowsKept":1,"hasMore":true,"columns":2}`, out.String())
	loaded, err := sink.store.Load(sink.id)
	require.NoError(t, err)
	require.True(t, loaded.Anonymous)
	require.Len(t, loaded.Recordset.Rows, 1)
	require.Equal(t, queryresults.UnavailableNoRelMetadata, loaded.Meta.Unavailable.Related)
	require.Equal(t, queryresults.UnavailableNoRelMetadata, loaded.Meta.Unavailable.Participants)
	require.Equal(t, loaded.Recordset.Columns[0].Name, loaded.Recordset.Columns[1].Name)
	require.FileExists(t, filepath.Join(root, sink.id, queryresults.DataFile))
}

func TestClickHouseAnonymousSinkReportsStoreAndStdoutFailures(t *testing.T) {
	root := resultsHome(t)
	sink, err := openResultSink(queryOptions{resultID: "removed", resultRows: 10})
	require.NoError(t, err)
	// Only this test's empty, newly created result directory is removed.
	require.NoError(t, os.Remove(filepath.Join(root, sink.id)))
	err = sink.writeAnonymous(io.Discard, clickHouseCommandResult())
	require.Error(t, err)
	require.False(t, sink.written)
	sink, err = openResultSink(queryOptions{resultID: "stdout-failed", resultRows: 10})
	require.NoError(t, err)
	broken := errors.New("closed result output")
	err = sink.writeAnonymous(&clickHouseOutputFailure{err: broken}, clickHouseCommandResult())
	require.ErrorIs(t, err, broken)
	require.True(t, sink.written, "a stdout failure must not destroy already persisted typed data")
	_, err = sink.store.Load(sink.id)
	require.NoError(t, err)
}

func TestClickHouseAnonymousShowRefusesBadFormatMissingResultAndStore(t *testing.T) {
	resultsHome(t)
	_, _, code := runResults(t, "show", "--id", "missing", "--format", "csv")
	require.Equal(t, exitCodeUsage, code)
	out, stderr, code := runResults(t, "show", "--id", "missing")
	require.NotZero(t, code)
	require.Empty(t, out)
	require.NotContains(t, stderr, ".datatug")
	old := openResultsStore
	openResultsStore = func() (*queryresults.Store, error) {
		return nil, &queryresults.Error{Reason: queryresults.ReasonUnwritable}
	}
	t.Cleanup(func() { openResultsStore = old })
	_, stderr, code = runResults(t, "show", "--id", "missing")
	require.Equal(t, exitCodeUsage, code)
	require.Contains(t, stderr, queryresults.ReasonUnwritable)
}
