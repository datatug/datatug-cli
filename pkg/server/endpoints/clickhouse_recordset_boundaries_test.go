package endpoints

import (
	"context"
	"strconv"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func endpointAnonymousRecordset() apicontract.Recordset {
	row := []apicontract.TypedValue{apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewDecimalValue("12345678901234567890.123456789")}
	return apicontract.Recordset{Columns: []apicontract.Column{{Name: "same", Type: "integer"}, {Name: "same", Type: "decimal"}}, Rows: [][]apicontract.TypedValue{row, append([]apicontract.TypedValue(nil), row...)}}
}

func TestContractAnonymousRecordsetKeepsDuplicateOrdinals(t *testing.T) {
	rs := endpointAnonymousRecordset()
	got, err := toContractRecordset(secureread.Result{AnonymousRecordset: &rs})
	require.NoError(t, err)
	require.Equal(t, rs, got)
	require.Equal(t, "18446744073709551615", got.Rows[0][0].Str)
	require.Equal(t, "12345678901234567890.123456789", got.Rows[0][1].Str)
	require.Equal(t, got.Rows[0], got.Rows[1], "duplicate rows have no invented identity")
}

func TestRunQueryBoundsAnonymousRowsWithoutMapConversion(t *testing.T) {
	for _, limit := range []int{1, 3} {
		t.Run("limit="+strconv.Itoa(limit), func(t *testing.T) {
			var req apicontract.ExecutionRequest
			decodeRequestFixture(t, "execution_request_adhoc.json", &req)
			dir, project := writeRunQueryTestProject(t)
			scope := configureSemanticSession(t, dir, project, "alice", []string{"admin"})
			req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
			req.Source, req.Limit = semanticTestSource, &limit
			want := endpointAnonymousRecordset()
			called := false
			old := runTugQLHook
			runTugQLHook = func(_ *secureread.Executor, _ context.Context, _ string, document []byte, _ map[string]any) (secureread.Result, error) {
				called = true
				require.Equal(t, req.TugQL, string(document))
				rs := endpointAnonymousRecordset()
				return secureread.Result{AnonymousRecordset: &rs}, nil
			}
			t.Cleanup(func() { runTugQLHook = old })
			got, err := computeRunQuery(context.Background(), req)
			require.NoError(t, err)
			require.True(t, called)
			require.Equal(t, limit == 1, got.Truncated)
			if limit == 1 {
				want.Rows = want.Rows[:1]
			}
			require.Equal(t, want, got.Recordset)
			require.NoError(t, got.Validate())
		})
	}
}

func TestRelatedKeyedReaderRefusesAnonymousSourceBeforeExecution(t *testing.T) {
	// A nil leaf executor is intentional: neither an invalid source nor an
	// anonymous ClickHouse source may reach the keyed RunStructured call.
	_, err := runStructuredRelatedHook(nil, context.Background(), "unsupported:synthetic", nil, nil)
	require.Error(t, err)
	_, err = runStructuredRelatedHook(nil, context.Background(), "clickhouse+http://127.0.0.1:18123/explore", nil, nil)
	require.ErrorIs(t, err, dal.ErrNotSupported)
	require.ErrorContains(t, err, "rows are anonymous")
	require.NotContains(t, err.Error(), "127.0.0.1")
}
