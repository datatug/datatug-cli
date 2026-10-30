package commands

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dal-go/dalgo2couchdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lateSinkWriter stands in for a stdout that fails after the query has run and
// the result files have been written.
type lateSinkWriter struct{}

func (lateSinkWriter) Write([]byte) (int, error) { return 0, errors.New("late sink write failed") }

// An index refusal from the CouchDB adapter is the command's own actionable
// message: the read stays refused rather than falling back to a full scan.
func TestQueryRunCouchDBIndexRequiredIsActionable(t *testing.T) {
	covCSeamOpenBackend(t, covCFakeDB{reader: &covCReader{err: fmt.Errorf("read orders: %w", dalgo2couchdb.ErrIndexRequired)}})
	cmd, out, _ := covCRunCmd(t, map[string]string{
		queryDBFlag:         "couchdb+http://127.0.0.1:5984/orders",
		queryFileFlag:       "-",
		queryNoPoliciesFlag: "true",
		queryFormatFlag:     "json",
	})
	cmd.SetIn(strings.NewReader("from: {name: orders}\ncolumns:\n  - {field: _id}\nwhere:\n  op: ==\n  left: {field: status}\n  right: {value: active}\norderBy:\n  - {field: score, desc: true}\nlimit: 3\n"))
	err := queryRunCommandAction(cmd, nil)
	require.Error(t, err)
	assert.Equal(t, exitCodeDatabase, covCExitCode(t, err))
	assert.Empty(t, out.String(), "a refused read prints no rows")
	assert.Contains(t, err.Error(), "CouchDB requires a Mango index for a filtered query")
	assert.Contains(t, err.Error(), "Create an index outside DataTug")
	assert.NotContains(t, err.Error(), "127.0.0.1")
}

// A failure to write the result after a successful read is the command's
// failure: it is never reported as a successful run with no locator line.
func TestQueryRunReportsALateResultSinkFailure(t *testing.T) {
	resultsHome(t)
	cmd, _, _ := covCRunCmd(t, map[string]string{
		queryDBFlag:         "sqlite://" + chinookFixture(t),
		queryFromFlag:       "Genre",
		queryNoPoliciesFlag: "true",
		queryResultIDFlag:   "late-sink-failure",
	})
	cmd.SetOut(lateSinkWriter{})
	err := queryRunCommandAction(cmd, nil)
	require.Error(t, err)
	assert.Equal(t, exitCodeDatabase, covCExitCode(t, err))
	assert.Contains(t, err.Error(), "late sink write failed")
}
