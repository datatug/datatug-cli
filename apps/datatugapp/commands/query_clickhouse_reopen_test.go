package commands

import (
	"bytes"
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func TestAnonymousResultShowReopensOrdinalsAndRefusesKeyedUI(t *testing.T) {
	resultsHome(t)
	store, err := openResultsStore()
	require.NoError(t, err)
	rs := apicontract.Recordset{Columns: []apicontract.Column{{Name: "same", Type: "integer"}, {Name: "same", Type: "decimal"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewDecimalValue("12345678901234567890.123456789")}}}
	data, err := queryresults.BuildAnonymous("anon", rs, []string{"UInt64", "Decimal(38,9)"}, []string{"", ""}, 10)
	require.NoError(t, err)
	file, err := store.Create("anon")
	require.NoError(t, err)
	require.NoError(t, file.Write(data.Bytes, queryresults.NewMeta("anon", "test", data).Encode()))
	cmd := queryResultsShowCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--id", "anon"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `"anonymous":true`)
	require.Contains(t, out.String(), "18446744073709551615")
	require.Contains(t, out.String(), "12345678901234567890.123456789")
	require.NotContains(t, out.String(), "$key")
	_, err = loadResultForUI(context.Background(), "anon")
	require.Error(t, err)
	require.Contains(t, err.Error(), "query results show")
}
